import { MessageSquare, Plus, SendHorizonal, ShieldAlert } from 'lucide-react';
import { type ReactNode, useEffect, useMemo, useRef, useState } from 'react';
import { useSearchParams } from 'react-router-dom';
import { ApiError } from '../api/client';
import { resolveApproval } from '../api/endpoints';
import { AssistantTurn } from '../components/chat/assistant-turn';
import { Avatar } from '../components/avatar';
import { EmptyState } from '../components/async-state';
import { PresenceDot, runStatusColor, runStatusText } from '../components/status';
import { useAgentsStore } from '../stores/agents.store';
import { buildMessages, conversationLabel, aggregateRunStream, useChatStore, ACTIVE, type ChatMessage } from '../stores/chat.store';
import { useRunsStore } from '../stores/runs.store';
import { toast } from '../stores/toast.store';
import { formatTime } from '../utils/format';
import { promptRejectionReason } from '../utils/prompt';

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
            <div className="text-body text-text-primary truncate">{c.title}</div>
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
  const liveStream = useMemo(
    () => (latestRunId ? aggregateRunStream(timelines[latestRunId] ?? []) : { reasoning: '', answerDraft: '' }),
    [latestRunId, timelines],
  );
  const awaitingReply =
    !!latestRun && ACTIVE.has(latestRun.status) && !messages.some((m) => m.kind === 'assistant' && m.runId === latestRunId);

  useEffect(() => {
    const el = scrollRef.current;
    if (!el) return;
    el.scrollTo({ top: el.scrollHeight, behavior: 'smooth' });
  }, [messages.length, liveStream.reasoning.length, liveStream.answerDraft.length]);

  const pendingApprovals = latestRunId ? (approvals[latestRunId] ?? []).filter((a) => a.status === 'pending') : [];

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
        {renderTranscript(messages)}
        {awaitingReply && (
          <AssistantTurn
            reasoning={liveStream.reasoning}
            text={liveStream.answerDraft}
            streaming
            reasoningOnly={!liveStream.answerDraft && Boolean(liveStream.reasoning)}
          />
        )}
      </div>

      {/* 待审批卡片 */}
      {pendingApprovals.map((a) => (
        <div key={a.id} className="mx-comfortable mb-2 rounded-lg border border-status-warning/30 bg-status-warning/5 p-snug">
          <div className="flex items-center gap-1.5 text-body font-medium text-text-primary">
            <ShieldAlert className="w-4 h-4 text-status-warning" />
            审批请求 · {a.kind} · {a.risk}
          </div>
          <p className="text-caption text-text-secondary mt-1">{a.summary}</p>
          <div className="flex gap-2 mt-2">
            <ApprovalButton approvalId={a.id} runId={a.run_id} decision="approved" label="批准" />
            <ApprovalButton approvalId={a.id} runId={a.run_id} decision="rejected" label="拒绝" />
          </div>
        </div>
      ))}

      {/* 输入框 */}
      <div className="shrink-0 border-t border-border-subtle p-comfortable">
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
            placeholder={latestRun && !['succeeded', 'interrupted', 'cancelled', 'lost', 'failed'].includes(latestRun.status)
              ? '运行中：支持 steering 的 Runtime 可追加指令…'
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
      </div>
    </div>
  );
}

function renderTranscript(messages: ChatMessage[]) {
  const nodes: ReactNode[] = [];
  for (let i = 0; i < messages.length; i++) {
    const msg = messages[i];
    if (msg.kind === 'user') {
      nodes.push(<UserBubble key={msg.key} msg={msg} />);
      continue;
    }
    if (msg.kind === 'thinking') {
      const next = messages[i + 1];
      if (next?.kind === 'assistant' && next.runId === msg.runId) {
        nodes.push(
          <AssistantTurn
            key={msg.runId}
            reasoning={msg.text}
            text={next.text}
            at={next.at}
            reasoningOnly={!next.text && Boolean(msg.text)}
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
      nodes.push(<AssistantTurn key={msg.key} text={msg.text} at={msg.at} />);
      continue;
    }
    nodes.push(<MetaLine key={msg.key} msg={msg} />);
  }
  return nodes;
}

function UserBubble({ msg }: { msg: ChatMessage }) {
  return (
    <div className="flex justify-end py-1">
      <div className="max-w-[min(525px,82%)] rounded-[22px] bg-[hsl(var(--color-brand-muted))] px-4 py-2.5 text-base leading-6 text-text-primary whitespace-pre-wrap break-words">
        {msg.text}
        <span className="mt-1 block text-right text-[11px] tabular-nums text-text-tertiary">
          {formatTime(msg.at)}
        </span>
      </div>
    </div>
  );
}

function MetaLine({ msg }: { msg: ChatMessage }) {
  return (
    <div className={`text-center text-caption ${msg.kind === 'error' ? 'text-status-error' : 'text-text-tertiary'}`}>
      {msg.kind === 'error' ? '✕ ' : ''}{msg.text}
      <span className="ml-1 tabular-nums">{formatTime(msg.at)}</span>
    </div>
  );
}

function ApprovalButton({
  approvalId,
  runId,
  decision,
  label,
}: {
  approvalId: string;
  runId: string;
  decision: 'approved' | 'rejected';
  label: string;
}) {
  const fetchApprovals = useRunsStore((s) => s.fetchApprovals);
  const [busy, setBusy] = useState(false);
  return (
    <button
      disabled={busy}
      onClick={async () => {
        setBusy(true);
        try {
          let reason = '';
          if (decision === 'rejected') {
            const input = promptRejectionReason();
            if (input === null) return; // 用户取消弹窗：中止提交，不视为空理由拒绝
            reason = input;
          }
          await resolveApproval(approvalId, runId, decision, reason);
          await fetchApprovals(runId);
          toast.success(decision === 'approved' ? '已批准' : '已拒绝');
        } catch (err) {
          toast.error(err instanceof ApiError ? err.message : '操作失败');
        } finally {
          setBusy(false);
        }
      }}
      className={`text-caption rounded-button px-2 py-1 disabled:opacity-50 ${
        decision === 'approved'
          ? 'bg-status-success text-white'
          : 'border border-status-error/40 text-status-error'
      }`}
    >
      {label}
    </button>
  );
}
