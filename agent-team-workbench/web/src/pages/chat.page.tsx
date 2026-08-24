import { GitBranch, MessageSquare, Plus, SendHorizonal, X } from 'lucide-react';
import { type ReactNode, useEffect, useMemo, useRef, useState } from 'react';
import { useSearchParams } from 'react-router-dom';
import { AssistantTurn } from '../components/chat/assistant-turn';
import { ApprovalCard } from '../components/chat/approval-card';
import { ActivityGroup, groupActivity } from '../components/chat/tool-card';
import { MessageActions } from '../components/chat/message-actions';
import { PlanCard } from '../components/chat/plan-card';
import { Avatar } from '../components/avatar';
import { EmptyState } from '../components/async-state';
import { PresenceDot, runStatusColor, runStatusText } from '../components/status';
import { useAgentsStore } from '../stores/agents.store';
import { buildMessages, conversationLabel, aggregateRunStream, formatTokenUsage, useChatStore, ACTIVE, TERMINAL, type ChatMessage } from '../stores/chat.store';
import { useRunsStore } from '../stores/runs.store';
import { formatTime } from '../utils/format';

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
          <div className="flex-1 flex items-center justify-center text-text-tertiary">
            <div className="text-center space-y-2">
              <MessageSquare className="w-8 h-8 mx-auto text-text-tertiary" />
              <p className="text-body">选择一个 Agent 开始对话</p>
              <p className="text-caption">对话即任务：每条消息创建一个执行 Run，全程可追踪</p>
            </div>
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
            <div className="text-caption text-text-tertiary">
              {c.runs_count} 轮 · {conversationLabel(c, runSnapshots)}
            </div>
          </button>
        ))}
        {conversations.length === 0 && <EmptyState label="暂无会话，点 + 开始" />}
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
  const agents = useAgentsStore((s) => s.agents);

  const timelines = useRunsStore((s) => s.timelines);
  const runSnapshots = useRunsStore((s) => s.runs);
  const watchRun = useRunsStore((s) => s.watchRun);
  const unwatchRun = useRunsStore((s) => s.unwatchRun);
  const approvals = useRunsStore((s) => s.approvals);

  const [draft, setDraft] = useState('');
  const scrollRef = useRef<HTMLDivElement>(null);

  const agent = agents.find((a) => a.id === agentId);
  const conversation = conversations.find((c) => c.id === conversationId);
  const runIds = useMemo(() => runs.map((r) => r.id), [runs]);
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
  const awaitingReply =
    !!latestRun && ACTIVE.has(latestRun.status) && !messages.some((m) => m.kind === 'assistant' && m.runId === latestRunId);

  // 最新 run 的全部审批：pending 渲染交互卡，已决议转完成态行留在流内。
  const runApprovals = latestRunId ? (approvals[latestRunId] ?? []) : [];

  useEffect(() => {
    const el = scrollRef.current;
    if (!el) return;
    el.scrollTo({ top: el.scrollHeight, behavior: 'smooth' });
  }, [messages.length, liveStream.reasoning.length, liveStream.answerDraft.length, runApprovals.length]);

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
    void send(text);
  };

  return (
    <div className="flex flex-col flex-1 min-h-0 overflow-hidden">
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
        {latestRun && (
          <span className={`text-caption font-medium ${runStatusColor(latestRun.status)}`}>
            {runStatusText(latestRun.status)}
          </span>
        )}
      </div>

      {/* 消息流 */}
      <div
        ref={scrollRef}
        className="flex-1 overflow-y-auto overscroll-contain px-comfortable py-base space-y-3 min-h-0"
      >
        {messages.length === 0 && (
          <div className="text-center text-caption text-text-tertiary py-12">
            输入第一条消息，为该 Agent 创建任务并开始运行
          </div>
        )}
        {renderTranscript(messages, stoppedRuns, (key) => void forkConversation(key))}
        {awaitingReply && (
          <AssistantTurn
            reasoning={liveStream.reasoning}
            text={liveStream.answerDraft}
            streaming
            reasoningOnly={!liveStream.answerDraft && Boolean(liveStream.reasoning)}
          />
        )}
        {runApprovals.map((a) => (
          <ApprovalCard key={a.id} approval={a} />
        ))}
      </div>

      {/* 输入框 */}
      <div className="shrink-0 border-t border-border-subtle p-comfortable">
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
            value={draft}
            onChange={(e) => setDraft(e.target.value)}
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
          <button
            onClick={doSend}
            disabled={!draft.trim() || sending}
            className="flex items-center gap-1.5 bg-brand-primary text-white rounded-button px-base py-tight font-medium transition-all duration-150 hover:bg-brand-accent active:scale-[0.98] disabled:opacity-50 disabled:cursor-not-allowed"
          >
            <SendHorizonal className="w-4 h-4" />
            {sending ? '发送中' : '发送'}
          </button>
        </div>
        {usageText && (
          <div className="mt-1 flex justify-end">
            <span className="text-[11px] tabular-nums text-text-tertiary">{usageText}</span>
          </div>
        )}
      </div>
    </div>
  );
}

function renderTranscript(
  messages: ChatMessage[],
  stoppedRuns: ReadonlySet<string>,
  onFork: (key: string) => void,
): ReactNode[] {
  const nodes: ReactNode[] = [];
  const segments = groupActivity(messages);
  for (let i = 0; i < segments.length; i++) {
    const seg = segments[i];
    if (seg.kind === 'activity') {
      nodes.push(<ActivityGroup key={seg.items[0].key} items={seg.items} stoppedRuns={stoppedRuns} />);
      continue;
    }
    const msg = seg.item;
    if (msg.kind === 'user') {
      nodes.push(<UserBubble key={msg.key} msg={msg} />);
      continue;
    }
    if (msg.kind === 'thinking') {
      const lookahead = segments[i + 1];
      const next = lookahead?.kind === 'single' ? lookahead.item : undefined;
      if (next?.kind === 'assistant' && next.runId === msg.runId) {
        nodes.push(
          <AssistantTurn
            key={msg.runId}
            reasoning={msg.text}
            text={next.text}
            at={next.at}
            reasoningOnly={!next.text && Boolean(msg.text)}
            // 分叉锚定随轮落定的 assistant 正文（合并分支用 next 的 key）；
            // reasoningOnly 无正文的不给入口。
            forkKey={next.text ? next.key : undefined}
            onFork={next.text ? onFork : undefined}
          />,
        );
        i += 1;
        continue;
      }
      nodes.push(
        <AssistantTurn
          key={msg.key}
          reasoning={msg.text}
          reasoningOnly
        />,
      );
      continue;
    }
    if (msg.kind === 'assistant') {
      nodes.push(<AssistantTurn key={msg.key} text={msg.text} at={msg.at} forkKey={msg.key} onFork={onFork} />);
      continue;
    }
    if (msg.kind === 'plan') {
      nodes.push(<PlanCard key={msg.key} msg={msg} />);
      continue;
    }
    nodes.push(<MetaLine key={msg.key} msg={msg} />);
  }
  return nodes;
}

function UserBubble({ msg }: { msg: ChatMessage }) {
  return (
    <div className="group flex justify-end py-1">
      <div className="max-w-[min(525px,82%)] rounded-[22px] bg-[hsl(var(--color-brand-muted))] px-4 py-2.5 text-base leading-6 text-text-primary whitespace-pre-wrap break-words">
        {msg.text}
        <MessageActions text={msg.text} at={msg.at} side="right" className="mt-1" />
      </div>
    </div>
  );
}

function MetaLine({ msg }: { msg: ChatMessage }) {
  return (
    <div className="py-0.5">
      <div className={`text-center text-caption ${msg.kind === 'error' ? 'text-status-error' : 'text-text-tertiary'}`}>
        {msg.kind === 'error' ? '✕ ' : ''}{msg.text}
        <span className="ml-1 tabular-nums">{formatTime(msg.at)}</span>
      </div>
      {msg.detail && (
        <pre className="mx-auto mt-1 max-h-48 w-full max-w-2xl overflow-y-auto whitespace-pre-wrap break-words rounded-md bg-surface-base px-3 py-2 text-left font-mono text-[11px] leading-4 text-text-secondary">
          {msg.detail}
        </pre>
      )}
    </div>
  );
}

