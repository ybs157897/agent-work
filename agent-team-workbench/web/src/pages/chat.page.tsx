import { GitBranch, MessageSquare, PanelRight, Plus, SendHorizonal, Square, X } from 'lucide-react';
import { useEffect, useMemo, useRef, useState, type ReactNode } from 'react';
import { useSearchParams } from 'react-router-dom';
import { TranscriptView } from '../components/chat/transcript-view';
import { ChatBottomDock } from '../components/chat/chat-bottom-dock';
import { ArtifactShelf } from '../components/chat/artifact-shelf';
import { ArtifactWorkspace } from '../components/chat/artifact-workspace';
import { Avatar } from '../components/avatar';
import { Button, EmptyState } from '../components/ui';
import { SseStatusPill } from '../components/sse-status';
import { runStatusColor, runStatusText } from '../components/status';
import { useAgentsStore } from '../stores/agents.store';
import { buildMessages, conversationLabel, aggregateRunStream, formatTokenUsage, hideLiveRunDrafts, isRunLive, useChatStore, ACTIVE, TERMINAL } from '../stores/chat.store';
import { mergeApprovalSegments, transcriptSegmentKey } from '../utils/approval-transcript';
import { conversationStatusDotClass, suggestedPrompts } from '../utils/chat-session-visuals';
import { useRunsStore } from '../stores/runs.store';
import { REPLY_TIMEOUT_MS } from '../utils/chat-errors';
import { deriveChatDock } from '../utils/derive-chat-dock';
import {
  buildTranscriptSegments,
  injectPendingUsers,
  supplementUserFromTimeline,
} from '../utils/chronological-transcript';
import { runHasVisibleOutput } from '../utils/run-timeline';

/** 对话页：Agent 选择器 + 会话列表 + 气泡消息流 + 输入框（协议 §5.2/§5.3）。 */
export default function ChatPage() {
  const agents = useAgentsStore((s) => s.agents);
  const agentId = useChatStore((s) => s.agentId);
  const conversationId = useChatStore((s) => s.conversationId);
  const selectAgent = useChatStore((s) => s.selectAgent);
  const openConversation = useChatStore((s) => s.openConversation);

  const [searchParams, setSearchParams] = useSearchParams();
  const urlBooted = useRef(false);

  // URL 初始值（如从 Agent 详情「发起对话」跳入）。
  useEffect(() => {
    const qAgent = searchParams.get('agent');
    const qConv = searchParams.get('c');
    if (qAgent && qAgent !== agentId) selectAgent(qAgent);
    if (qConv) openConversation(qConv);
    urlBooted.current = true;
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  // 新会话创建时同步 ?c=，便于刷新后恢复。
  useEffect(() => {
    if (!urlBooted.current || !agentId) return;
    const qAgent = searchParams.get('agent');
    const qConv = searchParams.get('c');
    if (conversationId) {
      if (qAgent === agentId && qConv === conversationId) return;
      setSearchParams({ agent: agentId, c: conversationId }, { replace: true });
      return;
    }
    if (qAgent === agentId && !qConv) return;
    setSearchParams({ agent: agentId }, { replace: true });
  }, [agentId, conversationId, searchParams, setSearchParams]);

  const pick = (id: string) => {
    selectAgent(id);
    setSearchParams({ agent: id }, { replace: true });
  };

  return (
    <div className="tx-scope flex h-full min-h-0 w-full overflow-hidden">
      {/* 左栏：Agent 切换排 + 任务列表（对话即任务） */}
      <aside className="flex min-h-0 w-64 shrink-0 flex-col border-r border-border-subtle bg-surface-sunken">
        <div className="shrink-0 border-b border-border-subtle/60 p-2">
          <div className="mb-1 px-1 text-caption font-medium uppercase tracking-wide text-text-tertiary">Agent</div>
          <div className="flex flex-wrap gap-1">
            {agents.map((a) => (
              <button
                key={a.id}
                onClick={() => pick(a.id)}
                title={`${a.name} · ${a.role}`}
                aria-label={`${a.name}（${a.role}）`}
                aria-pressed={agentId === a.id}
                className={`chat-agent-chip ${agentId === a.id ? 'chat-agent-chip-active' : ''}`}
              >
                <Avatar name={a.name} url={a.avatar} size={26} />
                {(a.presence === 'idle' || a.presence === 'busy') && (
                  <span className={`absolute -right-0.5 -top-0.5 h-2 w-2 rounded-full border border-surface-sunken ${a.presence === 'busy' ? 'bg-status-warning' : 'bg-status-success'}`} aria-hidden />
                )}
              </button>
            ))}
          </div>
        </div>
        {agentId && <ConversationList onPick={(id) => {
          openConversation(id);
          setSearchParams(id ? { agent: agentId, c: id } : { agent: agentId }, { replace: true });
        }} />}
      </aside>

      {/* 右侧对话区 */}
      <div className="flex-1 flex flex-col min-w-0 min-h-0 overflow-hidden">
        {agentId ? <ConversationPane /> : (
          <div className="flex min-h-0 flex-1 flex-col overflow-hidden">
            <ChatChrome
              left={<span className="text-body font-semibold text-text-primary">对话</span>}
              right={<SseStatusPill />}
            />
            <div className="flex flex-1 items-center justify-center">
              <EmptyState
                icon={<MessageSquare className="w-5 h-5" />}
                title="选择一个 Agent 开始对话"
                description="对话即任务：每条消息创建一个执行 Run，全程可追溯"
              />
            </div>
          </div>
        )}
      </div>
    </div>
  );
}

function ChatChrome({ left, right }: { left: ReactNode; right?: ReactNode }) {
  return (
    <div className="flex h-12 shrink-0 items-center justify-between border-b border-border-subtle bg-surface-base px-6">
      <div className="flex min-w-0 items-center gap-snug">{left}</div>
      <div className="flex shrink-0 items-center gap-2">{right}</div>
    </div>
  );
}

function ConversationList({ onPick }: { onPick: (id: string | null) => void }) {
  const conversations = useChatStore((s) => s.conversations);
  const conversationId = useChatStore((s) => s.conversationId);
  const runSnapshots = useRunsStore((s) => s.runs);

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div className="flex items-center justify-between px-3 pb-1 pt-3">
        <span className="text-caption font-medium uppercase tracking-wide text-text-tertiary">任务列表</span>
        <button
          onClick={() => onPick(null)}
          title="新任务"
          aria-label="新任务"
          className="inline-flex h-7 w-7 items-center justify-center rounded-button text-text-tertiary transition-colors hover:bg-surface-raised hover:text-text-primary"
        >
          <Plus className="w-4 h-4" />
        </button>
      </div>
      <div className="min-h-0 flex-1 overflow-y-auto px-2 pb-2">
        {conversations.map((c) => (
          <button
            key={c.id}
            onClick={() => onPick(c.id)}
            className={`mb-0.5 w-full rounded-lg border px-tight py-tight text-left transition-colors duration-150 ${
              conversationId === c.id
                ? 'border-brand-primary/25 bg-brand-muted/50'
                : 'border-transparent hover:border-border-subtle/60 hover:bg-surface-raised/60'
            }`}
          >
            <div className="flex items-center gap-1 text-body text-text-primary">
              {c.parent_id && (
                <GitBranch className="h-3 w-3 shrink-0 text-text-tertiary" aria-label="分叉会话" />
              )}
              <span className="truncate font-medium">{c.title}</span>
            </div>
            <div className="mt-0.5 flex items-center gap-1.5 text-caption text-text-tertiary">
              <span
                className={`h-1.5 w-1.5 rounded-full shrink-0 ${conversationStatusDotClass(c, runSnapshots)}`}
              />
              {c.runs_count} 轮 · {conversationLabel(c, runSnapshots)}
            </div>
          </button>
        ))}
        {conversations.length === 0 && (
          <EmptyState title="暂无会话" description="点右上角 + 开始新对话" className="py-6" />
        )}
      </div>
    </div>
  );
}

function ConversationPane() {
  const agentId = useChatStore((s) => s.agentId);
  const conversationId = useChatStore((s) => s.conversationId);
  const conversations = useChatStore((s) => s.conversations);
  const runs = useChatStore((s) => s.runs);
  const sending = useChatStore((s) => s.sending);
  const send = useChatStore((s) => s.send);
  const queue = useChatStore((s) => s.queue);
  const removeQueued = useChatStore((s) => s.removeQueued);
  const drainQueue = useChatStore((s) => s.drainQueue);
  const forkConversation = useChatStore((s) => s.forkConversation);
  const stopActiveRun = useChatStore((s) => s.stopActiveRun);
  const stoppingRunId = useChatStore((s) => s.stoppingRunId);
  const runAlerts = useChatStore((s) => s.runAlerts);
  const pendingUsers = useChatStore((s) => s.pendingUsers);
  const agents = useAgentsStore((s) => s.agents);

  const timelines = useRunsStore((s) => s.timelines);
  const runSnapshots = useRunsStore((s) => s.runs);
  const watchRun = useRunsStore((s) => s.watchRun);
  const unwatchRun = useRunsStore((s) => s.unwatchRun);
  const approvals = useRunsStore((s) => s.approvals);

  const [draft, setDraft] = useState('');
  const [workspaceOpen, setWorkspaceOpen] = useState(false);
  const scrollRef = useRef<HTMLDivElement>(null);
  const textareaRef = useRef<HTMLTextAreaElement>(null);
  const followStreamRef = useRef(true);
  const approvalAnchorsRef = useRef<Record<string, string>>({});
  const [approvalAnchors, setApprovalAnchors] = useState<Record<string, string>>({});

  const agent = agents.find((a) => a.id === agentId);
  const conversation = conversations.find((c) => c.id === conversationId);
  const runIds = useMemo(() => runs.map((r) => r.id), [runs]);
  // 本会话全部成果（watchRun 已按 run 拉取，artifact 事件驱动刷新）。
  const artifactsByRun = useRunsStore((s) => s.artifacts);
  const conversationArtifacts = useMemo(
    () => runIds.flatMap((id) => artifactsByRun[id] ?? []),
    [runIds, artifactsByRun],
  );
  const latestRunId = runIds[runIds.length - 1];
  const latestRun = latestRunId ? runSnapshots[latestRunId] ?? runs[runs.length - 1] : undefined;

  // 订阅当前会话所有 run，确保历史轮次消息可回放。
  useEffect(() => {
    for (const id of runIds) watchRun(id);
    return () => {
      for (const id of runIds) unwatchRun(id);
    };
  }, [runIds, watchRun, unwatchRun]);

  const messages = useMemo(() => buildMessages(runIds, timelines), [runIds, timelines]);
  // 已到任何终态的 run：其内仍 running 的工具行按 stopped（中断/截断）展示，不再扫光--
  // 覆盖中断/取消，也覆盖历史数据缺 completed 帧的挂起行（对齐 DSH 的 interruption 投影语义）。
  const stoppedRuns = useMemo(() => {
    const set = new Set<string>();
    for (const id of runIds) {
      const status = runSnapshots[id]?.status;
      if (status && TERMINAL.has(status)) set.add(id);
    }
    return set;
  }, [runIds, runSnapshots]);
  const liveStream = useMemo(
    () => (latestRunId ? aggregateRunStream(timelines[latestRunId] ?? []) : { reasoning: '', answerDraft: '' }),
    [latestRunId, timelines],
  );
  const liveRunActive = isRunLive(latestRun?.status);
  const displayMessages = useMemo(
    () => hideLiveRunDrafts(messages, latestRunId, liveRunActive),
    [messages, latestRunId, liveRunActive],
  );
  const runApprovals = useMemo(
    () => (latestRunId ? approvals[latestRunId] ?? [] : []),
    [approvals, latestRunId],
  );
  const runStatuses = useMemo(() => {
    const map: Record<string, string> = {};
    for (const id of runIds) {
      const status = runSnapshots[id]?.status;
      if (status) map[id] = status;
    }
    return map;
  }, [runIds, runSnapshots]);
  const hasPendingApproval = runApprovals.some((a) => a.status === 'pending');
  const transcriptMessages = useMemo(() => {
    const supplemented = supplementUserFromTimeline(displayMessages, runIds, timelines);
    return injectPendingUsers(supplemented, pendingUsers);
  }, [displayMessages, runIds, timelines, pendingUsers]);
  const baseSegments = useMemo(
    () =>
      buildTranscriptSegments(transcriptMessages, {
        runStatuses,
        liveRunId: latestRunId,
        liveStream,
        liveRunActive,
        hasPendingApproval,
        pendingUsers,
        rawMessages: messages,
      }),
    [transcriptMessages, runStatuses, latestRunId, liveStream, liveRunActive, hasPendingApproval, pendingUsers, messages],
  );

  useEffect(() => {
    setApprovalAnchors({});
    approvalAnchorsRef.current = {};
  }, [conversationId]);

  // 审批出现时钉住 anchor：之后的新输出排在审批卡之后（对齐 kanna inline approval）。
  useEffect(() => {
    if (!baseSegments.length) return;
    const lastKey = transcriptSegmentKey(baseSegments[baseSegments.length - 1]);
    let changed = false;
    const next = { ...approvalAnchorsRef.current };
    for (const a of runApprovals) {
      if (a.status === 'pending' && next[a.id] === undefined) {
        next[a.id] = lastKey;
        changed = true;
      }
    }
    if (changed) {
      approvalAnchorsRef.current = next;
      setApprovalAnchors(next);
    }
  }, [runApprovals, baseSegments]);

  const transcriptSegments = useMemo(
    () => mergeApprovalSegments(baseSegments, runApprovals, approvalAnchors),
    [baseSegments, runApprovals, approvalAnchors],
  );
  const dock = useMemo(
    () => deriveChatDock(transcriptMessages, latestRunId, timelines),
    [transcriptMessages, latestRunId, timelines],
  );

  // run.created 落 timeline 后清除 optimistic 用户行。
  useEffect(() => {
    const stale = Object.keys(pendingUsers).filter((runId) =>
      messages.some((m) => m.runId === runId && m.kind === 'user'),
    );
    if (!stale.length) return;
    useChatStore.setState((s) => {
      const next = { ...s.pendingUsers };
      for (const id of stale) delete next[id];
      return { pendingUsers: next };
    });
  }, [messages, pendingUsers]);
  const runInFlight = !!latestRun && ACTIVE.has(latestRun.status);
  const hasVisibleOutput = useMemo(
    () => (latestRunId ? runHasVisibleOutput(timelines[latestRunId] ?? []) : false),
    [latestRunId, timelines],
  );
  // 最新 run 的全部审批：pending 渲染交互卡，已决议转完成态行留在流内。
  const latestRunAlert = latestRunId ? runAlerts[latestRunId] : undefined;

  // 首响超时：活动 run 在 60s 内无任何可见输出（推理/正文/工具）则自动中断。
  useEffect(() => {
    if (!runInFlight || !latestRunId || hasVisibleOutput || stoppingRunId === latestRunId) {
      return;
    }
    const runId = latestRunId;
    const timer = window.setTimeout(() => {
      const runsStore = useRunsStore.getState();
      const run = runsStore.runs[runId];
      if (!run || !ACTIVE.has(run.status)) return;
      if (runHasVisibleOutput(runsStore.timelines[runId] ?? [])) return;
      void stopActiveRun(runId, 'reply_timeout');
    }, REPLY_TIMEOUT_MS);
    return () => window.clearTimeout(timer);
  }, [runInFlight, latestRunId, hasVisibleOutput, stoppingRunId, stopActiveRun]);

  useEffect(() => {
    const el = scrollRef.current;
    if (!el) return;
    const syncFollow = () => {
      followStreamRef.current = el.scrollHeight - el.scrollTop - el.clientHeight < 96;
    };
    syncFollow();
    el.addEventListener('scroll', syncFollow, { passive: true });
    return () => el.removeEventListener('scroll', syncFollow);
  }, [conversationId]);

  useEffect(() => {
    const el = scrollRef.current;
    if (!el || !followStreamRef.current) return;
    el.scrollTop = el.scrollHeight;
  }, [transcriptSegments.length, liveStream.reasoning, liveStream.answerDraft, runApprovals.length]);

  // 自动续发：最新 run「进入」succeeded 且队列非空时出队首条开新轮。
  // 只在状态边沿触发一次：drain 失败后 sending 复位也不会原地重试风暴；
  // failed/cancelled/lost/interrupted 不自动发（留给用户手动「继续发送」）。
  const drainedEdgeRef = useRef('');
  useEffect(() => {
    if (!latestRun || latestRun.status !== 'succeeded' || sending || queue.length === 0) return;
    const edge = `${latestRun.id}:succeeded`;
    if (drainedEdgeRef.current === edge) return;
    drainedEdgeRef.current = edge;
    void drainQueue();
  }, [latestRun, sending, queue.length, drainQueue]);

  // 最新 run 的累计输入用量；后端未上报（字段缺失）时不渲染。
  // 上下文窗口需另拉 /models 匹配 agent 模型--不值得为凑格式加请求，只显 used。
  const usageText = formatTokenUsage(latestRun?.usage_in);

  const doSend = () => {
    const text = draft.trim();
    if (!text) return;
    setDraft('');
    if (textareaRef.current) textareaRef.current.style.height = 'auto';
    void send(text);
  };

  /** 输入框随内容增高，封顶 160px 后内部滚动。 */
  const autoGrow = (el: HTMLTextAreaElement) => {
    el.style.height = 'auto';
    el.style.height = `${Math.min(el.scrollHeight, 160)}px`;
  };

  const applyPrompt = (text: string) => {
    setDraft(text);
    textareaRef.current?.focus();
  };

  return (
    <div className="flex flex-1 min-h-0 overflow-hidden">
      <div className="flex min-h-0 min-w-0 flex-1 flex-col overflow-hidden">
      <ChatChrome
        left={(
          <>
            {agent && <Avatar name={agent.name} url={agent.avatar} size={24} />}
            <span className="truncate text-body font-semibold text-text-primary">
              {agent?.name ?? ''}
            </span>
            <span className="truncate text-caption text-text-tertiary" title={conversation?.title}>
              {conversation ? conversation.title : '新对话'}
            </span>
          </>
        )}
        right={(
          <>
            {conversationArtifacts.length > 0 && (
              <button
                type="button"
                onClick={() => setWorkspaceOpen((v) => !v)}
                title={workspaceOpen ? '关闭工作区' : '打开工作区'}
                aria-label={workspaceOpen ? '关闭工作区' : '打开工作区'}
                className="inline-flex h-8 w-8 items-center justify-center rounded-button text-text-tertiary transition-colors hover:bg-surface-sunken hover:text-text-primary"
              >
                <PanelRight className="h-4 w-4" />
              </button>
            )}
            {latestRun && (
              <span className={`inline-flex items-center gap-1.5 rounded-full border border-border-subtle bg-surface-sunken px-2 py-0.5 text-caption font-medium ${runStatusColor(latestRun.status)}`}>
                <span className="h-1.5 w-1.5 rounded-full bg-current" aria-hidden />
                {latestRun.status === 'reconnecting' ? '正在重连…' : runStatusText(latestRun.status)}
              </span>
            )}
            <SseStatusPill />
          </>
        )}
      />

      {/* 消息流（tx：正文独立暗色皮肤，决策见 notes tx-transcript-standalone-skin） */}
      <div
        ref={scrollRef}
        data-chat-scroll="transcript"
        className="relative min-h-0 flex-1 overflow-y-auto overscroll-contain px-6 py-comfortable"
      >
        {messages.length === 0 && transcriptSegments.length === 0 && (
          <div className="chat-thread flex min-h-full flex-col items-center justify-center py-12">
            <p className="text-center text-caption text-text-tertiary">
              输入第一条消息，为 {agent?.name ?? 'Agent'} 创建任务并开始运行；或从建议开始：
            </p>
            <div className="mt-3 flex flex-wrap justify-center gap-2">
              {suggestedPrompts(agent?.role).map((p) => (
                <button
                  key={p}
                  type="button"
                  onClick={() => applyPrompt(p)}
                  className="rounded-button border border-border-subtle bg-surface-raised/85 px-snug py-tight text-caption text-text-secondary shadow-card transition-all duration-ink hover:-translate-y-0.5 hover:border-brand-primary/35 hover:text-brand-primary"
                >
                  {p}
                </button>
              ))}
            </div>
          </div>
        )}
        <div className="chat-thread space-y-3 pb-2">
          <TranscriptView
            segments={transcriptSegments}
            stoppedRuns={stoppedRuns}
            onFork={(key) => void forkConversation(key)}
            agent={agent ? { name: agent.name, avatar: agent.avatar } : undefined}
          />
        </div>
        {latestRunAlert && (
          <div className="chat-thread py-0.5 text-center text-caption text-status-error">
            ✕ {latestRunAlert.message}
          </div>
        )}
      </div>

      {/* 底部固定：成果摘要 + 计划 / 目标 + 一体化输入卡 */}
      <div className="shrink-0 border-t border-border-subtle bg-surface-base px-6 pb-4 pt-2">
        {conversationArtifacts.length > 0 && (
          <div className="mb-2">
            <ArtifactShelf artifacts={conversationArtifacts} onOpen={() => setWorkspaceOpen(true)} />
          </div>
        )}
        <ChatBottomDock goal={dock.goal} todoPlan={dock.todoPlan} proposedPlan={dock.proposedPlan} />
        <div className="chat-composer" data-chat-composer>
            {/* 待发送队列（运行中入队，本轮成功后自动续发） */}
            {queue.length > 0 && (
              <div className="chat-composer-queue">
                <div className="flex items-center justify-between gap-2">
                  <span className="text-caption text-text-tertiary">待发送队列（{queue.length} 条）</span>
                  {latestRun && TERMINAL.has(latestRun.status) && latestRun.status !== 'succeeded' && (
                    <button
                      type="button"
                      onClick={() => void drainQueue()}
                      disabled={sending}
                      className="text-caption font-medium text-brand-primary transition-colors hover:text-brand-accent disabled:opacity-50 disabled:cursor-not-allowed"
                    >
                      继续发送
                    </button>
                  )}
                </div>
                {queue.map((q, i) => (
                  <div key={q.clientKey} className="flex items-center gap-2">
                    <span className="text-caption tabular-nums text-text-tertiary">{i + 1}</span>
                    <span className="min-w-0 flex-1 truncate text-caption text-text-secondary">{q.text}</span>
                    <button
                      type="button"
                      aria-label="移除待发送消息"
                      title="移除"
                      onClick={() => removeQueued(i)}
                      className="inline-flex h-6 w-6 shrink-0 items-center justify-center rounded-button text-text-tertiary transition-colors hover:bg-surface-sunken hover:text-text-primary"
                    >
                      <X className="h-3.5 w-3.5" />
                    </button>
                  </div>
                ))}
              </div>
            )}
            <textarea
              ref={textareaRef}
              value={draft}
              onChange={(e) => {
                setDraft(e.target.value);
                autoGrow(e.currentTarget);
              }}
              onKeyDown={(e) => {
                if (e.key === 'Enter' && !e.shiftKey) {
                  e.preventDefault();
                  doSend();
                }
              }}
              rows={1}
              placeholder={latestRun && !TERMINAL.has(latestRun.status)
                ? '运行中，消息将进入队列，完成后自动发送'
                : '输入消息，Enter 发送，Shift+Enter 换行'}
              className="chat-composer-input"
            />
            <div className="flex items-center gap-2 px-snug pb-tight pt-0">
              <span className="min-w-0 flex-1 truncate text-caption tabular-nums text-text-tertiary">{usageText}</span>
              {runInFlight && (
                <Button variant="ghost" type="button" onClick={() => latestRunId && void stopActiveRun(latestRunId, 'user_stopped')} disabled={!latestRunId || stoppingRunId === latestRunId}>
                  <Square className="w-3.5 h-3.5" />
                  {stoppingRunId === latestRunId ? '停止中' : '停止'}
                </Button>
              )}
              <Button variant="primary" onClick={doSend} disabled={!draft.trim() || sending}>
                {sending ? '发送中' : '发送'}
                <SendHorizonal className="w-4 h-4" />
              </Button>
            </div>
          </div>
      </div>
      </div>
      {workspaceOpen && (
        <ArtifactWorkspace artifacts={conversationArtifacts} onClose={() => setWorkspaceOpen(false)} />
      )}
    </div>
  );
}
