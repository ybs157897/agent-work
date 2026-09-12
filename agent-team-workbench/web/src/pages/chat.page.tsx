import { ArchiveRestore, BookOpen, Boxes, Code2, GitBranch, ListChecks, LoaderCircle, MessageSquare, Moon, PanelLeft, PanelRight, Pin, PinOff, Plus, Search, Settings2, Sun } from 'lucide-react';
import { useCallback, useEffect, useInsertionEffect, useLayoutEffect, useMemo, useRef, useState, type ReactNode } from 'react';
import { Link, useLocation, useSearchParams } from 'react-router-dom';
import { AgentTranscriptReader } from '../components/chat/transcript-view';
import { ChatAnalysisPanel } from '../components/chat/chat-analysis-panel';
import { ChatDecisionPanel } from '../components/chat/chat-decision-panel';
import { TaskDraftPreview } from '../components/chat/task-draft-preview';
import { ChatSourceShelf } from '../components/chat/chat-source-shelf';
import { CodeWorkspace } from '../components/code-workspace/code-workspace';
import { FileChangesCard } from '../components/chat/file-changes-card';
import { RunErrorBanner } from '../components/chat/run-error-banner';
import { NativeQuestionCard } from '../components/chat/native-question-card';
import { SendErrorNotice } from '../components/chat/send-error-notice';
import { ChatBottomDock } from '../components/chat/chat-bottom-dock';
import { ArtifactShelf } from '../components/chat/artifact-shelf';
import { ArtifactWorkspace } from '../components/chat/artifact-workspace';
import { SwarmMemberWorkspace, isSameSwarmMemberSelection, type SwarmMemberSelection } from '../components/chat/swarm-member-workspace';
import { PROMPT_LIBRARY, PromptBox, type PromptAttachment } from '../components/chat/prompt-box';
import { Avatar } from '../components/avatar';
import { Button, EmptyState } from '../components/ui';
import { SseStatusPill } from '../components/sse-status';
import { runStatusColor, runStatusText } from '../components/status';
import { useAgentsStore } from '../stores/agents.store';
import { useWorkspaceStore } from '../stores/workspace.store';
import { CHAT_ANALYSIS_INSTRUCTION, CHAT_ANALYSIS_OUTPUT_CONTRACT, useChatAnalysisStore } from '../stores/chat-analysis.store';
import { CHAT_ANALYSIS_CHANGE_INSTRUCTION, useChatDecisionsStore } from '../stores/chat-decisions.store';
import { usePublicationStore } from '../stores/publication.store';
import { useWorkbenchThemeStore, type WorkbenchTheme } from '../stores/workbench-theme.store';
import { buildMessages, conversationLabel, aggregateRunStream, formatTokenUsage, hideLiveRunDrafts, isRunLive, useChatStore, ACTIVE, TERMINAL, type ChatAttachmentInput, type ChatMessage } from '../stores/chat.store';
import { useChatPreferencesStore } from '../stores/chat-preferences.store';
import { readChatWorkspaceState, readLegacyTaskIntakeRecovery, useChatWorkspaceStorageNotice, writeChatWorkspaceState, type ChatComposerDraft, type LegacyTaskIntakeRecovery, type LegacyTaskIntakeRecoveryEntry } from '../stores/chat-workspace-state';
import { mergeApprovalSegments, transcriptSegmentKey } from '../utils/approval-transcript';
import { conversationStatusDotClass, suggestedPrompts } from '../utils/chat-session-visuals';
import { useRunsStore } from '../stores/runs.store';
import { useNativeQuestionsStore } from '../stores/questions.store';
import type { WorkItem } from '../api/types';
import { REPLY_TIMEOUT_MS } from '../utils/chat-errors';
import { isChatAgent, isKnowledgeLibrarianAgent, isUserManagedAgent } from '../utils/agent-scope';
import { isChatPath } from '../utils/route-layout';
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

function routePath(route: string): string {
  return route.split(/[?#]/, 1)[0] || '/';
}

/** Outgoing Chat must not claim a new Workspace while LayoutShell is restoring
 * that Workspace's remembered route. */
export function shouldRebindChatOwner(
  previousWorkspaceId: string | null,
  currentWorkspaceId: string,
  currentPath: string,
  rememberedRoute: string | null,
): boolean {
  if (!previousWorkspaceId || previousWorkspaceId === currentWorkspaceId) return true;
  return routePath(currentPath) === routePath(rememberedRoute ?? '/chat');
}

/** Keep a deep-linked conversation query while its async hydration is pending. */
export function shouldPreserveChatDeepLink(
  requestedConversation: string | null,
  pendingConversation: string | null,
  activeConversation: string | null,
): boolean {
  if (!requestedConversation) return false;
  return requestedConversation === pendingConversation || requestedConversation === activeConversation;
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
  const allAgents = useAgentsStore((s) => s.agents);
  const agents = useMemo(
    () => allAgents.filter(isChatAgent),
    [allAgents],
  );
  const agentId = useChatStore((s) => s.agentId);
  const conversationId = useChatStore((s) => s.conversationId);
  const runsLoadedConversationId = useChatStore((s) => s.runsLoadedConversationId);
  const selectAgent = useChatStore((s) => s.selectAgent);
  const startConversation = useChatStore((s) => s.startConversation);
  const openConversation = useChatStore((s) => s.openConversation);
  const restoreWorkspace = useChatStore((s) => s.restoreWorkspace);

  const [searchParams, setSearchParams] = useSearchParams();
  const location = useLocation();
  const [sidebarView, setSidebarView] = useState<SidebarView>('chats');
  const [promptSeed, setPromptSeed] = useState<{ id: number; text: string } | null>(null);
  const [legacyRecovery, setLegacyRecovery] = useState<LegacyTaskIntakeRecovery | null>(null);
  const chatTheme = useWorkbenchThemeStore((state) => state.theme);
  const changeChatTheme = useWorkbenchThemeStore((state) => state.toggleTheme);
  const urlBooted = useRef(false);
  const pendingUrlConversationRef = useRef<string | null>(null);
  const lastWorkspaceRef = useRef<string | null>(null);
  const pageOwnerRef = useRef<{ workspaceId: string; generation: number } | null>(null);
  const [pageOwner, setPageOwner] = useState<{ workspaceId: string; generation: number } | null>(null);
  const activePathRef = useRef(location.pathname);
  const workspaceId = useWorkspaceStore((state) => state.workspace?.id);
  const workspaceProjectReady = useWorkspaceStore((state) => {
    const workspace = state.workspace;
    return (!workspace?.project || workspace.project.status === 'ready') && (!workspace?.setup || workspace.setup.status === 'ready');
  });
  const generation = useWorkspaceStore((state) => state.generation);
  const lastRouteFor = useWorkspaceStore((state) => state.lastRouteFor);
  const chatPathActive = isChatPath(location.pathname);
  const ownsChatScope = chatPathActive
    && pageOwner?.workspaceId === workspaceId
    && pageOwner?.generation === generation;
  const chatNavigationParams = useCallback((nextAgentId: string, nextConversationId?: string) => {
    const next = new URLSearchParams();
    if (workspaceId) next.set('ws', workspaceId);
    next.set('agent', nextAgentId);
    if (nextConversationId) next.set('c', nextConversationId);
    return next;
  }, [workspaceId]);
  const currentAgent = agents.find((agent) => agent.id === agentId);
  const [codeOverrides, setCodeOverrides] = useState<Record<string, boolean>>({});
  const [workspaceNavigationOpen, setWorkspaceNavigationOpen] = useState(false);
  const [narrowPanel, setNarrowPanel] = useState<'document' | 'chat'>('document');
  const codeKey = `${workspaceId ?? ''}:${agentId ?? ''}:${conversationId ?? 'new'}`;
  const codeAvailable = workspaceProjectReady && !!currentAgent && currentAgent.role === 'developer' && currentAgent.availability === 'enabled' && isUserManagedAgent(currentAgent);
  const codeEnabled = codeAvailable && !!workspaceId && (codeOverrides[codeKey]
    ?? (searchParams.get('canvas') === 'code' && searchParams.get('agent') === agentId ? true : false));
  const codeRunsLoaded = !conversationId || runsLoadedConversationId === conversationId;

  useEffect(() => {
    setLegacyRecovery(workspaceId ? readLegacyTaskIntakeRecovery(workspaceId) : null);
  }, [workspaceId]);

  useLayoutEffect(() => {
    activePathRef.current = location.pathname;
  }, [location.pathname]);

  useEffect(() => {
    if (!chatPathActive || !workspaceId) return;
    const previousOwner = pageOwnerRef.current;
    if (previousOwner && !shouldRebindChatOwner(previousOwner.workspaceId, workspaceId, location.pathname, lastRouteFor(workspaceId))) return;
    if (!pageOwnerRef.current || pageOwnerRef.current.workspaceId !== workspaceId || pageOwnerRef.current.generation !== generation) {
      const nextOwner = { workspaceId, generation };
      pageOwnerRef.current = nextOwner;
      setPageOwner(nextOwner);
    }
  }, [chatPathActive, generation, lastRouteFor, location.pathname, workspaceId]);

  const isChatNavigationCurrent = useCallback(() => {
    const current = useWorkspaceStore.getState();
    return isChatPath(activePathRef.current)
      && pageOwnerRef.current?.workspaceId === current.workspace?.id
      && pageOwnerRef.current?.generation === current.generation;
  }, []);

  const restoreLegacy = (entryIndex = 0) => {
    if (!legacyRecovery) return;
    const entry = legacyRecovery.entries[entryIndex] ?? legacyRecovery;
    const content = formatLegacyRecoveryContent(entry);
    if (!content) return;
    const targetAgent = currentAgent ?? agents.find((agent) => isChatAgent(agent));
    if (!targetAgent) return;
    selectAgent(targetAgent.id);
    openConversation(null);
    setPromptSeed({ id: Date.now(), text: content });
    setSidebarView('chats');
    setSearchParams(chatNavigationParams(targetAgent.id), { replace: true });
  };

  // Workspace is the outer identity boundary. Restore the last Agent and
  // conversation only after the target bootstrap has made it current; on a
  // real A → B switch, discard URL-local Agent parameters so B cannot inherit
  // A's deep link accidentally.
  useEffect(() => {
    if (!workspaceId || !ownsChatScope) return;
    const switched = lastWorkspaceRef.current !== null && lastWorkspaceRef.current !== workspaceId;
    lastWorkspaceRef.current = workspaceId;
    restoreWorkspace(workspaceId);
    urlBooted.current = false;
    pendingUrlConversationRef.current = null;
    if (switched) {
      const next = new URLSearchParams(searchParams);
      next.set('ws', workspaceId);
      next.delete('agent');
      next.delete('c');
      next.delete('new');
      next.delete('canvas');
      setSearchParams(next, { replace: true });
    }
  }, [ownsChatScope, restoreWorkspace, searchParams, setSearchParams, workspaceId]);
  const toggleCode = () => {
    if (!workspaceId || !agentId || !codeAvailable) return;
    const enabled = !codeEnabled;
    setCodeOverrides((current) => ({ ...current, [codeKey]: enabled }));
    const params = new URLSearchParams(searchParams);
    if (workspaceId) params.set('ws', workspaceId);
    if (enabled) {
      params.set('agent', agentId);
      params.set('canvas', 'code');
    } else {
      params.delete('canvas');
    }
    setSearchParams(params, { replace: true });
    setWorkspaceNavigationOpen(false);
    setNarrowPanel('document');
  };
  const freshConversation = searchParams.get('new') === '1';

  // URL 初始值（如从 Agent 详情「发起对话」跳入）。
  useEffect(() => {
    if (!ownsChatScope || urlBooted.current) return;
    const qAgent = searchParams.get('agent');
    const qConv = searchParams.get('c');
    if (qAgent && agents.length === 0) return;
    if (qAgent && agents.length > 0 && !agents.some((agent) => agent.id === qAgent)) {
      const next = new URLSearchParams(searchParams);
      if (workspaceId) next.set('ws', workspaceId);
      next.delete('agent');
      next.delete('c');
      next.delete('new');
      next.delete('canvas');
      setSearchParams(next, { replace: true });
      urlBooted.current = true;
      return;
    }
    if (freshConversation && qAgent) {
      pendingUrlConversationRef.current = null;
      startConversation(qAgent);
      setPromptSeed({ id: Date.now(), text: '' });
      setSidebarView('chats');
      const next = new URLSearchParams(searchParams);
      next.delete('new');
      next.delete('c');
      setSearchParams(next, { replace: true });
      urlBooted.current = true;
      return;
    }
    if (qAgent && qAgent !== agentId) selectAgent(qAgent);
    if (qConv) {
      pendingUrlConversationRef.current = qConv;
      void openConversation(qConv).then((opened) => {
        if (pendingUrlConversationRef.current === qConv) pendingUrlConversationRef.current = null;
        if (opened || !isChatNavigationCurrent()) return;
        const currentParams = new URLSearchParams(window.location.search);
        if (currentParams.get('c') !== qConv) return;
        const next = new URLSearchParams(currentParams);
        if (workspaceId) next.set('ws', workspaceId);
        next.delete('c');
        setSearchParams(next, { replace: true });
      });
    }
    urlBooted.current = true;
  }, [freshConversation, searchParams, agents, agentId, selectAgent, startConversation, openConversation, setSearchParams, workspaceId, ownsChatScope, isChatNavigationCurrent]);

  // 新会话创建时同步 ?c=，便于刷新后恢复。
  useEffect(() => {
    if (!ownsChatScope || freshConversation || !urlBooted.current || !agentId) return;
    const current = useChatStore.getState();
    if (current.agentId !== agentId || current.conversationId !== conversationId) return;
    const qAgent = searchParams.get('agent');
    const qConv = searchParams.get('c');
    if (shouldPreserveChatDeepLink(qConv, pendingUrlConversationRef.current, conversationId)) return;
    const next = new URLSearchParams(searchParams);
    if (workspaceId) next.set('ws', workspaceId);
    next.set('agent', agentId);
    if (conversationId) {
      if (qAgent === agentId && qConv === conversationId) return;
      next.set('c', conversationId);
      setSearchParams(next, { replace: true });
      return;
    }
    if (qAgent === agentId && !qConv) return;
    next.delete('c');
    setSearchParams(next, { replace: true });
  }, [agentId, conversationId, freshConversation, searchParams, setSearchParams, workspaceId, ownsChatScope]);

  const pick = (id: string) => {
    selectAgent(id);
    setSidebarView('chats');
    setPromptSeed(null);
    setWorkspaceNavigationOpen(false);
    setSearchParams(chatNavigationParams(id), { replace: true });
  };
  return (
    <div className={`chat-languagegui-skin flex h-full min-h-0 w-full overflow-hidden${codeEnabled ? ' chat-code-page' : ''}`} data-theme={chatTheme} data-navigation-open={workspaceNavigationOpen}>
      {/* 左栏：Agent 切换排 + 独立 Chat 记录列表 */}
      <aside className="chat-languagegui-sidebar flex min-h-0 w-64 shrink-0 flex-col border-r border-border-subtle bg-surface-sunken">
        <div className="shrink-0 border-b border-border-subtle/60 p-2">
          <div className="mb-1 px-1 text-caption font-medium text-text-tertiary">选择要咨询的智能体</div>
          <div className="flex flex-wrap gap-1">
            {agents.map((a) => (
              <button
                key={a.id}
                onClick={() => pick(a.id)}
                title={`${a.name} · ${isKnowledgeLibrarianAgent(a) ? '系统内置' : a.role}`}
                aria-label={`${a.name}（${isKnowledgeLibrarianAgent(a) ? '系统内置' : a.role}）`}
                aria-pressed={agentId === a.id}
                className={`chat-agent-chip !w-auto max-w-full gap-tight px-tight ${agentId === a.id ? 'chat-agent-chip-active' : ''}`}
              >
                <Avatar name={a.name} url={a.avatar} size={26} />
                <span className="truncate text-caption text-text-primary">{a.name}</span>
                {isKnowledgeLibrarianAgent(a) && <span className="text-caption text-text-tertiary">内置</span>}
                {(a.presence === 'idle' || a.presence === 'busy') && (
                  <span className={`absolute -right-0.5 -top-0.5 h-2 w-2 rounded-full border border-surface-sunken ${a.presence === 'busy' ? 'bg-status-warning' : 'bg-status-success'}`} aria-hidden />
                )}
              </button>
            ))}
          </div>
        </div>
        {agentId && (
          <>
            {legacyRecovery && <LegacyTaskIntakeRecoveryNotice recovery={legacyRecovery} onRestore={restoreLegacy} disabled={!agents.length} />}
            <ChatSidebarNav view={sidebarView} onChange={setSidebarView} />
            {sidebarView === 'chats' && <ConversationList onPick={(id) => {
              setPromptSeed(null);
              setNarrowPanel('chat');
              openConversation(id);
              setSearchParams(chatNavigationParams(agentId, id ?? undefined), { replace: true });
            }} />}
            {sidebarView === 'library' && <SidebarLibrary onUse={(text) => {
              openConversation(null);
              setSearchParams(chatNavigationParams(agentId), { replace: true });
              setPromptSeed({ id: Date.now(), text });
              setSidebarView('chats');
            }} />}
            {sidebarView === 'apps' && <SidebarApps />}
          </>
        )}
        {!agentId && legacyRecovery && <LegacyTaskIntakeRecoveryNotice recovery={legacyRecovery} onRestore={restoreLegacy} disabled={!agents.length} />}
        <Link to="/library" className="mt-auto flex shrink-0 items-center gap-tight border-t border-border-subtle px-snug py-base text-body text-text-secondary hover:text-brand-primary focus-visible:ring-2 focus-visible:ring-brand-primary/40">
          <BookOpen className="h-4 w-4" aria-hidden />查看团队知识
        </Link>
      </aside>

      {/* 右侧对话区 */}
      <div className="chat-languagegui-main chat-split-host flex-1 flex flex-col min-w-0 min-h-0 overflow-hidden">
        {agentId ? <ConversationPane key={`${workspaceId}:${generation}:${agentId}:${promptSeed?.id ?? 'chat'}`} initialPrompt={conversationId ? '' : promptSeed?.text ?? ''} chatTheme={chatTheme} onToggleTheme={changeChatTheme} codeAvailable={codeAvailable} codeEnabled={codeEnabled} onToggleCode={toggleCode} codeRunsLoaded={codeRunsLoaded} navigationOpen={workspaceNavigationOpen} onToggleNavigation={() => setWorkspaceNavigationOpen((value) => !value)} narrowPanel={narrowPanel} onNarrowPanelChange={setNarrowPanel} /> : (
          <div className="flex min-h-0 flex-1 flex-col overflow-hidden">
            <ChatChrome
              left={<span className="text-body font-semibold text-text-primary">对话</span>}
              right={<><ChatThemeToggle theme={chatTheme} onToggle={changeChatTheme} /><SseStatusPill /></>}
            />
            <div className="flex flex-1 items-center justify-center">
              <EmptyState
                icon={<MessageSquare className="w-5 h-5" />}
                title="选一位团队成员，说说你需要什么帮助"
                description="咨询和需求整理都从这里开始；选择团队成员后直接说明目标、范围和限制。"
              />
            </div>
          </div>
        )}
      </div>
    </div>
  );
}

export function formatLegacyRecoveryContent(entry: LegacyTaskIntakeRecoveryEntry): string {
  const historicalContent = [
    entry.composerText,
    entry.draft ? `旧任务草案：${entry.draft.title}\n${entry.draft.description}\n验收标准：${entry.draft.acceptance_criteria.join('；')}` : undefined,
    entry.messages.filter((message) => message.role === 'user').slice(-3).map((message) => message.content).join('\n'),
  ].filter((value): value is string => Boolean(value?.trim()));
  if (historicalContent.length === 0) return '';
  const source = entry.projectKey ? `历史来源 projectKey：${entry.projectKey}（仅作来源标签）` : '历史来源：未绑定旧项目（仅作来源标签）';
  const boundary = '本次仅恢复历史文字到当前 Chat，不切换当前工作区目录、不自动发布任务；当前全局 WorkspaceProject 代码根仍有效。';
  return [source, boundary, ...historicalContent].join('\n\n');
}

function ChatChrome({ left, right }: { left: ReactNode; right?: ReactNode }) {
  return (
    <div className="chat-chrome flex h-12 shrink-0 items-center justify-between border-b border-border-subtle bg-surface-base px-6">
      <div className="flex min-w-0 items-center gap-snug">{left}</div>
      <div className="flex shrink-0 items-center gap-2">{right}</div>
    </div>
  );
}

function LegacyTaskIntakeRecoveryNotice({ recovery, onRestore, disabled }: { recovery: LegacyTaskIntakeRecovery; onRestore: (entryIndex: number) => void; disabled: boolean }) {
  return (
    <section className="mx-tight mb-tight rounded-card border border-status-warning/30 bg-status-warning/5 px-snug py-tight" aria-label="旧任务草案恢复">
      <div className="flex items-start gap-tight">
        <ArchiveRestore className="mt-0.5 h-4 w-4 shrink-0 text-status-warning" aria-hidden />
        <div className="min-w-0 flex-1">
          <p className="text-caption font-medium text-text-primary">发现旧任务对话草案</p>
          <p className="mt-micro text-caption text-text-secondary">它仍按当前工作区保存在本地，共发现 {recovery.entries.length} 份历史内容{recovery.bindingPresent ? '，并保留了旧项目绑定记录' : ''}；仅恢复文字，不切换当前工作区目录，也不会自动发布任务。</p>
          <div className="mt-tight space-y-micro">
            {recovery.entries.map((entry, index) => <LegacyRecoveryEntryRow key={`${entry.projectKey ?? 'unbound'}:${index}`} entry={entry} index={index} onRestore={onRestore} disabled={disabled} />)}
            {recovery.entries.length === 0 && <span className="text-caption text-text-tertiary">只有旧项目绑定记录，未发现可导入正文。</span>}
          </div>
        </div>
      </div>
    </section>
  );
}

function LegacyRecoveryEntryRow({ entry, index, onRestore, disabled }: { entry: LegacyTaskIntakeRecoveryEntry; index: number; onRestore: (index: number) => void; disabled: boolean }) {
  const details = [entry.messages.length ? `${entry.messages.length} 条消息` : '', entry.draft ? '含草案' : '', entry.composerText ? '含未发送输入' : '', entry.hasTerminalState ? '含旧发布状态' : ''].filter(Boolean).join(' · ');
  const title = entry.draft?.title ?? `历史记录 ${index + 1}`;
  const source = entry.projectKey ? `projectKey：${entry.projectKey}` : '未绑定旧项目';
  return <div className="flex items-start gap-tight rounded-button border border-border-subtle bg-surface-base px-tight py-micro"><div className="min-w-0 flex-1 text-caption text-text-secondary"><p className="break-words font-medium text-text-primary">{title}</p><p className="mt-micro break-all text-text-tertiary" title={entry.projectKey ?? undefined}>{source}</p><p className="mt-micro break-words text-text-tertiary">{details || '仅有历史正文'}</p></div><Button type="button" size="sm" className="shrink-0 px-tight" onClick={() => onRestore(index)} disabled={disabled}>恢复文字</Button></div>;
}

function ChatThemeToggle({ theme, onToggle }: { theme: WorkbenchTheme; onToggle: () => void }) {
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

function ConversationPane({ initialPrompt, chatTheme, onToggleTheme, codeAvailable, codeEnabled, onToggleCode, codeRunsLoaded, navigationOpen, onToggleNavigation, narrowPanel, onNarrowPanelChange }: {
  initialPrompt: string;
  chatTheme: WorkbenchTheme;
  onToggleTheme: () => void;
  codeAvailable: boolean;
  codeEnabled: boolean;
  onToggleCode: () => void;
  codeRunsLoaded: boolean;
  navigationOpen: boolean;
  onToggleNavigation: () => void;
  narrowPanel: 'document' | 'chat';
  onNarrowPanelChange: (panel: 'document' | 'chat') => void;
}) {
  const workspaceId = useWorkspaceStore((state) => state.workspace?.id);
  const switchingWorkspace = useWorkspaceStore((state) => state.switching);
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
  const analysisProjection = useChatAnalysisStore((s) => s.projection);
  const analysisDraft = useChatAnalysisStore((s) => s.draft);
  const analysisLoading = useChatAnalysisStore((s) => s.loading);
  const analysisSubmitting = useChatAnalysisStore((s) => s.submitting);
  const analysisError = useChatAnalysisStore((s) => s.error);
  const refreshAnalysis = useChatAnalysisStore((s) => s.refresh);
  const setAnalysisSelection = useChatAnalysisStore((s) => s.setSelection);
  const setAnalysisText = useChatAnalysisStore((s) => s.setText);
  const submitAnalysisAnswer = useChatAnalysisStore((s) => s.submitAnswer);
  const restoreAnalysisDraftText = useChatAnalysisStore((s) => s.restoreDraftText);
  const discardAnalysisDraft = useChatAnalysisStore((s) => s.discardDraft);
  const decisionHistory = useChatDecisionsStore((s) => s.history);
  const selectedDecisionItemId = useChatDecisionsStore((s) => s.selectedItemId);
  const decisionDraft = useChatDecisionsStore((s) => s.draft);
  const decisionLoading = useChatDecisionsStore((s) => s.loading);
  const decisionHistoryLoading = useChatDecisionsStore((s) => s.historyLoading);
  const decisionSubmitting = useChatDecisionsStore((s) => s.submitting);
  const decisionError = useChatDecisionsStore((s) => s.error);
  const refreshDecisions = useChatDecisionsStore((s) => s.refresh);
  const selectDecisionItem = useChatDecisionsStore((s) => s.selectItem);
  const recheckDecisions = useChatDecisionsStore((s) => s.recheck);
  const setDecisionOutcome = useChatDecisionsStore((s) => s.setOutcome);
  const setDecisionConclusion = useChatDecisionsStore((s) => s.setConclusion);
  const setDecisionBasis = useChatDecisionsStore((s) => s.setBasis);
  const setDecisionProductVersion = useChatDecisionsStore((s) => s.setProductVersion);
  const submitDecision = useChatDecisionsStore((s) => s.submit);
  const restoreDecisionDraftText = useChatDecisionsStore((s) => s.restoreDraftText);
  const discardDecisionDraft = useChatDecisionsStore((s) => s.discardDraft);
  const resetDecisions = useChatDecisionsStore((s) => s.reset);
  const publicationDraft = usePublicationStore((s) => s.draft);
  const publicationLoading = usePublicationStore((s) => s.loading);
  const publicationSaving = usePublicationStore((s) => s.saving);
  const publicationPublishing = usePublicationStore((s) => s.publishing);
  const publicationError = usePublicationStore((s) => s.error);
  const hydratePublication = usePublicationStore((s) => s.hydrate);
  const refreshPublication = usePublicationStore((s) => s.refresh);
  const reconcilePublication = usePublicationStore((s) => s.reconcile);
  const openPublication = usePublicationStore((s) => s.openFromAnalysis);
  const togglePublicationItem = usePublicationStore((s) => s.toggleItem);
  const setPublicationTitle = usePublicationStore((s) => s.setTitle);
  const startNewPublicationDraft = usePublicationStore((s) => s.startNewDraft);
  const savePublicationDraft = usePublicationStore((s) => s.saveDraft);
  const publishPublication = usePublicationStore((s) => s.publish);
  const resetPublication = usePublicationStore((s) => s.reset);
  const resetAnalysis = useChatAnalysisStore((s) => s.reset);
  const conversationSources = useChatStore((s) => conversationId ? s.sourcesByConversation[conversationId] : undefined);
  const sourcesLoading = useChatStore((s) => conversationId ? s.sourcesLoadingByConversation[conversationId] === true : false);
  const sourcesError = useChatStore((s) => conversationId ? s.sourcesErrorByConversation[conversationId] : undefined);
  const refreshSources = useChatStore((s) => s.refreshSources);
  const runAlerts = useChatStore((s) => s.runAlerts);
  const pendingUsers = useChatStore((s) => s.pendingUsers);
  const sendError = useChatStore((s) => s.sendError);
  const storageNotice = useChatWorkspaceStorageNotice();
  const allAgents = useAgentsStore((s) => s.agents);
  const agents = useMemo(
    () => allAgents.filter(isChatAgent),
    [allAgents],
  );
  const showReasoning = useChatPreferencesStore((state) => state.showReasoning);
  const groupExploreTools = useChatPreferencesStore((state) => state.groupExploreTools);
  const groupTerminalTools = useChatPreferencesStore((state) => state.groupTerminalTools);
  const groupChangesTools = useChatPreferencesStore((state) => state.groupChangesTools);

  const timelines = useRunsStore((s) => s.timelines);
  const runSnapshots = useRunsStore((s) => s.runs);
  const watchRun = useRunsStore((s) => s.watchRun);
  const unwatchRun = useRunsStore((s) => s.unwatchRun);
  const approvals = useRunsStore((s) => s.approvals);

  const savedComposer = workspaceId && agentId
    ? readChatWorkspaceState(workspaceId, agentId, conversationId)?.composer
    : undefined;
  const [composer, setComposerState] = useState<ChatComposerDraft>(initialPrompt
    ? { draft: initialPrompt }
    : savedComposer ?? { draft: '' });
  const [composerAttachments, setComposerAttachments] = useState<readonly PromptAttachment[]>([]);
  const [attachmentSendPending, setAttachmentSendPending] = useState(false);
  const attachmentSendPendingRef = useRef(false);
  const [attachmentClearRequest, setAttachmentClearRequest] = useState(0);
  const [analysisLaunchPending, setAnalysisLaunchPending] = useState(false);
  const { draft } = composer;
  const setDraft = useCallback((value: string | ((previous: string) => string)) => {
    setComposerState((current) => {
      const next = { ...current, draft: typeof value === 'function' ? value(current.draft) : value };
      if (workspaceId && agentId) writeChatWorkspaceState(workspaceId, agentId, conversationId, { composer: next, queue: useChatStore.getState().queue });
      return next;
    });
  }, [agentId, conversationId, workspaceId]);
  const [workspaceOpen, setWorkspaceOpen] = useState(false);
  const [selectedSwarmMember, setSelectedSwarmMember] = useState<SwarmMemberSelection | null>(null);
  const scrollRef = useRef<HTMLDivElement>(null);
  const textareaRef = useRef<HTMLTextAreaElement>(null);
  const followStreamRef = useRef(true);
  const approvalAnchorsRef = useRef<Record<string, string>>({});
  const [approvalAnchors, setApprovalAnchors] = useState<Record<string, string>>({});
  const previousConversationRef = useRef(conversationId);

  const onComposerAttachmentsChange = useCallback((attachments: readonly PromptAttachment[]) => {
    setComposerAttachments(attachments);
  }, []);

  useLayoutEffect(() => {
    const previous = previousConversationRef.current;
    if (previous === conversationId) return;
    previousConversationRef.current = conversationId;
    // Creating the first Run changes the conversation ID; the Agent's reader
    // and any newly typed follow-up must remain in place during that transition.
    if (!(previous === null && conversationId !== null && sending)) {
      const restored = workspaceId && agentId
        ? readChatWorkspaceState(workspaceId, agentId, conversationId)?.composer
        : undefined;
      setComposerState(restored ?? { draft: '' });
      if (!attachmentSendPendingRef.current) setComposerAttachments([]);
    }
    setWorkspaceOpen(false);
    setSelectedSwarmMember(null);
    followStreamRef.current = true;
    approvalAnchorsRef.current = {};
    setApprovalAnchors({});
  }, [agentId, conversationId, sending, workspaceId]);

  useEffect(() => {
    if (conversationId) void refreshSources(conversationId);
  }, [conversationId, refreshSources]);

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
  const nativeQuestionItems = useNativeQuestionsStore((state) => latestRunId ? state.itemsByRun[latestRunId] : undefined);
  // 待答问题以 questions store 为权威，不再拿 run 状态快照二次门控：快照缺失或
  // 瞬时非活跃都会把提问卡藏起来，用户答不了就只能等 idle 看门狗把 run 判死。
  const nativeQuestions = useMemo(() => nativeQuestionItems ?? [], [nativeQuestionItems]);
  const nativeQuestionError = useNativeQuestionsStore((state) => latestRunId ? state.errorByRun[latestRunId] : undefined);
  const nativeQuestionSubmitting = useNativeQuestionsStore((state) => state.submittingByQuestion);
  const refreshNativeQuestions = useNativeQuestionsStore((state) => state.refresh);
  const resolveNativeQuestion = useNativeQuestionsStore((state) => state.resolve);
  const clearNativeQuestions = useNativeQuestionsStore((state) => state.clear);

  useEffect(() => {
    if (!latestRunId) {
      clearNativeQuestions();
      return;
    }
    void refreshNativeQuestions(latestRunId);
  }, [clearNativeQuestions, latestRunId, refreshNativeQuestions]);

  useEffect(() => {
    if (!workspaceId || !agentId || !conversationId) {
      resetAnalysis();
      return;
    }
    void refreshAnalysis(workspaceId, agentId, conversationId);
  }, [agentId, conversationId, refreshAnalysis, resetAnalysis, workspaceId]);

  useEffect(() => {
    if (!workspaceId || !agentId || !conversationId || !latestRunId || !latestRun?.status) return;
    void refreshAnalysis(workspaceId, agentId, conversationId);
  }, [agentId, conversationId, latestRun?.status, latestRunId, refreshAnalysis, workspaceId]);

  useEffect(() => {
    if (!workspaceId || !agentId || !conversationId) {
      resetDecisions();
      return;
    }
    if (!analysisProjection) return;
    void refreshDecisions(workspaceId, agentId, conversationId);
  }, [agentId, analysisProjection, conversationId, refreshDecisions, resetDecisions, workspaceId]);

  useEffect(() => {
    if (!workspaceId || !agentId || !conversationId) {
      resetPublication();
      return;
    }
    hydratePublication(workspaceId, agentId, conversationId);
  }, [agentId, conversationId, hydratePublication, resetPublication, workspaceId]);

  useEffect(() => {
    if (!workspaceId || !agentId || !conversationId || !analysisProjection) return;
    void refreshPublication();
  }, [agentId, analysisProjection, conversationId, refreshPublication, workspaceId]);

  useEffect(() => {
    if (analysisProjection) reconcilePublication(analysisProjection);
  }, [analysisProjection, reconcilePublication]);

  // 订阅当前会话所有 run，确保历史轮次消息可回放。
  useEffect(() => {
    for (const id of runIds) watchRun(id);
    return () => {
      for (const id of runIds) unwatchRun(id);
    };
  }, [runIds, watchRun, unwatchRun]);

  const messages = useMemo(() => buildMessages(runIds, timelines), [runIds, timelines]);
  const analysisRunIds = useMemo(() => new Set(
    [...runs, ...Object.values(runSnapshots)]
      .filter((run) => run.output_contract === 'chat-analysis/v1')
      .map((run) => run.id),
  ), [runSnapshots, runs]);
  const transcriptSourceMessages = useMemo(
    () => messages.filter((message) => !(message.kind === 'assistant' && analysisRunIds.has(message.runId))),
    [analysisRunIds, messages],
  );
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
  const transcriptLiveStream = useMemo(
    () => latestRunId && analysisRunIds.has(latestRunId) ? { ...liveStream, answerDraft: '' } : liveStream,
    [analysisRunIds, latestRunId, liveStream],
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
    () => hideLiveRunDrafts(transcriptSourceMessages, latestRunId, liveRunActive),
    [latestRunId, liveRunActive, transcriptSourceMessages],
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
    // 有待答问题的 run 一律按运行中投影：提问是等人，不是停滞——否则在途工具行
    // 会被渲染成「已中断」，用户据此以为流程断了。
    for (const question of nativeQuestions) map[question.run_id] = 'running';
    return map;
  }, [runIds, runSnapshots, runs, nativeQuestions]);
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
        liveStream: transcriptLiveStream,
        liveRunActive,
        hasPendingApproval,
        pendingUsers,
        rawMessages: transcriptSourceMessages,
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
      transcriptLiveStream,
      liveRunActive,
      hasPendingApproval,
      pendingUsers,
      transcriptSourceMessages,
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
  }, [agentId, conversationId]);

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
  }, [conversationId, presentedSegments.length, liveStream.reasoning, liveStream.answerDraft, runApprovals.length]);

  const previousAnalysisStatusRef = useRef(analysisProjection?.status);
  useEffect(() => {
    const previous = previousAnalysisStatusRef.current;
    const next = analysisProjection?.status;
    previousAnalysisStatusRef.current = next;
    if (next !== 'needs_answer' || previous === next || !followStreamRef.current || !scrollRef.current) return;
    scrollRef.current.scrollTop = scrollRef.current.scrollHeight;
  }, [analysisProjection?.status]);

  // 自动续发：最新 run「进入」succeeded 且队列非空时出队首条开新轮。
  // 只在状态边沿触发一次：drain 失败后 sending 复位也不会原地重试风暴；
  // failed/cancelled/lost/interrupted 不自动发（留给用户手动「继续发送」）。
  const drainedEdgeRef = useRef('');
  useEffect(() => {
    if (!latestRun || latestRun.status !== 'succeeded' || sending || sendError || queue.length === 0) return;
    const edge = `${latestRun.id}:succeeded`;
    if (drainedEdgeRef.current === edge) return;
    drainedEdgeRef.current = edge;
    void drainQueue();
  }, [latestRun, sending, sendError, queue.length, drainQueue]);

  // 最新 run 的累计输入用量；后端未上报（字段缺失）时不渲染。
  // 上下文窗口需另拉 /models 匹配 agent 模型--不值得为凑格式加请求，只显 used。
  const usageText = formatTokenUsage(latestRun?.usage_in);
  const attachmentScope = useMemo(
    () => (workspaceId && agentId ? { workspaceId, agentId, conversationId } : undefined),
    [agentId, conversationId, workspaceId],
  );
  const analysisQueued = queue.some((item) => item.outputContract === CHAT_ANALYSIS_OUTPUT_CONTRACT);
  const decisionItem = useMemo(() => {
    const items = analysisProjection?.document?.items ?? [];
    const selectedId = selectedDecisionItemId && items.some((item) => item.id === selectedDecisionItemId)
      ? selectedDecisionItemId
      : items.find((item) => analysisProjection?.decisions.some((decision) => decision.item_id === item.id))?.id ?? items[0]?.id;
    return items.find((item) => item.id === selectedId) ?? null;
  }, [analysisProjection, selectedDecisionItemId]);
  const decision = useMemo(
    () => analysisProjection && decisionItem
      ? analysisProjection.decisions.find((candidate) => candidate.item_id === decisionItem.id) ?? null
      : null,
    [analysisProjection, decisionItem],
  );
  const decisionRecheckReason = decision?.review_reason ?? (analysisProjection?.status === 'stale' ? analysisProjection.error : undefined);
  const publicationItems = useMemo(() => {
    if (!analysisProjection?.document || ['stale', 'analyzing', 'failed'].includes(analysisProjection.status)) return [];
    const itemIds = new Set(analysisProjection.document.items.map((item) => item.id));
    const confirmed = new Set(analysisProjection.decisions
      .filter((candidate) => candidate.revision === analysisProjection.revision && candidate.status === 'valid' && candidate.outcome === 'confirmed' && itemIds.has(candidate.item_id))
      .map((candidate) => candidate.item_id));
    return analysisProjection.document.items.filter((item) => confirmed.has(item.id));
  }, [analysisProjection]);
  const publicationDecisions = useMemo(
    () => analysisProjection?.decisions.filter((candidate) => publicationItems.some((item) => item.id === candidate.item_id)) ?? [],
    [analysisProjection, publicationItems],
  );
  const publicationBlocked = !analysisProjection || ['stale', 'analyzing', 'failed'].includes(analysisProjection.status);

  const sendComposer = async (options: { outputContract?: 'languagegui/v1' | 'chat-analysis/v1'; fallbackText?: string; clearAttachmentsAfterSend?: boolean } = {}): Promise<boolean> => {
    if (switchingWorkspace) return false;
    const text = draft.trim();
    const messageText = text || options.fallbackText || '';
    if (!messageText && composerAttachments.length === 0) return false;
    if (codeEnabled) onNarrowPanelChange('chat');
    const attachmentInputs: ChatAttachmentInput[] = composerAttachments.map((attachment) => ({ key: attachment.key, file: attachment.file }));
    if (attachmentInputs.length > 0) {
      attachmentSendPendingRef.current = true;
      setAttachmentSendPending(true);
    }
    setComposerState({ draft: '' });
    if (workspaceId && agentId) writeChatWorkspaceState(workspaceId, agentId, conversationId, { composer: { draft: '' }, queue: useChatStore.getState().queue });
    let retained = false;
    try {
      retained = await send(messageText, attachmentInputs, options.outputContract ? { outputContract: options.outputContract } : undefined);
      if (retained) {
        if (options.clearAttachmentsAfterSend && attachmentInputs.length > 0) setAttachmentClearRequest((value) => value + 1);
        const activeConversationId = useChatStore.getState().conversationId;
        if (activeConversationId && workspaceId && agentId && options.outputContract === CHAT_ANALYSIS_OUTPUT_CONTRACT) void refreshAnalysis(workspaceId, agentId, activeConversationId);
        if (activeConversationId && options.outputContract !== CHAT_ANALYSIS_OUTPUT_CONTRACT) void refreshSources(activeConversationId);
      }
    } catch {
      retained = false;
    } finally {
      if (attachmentInputs.length > 0) {
        attachmentSendPendingRef.current = false;
        setAttachmentSendPending(false);
      }
    }
    if (!retained) setComposerState((current) => {
      const restoredText = current.draft ? `${text}\n\n${current.draft}` : text;
      const next = { ...current, draft: restoredText };
      const recoveryConversationId = useChatStore.getState().conversationId;
      if (workspaceId && agentId) writeChatWorkspaceState(workspaceId, agentId, recoveryConversationId, { composer: next, queue: useChatStore.getState().queue });
      return next;
    });
    return retained;
  };

  const startAnalysis = async () => {
    if (switchingWorkspace || analysisLaunchPending || analysisQueued || analysisSubmitting || analysisProjection?.status === 'analyzing' || analysisProjection?.status === 'needs_answer') return;
    setAnalysisLaunchPending(true);
    try {
      await sendComposer({
        outputContract: CHAT_ANALYSIS_OUTPUT_CONTRACT,
        fallbackText: CHAT_ANALYSIS_INSTRUCTION,
        clearAttachmentsAfterSend: true,
      });
    } finally {
      setAnalysisLaunchPending(false);
    }
  };

  const startChangeReview = async () => {
    if (switchingWorkspace || analysisLaunchPending || analysisQueued || analysisSubmitting) return;
    setAnalysisLaunchPending(true);
    try {
      await sendComposer({
        outputContract: CHAT_ANALYSIS_OUTPUT_CONTRACT,
        fallbackText: CHAT_ANALYSIS_CHANGE_INSTRUCTION,
        clearAttachmentsAfterSend: true,
      });
    } finally {
      setAnalysisLaunchPending(false);
    }
  };

  const doSend = () => sendComposer();

  const applyPrompt = (text: string) => {
    setDraft(text);
    textareaRef.current?.focus();
  };

  return (
    <div className="chat-split-workspace flex min-h-0 flex-1 flex-col overflow-hidden">
      {storageNotice && <p className="mx-auto w-full max-w-[920px] shrink-0 border-b border-status-warning/30 bg-status-warning/5 px-comfortable py-tight text-caption text-status-warning" role="status">{storageNotice}</p>}
      {codeEnabled && (
        <div className="chat-split-tabs" role="group" aria-label="代码工作区视图">
          <button type="button" onClick={onToggleNavigation} aria-expanded={navigationOpen} aria-label="切换成员与会话列表"><PanelLeft className="h-4 w-4" aria-hidden /></button>
          <button type="button" aria-pressed={narrowPanel === 'document'} onClick={() => { setWorkspaceOpen(false); setSelectedSwarmMember(null); onNarrowPanelChange('document'); }}><Code2 className="h-4 w-4" aria-hidden />代码</button>
          <button type="button" aria-pressed={narrowPanel === 'chat'} onClick={() => onNarrowPanelChange('chat')}><MessageSquare className="h-4 w-4" aria-hidden />对话</button>
          <button type="button" onClick={onToggleCode}>关闭代码</button>
        </div>
      )}
    <div className={`chat-split-layout flex flex-1 min-h-0 overflow-hidden${codeEnabled ? ' chat-split-layout-active' : ''}`} data-active-panel={narrowPanel} data-inspector={workspaceOpen || !!selectedMember}>
      {codeEnabled && workspaceId && agentId && (
        <section className="chat-split-document code-workspace-document" aria-label="Java 代码工作台">
          <CodeWorkspace workspaceId={workspaceId} agentId={agentId} conversationId={conversationId} latestRunId={latestRunId} runsLoaded={codeRunsLoaded} theme={chatTheme} />
        </section>
      )}
      <div className="chat-languagegui-main chat-split-conversation flex min-h-0 min-w-0 flex-1 flex-col overflow-hidden">
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
            {codeEnabled && <button type="button" onClick={onToggleNavigation} aria-label="切换成员与会话列表" title="成员与会话" aria-expanded={navigationOpen} className="chat-split-navigation-toggle"><PanelLeft className="h-4 w-4" aria-hidden /></button>}
            {codeAvailable && <button type="button" onClick={onToggleCode} aria-pressed={codeEnabled} aria-label={codeEnabled ? '关闭代码工作台' : '打开代码工作台'} className="chat-split-code-toggle"><Code2 className="h-4 w-4" aria-hidden />代码</button>}
            <button
              type="button"
              onClick={() => void startAnalysis()}
              disabled={switchingWorkspace || analysisLaunchPending || analysisQueued || analysisSubmitting || analysisProjection?.status === 'analyzing' || analysisProjection?.status === 'needs_answer'}
              aria-label="整理需求，逐项确认"
              title={runInFlight ? '当前运行结束后开始需求整理' : '整理当前对话的需求、异常场景和材料冲突'}
              className="inline-flex h-8 items-center gap-micro rounded-button border border-brand-primary/30 px-tight text-caption text-brand-primary transition-colors hover:bg-brand-muted/35 disabled:cursor-not-allowed disabled:opacity-55"
            >
              {analysisLaunchPending || analysisProjection?.status === 'analyzing' ? <LoaderCircle className="h-3.5 w-3.5 animate-spin motion-reduce:animate-none" aria-hidden="true" /> : <ListChecks className="h-3.5 w-3.5" aria-hidden="true" />}
              {analysisQueued ? '已加入整理队列' : analysisProjection?.status === 'needs_answer' ? '继续确认' : analysisProjection?.status === 'ready' ? '重新整理需求' : '整理需求，逐项确认'}
            </button>
            <ChatThemeToggle theme={chatTheme} onToggle={onToggleTheme} />
            {conversationArtifacts.length > 0 && (
              <button
                type="button"
                title={workspaceOpen ? '关闭成果' : '查看成果'}
                aria-label={workspaceOpen ? '关闭成果' : '查看成果'}
                aria-pressed={workspaceOpen}
                onClick={() => { setSelectedSwarmMember(null); setWorkspaceOpen((v) => !v); }}
                className="inline-flex h-8 w-8 items-center justify-center rounded-button text-text-tertiary transition-colors hover:bg-surface-sunken hover:text-text-primary"
              >
                <PanelRight className="h-4 w-4" />
              </button>
            )}
            {latestRunId && (
              <Link
                to={`/runs/${latestRunId}/journal`}
                className="inline-flex h-8 items-center rounded-button px-2 text-caption text-text-tertiary transition-colors hover:bg-surface-sunken hover:text-text-primary"
                title="查看运行环节时间线"
                aria-label="查看运行环节时间线"
              >
                环节
              </Link>
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
              直接告诉 {agent?.name ?? '智能体'} 你想了解什么，也可以从下面的问题开始。
            </p>
            <div className="mt-3 flex flex-wrap justify-center gap-2">
              {suggestedPrompts(agent?.role).map((p) => (
                <button
                  key={p}
                  type="button"
                  onClick={() => applyPrompt(p)}
                  className="rounded-button border border-border-subtle bg-surface-raised/85 px-snug py-tight text-caption text-text-secondary shadow-card transition-all duration-motion hover:-translate-y-0.5 hover:border-brand-primary/35 hover:text-brand-primary"
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
          {nativeQuestions.map((question) => (
            <NativeQuestionCard
              key={question.id}
              question={question}
              submitting={nativeQuestionSubmitting[question.id] === true}
              onResolve={(response) => resolveNativeQuestion(question.run_id, question.id, response)}
            />
          ))}
          {nativeQuestionError && latestRunId && <div className="flex items-center gap-tight text-caption text-status-error" role="alert"><span>{nativeQuestionError}</span><Button type="button" size="sm" onClick={() => void refreshNativeQuestions(latestRunId)}>重试读取问题</Button></div>}
          {latestRunId && latestRun && TERMINAL.has(latestRun.status) && (
            <FileChangesCard runId={latestRunId} />
          )}
          <ChatAnalysisPanel
            projection={analysisProjection}
            draft={analysisDraft}
            loading={analysisLoading}
            submitting={analysisSubmitting}
            error={analysisError}
            conversationSources={conversationSources ?? []}
            onSelection={setAnalysisSelection}
            onText={setAnalysisText}
            onSubmit={(disposition) => { void submitAnalysisAnswer(disposition); }}
            onRestart={() => void startAnalysis()}
            onRefresh={() => workspaceId && agentId && conversationId && void refreshAnalysis(workspaceId, agentId, conversationId)}
            onRestoreDraftText={restoreAnalysisDraftText}
            onDiscardDraft={discardAnalysisDraft}
          />
          <ChatDecisionPanel
            item={decisionItem}
            items={analysisProjection?.document?.items ?? []}
            selectedItemId={selectedDecisionItemId}
            decision={decision}
            revision={analysisProjection?.revision ?? 0}
            recheckReason={decisionRecheckReason}
            history={decisionHistory}
            draft={decisionDraft}
            loading={decisionLoading}
            historyLoading={decisionHistoryLoading}
            submitting={decisionSubmitting}
            error={decisionError}
            onOutcome={setDecisionOutcome}
            onSelectItem={selectDecisionItem}
            onConclusion={setDecisionConclusion}
            onBasis={setDecisionBasis}
            onProductVersion={setDecisionProductVersion}
            onSubmit={() => { void submitDecision(); }}
            onRecheck={() => { void recheckDecisions(); }}
            onRestoreDraftText={restoreDecisionDraftText}
            onDiscardDraft={discardDecisionDraft}
            onReviewChanges={() => { void startChangeReview(); }}
          />
          <TaskDraftPreview
            items={publicationItems}
            decisions={publicationDecisions}
            draft={publicationDraft}
            analysisBlocked={publicationBlocked}
            loading={publicationLoading}
            saving={publicationSaving}
            publishing={publicationPublishing}
            error={publicationError}
            onOpen={openPublication}
            onToggleItem={togglePublicationItem}
            onTitle={setPublicationTitle}
            onNewDraft={startNewPublicationDraft}
            onSave={() => { void savePublicationDraft(); }}
            onPublish={() => { void publishPublication(); }}
            onRetry={() => { void refreshPublication(); }}
          />
        </div>
      </div>

      {/* 底部固定：成果摘要 + 计划 / 目标 + 一体化输入卡 */}
      <div className="chat-bottom-region shrink-0 border-t border-border-subtle bg-surface-base px-6 pb-4 pt-2">
        <div className="chat-composer-stack">
          {sendError && <SendErrorNotice message={sendError} agentId={agentId} />}
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
          <ChatSourceShelf
            chatId={conversationId}
            sources={conversationSources ?? []}
            loading={sourcesLoading}
            error={sourcesError}
            onRetry={() => conversationId && void refreshSources(conversationId)}
          />
          <PromptBox
            draft={draft}
            onDraftChange={setDraft}
            onSend={doSend}
            placeholder={latestRun && !TERMINAL.has(latestRun.status)
              ? '运行中，消息将进入队列，完成后自动发送'
              : '输入消息，Enter 发送，Shift+Enter 换行'}
            inputRef={textareaRef}
            queue={queue}
            onRemoveQueued={removeQueued}
            canDrainQueue={!runInFlight && (!!sendError || (!!latestRun && TERMINAL.has(latestRun.status) && latestRun.status !== 'succeeded'))}
            onDrainQueue={() => void drainQueue()}
            sending={sending || switchingWorkspace}
            runInFlight={runInFlight}
            stopping={!!latestRunId && stoppingRunId === latestRunId}
            onStop={() => latestRunId && void stopActiveRun(latestRunId, 'user_stopped')}
            usageText={usageText}
            attachmentScope={attachmentScope}
            preserveAttachmentsOnScopeChange={attachmentSendPending}
            clearAttachmentsRequest={attachmentClearRequest}
            onAttachmentsChange={onComposerAttachmentsChange}
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
    </div>
  );
}
