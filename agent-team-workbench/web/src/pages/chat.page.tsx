import { BookOpen, Boxes, GitBranch, MessageSquare, Moon, PanelRight, Pin, PinOff, Plus, Search, Settings2, Sun } from 'lucide-react';
import { useEffect, useInsertionEffect, useMemo, useRef, useState, type ReactNode } from 'react';
import { Link, useSearchParams } from 'react-router-dom';
import { AgentTranscriptReader } from '../components/chat/transcript-view';
import { FileChangesCard } from '../components/chat/file-changes-card';
import { RunErrorBanner } from '../components/chat/run-error-banner';
import { ChatBottomDock } from '../components/chat/chat-bottom-dock';
import { ArtifactShelf } from '../components/chat/artifact-shelf';
import { ArtifactWorkspace } from '../components/chat/artifact-workspace';
import { SwarmMemberWorkspace, isSameSwarmMemberSelection, type SwarmMemberSelection } from '../components/chat/swarm-member-workspace';
import { PROMPT_LIBRARY, PromptBox } from '../components/chat/prompt-box';
import { Avatar } from '../components/avatar';
import { EmptyState } from '../components/ui';
import { SseStatusPill } from '../components/sse-status';
import { runStatusColor, runStatusText } from '../components/status';
import { useAgentsStore } from '../stores/agents.store';
import { buildMessages, conversationLabel, aggregateRunStream, formatTokenUsage, hideLiveRunDrafts, isRunLive, useChatStore, ACTIVE, TERMINAL, type ChatMessage } from '../stores/chat.store';
import { useChatPreferencesStore } from '../stores/chat-preferences.store';
import { mergeApprovalSegments, transcriptSegmentKey } from '../utils/approval-transcript';
import { conversationStatusDotClass, suggestedPrompts } from '../utils/chat-session-visuals';
import { useRunsStore } from '../stores/runs.store';
import type { WorkItem } from '../api/types';
import { REPLY_TIMEOUT_MS } from '../utils/chat-errors';
import { deriveChatDock } from '../utils/derive-chat-dock';
import {
  buildTranscriptSegments,
  injectPendingUsers,
  supplementUserFromTimeline,
} from '../utils/chronological-transcript';
import { buildAgentTranscriptProjection } from '../utils/agent-transcript-projection';
import {
  projectWorkActivityTimeline,
  type PresentedTranscriptSegment,
  type WorkActivityItem,
} from '../utils/work-activity-timeline';
import { runHasVisibleOutput } from '../utils/run-timeline';
import {
  isOutputTraceEnabled,
  outputTraceHash,
  stableOutputTraceJson,
  traceOutput,
  type OutputTraceInput,
} from '../utils/output-trace';
interface ProjectionTrace {
  signature: string;
  input: OutputTraceInput;
}

function useProjectionTrace(trace: ProjectionTrace | undefined): void {
  const previous = useRef('');
  useInsertionEffect(() => {
    if (!trace || previous.current === trace.signature) return;
    previous.current = trace.signature;
    traceOutput(trace.input);
  }, [trace]);
}

function messageTraceSnapshot(messages: readonly ChatMessage[]) {
  return messages.map((message) => ({
    key: message.key,
    runId: message.runId,
    kind: message.kind,
    text: message.text,
    detail: message.detail,
    liveOutput: message.liveOutput,
    itemType: message.itemType,
    phaseId: message.phaseId,
    contentBlocks: message.contentBlocks,
    toolStatus: message.toolStatus,
  }));
}

function workItemTraceSnapshot(item: WorkActivityItem): Record<string, unknown> {
  if (item.kind === 'activity') {
    return {
      kind: item.kind,
      runId: item.runId,
      items: item.items.map((tool) => ({
        key: tool.key,
        tool: tool.tool,
        toolStatus: tool.toolStatus,
        text: tool.text,
        detail: tool.detail,
        liveOutput: tool.liveOutput,
      })),
    };
  }
  if (item.kind === 'approval') {
    return { kind: item.kind, id: item.approval.id, status: item.approval.status };
  }
  if (item.kind === 'thinking-placeholder') return { kind: item.kind, runId: item.runId };
  return {
    kind: item.kind,
    key: item.kind === 'assistant' || item.kind === 'thinking'
      ? item.renderKey ?? item.msg.key
      : item.msg.key,
    runId: item.msg.runId,
    text: item.msg.text,
    streaming: 'streaming' in item ? item.streaming === true : false,
    contentBlocks: item.kind === 'assistant' ? item.msg.contentBlocks : undefined,
  };
}

function segmentTraceSnapshot(segments: readonly PresentedTranscriptSegment[]) {
  return segments.map((segment) => {
    if (segment.kind === 'thinking-placeholder') {
      return { kind: segment.kind, runId: segment.runId };
    }
    if (segment.kind === 'assistant' || segment.kind === 'user') {
      return {
        kind: segment.kind,
        key: segment.kind === 'assistant'
          ? segment.renderKey ?? segment.msg.key
          : segment.msg.key,
        runId: segment.msg.runId,
        text: segment.msg.text,
        streaming: 'streaming' in segment ? segment.streaming === true : false,
        contentBlocks: segment.msg.contentBlocks,
      };
    }
    if (segment.kind === 'work-timeline') {
      return {
        kind: segment.kind,
        runId: segment.runId,
        status: segment.status,
        createdAt: segment.createdAt,
        updatedAt: segment.updatedAt,
        items: segment.items.map(workItemTraceSnapshot),
      };
    }
    return segment;
  });
}

/** 对话页：Agent 选择器 + 会话列表 + 气泡消息流 + 输入框（协议 §5.2/§5.3）。 */
export default function ChatPage() {
  const agents = useAgentsStore((s) => s.agents);
  const agentId = useChatStore((s) => s.agentId);
  const conversationId = useChatStore((s) => s.conversationId);
  const selectAgent = useChatStore((s) => s.selectAgent);
  const openConversation = useChatStore((s) => s.openConversation);

  const [searchParams, setSearchParams] = useSearchParams();
  const [sidebarView, setSidebarView] = useState<SidebarView>('chats');
  const [promptSeed, setPromptSeed] = useState<{ id: number; text: string } | null>(null);
  const [chatTheme, setChatTheme] = useState<ChatTheme>(readInitialChatTheme);
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
    setSidebarView('chats');
    setPromptSeed(null);
    setSearchParams({ agent: id }, { replace: true });
  };
  const changeChatTheme = () => {
    const next = chatTheme === 'light' ? 'dark' : 'light';
    setChatTheme(next);
    if (typeof window !== 'undefined') {
      try {
        window.localStorage.setItem('chat:theme', next);
      } catch {
        // 存储不可用时仍保留本次页面状态。
      }
    }
  };

  return (
    <div className="tx-scope chat-languagegui-skin flex h-full min-h-0 w-full overflow-hidden" data-theme={chatTheme}>
      {/* 左栏：Agent 切换排 + 任务列表（对话即任务） */}
      <aside className="chat-languagegui-sidebar flex min-h-0 w-64 shrink-0 flex-col border-r border-border-subtle bg-surface-sunken">
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
        {agentId && (
          <>
            <ChatSidebarNav view={sidebarView} onChange={setSidebarView} />
            {sidebarView === 'chats' && <ConversationList onPick={(id) => {
              setPromptSeed(null);
              openConversation(id);
              setSearchParams(id ? { agent: agentId, c: id } : { agent: agentId }, { replace: true });
            }} />}
            {sidebarView === 'library' && <SidebarLibrary onUse={(text) => {
              openConversation(null);
              setSearchParams({ agent: agentId }, { replace: true });
              setPromptSeed({ id: Date.now(), text });
              setSidebarView('chats');
            }} />}
            {sidebarView === 'apps' && <SidebarApps />}
          </>
        )}
      </aside>

      {/* 右侧对话区 */}
      <div className="chat-languagegui-main flex-1 flex flex-col min-w-0 min-h-0 overflow-hidden">
        {agentId ? <ConversationPane key={`${agentId}:${promptSeed?.id ?? 'chat'}`} initialPrompt={promptSeed?.text ?? ''} chatTheme={chatTheme} onToggleTheme={changeChatTheme} /> : (
          <div className="flex min-h-0 flex-1 flex-col overflow-hidden">
            <ChatChrome
              left={<span className="text-body font-semibold text-text-primary">对话</span>}
              right={<><ChatThemeToggle theme={chatTheme} onToggle={changeChatTheme} /><SseStatusPill /></>}
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
    <div className="chat-chrome flex h-12 shrink-0 items-center justify-between border-b border-border-subtle bg-surface-base px-6">
      <div className="flex min-w-0 items-center gap-snug">{left}</div>
      <div className="flex shrink-0 items-center gap-2">{right}</div>
    </div>
  );
}

type ChatTheme = 'light' | 'dark';

function readInitialChatTheme(): ChatTheme {
  if (typeof window === 'undefined') return 'light';
  return window.localStorage.getItem('chat:theme') === 'dark' ? 'dark' : 'light';
}

function ChatThemeToggle({ theme, onToggle }: { theme: ChatTheme; onToggle: () => void }) {
  const dark = theme === 'dark';
  return (
    <button type="button" onClick={onToggle} className="inline-flex h-8 w-8 items-center justify-center rounded-button text-text-tertiary transition-colors hover:bg-surface-sunken hover:text-text-primary" aria-label={dark ? '切换到浅色模式' : '切换到暗色模式'} title={dark ? '浅色模式' : '暗色模式'} aria-pressed={dark}>
      {dark ? <Sun className="h-4 w-4" /> : <Moon className="h-4 w-4" />}
    </button>
  );
}

type SidebarView = 'chats' | 'library' | 'apps';

function ChatSidebarNav({ view, onChange }: { view: SidebarView; onChange: (view: SidebarView) => void }) {
  const items: Array<{ id: SidebarView; label: string; icon: ReactNode }> = [
    { id: 'chats', label: '对话', icon: <MessageSquare className="h-3.5 w-3.5" /> },
    { id: 'library', label: 'Library', icon: <BookOpen className="h-3.5 w-3.5" /> },
    { id: 'apps', label: 'Apps', icon: <Boxes className="h-3.5 w-3.5" /> },
  ];
  return (
    <nav className="chat-sidebar-nav" aria-label="对话资源">
      {items.map((item) => (
        <button key={item.id} type="button" aria-pressed={view === item.id} onClick={() => onChange(item.id)} className={`chat-sidebar-nav-item${view === item.id ? ' chat-sidebar-nav-item-active' : ''}`}>
          {item.icon}<span>{item.label}</span>
        </button>
      ))}
    </nav>
  );
}

function SidebarLibrary({ onUse }: { onUse: (prompt: string) => void }) {
  return (
    <section className="chat-sidebar-panel" aria-labelledby="chat-library-title">
      <div className="chat-sidebar-panel-head">
        <BookOpen className="h-4 w-4" aria-hidden />
        <span id="chat-library-title">Prompt Library</span>
      </div>
      <p className="px-snug pb-tight text-caption leading-5 text-text-tertiary">选择一个模板，在新对话中继续编辑后发送。</p>
      <div className="space-y-tight px-tight pb-snug">
        {PROMPT_LIBRARY.map((item) => (
          <button key={item.title} type="button" onClick={() => onUse(item.prompt)} className="w-full rounded-card border border-border-subtle bg-surface-raised px-snug py-tight text-left shadow-card transition-colors hover:border-brand-primary/30 hover:bg-brand-muted/20">
            <span className="block text-caption font-medium text-text-primary">{item.title}</span>
            <span className="mt-micro line-clamp-2 block text-caption leading-5 text-text-tertiary">{item.prompt}</span>
          </button>
        ))}
      </div>
    </section>
  );
}

function SidebarApps() {
  return (
    <section className="chat-sidebar-panel" aria-labelledby="chat-apps-title">
      <div className="chat-sidebar-panel-head">
        <Boxes className="h-4 w-4" aria-hidden />
        <span id="chat-apps-title">Apps</span>
      </div>
      <div className="space-y-tight px-tight pb-snug">
        <div className="chat-sidebar-app-card">
          <span><strong>LanguageGUI v1</strong><small>结构化正文输出</small></span>
          <b className="text-status-success">已启用</b>
        </div>
        <div className="chat-sidebar-app-card">
          <span><strong>外部 Apps</strong><small>连接器与第三方服务</small></span>
          <b className="text-text-tertiary">尚未配置</b>
        </div>
        <Link to="/agents" className="chat-sidebar-settings-link">
          <Settings2 className="h-3.5 w-3.5" aria-hidden />在 Agent 配置中管理工具权限
        </Link>
      </div>
    </section>
  );
}

function ConversationList({ onPick }: { onPick: (id: string | null) => void }) {
  const conversations = useChatStore((s) => s.conversations);
  const conversationId = useChatStore((s) => s.conversationId);
  const agentId = useChatStore((s) => s.agentId);
  const runSnapshots = useRunsStore((s) => s.runs);
  const [query, setQuery] = useState('');
  const [pinnedIds, setPinnedIds] = useState<string[]>([]);
  const searchRef = useRef<HTMLInputElement>(null);

  useEffect(() => {
    if (!agentId || typeof window === 'undefined') {
      setPinnedIds([]);
      return;
    }
    try {
      const value = JSON.parse(window.localStorage.getItem(`chat:pinned:${agentId}`) ?? '[]') as unknown;
      setPinnedIds(Array.isArray(value) ? value.filter((id): id is string => typeof id === 'string') : []);
    } catch {
      setPinnedIds([]);
    }
  }, [agentId]);

  useEffect(() => {
    const focusSearch = (event: globalThis.KeyboardEvent) => {
      if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === 'k') {
        event.preventDefault();
        searchRef.current?.focus();
      }
    };
    document.addEventListener('keydown', focusSearch);
    return () => document.removeEventListener('keydown', focusSearch);
  }, []);

  const togglePinned = (id: string) => {
    setPinnedIds((current) => {
      const next = current.includes(id) ? current.filter((value) => value !== id) : [...current, id];
      if (agentId && typeof window !== 'undefined') {
        try {
          window.localStorage.setItem(`chat:pinned:${agentId}`, JSON.stringify(next));
        } catch {
          // 浏览器禁用存储时保留本次会话内状态。
        }
      }
      return next;
    });
  };

  const normalizedQuery = query.trim().toLocaleLowerCase();
  const filtered = conversations.filter((conversation) => !normalizedQuery || conversation.title.toLocaleLowerCase().includes(normalizedQuery));
  const pinned = filtered.filter((conversation) => pinnedIds.includes(conversation.id));
  const history = filtered.filter((conversation) => !pinnedIds.includes(conversation.id));

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div className="px-tight pb-tight pt-tight">
        <div className="chat-conversation-search">
          <Search className="h-3.5 w-3.5 shrink-0 text-text-tertiary" aria-hidden />
          <input ref={searchRef} value={query} onChange={(event) => setQuery(event.target.value)} placeholder="搜索对话" aria-label="搜索对话" />
          <kbd>⌘K</kbd>
        </div>
      </div>
      <div className="flex items-center justify-between px-snug pb-tight">
        <span className="text-caption font-medium text-text-tertiary">对话（{filtered.length}）</span>
        <button
          onClick={() => onPick(null)}
          title="新对话"
          aria-label="新对话"
          className="inline-flex h-7 w-7 items-center justify-center rounded-button text-text-tertiary transition-colors hover:bg-surface-raised hover:text-text-primary"
        >
          <Plus className="w-4 h-4" />
        </button>
      </div>
      <div className="min-h-0 flex-1 overflow-y-auto px-2 pb-2">
        {pinned.length > 0 && (
          <ConversationGroup label="置顶" items={pinned} conversationId={conversationId} pinnedIds={pinnedIds} runSnapshots={runSnapshots} onPick={onPick} onTogglePinned={togglePinned} />
        )}
        {history.length > 0 && (
          <ConversationGroup label="历史" items={history} conversationId={conversationId} pinnedIds={pinnedIds} runSnapshots={runSnapshots} onPick={onPick} onTogglePinned={togglePinned} />
        )}
        {filtered.length === 0 && (
          <EmptyState title={query ? '没有匹配的对话' : '暂无会话'} description={query ? '换一个关键词试试' : '点右上角 + 开始新对话'} className="py-6" />
        )}
      </div>
    </div>
  );
}

function ConversationGroup({
  label,
  items,
  conversationId,
  pinnedIds,
  runSnapshots,
  onPick,
  onTogglePinned,
}: {
  label: string;
  items: WorkItem[];
  conversationId: string | null;
  pinnedIds: string[];
  runSnapshots: Record<string, { status: string }>;
  onPick: (id: string | null) => void;
  onTogglePinned: (id: string) => void;
}) {
  return (
    <section className="mb-snug" aria-label={label}>
      <div className="px-tight pb-micro text-caption font-medium uppercase tracking-wide text-text-tertiary">{label}</div>
      <div className="space-y-micro">
        {items.map((conversation) => {
          const isPinned = pinnedIds.includes(conversation.id);
          const selected = conversationId === conversation.id;
          return (
            <div key={conversation.id} className={`chat-conversation-row group${selected ? ' chat-conversation-row-active' : ''}`}>
              <button type="button" onClick={() => onPick(conversation.id)} className="min-w-0 flex-1 px-tight py-tight text-left">
                <div className="flex items-center gap-1 text-body text-text-primary">
                  {conversation.parent_id && <GitBranch className="h-3 w-3 shrink-0 text-text-tertiary" aria-label="分叉会话" />}
                  <span className="truncate font-medium">{conversation.title}</span>
                </div>
                <div className="mt-0.5 flex items-center gap-1.5 text-caption text-text-tertiary">
                  <span className={`h-1.5 w-1.5 shrink-0 rounded-full ${conversationStatusDotClass(conversation, runSnapshots)}`} aria-hidden />
                  {conversation.runs_count} 轮 · {conversationLabel(conversation, runSnapshots)}
                </div>
              </button>
              <button type="button" onClick={() => onTogglePinned(conversation.id)} className="chat-conversation-pin" aria-label={isPinned ? `取消置顶：${conversation.title}` : `置顶：${conversation.title}`} title={isPinned ? '取消置顶' : '置顶'}>
                {isPinned ? <PinOff className="h-3.5 w-3.5" /> : <Pin className="h-3.5 w-3.5" />}
              </button>
            </div>
          );
        })}
      </div>
    </section>
  );
}

function ConversationPane({ initialPrompt, chatTheme, onToggleTheme }: { initialPrompt: string; chatTheme: ChatTheme; onToggleTheme: () => void }) {
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
  const retryRun = useChatStore((s) => s.retryRun);
  const runAlerts = useChatStore((s) => s.runAlerts);
  const pendingUsers = useChatStore((s) => s.pendingUsers);
  const agents = useAgentsStore((s) => s.agents);
  const showReasoning = useChatPreferencesStore((state) => state.showReasoning);
  const groupExploreTools = useChatPreferencesStore((state) => state.groupExploreTools);
  const groupTerminalTools = useChatPreferencesStore((state) => state.groupTerminalTools);
  const groupChangesTools = useChatPreferencesStore((state) => state.groupChangesTools);

  const timelines = useRunsStore((s) => s.timelines);
  const runSnapshots = useRunsStore((s) => s.runs);
  const watchRun = useRunsStore((s) => s.watchRun);
  const unwatchRun = useRunsStore((s) => s.unwatchRun);
  const approvals = useRunsStore((s) => s.approvals);

  const [draft, setDraft] = useState(initialPrompt);
  const [workspaceOpen, setWorkspaceOpen] = useState(false);
  const [selectedSwarmMember, setSelectedSwarmMember] = useState<SwarmMemberSelection | null>(null);
  const scrollRef = useRef<HTMLDivElement>(null);
  const textareaRef = useRef<HTMLTextAreaElement>(null);
  const followStreamRef = useRef(true);
  const approvalAnchorsRef = useRef<Record<string, string>>({});
  const [approvalAnchors, setApprovalAnchors] = useState<Record<string, string>>({});

  useEffect(() => {
    if (initialPrompt) textareaRef.current?.focus();
  }, [initialPrompt]);

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
  const latestRunNotice = latestRunId ? runAlerts[latestRunId] : undefined;

  // 订阅当前会话所有 run，确保历史轮次消息可回放。
  useEffect(() => {
    for (const id of runIds) watchRun(id);
    return () => {
      for (const id of runIds) unwatchRun(id);
    };
  }, [runIds, watchRun, unwatchRun]);

  const messages = useMemo(() => buildMessages(runIds, timelines), [runIds, timelines]);
  const selectedMember = useMemo(() => {
    if (!selectedSwarmMember) return undefined;
    const swarmMessage = messages.find((message) => message.runId === selectedSwarmMember.runId && message.kind === 'swarm' && message.swarm?.id === selectedSwarmMember.swarmId);
    if (swarmMessage?.swarm) return swarmMessage.swarm.members.find((member) => member.id === selectedSwarmMember.memberId);
    return messages.find((message) => message.runId === selectedSwarmMember.runId && message.kind === 'subagent' && message.childAgent?.id === selectedSwarmMember.memberId)?.childAgent;
  }, [messages, selectedSwarmMember]);
  const liveStream = useMemo(
    () => (latestRunId ? aggregateRunStream(timelines[latestRunId] ?? []) : { reasoning: '', answerDraft: '' }),
    [latestRunId, timelines],
  );
  const liveRunActive = isRunLive(latestRun?.status);
  const messagesProjectionTrace = useMemo<ProjectionTrace | undefined>(() => {
    if (!isOutputTraceEnabled()) return undefined;
    const snapshot = stableOutputTraceJson(messageTraceSnapshot(messages));
    const hash = outputTraceHash(snapshot);
    const latestAssistant = [...messages].reverse().find((message) => message.kind === 'assistant');
    const contentBlockMessages = messages.filter((message) => message.contentBlocks);
    return {
      signature: `${liveRunActive ? 'streaming' : 'final'}:${hash}`,
      input: {
        stage: 'messages.projected',
        mode: liveRunActive ? 'streaming' : 'final',
        source: 'projection',
        runId: latestRunId,
        messageId: latestAssistant?.key,
        text: latestAssistant?.text,
        projection: {
          messages: messages.length,
          assistantMessages: messages.filter((message) => message.kind === 'assistant').length,
          thinkingMessages: messages.filter((message) => message.kind === 'thinking').length,
          toolMessages: messages.filter((message) => message.toolStatus !== undefined).length,
          contentBlocks: contentBlockMessages.reduce((total, message) => total + (message.contentBlocks?.blocks.length ?? 0), 0),
          blockTypes: contentBlockMessages.flatMap((message) => message.contentBlocks?.blocks.map((block) => block.type) ?? []),
          hash,
        },
      },
    };
  }, [latestRunId, liveRunActive, messages]);
  useProjectionTrace(messagesProjectionTrace);

  const liveDraftProjectionTrace = useMemo<ProjectionTrace | undefined>(() => {
    if (!isOutputTraceEnabled()) return undefined;
    const liveSnapshot = stableOutputTraceJson(liveStream);
    const liveHash = outputTraceHash(liveSnapshot);
    return {
      signature: `${liveRunActive ? 'streaming' : 'final'}:${liveHash}`,
      input: {
        stage: 'live.draft',
        mode: liveRunActive ? 'streaming' : 'final',
        source: 'projection',
        runId: latestRunId,
        text: liveStream.answerDraft,
        projection: { hash: liveHash },
        metadata: {
          answerChars: liveStream.answerDraft.length,
          reasoningChars: liveStream.reasoning.length,
          reasoningHash: outputTraceHash(liveStream.reasoning),
        },
      },
    };
  }, [latestRunId, liveRunActive, liveStream]);
  useProjectionTrace(liveDraftProjectionTrace);
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
    const listedRuns = new Map(runs.map((run) => [run.id, run]));
    for (const id of runIds) {
      const status = runSnapshots[id]?.status ?? listedRuns.get(id)?.status;
      if (status) map[id] = status;
    }
    return map;
  }, [runIds, runSnapshots, runs]);
  const runTimings = useMemo(() => {
    const map: Record<string, { createdAt?: string; updatedAt?: string }> = {};
    const listedRuns = new Map(runs.map((run) => [run.id, run]));
    for (const id of runIds) {
      const run = runSnapshots[id] ?? listedRuns.get(id);
      if (!run) continue;
      map[id] = { createdAt: run.created_at, updatedAt: run.updated_at };
    }
    return map;
  }, [runIds, runSnapshots, runs]);
  const selectedMemberSegments = useMemo(() => {
    if (!selectedSwarmMember || !selectedMember) return undefined;
    const childStatus = selectedMember.status === 'completed' ? 'succeeded' : selectedMember.status === 'failed' ? 'failed' : selectedMember.status === 'stopped' ? 'interrupted' : selectedMember.status === 'waiting' ? 'waiting_approval' : selectedMember.status;
    return buildAgentTranscriptProjection({
      runId: selectedSwarmMember.runId,
      agentId: selectedMember.id,
      runStatus: childStatus,
      timeline: timelines[selectedSwarmMember.runId] ?? [],
      showReasoning,
      toolGrouping: {
        groupExplore: groupExploreTools,
        groupExecute: groupTerminalTools,
        groupChanges: groupChangesTools,
      },
    });
  }, [groupChangesTools, groupExploreTools, groupTerminalTools, selectedMember, selectedSwarmMember, showReasoning, timelines]);
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
        showReasoning,
        toolGrouping: {
          groupExplore: groupExploreTools,
          groupExecute: groupTerminalTools,
          groupChanges: groupChangesTools,
        },
      }),
    [
      transcriptMessages,
      runStatuses,
      latestRunId,
      liveStream,
      liveRunActive,
      hasPendingApproval,
      pendingUsers,
      messages,
      showReasoning,
      groupExploreTools,
      groupTerminalTools,
      groupChangesTools,
    ],
  );

  useEffect(() => {
    setApprovalAnchors({});
    approvalAnchorsRef.current = {};
    setSelectedSwarmMember(null);
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
  const presentedSegments = useMemo(
    () => projectWorkActivityTimeline(transcriptSegments, {
      runStatuses,
      timingByRun: runTimings,
    }),
    [transcriptSegments, runStatuses, runTimings],
  );
  const transcriptProjectionTrace = useMemo<ProjectionTrace | undefined>(() => {
    if (!isOutputTraceEnabled()) return undefined;
    const snapshot = stableOutputTraceJson(segmentTraceSnapshot(presentedSegments));
    const hash = outputTraceHash(snapshot);
    const timelineItems = presentedSegments.flatMap((segment) => segment.kind === 'work-timeline' ? segment.items : []);
    const assistantMessages = presentedSegments.flatMap((segment) => {
      if (segment.kind === 'assistant') return [segment];
      if (segment.kind === 'work-timeline') return segment.items.filter((item) => item.kind === 'assistant');
      return [];
    });
    const latestAssistant = assistantMessages.at(-1);
    const contentBlockMessages = assistantMessages.filter((segment) => segment.msg.contentBlocks);
    return {
      signature: `${liveRunActive ? 'streaming' : 'final'}:${hash}`,
      input: {
        stage: 'transcript.projected',
        mode: liveRunActive ? 'streaming' : 'final',
        source: 'projection',
        runId: latestRunId,
        messageId: latestAssistant?.renderKey ?? latestAssistant?.msg.key,
        text: latestAssistant?.msg.text,
        projection: {
          messages: presentedSegments.length,
          workTimelines: presentedSegments.filter((segment) => segment.kind === 'work-timeline').length,
          assistantMessages: assistantMessages.length,
          thinkingMessages: timelineItems.filter((item) => item.kind === 'thinking').length,
          toolMessages: timelineItems.reduce(
            (total, item) => total + (item.kind === 'activity' ? item.items.length : 0),
            0,
          ),
          contentBlocks: contentBlockMessages.reduce(
            (total, segment) => total + (segment.msg.contentBlocks?.blocks.length ?? 0),
            0,
          ),
          blockTypes: contentBlockMessages.flatMap(
            (segment) => segment.msg.contentBlocks?.blocks.map((block) => block.type) ?? [],
          ),
          hash,
        },
      },
    };
  }, [latestRunId, liveRunActive, presentedSegments]);
  useProjectionTrace(transcriptProjectionTrace);
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
  }, [presentedSegments.length, liveStream.reasoning, liveStream.answerDraft, runApprovals.length]);

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
    void send(text);
  };

  const applyPrompt = (text: string) => {
    setDraft(text);
    textareaRef.current?.focus();
  };

  return (
    <div className="flex flex-1 min-h-0 overflow-hidden">
      <div className="chat-languagegui-main flex min-h-0 min-w-0 flex-1 flex-col overflow-hidden">
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
            <ChatThemeToggle theme={chatTheme} onToggle={onToggleTheme} />
            {conversationArtifacts.length > 0 && (
              <button
                type="button"
                title={workspaceOpen ? '关闭工作区' : '打开工作区'}
                aria-label={workspaceOpen ? '关闭工作区' : '打开工作区'}
                aria-pressed={workspaceOpen}
                onClick={() => { setSelectedSwarmMember(null); setWorkspaceOpen((v) => !v); }}
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
        className="chat-languagegui-transcript relative min-h-0 flex-1 overflow-y-auto overscroll-contain px-6 py-comfortable"
      >
        {messages.length === 0 && presentedSegments.length === 0 && (
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
          <AgentTranscriptReader
            segments={presentedSegments}
            onFork={(key) => void forkConversation(key)}
            agent={agent ? { name: agent.name, avatar: agent.avatar } : undefined}
            selectedSwarmMemberKey={selectedSwarmMember ? `${selectedSwarmMember.runId}:${selectedSwarmMember.swarmId}:${selectedSwarmMember.memberId}` : undefined}
            onSelectSwarmMember={(runId, swarmId, member) => {
              setWorkspaceOpen(false);
              const next = { runId, swarmId, memberId: member.id };
              setSelectedSwarmMember((current) => isSameSwarmMemberSelection(current, next) ? null : next);
            }}
          />
          {latestRunId && latestRun && TERMINAL.has(latestRun.status) && (
            <FileChangesCard runId={latestRunId} />
          )}
        </div>
      </div>

      {/* 底部固定：成果摘要 + 计划 / 目标 + 一体化输入卡 */}
      <div className="chat-bottom-region shrink-0 border-t border-border-subtle bg-surface-base px-6 pb-4 pt-2">
        <div className="chat-composer-stack">
          {latestRunNotice?.code === 'reply_timeout' && (
            <div
              className="rounded-button border border-status-warning/30 bg-status-warning/5 px-snug py-tight text-caption text-status-warning"
              role="status"
              aria-live="polite"
            >
              {latestRunNotice.message}
            </div>
          )}
          {latestRun && latestRun.status === 'failed' && (
            <RunErrorBanner run={latestRun} onRetry={retryRun} />
          )}
          {conversationArtifacts.length > 0 && (
            <div className="mb-2">
              <ArtifactShelf artifacts={conversationArtifacts} onOpen={() => { setSelectedSwarmMember(null); setWorkspaceOpen(true); }} />
            </div>
          )}
          <ChatBottomDock workflow={dock.workflow} runStatus={latestRun?.status} />
          <PromptBox
            key={conversationId ?? 'new-conversation'}
            draft={draft}
            onDraftChange={setDraft}
            onSend={doSend}
            placeholder={latestRun && !TERMINAL.has(latestRun.status)
              ? '运行中，消息将进入队列，完成后自动发送'
              : '输入消息，Enter 发送，Shift+Enter 换行'}
            inputRef={textareaRef}
            queue={queue}
            onRemoveQueued={removeQueued}
            canDrainQueue={!!latestRun && TERMINAL.has(latestRun.status) && latestRun.status !== 'succeeded'}
            onDrainQueue={() => void drainQueue()}
            sending={sending}
            runInFlight={runInFlight}
            stopping={!!latestRunId && stoppingRunId === latestRunId}
            onStop={() => latestRunId && void stopActiveRun(latestRunId, 'user_stopped')}
            usageText={usageText}
          />
        </div>
      </div>
      </div>
      {workspaceOpen && (
        <ArtifactWorkspace artifacts={conversationArtifacts} onClose={() => setWorkspaceOpen(false)} />
      )}
      {selectedSwarmMember && selectedMember && !workspaceOpen && (
        <SwarmMemberWorkspace runId={selectedSwarmMember.runId} member={selectedMember} segments={selectedMemberSegments?.segments} onClose={() => setSelectedSwarmMember(null)} />
      )}
    </div>
  );
}
