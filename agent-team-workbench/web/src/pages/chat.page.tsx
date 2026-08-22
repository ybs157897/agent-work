import { MessageSquare, Plus, SendHorizonal, ShieldAlert } from 'lucide-react';
import { useEffect, useMemo, useRef, useState } from 'react';
import { useSearchParams } from 'react-router-dom';
import { ApiError } from '../api/client';
import { resolveApproval } from '../api/endpoints';
import { Avatar } from '../components/avatar';
import { EmptyState } from '../components/async-state';
import { PresenceDot, runStatusColor, runStatusText } from '../components/status';
import { useAgentsStore } from '../stores/agents.store';
import { buildMessages, useChatStore } from '../stores/chat.store';
import { useRunsStore } from '../stores/runs.store';
import { toast } from '../stores/toast.store';
import { formatTime } from '../utils/format';

/** 对话页：Agent 选择器 + 会话列表 + 气泡消息流 + 输入框（协议 §5.2/§5.3）。 */
export default function ChatPage() {
  const agents = useAgentsStore((s) => s.agents);
  const agentId = useChatStore((s) => s.agentId);
  const selectAgent = useChatStore((s) => s.selectAgent);
  const openConversation = useChatStore((s) => s.openConversation);

  const [searchParams, setSearchParams] = useSearchParams();

  // URL 初始值（如从 Agent 详情「发起对话」跳入）。
  useEffect(() => {
    const qAgent = searchParams.get('agent');
    const qConv = searchParams.get('c');
    if (qAgent && qAgent !== agentId) selectAgent(qAgent);
    if (qConv) openConversation(qConv);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const pick = (id: string) => {
    selectAgent(id);
    setSearchParams({ agent: id }, { replace: true });
  };

  return (
    <div className="h-full flex min-h-0">
      {/* 左栏：Agent 选择器 + 会话列表 */}
      <div className="w-64 shrink-0 border-r border-border-subtle bg-surface-raised flex flex-col min-h-0">
        <div className="p-3 border-b border-border-subtle">
          <h3 className="text-body font-semibold text-text-primary">选择 Agent</h3>
        </div>
        <div className="overflow-y-auto p-2 space-y-1 max-h-64">
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
      <div className="flex-1 flex flex-col min-w-0 min-h-0">
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
              {c.runs_count} 轮 · {c.status}
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
  const bottomRef = useRef<HTMLDivElement>(null);

  const agent = agents.find((a) => a.id === agentId);
  const conversation = conversations.find((c) => c.id === conversationId);
  const runIds = useMemo(() => runs.map((r) => r.id), [runs]);
  const latestRunId = runIds[runIds.length - 1];
  const latestRun = latestRunId ? runSnapshots[latestRunId] ?? runs[runs.length - 1] : undefined;

  // 订阅最新 run 的实时事件与历史回放。
  useEffect(() => {
    if (!latestRunId) return;
    watchRun(latestRunId);
    return () => unwatchRun(latestRunId);
  }, [latestRunId, watchRun, unwatchRun]);

  const messages = useMemo(() => buildMessages(runIds, timelines), [runIds, timelines]);

  useEffect(() => {
    bottomRef.current?.scrollIntoView({ behavior: 'smooth' });
  }, [messages.length]);

  const pendingApprovals = latestRunId ? (approvals[latestRunId] ?? []).filter((a) => a.status === 'pending') : [];

  const doSend = () => {
    const text = draft.trim();
    if (!text) return;
    setDraft('');
    void send(text);
  };

  return (
    <>
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
      <div className="flex-1 overflow-y-auto px-comfortable py-base space-y-3 min-h-0">
        {messages.length === 0 && (
          <div className="text-center text-caption text-text-tertiary py-12">
            输入第一条消息，为该 Agent 创建任务并开始运行
          </div>
        )}
        {messages.map((m) => <Bubble key={m.key} msg={m} agentName={agent?.name ?? ''} />)}
        <div ref={bottomRef} />
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
              ? '运行中：输入将追加为 steering 指令…'
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
    </>
  );
}

function Bubble({ msg, agentName }: { msg: ReturnType<typeof buildMessages>[number]; agentName: string }) {
  if (msg.kind === 'tool' || msg.kind === 'system' || msg.kind === 'error') {
    return (
      <div className={`text-center text-caption ${msg.kind === 'error' ? 'text-status-error' : 'text-text-tertiary'}`}>
        {msg.kind === 'error' ? '✕ ' : ''}{msg.text}
        <span className="ml-1 tabular-nums">{formatTime(msg.at)}</span>
      </div>
    );
  }
  const isUser = msg.kind === 'user';
  return (
    <div className={`flex ${isUser ? 'justify-end' : 'justify-start'}`}>
      <div
        className={`max-w-[70%] rounded-xl px-snug py-tight text-body whitespace-pre-wrap break-words ${
          isUser
            ? 'bg-brand-primary text-white rounded-br-sm'
            : 'bg-surface-raised border border-border-subtle text-text-primary rounded-bl-sm shadow-level-1'
        }`}
        title={isUser ? '我' : agentName}
      >
        {msg.text}
        <span className={`block text-right text-[10px] mt-0.5 tabular-nums ${isUser ? 'text-white/70' : 'text-text-tertiary'}`}>
          {formatTime(msg.at)}
        </span>
      </div>
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
          const reason = decision === 'rejected' ? window.prompt('拒绝原因（可选）') ?? '' : '';
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
