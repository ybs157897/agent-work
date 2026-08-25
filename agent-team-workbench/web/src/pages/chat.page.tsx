import { GitBranch, MessageSquare, PanelRight, Plus, SendHorizonal, Square, X } from 'lucide-react';
import { useEffect, useMemo, useRef, useState } from 'react';
import { useSearchParams } from 'react-router-dom';
import { TranscriptView } from '../components/chat/transcript-view';
import { ChatBottomDock } from '../components/chat/chat-bottom-dock';
import { ArtifactShelf } from '../components/chat/artifact-shelf';
import { ArtifactWorkspace } from '../components/chat/artifact-workspace';
import { Avatar } from '../components/avatar';
import { Button, EmptyState } from '../components/ui';
import { PresenceDot, runStatusColor, runStatusText } from '../components/status';
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
    <div className="h-full min-h-0 w-full flex overflow-hidden">
      {/* 左栏：Agent 选择器 + 会话列表 */}
      <div className="w-64 shrink-0 border-r border-border-subtle bg-surface-raised flex flex-col min-h-0">
        <div className="p-3 border-b border-border-subtle shrink-0">
          <h3 className="text-body font-semibold text-text-primary">选择 Agent</h3>
        </div>
        <div
          className={`overflow-y-auto p-2 space-y-1 ${
            agentId ? 'shrink-0 max-h-[45%]' : 'flex-1'
          }`}
        >
          {agents.map((a) => (
            <button
              key={a.id}
              onClick={() => pick(a.id)}
              className={`w-full flex items-center gap-2 p-2 rounded-lg text-left transition-colors ${
                agentId === a.id ? 'bg-brand-primary/10 ring-1 ring-brand-primary/30' : 'hover:bg-surface-base'
              }`}
            >
              <Avatar name={a.name} url={a.avatar} size={28} />
              <div className="min-w-0 flex-1">
                <div className="text-body font-medium text-text-primary truncate">{a.name}</div>
                <div className="text-caption text-text-tertiary truncate">{a.role}</div>
              </div>
              <PresenceDot presence={a.presence} pulse={false} />
            </button>
          ))}
        </div>
        {agentId && <ConversationList onPick={(id) => {
          openConversation(id);
          setSearchParams(id ? { agent: agentId, c: id } : { agent: agentId }, { replace: true });
        }} />}
      </div>

      {/* 右侧对话区 */}
      <div className="flex-1 flex flex-col min-w-0 min-h-0 overflow-hidden">
        {agentId ? <ConversationPane /> : (
          <div className="flex-1 flex items-center justify-center">
            <EmptyState
              icon={<MessageSquare className="w-5 h-5" />}
              title="选择一个 Agent 开始对话"
              description="对话即任务：每条消息创建一个执行 Run，全程可追溯"
            />
          </div>
        )}
      </div>
    </div>
  );
}

function ConversationList({ onPick }: { onPick: (id: string | null) => void }) {
  const conversations = useChatStore((s) => s.conversations);
  const conversationId = useChatStore((s) => s.conversationId);
  const runSnapshots = useRunsStore((s) => s.runs);

  return (
    <div className="flex-1 flex flex-col min-h-0 border-t border-border-subtle">
      <div className="p-3 flex items-center justify-between">
        <h3 className="text-body font-semibold text-text-primary">会话</h3>
        <button
          onClick={() => onPick(null)}
          title="新对话"
          className="p-1 hover:bg-surface-base rounded text-text-tertiary hover:text-text-primary transition-colors"
        >
          <Plus className="w-4 h-4" />
        </button>
      </div>
      <div className="flex-1 overflow-y-auto p-2 space-y-1">
        {conversations.map((c) => (
          <button
            key={c.id}
            onClick={() => onPick(c.id)}
            className={`w-full text-left p-2 rounded-lg transition-colors ${
              conversationId === c.id ? 'bg-brand-primary/10 ring-1 ring-brand-primary/30' : 'hover:bg-surface-base'
            }`}
          >
            <div className="flex items-center gap-1 text-body text-text-primary">
              {c.parent_id && (
                <GitBranch className="h-3 w-3 shrink-0 text-text-tertiary" aria-label="分叉会话" />
              )}
              <span className="truncate">{c.title}</span>
            </div>
            <div className="flex items-center gap-1.5 text-caption text-text-tertiary">
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
  }, [runIds.join(','), watchRun, unwatchRun]);

  const messages = useMemo(() => buildMessages(runIds, timelines), [runIds, timelines]);
  // 已到任何终态的 run：其内仍 running 的工具行按 stopped（中断/截断）展示，不再扫光——
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
  const runApprovals = latestRunId ? (approvals[latestRunId] ?? []) : [];
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
  const latestTimeline = latestRunId ? timelines[latestRunId] ?? [] : [];
  const hasVisibleOutput = useMemo(() => runHasVisibleOutput(latestTimeline), [latestTimeline]);
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
  // 上下文窗口需另拉 /models 匹配 agent 模型——不值得为凑格式加请求，只显 used。
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
      <div className="flex flex-col flex-1 min-w-0 min-h-0 overflow-hidden">
      {/* 头部 */}
      <div className="h-[52px] shrink-0 px-comfortable flex items-center justify-between border-b border-border-subtle">
        <div className="flex items-center gap-2 min-w-0">
          {agent && <Avatar name={agent.name} url={agent.avatar} size={28} />}
          <span className="text-body-lg font-semibold text-text-primary truncate">
            {agent?.name ?? ''}
          </span>
          <span className="text-caption text-text-tertiary truncate">
            {conversation ? conversation.title : '新对话'}
          </span>
        </div>
        <div className="flex items-center gap-2 shrink-0">
          {conversationArtifacts.length > 0 && (
            <button
              type="button"
              onClick={() => setWorkspaceOpen((v) => !v)}
              title={workspaceOpen ? '关闭工作区' : '打开工作区'}
              aria-label={workspaceOpen ? '关闭工作区' : '打开工作区'}
              className="p-1 rounded hover:bg-surface-base text-text-tertiary hover:text-text-primary transition-colors"
            >
              <PanelRight className="h-4 w-4" />
            </button>
          )}
          {latestRun && (
            <span className={`text-caption font-medium ${runStatusColor(latestRun.status)}`}>
              {latestRun.status === 'reconnecting' ? '正在重连…' : runStatusText(latestRun.status)}
            </span>
          )}
        </div>
      </div>

      {/* 消息流 */}
      <div
        ref={scrollRef}
        className="flex-1 overflow-y-auto overscroll-contain px-4 sm:px-6 py-base min-h-0"
      >
        {messages.length === 0 && transcriptSegments.length === 0 && (
          <div className="chat-thread py-12">
            <p className="text-center text-caption text-text-tertiary">
              输入第一条消息，为 {agent?.name ?? 'Agent'} 创建任务并开始运行；或从建议开始：
            </p>
            <div className="mt-3 flex flex-wrap justify-center gap-2">
              {suggestedPrompts(agent?.role).map((p) => (
                <button
                  key={p}
                  type="button"
                  onClick={() => applyPrompt(p)}
                  className="rounded-full border border-border-subtle bg-surface-raised px-3 py-1.5 text-caption text-text-secondary transition-colors hover:border-brand-primary/35 hover:text-brand-primary"
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

      {/* 底部固定：成果摘要 + 计划 / 目标 + 输入 */}
      <div className="shrink-0 border-t border-border-subtle bg-surface-base/95 backdrop-blur-sm">
        {conversationArtifacts.length > 0 && (
          <div className="chat-thread px-4 sm:px-6 pt-2">
            <ArtifactShelf artifacts={conversationArtifacts} onOpen={() => setWorkspaceOpen(true)} />
          </div>
        )}
        <div className="chat-thread px-4 sm:px-6 pt-2">
          <ChatBottomDock goal={dock.goal} todoPlan={dock.todoPlan} proposedPlan={dock.proposedPlan} />
        </div>
        <div className="chat-thread px-4 sm:px-6 pb-comfortable">
        {/* 待发送队列：运行中入队的消息，本轮成功后自动续发；终态非成功时可手动继续 */}
        {queue.length > 0 && (
          <div className="mb-2 space-y-1 rounded-lg border border-border-subtle bg-surface-raised px-3 py-2">
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
                  className="shrink-0 rounded p-0.5 text-text-tertiary transition-colors hover:text-text-primary"
                >
                  <X className="h-3.5 w-3.5" />
                </button>
              </div>
            ))}
          </div>
        )}
        <div className="flex items-end gap-2">
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
            rows={2}
            placeholder={latestRun && !TERMINAL.has(latestRun.status)
              ? '运行中——消息将进入队列，完成后自动发送'
              : '输入消息，Enter 发送，Shift+Enter 换行'}
            className="flex-1 rounded-input border border-border-strong bg-surface-raised px-snug py-tight text-body outline-none focus:ring-2 focus:ring-brand-primary/30 resize-none"
          />
          {runInFlight && (
            <button
              type="button"
              onClick={() => latestRunId && void stopActiveRun(latestRunId, 'user_stopped')}
              disabled={!latestRunId || stoppingRunId === latestRunId}
              title="停止当前运行"
              className="flex items-center gap-1.5 rounded-button border border-border-strong px-base py-tight text-body font-medium text-text-secondary transition-colors hover:bg-surface-raised disabled:cursor-not-allowed disabled:opacity-50"
            >
              <Square className="w-4 h-4" />
              {stoppingRunId === latestRunId ? '停止中' : '停止'}
            </button>
          )}
          <Button variant="primary" onClick={doSend} disabled={!draft.trim() || sending}>
            <SendHorizonal className="w-4 h-4" />
            {sending ? '发送中' : '发送'}
          </Button>
        </div>
        {usageText && (
          <div className="mt-1 flex justify-end">
            <span className="text-caption tabular-nums text-text-tertiary">{usageText}</span>
          </div>
        )}
        </div>
      </div>
      </div>
      {workspaceOpen && (
        <ArtifactWorkspace artifacts={conversationArtifacts} onClose={() => setWorkspaceOpen(false)} />
      )}
    </div>
  );
}

