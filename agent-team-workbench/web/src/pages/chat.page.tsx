import {
  ArrowUpDown,
  CheckCircle2,
  ChevronDown,
  ChevronRight,
  CircleDot,
  CircleX,
  Clock3,
  ImagePlus,
  MessageSquare,
  PanelLeftOpen,
  PanelRightClose,
  PanelRightOpen,
  Plus,
  Search,
  SendHorizonal,
  Sparkles,
  X,
} from 'lucide-react';
import { type ReactNode, type RefObject, useEffect, useMemo, useRef, useState } from 'react';
import { Link, useSearchParams } from 'react-router-dom';
import { listRuntimeBindings, uploadWorkspaceImage } from '../api/endpoints';
import type { AgentProfile, ExecutionRun } from '../api/types';
import { Avatar } from '../components/avatar';
import { EmptyState } from '../components/async-state';
import { ApprovalCard } from '../components/chat/approval-card';
import { AssistantTurn } from '../components/chat/assistant-turn';
import { PlanCard } from '../components/chat/plan-card';
import { ActivityGroup, groupActivity } from '../components/chat/tool-card';
import { PresenceDot, runStatusColor, runStatusText } from '../components/status';
import { useAgentsStore } from '../stores/agents.store';
import {
  ACTIVE,
  aggregateRunStream,
  buildMessages,
  conversationLabel,
  formatTokenUsage,
  type ChatMessage,
  useChatStore,
} from '../stores/chat.store';
import { useRunsStore } from '../stores/runs.store';
import { toast } from '../stores/toast.store';
import { useWorkspaceStore } from '../stores/workspace.store';
import { agentRuntimeView, type RuntimeBindingsSnapshot } from '../utils/agent-runtime-status';
import { buildImageInstruction, CHAT_IMAGE_TYPES, MAX_CHAT_IMAGES, validateChatImages } from '../utils/chat-images';
import { formatTime } from '../utils/format';

const QUICK_PROMPTS = [
  {
    label: '梳理任务并给出计划',
    prompt: '请先梳理当前任务，说明关键问题并给出可执行计划。',
  },
  {
    label: '检查当前项目',
    prompt: '请检查当前项目状态，列出需要优先处理的问题。',
  },
  {
    label: '检查交互与可读性',
    prompt: '请检查当前页面的交互与信息可读性，并给出改进建议。',
  },
];

const PREVIEW_USER_MESSAGE = '帮我优化这个对话页，重点让消息更容易阅读。';
const PREVIEW_ASSISTANT_MESSAGE = `可以，建议采用左右分流：

- 你的消息固定靠右，使用品牌色气泡；
- Agent 回复靠左，保留头像、名称和状态；
- 工具执行与系统状态留在中轴，不混入对话。

这样视线能更快区分提问和回答。`;

interface PendingChatImage {
  id: string;
  file: File;
  previewUrl: string;
}

const FOCUSABLE_SELECTOR = [
  'a[href]',
  'button:not([disabled])',
  'input:not([disabled])',
  'textarea:not([disabled])',
  'select:not([disabled])',
  '[tabindex]:not([tabindex="-1"])',
].join(',');

function useMediaQuery(query: string): boolean {
  const [matches, setMatches] = useState(() => typeof window !== 'undefined' && window.matchMedia(query).matches);

  useEffect(() => {
    const media = window.matchMedia(query);
    const update = () => setMatches(media.matches);
    update();
    media.addEventListener('change', update);
    return () => media.removeEventListener('change', update);
  }, [query]);

  return matches;
}

/** React 18 不会可靠下发 inert 布尔属性，显式同步原生属性。 */
function useInert(ref: RefObject<HTMLElement | null>, active: boolean) {
  useEffect(() => {
    ref.current?.toggleAttribute('inert', active);
  }, [active, ref]);
}

/** 移动端抽屉/面板：进入时聚焦、Esc 关闭、Tab 留在面板内，退出后恢复触发点。 */
function useModalFocus(
  active: boolean,
  containerRef: RefObject<HTMLElement | null>,
  onClose: () => void,
  restoreFocusRef?: RefObject<HTMLElement | null>,
) {
  const onCloseRef = useRef(onClose);

  useEffect(() => {
    onCloseRef.current = onClose;
  }, [onClose]);

  useEffect(() => {
    if (!active) return;
    const previousFocus = restoreFocusRef?.current
      ?? (document.activeElement instanceof HTMLElement ? document.activeElement : null);
    const frame = window.requestAnimationFrame(() => {
      const first = containerRef.current?.querySelector<HTMLElement>(FOCUSABLE_SELECTOR);
      (first ?? containerRef.current)?.focus();
    });

    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        event.preventDefault();
        onCloseRef.current();
        return;
      }
      if (event.key !== 'Tab') return;
      const focusable = Array.from(containerRef.current?.querySelectorAll<HTMLElement>(FOCUSABLE_SELECTOR) ?? [])
        .filter((element) => element.getClientRects().length > 0);
      if (focusable.length === 0) {
        event.preventDefault();
        containerRef.current?.focus();
        return;
      }
      const first = focusable[0];
      const last = focusable[focusable.length - 1];
      if (event.shiftKey && document.activeElement === first) {
        event.preventDefault();
        last.focus();
      } else if (!event.shiftKey && document.activeElement === last) {
        event.preventDefault();
        first.focus();
      }
    };

    document.addEventListener('keydown', handleKeyDown);
    return () => {
      window.cancelAnimationFrame(frame);
      document.removeEventListener('keydown', handleKeyDown);
      window.requestAnimationFrame(() => {
        if (previousFocus?.isConnected) previousFocus.focus();
      });
    };
  }, [active, containerRef, restoreFocusRef]);
}

/** 对话页：紧凑导航 + Agent/会话工作区 + 专注消息流 + 可收起运行上下文。 */
export default function ChatPage() {
  const agents = useAgentsStore((s) => s.agents);
  const workspaceId = useWorkspaceStore((s) => s.workspace?.id);
  const agentId = useChatStore((s) => s.agentId);
  const conversationId = useChatStore((s) => s.conversationId);
  const selectAgent = useChatStore((s) => s.selectAgent);
  const openConversation = useChatStore((s) => s.openConversation);

  const [searchParams, setSearchParams] = useSearchParams();
  const [workspaceOpen, setWorkspaceOpen] = useState(false);
  const [runtimeBindings, setRuntimeBindings] = useState<RuntimeBindingsSnapshot>(undefined);
  const urlBooted = useRef(false);
  const workspaceRef = useRef<HTMLElement>(null);
  const conversationMainRef = useRef<HTMLElement>(null);
  const workspaceRestoreFocusRef = useRef<HTMLElement | null>(null);
  const workspaceDocked = useMediaQuery('(min-width: 1024px)');
  const workspaceModalOpen = workspaceOpen && !workspaceDocked;
  const workspaceInteractive = workspaceDocked || workspaceOpen;

  useInert(workspaceRef, !workspaceInteractive);
  useInert(conversationMainRef, workspaceModalOpen);
  useModalFocus(workspaceModalOpen, workspaceRef, () => setWorkspaceOpen(false), workspaceRestoreFocusRef);

  useEffect(() => {
    if (!workspaceId) {
      setRuntimeBindings(undefined);
      return;
    }
    let current = true;
    setRuntimeBindings(undefined);
    listRuntimeBindings(workspaceId)
      .then(({ items }) => {
        if (current) setRuntimeBindings(items);
      })
      .catch(() => {
        if (current) setRuntimeBindings(null);
      });
    return () => {
      current = false;
    };
  }, [workspaceId]);

  // URL 初始值优先；直接进入对话页时默认选择第一个 Agent，减少一次空态点击。
  useEffect(() => {
    if (urlBooted.current) return;
    const qAgent = searchParams.get('agent');
    const qConv = searchParams.get('c');
    if (!qAgent && agents.length === 0) return;

    const nextAgentId = qAgent ?? agents[0]?.id;
    if (nextAgentId && nextAgentId !== agentId) selectAgent(nextAgentId);
    if (qConv) openConversation(qConv);
    if (!qAgent && nextAgentId) setSearchParams({ agent: nextAgentId }, { replace: true });
    urlBooted.current = true;
  }, [agentId, agents, openConversation, searchParams, selectAgent, setSearchParams]);

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

  const pickAgent = (id: string) => {
    if (id !== agentId) selectAgent(id);
    setSearchParams({ agent: id }, { replace: true });
  };

  const pickConversation = (id: string | null) => {
    openConversation(id);
    if (agentId) {
      setSearchParams(id ? { agent: agentId, c: id } : { agent: agentId }, { replace: true });
    }
    setWorkspaceOpen(false);
  };

  const openWorkspace = () => {
    workspaceRestoreFocusRef.current = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    setWorkspaceOpen(true);
  };

  return (
    <div className="relative flex h-full min-h-0 w-full overflow-hidden bg-surface-raised">
      {workspaceOpen && (
        <button
          type="button"
          aria-label="关闭会话侧栏"
          tabIndex={-1}
          className="absolute inset-0 z-20 bg-sidebar/20 backdrop-blur-[1px] lg:hidden"
          onClick={() => setWorkspaceOpen(false)}
        />
      )}

      <aside
        ref={workspaceRef}
        role={workspaceModalOpen ? 'dialog' : 'complementary'}
        aria-modal={workspaceModalOpen || undefined}
        aria-label="会话工作区"
        aria-hidden={!workspaceInteractive || undefined}
        tabIndex={-1}
        className={`absolute inset-y-0 left-0 z-30 flex w-[min(360px,calc(100%-28px))] shrink-0 flex-col border-r border-border-subtle bg-surface-raised transition-transform duration-200 lg:relative lg:z-auto lg:w-[360px] lg:translate-x-0 ${
          workspaceOpen ? 'translate-x-0 shadow-level-3' : '-translate-x-full'
        }`}
      >
        <WorkspaceSidebar
          agents={agents}
          runtimeBindings={runtimeBindings}
          agentId={agentId}
          conversationId={conversationId}
          onPickAgent={pickAgent}
          onPickConversation={pickConversation}
          onClose={() => setWorkspaceOpen(false)}
        />
      </aside>

      <main ref={conversationMainRef} className="flex min-w-0 flex-1 flex-col overflow-hidden">
        {agentId ? (
          <ConversationPane runtimeBindings={runtimeBindings} onOpenWorkspace={openWorkspace} />
        ) : (
          <div className="flex flex-1 items-center justify-center px-6 text-text-secondary">
            <div className="max-w-sm text-center">
              <div className="mx-auto mb-4 flex h-12 w-12 items-center justify-center rounded-xl bg-brand-primary/10 text-brand-primary">
                <MessageSquare className="h-5 w-5" />
              </div>
              <p className="text-body-lg font-semibold text-text-primary">选择一个 Agent 开始对话</p>
              <p className="mt-1 text-body leading-6">每条消息都会创建可追踪的执行 Run。</p>
              <button type="button" className="btn-primary mt-5 lg:hidden" onClick={openWorkspace}>
                选择 Agent
              </button>
            </div>
          </div>
        )}
      </main>
    </div>
  );
}

function WorkspaceSidebar({
  agents,
  runtimeBindings,
  agentId,
  conversationId,
  onPickAgent,
  onPickConversation,
  onClose,
}: {
  agents: AgentProfile[];
  runtimeBindings: RuntimeBindingsSnapshot;
  agentId: string | null;
  conversationId: string | null;
  onPickAgent: (id: string) => void;
  onPickConversation: (id: string | null) => void;
  onClose: () => void;
}) {
  const conversations = useChatStore((s) => s.conversations);
  const runSnapshots = useRunsStore((s) => s.runs);
  const [agentMenuOpen, setAgentMenuOpen] = useState(false);
  const [query, setQuery] = useState('');
  const [oldestFirst, setOldestFirst] = useState(false);
  const agent = agents.find((item) => item.id === agentId);
  const runtimeView = agent ? agentRuntimeView(agent, runtimeBindings) : undefined;

  const visibleConversations = useMemo(() => {
    const normalized = query.trim().toLocaleLowerCase();
    const filtered = normalized
      ? conversations.filter((item) => item.title.toLocaleLowerCase().includes(normalized))
      : conversations;
    return oldestFirst ? [...filtered].reverse() : filtered;
  }, [conversations, oldestFirst, query]);

  return (
    <div className="flex h-full min-h-0 flex-col">
      <div className="relative shrink-0 border-b border-border-subtle p-5 pb-4">
        <div className="flex items-start gap-2">
          <button
            type="button"
            className="flex min-w-0 flex-1 items-center gap-3 rounded-xl border border-border-subtle bg-surface-raised p-3 text-left transition-colors hover:border-border-strong hover:bg-surface-base"
            aria-expanded={agentMenuOpen}
            onClick={() => setAgentMenuOpen((open) => !open)}
          >
            {agent ? <Avatar name={agent.name} url={agent.avatar} size={42} /> : <div className="h-[42px] w-[42px] rounded-full bg-surface-sunken" />}
            <div className="min-w-0 flex-1">
              <div className="truncate text-body-lg font-semibold text-text-primary">{agent?.name ?? '选择 Agent'}</div>
              <div className="mt-0.5 truncate text-caption text-text-secondary">{agent?.role ?? '选择协作角色'}</div>
            </div>
            {agent && (
              <span
                className="flex h-7 w-7 shrink-0 items-center justify-center rounded-lg bg-surface-base"
                title={runtimeView?.label}
                aria-label={`运行状态：${runtimeView?.label ?? '未知'}`}
              >
                <PresenceDot presence={runtimeView?.presence ?? agent.presence} pulse={false} />
              </span>
            )}
            <ChevronDown className={`h-4 w-4 shrink-0 text-text-secondary transition-transform ${agentMenuOpen ? 'rotate-180' : ''}`} />
          </button>
          <button
            type="button"
            aria-label="关闭会话侧栏"
            className="mt-1 rounded-lg p-2 text-text-secondary hover:bg-surface-base hover:text-text-primary lg:hidden"
            onClick={onClose}
          >
            <X className="h-4 w-4" />
          </button>
        </div>

        {agentMenuOpen && (
          <div className="absolute left-5 right-5 top-[82px] z-50 max-h-72 overflow-y-auto rounded-xl border border-border-subtle bg-surface-raised p-1.5 shadow-level-2">
            {agents.map((item) => {
              const itemRuntime = agentRuntimeView(item, runtimeBindings);
              return (
                <button
                  key={item.id}
                  type="button"
                  className={`flex w-full items-center gap-3 rounded-lg px-3 py-2.5 text-left transition-colors ${
                    item.id === agentId ? 'bg-brand-primary/[0.08]' : 'hover:bg-surface-base'
                  }`}
                  onClick={() => {
                    onPickAgent(item.id);
                    setAgentMenuOpen(false);
                  }}
                >
                  <Avatar name={item.name} url={item.avatar} size={32} />
                  <div className="min-w-0 flex-1">
                    <div className="truncate text-body font-medium text-text-primary">{item.name}</div>
                    <div className="truncate text-caption text-text-secondary">{item.role}</div>
                  </div>
                  <span title={itemRuntime.label} aria-label={`运行状态：${itemRuntime.label}`}>
                    <PresenceDot presence={itemRuntime.presence} pulse={false} />
                  </span>
                </button>
              );
            })}
          </div>
        )}

        <button
          type="button"
          className="mt-4 flex w-full items-center justify-center gap-2 rounded-xl border border-brand-primary/45 bg-brand-primary/[0.04] px-4 py-3 text-body font-semibold text-brand-accent transition-colors hover:bg-brand-primary/[0.09]"
          onClick={() => onPickConversation(null)}
        >
          <Plus className="h-4 w-4" />
          新对话
        </button>

      </div>

      <div className="shrink-0 px-5 pb-3 pt-4">
        <label className="flex min-w-0 items-center gap-2 rounded-xl border border-border-subtle bg-surface-raised px-3 py-2.5 transition-colors focus-within:border-brand-primary/40 focus-within:ring-2 focus-within:ring-brand-primary/10">
          <Search className="h-4 w-4 shrink-0 text-text-secondary" />
          <input
            aria-label="搜索会话"
            value={query}
            onChange={(event) => setQuery(event.target.value)}
            placeholder="搜索会话"
            className="min-w-0 flex-1 bg-transparent text-body text-text-primary outline-none placeholder:text-text-tertiary"
          />
        </label>
      </div>

      <div className="flex min-h-0 flex-1 flex-col">
        <div className="flex shrink-0 items-center justify-between px-5 py-2">
          <h3 className="text-body font-semibold text-text-primary">会话</h3>
          <button
            type="button"
            title={oldestFirst ? '切换为最近优先' : '切换为最早优先'}
            aria-pressed={oldestFirst}
            className="flex items-center gap-1.5 rounded-lg px-2 py-1.5 text-caption font-medium text-text-secondary transition-colors hover:bg-surface-base hover:text-text-primary"
            onClick={() => setOldestFirst((value) => !value)}
          >
            <ArrowUpDown className="h-3.5 w-3.5" />
            {oldestFirst ? '最早优先' : '最近使用'}
          </button>
        </div>
        <div className="flex-1 space-y-1 overflow-y-auto px-3 pb-3">
          {visibleConversations.map((item) => {
            const active = conversationId === item.id;
            return (
              <button
                key={item.id}
                type="button"
                className={`group relative w-full rounded-xl px-3.5 py-3 text-left transition-colors ${
                  active ? 'bg-brand-primary/[0.09]' : 'hover:bg-surface-base'
                }`}
                onClick={() => onPickConversation(item.id)}
              >
                {active && <span className="absolute inset-y-3 left-0 w-0.5 rounded-full bg-brand-primary" />}
                <div className="flex items-start gap-3">
                  <div className="min-w-0 flex-1">
                    <div className="truncate text-body font-medium text-text-primary">{item.title}</div>
                    <div className="mt-1 truncate text-caption text-text-secondary">
                      {item.runs_count} 轮 · {conversationLabel(item, runSnapshots)}
                    </div>
                  </div>
                  <span className="mt-0.5 text-caption tabular-nums text-text-tertiary">{relativeTime(item.updated_at)}</span>
                </div>
              </button>
            );
          })}
          {visibleConversations.length === 0 && (
            <EmptyState label={query ? '没有匹配的会话' : agentId ? '暂无历史会话' : '请先选择 Agent'} />
          )}
        </div>
      </div>

      {(query.trim() || oldestFirst) && (
        <button
          type="button"
          className="flex shrink-0 items-center justify-between border-t border-border-subtle px-5 py-3 text-caption font-medium text-text-secondary transition-colors hover:bg-surface-base hover:text-text-primary"
          onClick={() => {
            setQuery('');
            setOldestFirst(false);
          }}
        >
          <span>重置会话列表</span>
          <X className="h-4 w-4" />
        </button>
      )}
    </div>
  );
}

function ConversationPane({
  runtimeBindings,
  onOpenWorkspace,
}: {
  runtimeBindings: RuntimeBindingsSnapshot;
  onOpenWorkspace: () => void;
}) {
  const agentId = useChatStore((s) => s.agentId);
  const conversationId = useChatStore((s) => s.conversationId);
  const conversations = useChatStore((s) => s.conversations);
  const runs = useChatStore((s) => s.runs);
  const sending = useChatStore((s) => s.sending);
  const send = useChatStore((s) => s.send);
  const agents = useAgentsStore((s) => s.agents);
  const workspaceId = useWorkspaceStore((s) => s.workspace?.id);
  const me = useWorkspaceStore((s) => s.me);

  const timelines = useRunsStore((s) => s.timelines);
  const runSnapshots = useRunsStore((s) => s.runs);
  const watchRun = useRunsStore((s) => s.watchRun);
  const unwatchRun = useRunsStore((s) => s.unwatchRun);
  const approvals = useRunsStore((s) => s.approvals);

  const [draft, setDraft] = useState('');
  const [contextOpen, setContextOpen] = useState(false);
  const [pendingImages, setPendingImages] = useState<PendingChatImage[]>([]);
  const [uploadingImages, setUploadingImages] = useState(false);
  const pendingImagesRef = useRef<PendingChatImage[]>([]);
  const scrollRef = useRef<HTMLDivElement>(null);
  const headerRef = useRef<HTMLElement>(null);
  const conversationBodyRef = useRef<HTMLDivElement>(null);
  const contextRestoreFocusRef = useRef<HTMLElement | null>(null);
  const contextDocked = useMediaQuery('(min-width: 1280px)');
  const contextModalOpen = contextOpen && !contextDocked;

  useInert(headerRef, contextModalOpen);
  useInert(conversationBodyRef, contextModalOpen);

  useEffect(() => {
    pendingImagesRef.current = pendingImages;
  }, [pendingImages]);

  useEffect(() => () => {
    for (const image of pendingImagesRef.current) URL.revokeObjectURL(image.previewUrl);
  }, []);

  const agent = agents.find((item) => item.id === agentId);
  const runtimeView = agent ? agentRuntimeView(agent, runtimeBindings) : undefined;
  const conversation = conversations.find((item) => item.id === conversationId);
  const runIds = useMemo(() => runs.map((item) => item.id), [runs]);
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
  const liveStream = useMemo(
    () => (latestRunId ? aggregateRunStream(timelines[latestRunId] ?? []) : { reasoning: '', answerDraft: '' }),
    [latestRunId, timelines],
  );
  const awaitingReply =
    !!latestRun && ACTIVE.has(latestRun.status) && !messages.some((item) => item.kind === 'assistant' && item.runId === latestRunId);
  const runApprovals = latestRunId ? (approvals[latestRunId] ?? []) : [];

  useEffect(() => {
    const element = scrollRef.current;
    if (!element) return;
    element.scrollTo({ top: element.scrollHeight, behavior: 'smooth' });
  }, [messages.length, liveStream.reasoning.length, liveStream.answerDraft.length, runApprovals.length]);

  const usageText = formatTokenUsage(latestRun?.usage_in);
  const conversationTitle = conversation?.title ?? '新对话';

  const toggleContext = () => {
    if (!contextOpen) {
      contextRestoreFocusRef.current = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    }
    setContextOpen((open) => !open);
  };

  const selectImages = (files: File[]) => {
    const { accepted, rejected } = validateChatImages(files, pendingImages.length);
    if (rejected.length > 0) toast.error(rejected.join('；'));
    if (accepted.length === 0) return;
    setPendingImages((current) => [
      ...current,
      ...accepted.map((file) => ({
        id: crypto.randomUUID(),
        file,
        previewUrl: URL.createObjectURL(file),
      })),
    ]);
  };

  const removeImage = (id: string) => {
    setPendingImages((current) => {
      const removed = current.find((image) => image.id === id);
      if (removed) URL.revokeObjectURL(removed.previewUrl);
      return current.filter((image) => image.id !== id);
    });
  };

  const doSend = async () => {
    const draftAtSend = draft;
    const text = draftAtSend.trim();
    const imagesAtSend = pendingImages;
    if ((!text && imagesAtSend.length === 0) || sending || uploadingImages || runtimeView?.canSend === false || !workspaceId) return;
    setUploadingImages(true);
    try {
      const uploaded = await Promise.all(imagesAtSend.map((image) => uploadWorkspaceImage(workspaceId, image.file)));
      const instruction = buildImageInstruction(text, uploaded);
      const accepted = await send(instruction);
      if (accepted) {
        // 请求期间用户可能继续输入或添加图片；只清除本次发送时的内容。
        setDraft((current) => (current === draftAtSend ? '' : current));
        const sentIds = new Set(imagesAtSend.map((image) => image.id));
        setPendingImages((current) => current.filter((image) => {
          if (!sentIds.has(image.id)) return true;
          URL.revokeObjectURL(image.previewUrl);
          return false;
        }));
      }
    } catch (err) {
      toast.error(err instanceof Error ? err.message : '图片上传失败');
    } finally {
      setUploadingImages(false);
    }
  };

  return (
    <div className="flex min-h-0 flex-1 flex-col overflow-hidden bg-surface-raised">
      <header
        ref={headerRef}
        className="flex min-h-[92px] shrink-0 items-center justify-between gap-4 border-b border-border-subtle px-4 py-3 sm:px-6"
      >
        <div className="flex min-w-0 items-start gap-3">
          <button
            type="button"
            aria-label="打开会话侧栏"
            className="mt-0.5 rounded-lg p-2 text-text-secondary hover:bg-surface-base hover:text-text-primary lg:hidden"
            onClick={onOpenWorkspace}
          >
            <PanelLeftOpen className="h-5 w-5" />
          </button>
          <div className="min-w-0">
            <h1 className="truncate text-[20px] font-semibold leading-7 text-text-primary">{conversationTitle}</h1>
            <div className="mt-1.5 flex min-w-0 flex-wrap items-center gap-x-2.5 gap-y-1 text-caption text-text-secondary">
              {agent && <Avatar name={agent.name} url={agent.avatar} size={24} />}
              <span className="font-medium text-text-primary">{agent?.name ?? ''}</span>
              {agent && (
                <span className="flex items-center gap-1.5">
                  <PresenceDot presence={runtimeView?.presence ?? agent.presence} pulse={false} />
                  {runtimeView?.label}
                </span>
              )}
              {latestRun && (
                <>
                  <span className="h-3 w-px bg-border-strong" />
                  <span className={`flex items-center gap-1.5 font-medium ${runStatusColor(latestRun.status)}`}>
                    <CircleDot className={`h-3.5 w-3.5 ${ACTIVE.has(latestRun.status) ? 'status-pulse' : ''}`} />
                    {runStatusText(latestRun.status)}
                  </span>
                </>
              )}
            </div>
          </div>
        </div>

        <button
          type="button"
          aria-expanded={contextOpen}
          className={`flex shrink-0 items-center gap-2 rounded-lg px-3 py-2 text-body font-medium transition-colors ${
            contextOpen ? 'bg-brand-primary/10 text-brand-accent' : 'text-text-secondary hover:bg-surface-base hover:text-text-primary'
          }`}
          onClick={toggleContext}
        >
          {contextOpen ? <PanelRightClose className="h-4 w-4" /> : <PanelRightOpen className="h-4 w-4" />}
          <span className="hidden sm:inline">{contextOpen ? '收起' : '任务上下文'}</span>
          <ChevronRight className={`h-4 w-4 transition-transform ${contextOpen ? 'rotate-180' : ''}`} />
        </button>
      </header>

      <div className="relative flex min-h-0 flex-1 overflow-hidden">
        <div ref={conversationBodyRef} className="flex min-w-0 flex-1 flex-col">
          <div ref={scrollRef} className="min-h-0 flex-1 overflow-y-auto overscroll-contain bg-surface-base/70 px-4 py-6 sm:px-6">
            <div className="w-full space-y-5">
              {messages.length === 0 ? (
                <ConversationStarter agent={agent} userName={me?.name ?? 'Demo User'} onPrompt={setDraft} />
              ) : (
                <>
                  {renderTranscript(messages, agent, me?.name ?? 'Demo User')}
                  {!awaitingReply && messages.length <= 3 && <QuickActions onPrompt={setDraft} />}
                </>
              )}
              {awaitingReply && (
                <AssistantMessage agent={agent} streaming>
                  <AssistantTurn
                    reasoning={liveStream.reasoning}
                    text={liveStream.answerDraft}
                    streaming
                    reasoningOnly={!liveStream.answerDraft && Boolean(liveStream.reasoning)}
                  />
                </AssistantMessage>
              )}
              {runApprovals.map((approval) => (
                <div key={approval.id} className="ml-0 sm:ml-12">
                  <ApprovalCard approval={approval} />
                </div>
              ))}
            </div>
          </div>

          <Composer
            draft={draft}
            sending={sending}
            uploadingImages={uploadingImages}
            activeRun={Boolean(latestRun && ACTIVE.has(latestRun.status))}
            usageText={usageText}
            contextOpen={contextOpen}
            blockedReason={runtimeView?.blockedReason}
            images={pendingImages}
            onDraftChange={setDraft}
            onSelectImages={selectImages}
            onRemoveImage={removeImage}
            onSend={() => void doSend()}
            onToggleContext={toggleContext}
          />
        </div>

        {contextOpen && (
          <>
            {contextModalOpen && (
              <button
                type="button"
                tabIndex={-1}
                aria-label="关闭任务上下文"
                className="absolute inset-0 z-10 bg-sidebar/20 backdrop-blur-[1px] xl:hidden"
                onClick={() => setContextOpen(false)}
              />
            )}
            <ContextPanel
              modal={contextModalOpen}
              restoreFocusRef={contextRestoreFocusRef}
              agent={agent}
              conversationTitle={conversationTitle}
              latestRun={latestRun}
              messages={messages}
              runsCount={runs.length}
              approvalsCount={runApprovals.length}
              usageText={usageText}
              onClose={() => setContextOpen(false)}
            />
          </>
        )}
      </div>
    </div>
  );
}

function ConversationStarter({
  agent,
  userName,
  onPrompt,
}: {
  agent?: AgentProfile;
  userName: string;
  onPrompt: (prompt: string) => void;
}) {
  return (
    <div className="w-full pb-8 pt-2 sm:pt-4">
      <div className="mb-6 flex items-center gap-3" aria-label="示例对话">
        <span className="h-px flex-1 bg-border-subtle" />
        <span className="shrink-0 text-[11px] font-medium tracking-wide text-text-tertiary">交互示例 · 发送后进入真实会话</span>
        <span className="h-px flex-1 bg-border-subtle" />
      </div>

      <div className="space-y-5">
        <UserBubble text={PREVIEW_USER_MESSAGE} userName={userName} timeLabel="09:41" />
        <AssistantMessage agent={agent} timeLabel="09:42">
          <AssistantTurn text={PREVIEW_ASSISTANT_MESSAGE} />
        </AssistantMessage>
      </div>

      <div className="mt-8 text-center">
        <p className="text-caption text-text-secondary">选择一个快捷指令，或直接在下方输入你的任务。</p>
        <QuickActions onPrompt={onPrompt} className="mt-3 justify-center" />
      </div>
    </div>
  );
}

function QuickActions({
  onPrompt,
  className = '',
}: {
  onPrompt: (prompt: string) => void;
  className?: string;
}) {
  return (
    <div className={`flex flex-wrap gap-2 ${className || 'sm:ml-12'}`}>
      {QUICK_PROMPTS.map((item, index) => (
        <button
          key={item.label}
          type="button"
          className="inline-flex items-center gap-2 rounded-lg border border-border-subtle bg-surface-raised px-3 py-2 text-caption font-medium text-text-secondary transition-colors hover:border-border-strong hover:bg-surface-base hover:text-text-primary"
          onClick={() => onPrompt(item.prompt)}
        >
          {index === 0 ? <Sparkles className="h-3.5 w-3.5" /> : index === 1 ? <CheckCircle2 className="h-3.5 w-3.5" /> : <PanelLeftOpen className="h-3.5 w-3.5" />}
          {item.label}
        </button>
      ))}
    </div>
  );
}

function Composer({
  draft,
  sending,
  uploadingImages,
  activeRun,
  usageText,
  contextOpen,
  blockedReason,
  images,
  onDraftChange,
  onSelectImages,
  onRemoveImage,
  onSend,
  onToggleContext,
}: {
  draft: string;
  sending: boolean;
  uploadingImages: boolean;
  activeRun: boolean;
  usageText: string | null;
  contextOpen: boolean;
  blockedReason?: string;
  images: PendingChatImage[];
  onDraftChange: (value: string) => void;
  onSelectImages: (files: File[]) => void;
  onRemoveImage: (id: string) => void;
  onSend: () => void;
  onToggleContext: () => void;
}) {
  const inputRef = useRef<HTMLTextAreaElement>(null);
  const fileInputRef = useRef<HTMLInputElement>(null);

  useEffect(() => {
    const input = inputRef.current;
    if (!input) return;
    input.style.height = 'auto';
    input.style.height = `${Math.min(input.scrollHeight, 120)}px`;
  }, [draft]);

  return (
    <div className="shrink-0 bg-surface-base/70 px-3 pb-3 pt-2 sm:px-6 sm:pb-4">
      <div className="w-full overflow-hidden rounded-[14px] border border-border-strong bg-surface-raised shadow-level-1 transition-[border-color,box-shadow] duration-200 focus-within:border-brand-primary/45 focus-within:shadow-[0_0_0_3px_hsl(var(--color-brand-primary)/0.08)]">
        {images.length > 0 && (
          <div className="flex gap-2 overflow-x-auto border-b border-border-subtle bg-surface-base/45 px-3 py-2">
            {images.map((image) => (
              <div key={image.id} className="flex w-[180px] shrink-0 items-center gap-2 rounded-lg border border-border-subtle bg-surface-raised p-1.5">
                <img src={image.previewUrl} alt={image.file.name} className="h-10 w-10 shrink-0 rounded-md object-cover" />
                <span className="min-w-0 flex-1 truncate text-caption text-text-secondary" title={image.file.name}>{image.file.name}</span>
                <button
                  type="button"
                  aria-label={`移除图片 ${image.file.name}`}
                  className="flex h-7 w-7 shrink-0 items-center justify-center rounded-md text-text-tertiary transition-colors hover:bg-surface-base hover:text-text-primary"
                  onClick={() => onRemoveImage(image.id)}
                  disabled={uploadingImages}
                >
                  <X className="h-3.5 w-3.5" />
                </button>
              </div>
            ))}
          </div>
        )}
        <div className="flex items-end gap-1.5 p-2">
          <input
            ref={fileInputRef}
            type="file"
            accept={CHAT_IMAGE_TYPES.join(',')}
            multiple
            className="sr-only"
            aria-label="选择要上传的图片"
            onChange={(event) => {
              onSelectImages(Array.from(event.target.files ?? []));
              event.target.value = '';
            }}
          />
          <button
            type="button"
            title={`上传图片，最多 ${MAX_CHAT_IMAGES} 张`}
            aria-label="上传图片"
            className="flex h-10 shrink-0 items-center gap-1.5 rounded-lg px-2.5 text-caption font-medium text-text-secondary transition-colors hover:bg-surface-base hover:text-text-primary disabled:cursor-not-allowed disabled:opacity-45"
            onClick={() => fileInputRef.current?.click()}
            disabled={uploadingImages || images.length >= MAX_CHAT_IMAGES}
          >
            <ImagePlus className="h-4 w-4" />
            <span className="hidden sm:inline">上传图片</span>
          </button>
          <button
            type="button"
            aria-pressed={contextOpen}
            className={`flex h-10 shrink-0 items-center gap-1.5 rounded-lg px-2.5 text-caption font-medium transition-colors ${
              contextOpen ? 'bg-brand-primary/10 text-brand-accent' : 'text-text-secondary hover:bg-surface-base hover:text-text-primary'
            }`}
            onClick={onToggleContext}
          >
            <PanelRightOpen className="h-4 w-4" />
            <span className="hidden sm:inline">上下文</span>
          </button>
          <textarea
            ref={inputRef}
            value={draft}
            onChange={(event) => onDraftChange(event.target.value)}
            onKeyDown={(event) => {
              if (event.key === 'Enter' && !event.shiftKey && !event.nativeEvent.isComposing) {
                event.preventDefault();
                if (!sending && !uploadingImages && !blockedReason) onSend();
              }
            }}
            rows={1}
            aria-label="消息内容"
            aria-busy={sending || uploadingImages || undefined}
            aria-describedby={blockedReason ? 'composer-runtime-status' : undefined}
            placeholder={activeRun ? '追加运行指令…' : '输入消息，Enter 发送'}
            className="min-h-10 max-h-[120px] min-w-0 flex-1 resize-none overflow-y-auto bg-transparent px-2 py-2.5 text-body leading-5 text-text-primary outline-none placeholder:text-text-tertiary focus-visible:ring-0 focus-visible:ring-offset-0"
          />
          {usageText && <span className="hidden shrink-0 px-1 text-caption tabular-nums text-text-tertiary lg:inline">{usageText}</span>}
          <button
            type="button"
            onClick={onSend}
            title={blockedReason}
            disabled={(!draft.trim() && images.length === 0) || sending || uploadingImages || Boolean(blockedReason)}
            className="flex h-10 shrink-0 items-center gap-1.5 rounded-lg bg-brand-primary px-3.5 text-body font-semibold text-white transition-all hover:bg-brand-accent active:scale-[0.98] disabled:cursor-not-allowed disabled:bg-surface-sunken disabled:text-text-tertiary disabled:opacity-100"
          >
            <SendHorizonal className="h-4 w-4" />
            <span className="hidden sm:inline">{uploadingImages ? '上传中' : sending ? '发送中' : '发送'}</span>
          </button>
        </div>
      </div>
      {blockedReason && (
        <p id="composer-runtime-status" role="status" className="mt-1.5 w-full px-2 text-caption text-status-warning">
          当前无法发送 · {blockedReason}。
          {blockedReason.includes('正在') ? (
            '请稍候。'
          ) : (
            <Link className="ml-1 font-medium text-brand-accent hover:underline" to="/settings">检查运行环境设置</Link>
          )}
        </p>
      )}
    </div>
  );
}

function ContextPanel({
  modal,
  restoreFocusRef,
  agent,
  conversationTitle,
  latestRun,
  messages,
  runsCount,
  approvalsCount,
  usageText,
  onClose,
}: {
  modal: boolean;
  restoreFocusRef: RefObject<HTMLElement | null>;
  agent?: AgentProfile;
  conversationTitle: string;
  latestRun?: ExecutionRun;
  messages: ChatMessage[];
  runsCount: number;
  approvalsCount: number;
  usageText: string | null;
  onClose: () => void;
}) {
  const latestPlan = [...messages].reverse().find((item) => item.kind === 'plan');
  const panelRef = useRef<HTMLElement>(null);
  useModalFocus(modal, panelRef, onClose, restoreFocusRef);

  return (
    <aside
      ref={panelRef}
      role={modal ? 'dialog' : 'complementary'}
      aria-modal={modal || undefined}
      aria-label="任务上下文"
      tabIndex={-1}
      className="absolute inset-y-0 right-0 z-20 flex w-[min(328px,calc(100%-24px))] shrink-0 flex-col border-l border-border-subtle bg-surface-raised shadow-level-2 xl:relative xl:z-auto xl:w-[328px] xl:shadow-none"
    >
      <div className="flex h-14 shrink-0 items-center justify-between border-b border-border-subtle px-4">
        <h2 className="text-body font-semibold text-text-primary">任务上下文</h2>
        <button type="button" aria-label="关闭任务上下文" className="flex h-9 w-9 items-center justify-center rounded-lg text-text-secondary transition-colors hover:bg-surface-base hover:text-text-primary" onClick={onClose}>
          <X className="h-4 w-4" />
        </button>
      </div>
      <div className="flex-1 overflow-y-auto">
        <section className="border-b border-border-subtle px-4 py-4">
          <div className="text-caption font-semibold tracking-[0.04em] text-text-tertiary">当前任务</div>
          <div className="mt-2 text-body font-semibold text-text-primary">{conversationTitle}</div>
          <div className="mt-2 flex items-center gap-2 text-caption">
            <span className={`flex items-center gap-1.5 font-medium ${latestRun ? runStatusColor(latestRun.status) : 'text-text-secondary'}`}>
              <CircleDot className="h-3.5 w-3.5" />
              {latestRun ? runStatusText(latestRun.status) : '尚未开始'}
            </span>
            {agent && <span className="text-text-secondary">· {agent.name}</span>}
          </div>
        </section>

        <section className="border-b border-border-subtle px-4 py-4">
          <div className="flex items-center justify-between">
            <div className="text-caption font-semibold tracking-[0.04em] text-text-tertiary">执行步骤</div>
            <span className="text-caption text-text-tertiary">{latestPlan?.steps?.length ?? 0} 项</span>
          </div>
          {latestPlan?.steps?.length ? (
            <div className="mt-3 space-y-3">
              {latestPlan.steps.map((step) => (
                <div key={step.step} className="flex items-start gap-2.5">
                  {step.status === 'completed' ? (
                    <CheckCircle2 className="mt-0.5 h-4 w-4 shrink-0 text-status-success" />
                  ) : (
                    <CircleDot className={`mt-0.5 h-4 w-4 shrink-0 ${step.status === 'in_progress' ? 'text-brand-primary' : 'text-text-tertiary'}`} />
                  )}
                  <span className="text-caption leading-5 text-text-secondary">{step.step}</span>
                </div>
              ))}
            </div>
          ) : (
            <p className="mt-3 text-caption leading-5 text-text-secondary">运行开始后，计划步骤会在这里持续更新。</p>
          )}
        </section>

        <section className="px-4 py-4">
          <div className="text-caption font-semibold tracking-[0.04em] text-text-tertiary">会话信息</div>
          <dl className="mt-3 space-y-3 text-caption">
            <div className="flex items-center justify-between gap-3">
              <dt className="flex items-center gap-2 text-text-secondary"><Clock3 className="h-3.5 w-3.5" />运行轮次</dt>
              <dd className="font-medium tabular-nums text-text-primary">{runsCount}</dd>
            </div>
            <div className="flex items-center justify-between gap-3">
              <dt className="flex items-center gap-2 text-text-secondary"><CheckCircle2 className="h-3.5 w-3.5" />审批记录</dt>
              <dd className="font-medium tabular-nums text-text-primary">{approvalsCount}</dd>
            </div>
            <div className="flex items-center justify-between gap-3">
              <dt className="text-text-secondary">上下文用量</dt>
              <dd className="font-medium tabular-nums text-text-primary">{usageText ?? '暂无数据'}</dd>
            </div>
          </dl>
        </section>
      </div>
    </aside>
  );
}

function renderTranscript(messages: ChatMessage[], agent: AgentProfile | undefined, userName: string): ReactNode[] {
  const nodes: ReactNode[] = [];
  const segments = groupActivity(messages);
  for (let index = 0; index < segments.length; index++) {
    const segment = segments[index];
    if (segment.kind === 'activity') {
      nodes.push(
        <div key={segment.items[0].key} className="ml-0 sm:ml-12">
          <ActivityGroup items={segment.items} />
        </div>,
      );
      continue;
    }
    const message = segment.item;
    if (message.kind === 'user') {
      nodes.push(<UserTurn key={message.key} msg={message} userName={userName} />);
      continue;
    }
    if (message.kind === 'thinking') {
      const lookahead = segments[index + 1];
      const next = lookahead?.kind === 'single' ? lookahead.item : undefined;
      if (next?.kind === 'assistant' && next.runId === message.runId) {
        nodes.push(
          <AssistantMessage key={message.runId} agent={agent} at={next.at}>
            <AssistantTurn reasoning={message.text} text={next.text} reasoningOnly={!next.text && Boolean(message.text)} />
          </AssistantMessage>,
        );
        index += 1;
        continue;
      }
      nodes.push(
        <AssistantMessage key={message.key} agent={agent} at={message.at}>
          <AssistantTurn reasoning={message.text} reasoningOnly />
        </AssistantMessage>,
      );
      continue;
    }
    if (message.kind === 'assistant') {
      nodes.push(
        <AssistantMessage key={message.key} agent={agent} at={message.at}>
          <AssistantTurn text={message.text} />
        </AssistantMessage>,
      );
      continue;
    }
    if (message.kind === 'plan') {
      nodes.push(
        <div key={message.key} className="ml-0 sm:ml-12">
          <PlanCard msg={message} />
        </div>,
      );
      continue;
    }
    nodes.push(<MetaLine key={message.key} msg={message} />);
  }
  return nodes;
}

function UserTurn({ msg, userName }: { msg: ChatMessage; userName: string }) {
  return <UserBubble text={msg.text} userName={userName} timeLabel={formatTime(msg.at)} />;
}

function UserBubble({ text, userName, timeLabel }: { text: string; userName: string; timeLabel?: string }) {
  return (
    <article className="flex justify-end gap-2.5 py-1 sm:gap-3">
      <div className="flex max-w-[min(680px,82%)] min-w-0 flex-col items-end">
        <div className="mb-1.5 flex items-center justify-end gap-2 px-1">
          {timeLabel && <time className="text-[11px] tabular-nums text-text-tertiary">{timeLabel}</time>}
          <span className="text-caption font-medium text-text-secondary">{userName}</span>
        </div>
        <div className="rounded-[18px] rounded-tr-[6px] bg-brand-primary px-4 py-3 text-white shadow-level-1">
          <p className="whitespace-pre-wrap break-words text-base leading-6">{text}</p>
        </div>
      </div>
      <Avatar name={userName} size={36} />
    </article>
  );
}

function AssistantMessage({
  agent,
  at,
  timeLabel,
  streaming = false,
  children,
}: {
  agent?: AgentProfile;
  at?: string;
  timeLabel?: string;
  streaming?: boolean;
  children: ReactNode;
}) {
  return (
    <article className="flex justify-start gap-2.5 py-1 sm:gap-3">
      {agent ? <Avatar name={agent.name} url={agent.avatar} size={36} /> : <div className="h-9 w-9 shrink-0 rounded-full bg-brand-primary/15" />}
      <div className="min-w-0 max-w-[min(720px,82%)]">
        <div className="mb-1.5 flex items-center gap-2 px-1">
          <span className="text-body font-semibold text-text-primary">{agent?.name ?? 'Agent'}</span>
          {(timeLabel || at) && <time className="text-[11px] tabular-nums text-text-tertiary">{timeLabel ?? formatTime(at ?? '')}</time>}
          <span className={`inline-flex items-center gap-1 text-[11px] font-medium ${streaming ? 'text-brand-accent' : 'text-status-success'}`}>
            <span className={`h-1.5 w-1.5 rounded-full ${streaming ? 'status-pulse bg-brand-primary' : 'bg-status-success'}`} />
            {streaming ? '生成中' : '已回复'}
          </span>
        </div>
        <div className="rounded-[18px] rounded-tl-[6px] border border-border-subtle bg-surface-raised px-4 py-3 shadow-level-1">{children}</div>
      </div>
    </article>
  );
}

function MetaLine({ msg }: { msg: ChatMessage }) {
  const isError = msg.kind === 'error';
  return (
    <div className="py-0.5 sm:ml-12">
      <div className={`flex items-center justify-center gap-1.5 text-caption ${isError ? 'text-status-error' : 'text-text-secondary'}`}>
        {isError && <CircleX className="h-3.5 w-3.5" />}
        <span>{msg.text}</span>
        <span className="tabular-nums">{formatTime(msg.at)}</span>
      </div>
      {msg.detail && (
        <pre className="mx-auto mt-1 max-h-48 w-full max-w-2xl overflow-y-auto whitespace-pre-wrap break-words rounded-md bg-surface-base px-3 py-2 text-left font-mono text-[11px] leading-4 text-text-secondary">
          {msg.detail}
        </pre>
      )}
    </div>
  );
}

function relativeTime(value: string): string {
  const timestamp = Date.parse(value);
  if (!Number.isFinite(timestamp)) return '';
  const delta = Date.now() - timestamp;
  if (delta < 60_000) return '刚刚';
  if (delta < 3_600_000) return `${Math.max(1, Math.floor(delta / 60_000))} 分前`;
  if (delta < 86_400_000) return `${Math.floor(delta / 3_600_000)} 时前`;
  if (delta < 172_800_000) return '昨天';
  if (delta < 7 * 86_400_000) return `${Math.floor(delta / 86_400_000)} 天前`;
  return '更早';
}
