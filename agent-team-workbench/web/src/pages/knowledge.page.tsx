import {
  AlertCircle,
  BookOpen,
  Bot,
  FileText,
  GitBranch,
  MessageCircle,
  RefreshCw,
  Search,
} from 'lucide-react';
import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from 'react';
import { useNavigate } from 'react-router-dom';
import { ApiError } from '../api/client';
import {
  getKnowledgeConfig,
  getKnowledgeItem,
  getKnowledgeVersion,
  listKnowledgeItems,
  listKnowledgeRelations,
  listKnowledgeVersions,
  type KnowledgeConfig,
  type KnowledgeItem,
  type KnowledgeItemDetails,
  type KnowledgeRelation,
  type KnowledgeSource,
  type KnowledgeVersion,
} from '../api/knowledge';
import type { AgentProfile } from '../api/types';
import { AgentOutput } from '../components/chat/agent-output';
import { Drawer } from '../components/drawer';
import { Button, Card, EmptyState, Field, Input, Select, Skeleton, StatusPill, cx } from '../components/ui';
import { useAgentsStore } from '../stores/agents.store';
import { useWorkspaceStore } from '../stores/workspace.store';
import { captureScope, isCurrent } from '../stores/scope';
import { formatDateTime } from '../utils/format';
import { isKnowledgeLibrarianAgent } from '../utils/agent-scope';

export { isKnowledgeLibrarianAgent } from '../utils/agent-scope';

type AsyncState<T> =
  | { kind: 'loading' }
  | { kind: 'ready'; value: T }
  | { kind: 'error'; message: string };

type KnowledgeListValue<T> = { items: T[]; next_cursor?: string | null; truncated?: boolean };
type KnowledgeListState<T> = AsyncState<KnowledgeListValue<T>>;

const STATUS_LABELS: Record<string, string> = {
  candidate: '候选',
  draft: '草稿',
  effective: '已发布',
  superseded: '已取代',
  repealed: '已废止',
  accepted: '已发布',
  merged: '已合并',
  rejected: '已拒绝',
  needs_review: '待复核',
  received: '已收件',
  processing: '整理中',
  complete: '完整',
  partial: '部分覆盖',
  missing: '存在缺口',
  conflict: '存在冲突',
};

const STATUS_CLASSES: Record<string, string> = {
  effective: 'border-status-success/30 bg-status-success/10 text-status-success',
  accepted: 'border-status-success/30 bg-status-success/10 text-status-success',
  complete: 'border-status-success/30 bg-status-success/10 text-status-success',
  draft: 'border-status-warning/35 bg-status-warning/10 text-status-warning',
  candidate: 'border-status-warning/35 bg-status-warning/10 text-status-warning',
  needs_review: 'border-status-warning/35 bg-status-warning/10 text-status-warning',
  partial: 'border-status-warning/35 bg-status-warning/10 text-status-warning',
  missing: 'border-status-warning/35 bg-status-warning/10 text-status-warning',
  processing: 'border-status-info/30 bg-status-info/10 text-status-info',
  received: 'border-border-strong bg-surface-sunken text-text-secondary',
  merged: 'border-status-info/30 bg-status-info/10 text-status-info',
  conflict: 'border-status-error/35 bg-status-error/10 text-status-error',
  rejected: 'border-status-error/35 bg-status-error/10 text-status-error',
  repealed: 'border-border-subtle bg-surface-sunken text-text-tertiary',
  superseded: 'border-border-subtle bg-surface-sunken text-text-tertiary',
};

const RELATION_LABELS: Record<string, string> = {
  related_to: '相关于',
  depends_on: '依赖',
  impacts: '影响',
  triggers: '触发',
  calls: '调用',
  subscribes_to: '订阅',
  shares_state: '共享状态',
  constrained_by: '受约束于',
  conflicts_with: '冲突',
  supersedes: '取代',
};

const KNOWLEDGE_KIND_LABELS: Record<string, string> = {
  requirement: '产品需求',
  agreement: '团队约定',
  function: '功能说明',
  rule: '业务规则',
  decision: '决策记录',
  fact: '事实',
  observation: '观察与经验',
};

const SOURCE_KIND_LABELS: Record<string, string> = {
  run: '运行记录',
  artifact: '产物',
  work_item: '任务',
  document: '文档',
  code: '代码',
  test: '测试',
  user: '用户提供',
  agent: '智能体记录',
};

const KIND_FILTERS = [
  { value: '', label: '全部' },
  { value: 'requirement', label: '产品需求' },
  { value: 'agreement', label: '团队约定' },
  { value: 'function', label: '功能说明' },
  { value: 'rule', label: '业务规则' },
  { value: 'decision', label: '决策记录' },
  { value: 'fact', label: '事实' },
  { value: 'observation', label: '观察与经验' },
];

function statusLabel(status: string | undefined): string {
  return status ? STATUS_LABELS[status] ?? status : '未标记';
}

export function knowledgeStatusClass(status: string | undefined): string {
  return STATUS_CLASSES[status ?? ''] ?? 'border-border-subtle bg-surface-raised text-text-secondary';
}

export function relationLabel(kind: string): string {
  return RELATION_LABELS[kind] ?? '其他关联';
}

export function knowledgeKindLabel(kind: string): string {
  return KNOWLEDGE_KIND_LABELS[kind] ?? '其他知识';
}

export function sourceKindLabel(kind: string): string {
  return SOURCE_KIND_LABELS[kind] ?? '其他来源';
}

export function buildKnowledgeChatPath(agentId: string | undefined): string | null {
  const normalized = agentId?.trim();
  return normalized ? '/chat?agent=' + encodeURIComponent(normalized) : null;
}

export function formatKnowledgeScope(scope: Record<string, unknown> | undefined): string {
  if (!scope || Object.keys(scope).length === 0) return '未限定适用范围';
  return Object.entries(scope)
    .map(([key, value]) => key + '=' + String(value))
    .join(' · ');
}

export function sortKnowledgeItems(items: KnowledgeItem[]): KnowledgeItem[] {
  return [...items].sort((left, right) => {
    const leftTime = Date.parse(left.updated_at || left.created_at) || 0;
    const rightTime = Date.parse(right.updated_at || right.created_at) || 0;
    return rightTime - leftTime || right.title.localeCompare(left.title);
  });
}

export function filterKnowledgeItems(items: KnowledgeItem[], query: string): KnowledgeItem[] {
  const normalized = query.trim().toLocaleLowerCase();
  if (!normalized) return items;
  return items.filter((item) => [
    item.title,
    item.summary,
    ...(item.tags ?? []),
    ...(item.aliases ?? []),
  ].filter(Boolean).some((value) => value?.toLocaleLowerCase().includes(normalized)));
}

export function knowledgeListEmptyState(searchQuery: string, hasMore: boolean): { title: string; description: string } {
  if (hasMore) {
    return {
      title: '已加载内容暂未匹配',
      description: '还有更多知识待加载，继续加载后再判断全库是否匹配。',
    };
  }
  if (searchQuery.trim()) {
    return {
      title: '没有匹配的知识',
      description: '换一个关键词，或清空搜索后查看全部已发布知识。',
    };
  }
  return {
    title: '这里还没有已发布知识',
    description: '与团队成员聊需求，成员会交给管理员记录。',
  };
}

export type KnowledgeSourceVerificationLabel =
  | '已核验运行记录'
  | '已核验元信息'
  | '已核验任务引用'
  | '已登记摘录'
  | '未核验代码/测试'
  | '仅核验身份'
  | '待核验';

export function sourceVerificationLabel(source: KnowledgeSource): KnowledgeSourceVerificationLabel {
  const metadata = source.metadata;
  if (!source.id) return '待核验';
  switch (metadata?.verification) {
    case 'run_output_verified':
      return metadata.digest_verified === true ? '已核验运行记录' : '待核验';
    case 'artifact_manifest_verified':
      return metadata.content_read === false ? '已核验元信息' : '待核验';
    case 'work_item_reference_verified':
      return '已核验任务引用';
    case 'submitted_excerpt':
      return '已登记摘录';
    case 'submitted_excerpt_not_repository_verified':
      return '未核验代码/测试';
    case 'agent_identity_only':
      return '仅核验身份';
    default:
      return '待核验';
  }
}

function errorMessage(error: unknown, fallback: string): string {
  if (error instanceof ApiError) return error.message || fallback;
  if (error instanceof Error) return error.message || fallback;
  return fallback;
}

export default function KnowledgePage() {
  const workspace = useWorkspaceStore((state) => state.workspace);
  const workspaceId = workspace?.id ?? null;
  const agents = useAgentsStore((state) => state.agents);
  const navigate = useNavigate();
  const [configState, setConfigState] = useState<AsyncState<KnowledgeConfig>>({ kind: 'loading' });
  const [itemsState, setItemsState] = useState<KnowledgeListState<KnowledgeItem>>({ kind: 'loading' });
  const [kindFilter, setKindFilter] = useState('');
  const [searchQuery, setSearchQuery] = useState('');
  const [selectedItem, setSelectedItem] = useState<KnowledgeItem | null>(null);
  const [refreshing, setRefreshing] = useState(false);
  const [itemsLoadingMore, setItemsLoadingMore] = useState(false);
  const [itemsLoadMoreError, setItemsLoadMoreError] = useState<string | undefined>();
  const configRequest = useRef(0);
  const itemsRequest = useRef(0);

  const loadConfig = useCallback(async () => {
    if (!workspaceId) return;
    const scope = captureScope();
    const request = ++configRequest.current;
    setConfigState({ kind: 'loading' });
    try {
      const value = await getKnowledgeConfig(workspaceId);
      if (request !== configRequest.current || !isCurrent(scope)) return;
      setConfigState({ kind: 'ready', value });
    } catch (error) {
      if (request !== configRequest.current || !isCurrent(scope)) return;
      setConfigState({ kind: 'error', message: errorMessage(error, '知识管理员入口读取失败') });
    }
  }, [setConfigState, workspaceId]);

  const loadItems = useCallback(async () => {
    if (!workspaceId) return;
    const scope = captureScope();
    const request = ++itemsRequest.current;
    setItemsLoadingMore(false);
    setItemsLoadMoreError(undefined);
    setItemsState({ kind: 'loading' });
    try {
      const value = await listKnowledgeItems(workspaceId, {
        status: 'effective',
        kind: kindFilter || undefined,
        limit: 200,
      });
      if (request !== itemsRequest.current || !isCurrent(scope)) return;
      setItemsState({
        kind: 'ready',
        value: { ...value, items: sortKnowledgeItems(value.items) },
      });
    } catch (error) {
      if (request !== itemsRequest.current || !isCurrent(scope)) return;
      setItemsState({ kind: 'error', message: errorMessage(error, '已发布知识读取失败') });
    }
  }, [kindFilter, setItemsLoadMoreError, setItemsLoadingMore, setItemsState, workspaceId]);

  const loadMoreItems = useCallback(async () => {
    if (!workspaceId || itemsLoadingMore || itemsState.kind !== 'ready') return;
    const cursor = itemsState.value.next_cursor;
    if (!cursor) return;
    const scope = captureScope();
    const request = ++itemsRequest.current;
    setItemsLoadingMore(true);
    setItemsLoadMoreError(undefined);
    try {
      const value = await listKnowledgeItems(workspaceId, {
        status: 'effective',
        kind: kindFilter || undefined,
        cursor,
        limit: 200,
      });
      if (request !== itemsRequest.current || !isCurrent(scope)) return;
      setItemsState((state) => {
        if (state.kind !== 'ready') return state;
        const existing = new Set(state.value.items.map((item) => item.id));
        const appended = sortKnowledgeItems(value.items).filter((item) => !existing.has(item.id));
        return {
          kind: 'ready',
          value: {
            ...state.value,
            items: [...state.value.items, ...appended],
            next_cursor: value.next_cursor ?? null,
            truncated: value.truncated,
          },
        };
      });
    } catch (error) {
      if (request !== itemsRequest.current || !isCurrent(scope)) return;
      setItemsLoadMoreError(errorMessage(error, '更多知识读取失败'));
    } finally {
      if (request === itemsRequest.current && isCurrent(scope)) setItemsLoadingMore(false);
    }
  }, [itemsLoadingMore, itemsState, kindFilter, setItemsLoadMoreError, setItemsLoadingMore, setItemsState, workspaceId]);

  useEffect(() => {
    if (!workspaceId) return;
    void loadConfig();
  }, [loadConfig, workspaceId]);

  useEffect(() => {
    if (!workspaceId) return;
    setSelectedItem(null);
    void loadItems();
  }, [loadItems, workspaceId]);

  const visibleItems = useMemo(
    () => itemsState.kind === 'ready' ? filterKnowledgeItems(itemsState.value.items, searchQuery) : [],
    [itemsState, searchQuery],
  );

  const librarianAgent = agents.find(isKnowledgeLibrarianAgent);
  const librarianID = librarianAgent?.id;
  const chatPath = buildKnowledgeChatPath(librarianID);

  const refreshAll = async () => {
    const scope = captureScope();
    setRefreshing(true);
    try {
      await Promise.all([loadConfig(), loadItems()]);
    } finally {
      if (isCurrent(scope)) setRefreshing(false);
    }
  };

  if (!workspaceId) {
    return (
      <main className="page-shell">
        <header className="page-header">
          <div>
            <p className="text-caption font-medium uppercase tracking-wider text-brand-primary">知识库</p>
            <h1 className="page-title">知识库</h1>
            <p className="page-subtitle mt-1">工作区准备完成后，这里会显示团队已经确认的知识。</p>
          </div>
        </header>
        <Card padded>
          <EmptyState
            icon={<BookOpen className="h-5 w-5" aria-hidden="true" />}
            title="等待工作区"
            description="工作区准备完成后，可以浏览已发布知识。"
          />
        </Card>
      </main>
    );
  }

  return (
    <main className="page-shell">
      <header className="page-header">
        <div className="min-w-0">
          <p className="text-caption font-medium uppercase tracking-wider text-brand-primary">团队记忆</p>
          <h1 className="page-title">知识库</h1>
          <p className="page-subtitle mt-1">团队成员会在对话中将需要保留的内容交给管理员。这里用于浏览知识、来源和关联。</p>
        </div>
        <div className="flex shrink-0 flex-wrap items-center gap-tight">
          <StatusPill title="当前工作区">
            <BookOpen className="h-3.5 w-3.5 text-brand-primary" aria-hidden="true" />
            <span className="max-w-44 truncate">{workspace?.name ?? workspaceId}</span>
          </StatusPill>
          <Button type="button" onClick={() => void refreshAll()} disabled={refreshing}>
            <RefreshCw className={cx('h-4 w-4', refreshing && 'animate-spin')} aria-hidden="true" />
            刷新
          </Button>
        </div>
      </header>

      <KnowledgeLibrarianBanner
        state={configState}
        agent={librarianAgent}
        chatPath={chatPath}
        onChat={() => {
          if (chatPath) navigate(chatPath);
        }}
        onRetry={() => void loadConfig()}
      />

      <KnowledgeBrowserControls
        searchQuery={searchQuery}
        kindFilter={kindFilter}
        onSearchChange={setSearchQuery}
        onKindChange={setKindFilter}
      />

      <KnowledgeListSection
        state={itemsState}
        items={visibleItems}
        searchQuery={searchQuery}
        chatPath={chatPath}
        loadingMore={itemsLoadingMore}
        loadMoreError={itemsLoadMoreError}
        onRetry={() => void loadItems()}
        onLoadMore={() => void loadMoreItems()}
        onOpen={setSelectedItem}
        onChat={() => {
          if (chatPath) navigate(chatPath);
        }}
      />

      {selectedItem ? (
        <KnowledgeDetailDrawer
          workspaceId={workspaceId}
          item={selectedItem}
          onClose={() => setSelectedItem(null)}
        />
      ) : null}
    </main>
  );
}

function KnowledgeLibrarianBanner({
  state,
  agent,
  chatPath,
  onChat,
  onRetry,
}: {
  state: AsyncState<KnowledgeConfig>;
  agent?: AgentProfile;
  chatPath: string | null;
  onChat: () => void;
  onRetry: () => void;
}) {
  const stateLabel = state.kind === 'loading'
    ? '读取中…'
    : state.kind === 'error'
      ? '入口暂不可用'
      : chatPath
        ? '可对话'
        : '入口待配置';
  const stateClass = state.kind === 'error'
    ? knowledgeStatusClass('missing')
    : chatPath
      ? knowledgeStatusClass('effective')
      : knowledgeStatusClass('partial');

  return (
    <section className="flex flex-wrap items-center justify-between gap-base rounded-card border border-border-subtle bg-surface-raised px-comfortable py-snug shadow-card" aria-labelledby="knowledge-librarian-title">
      <div className="flex min-w-0 items-center gap-snug">
        <div className="flex h-9 w-9 shrink-0 items-center justify-center rounded-button bg-brand-muted text-brand-primary" aria-hidden="true">
          <Bot className="h-4 w-4" />
        </div>
        <div className="min-w-0">
          <p id="knowledge-librarian-title" className="text-caption text-text-tertiary">内置知识管理员</p>
          <p className="truncate text-body font-medium text-text-primary">{agent?.name ?? '知识管理员'}</p>
        </div>
        <StatusPill className={stateClass}>{stateLabel}</StatusPill>
      </div>
      <div className="flex flex-wrap items-center gap-tight">
        {state.kind === 'error' ? <Button type="button" size="sm" onClick={onRetry}>重试读取</Button> : null}
        {chatPath ? (
          <Button type="button" size="sm" variant="ghost" onClick={onChat}>
            <MessageCircle className="h-3.5 w-3.5" aria-hidden="true" />
            与知识管理员对话
          </Button>
        ) : null}
      </div>
      {state.kind === 'error' ? <p className="w-full text-caption text-status-warning" role="status">当前只能浏览已发布知识；入口恢复后可从普通对话进入管理员。</p> : null}
      {state.kind === 'ready' && !chatPath ? <p className="w-full text-caption text-status-warning" role="status">管理员入口正在准备，当前只显示已发布知识。</p> : null}
    </section>
  );
}

function KnowledgeBrowserControls({
  searchQuery,
  kindFilter,
  onSearchChange,
  onKindChange,
}: {
  searchQuery: string;
  kindFilter: string;
  onSearchChange: (value: string) => void;
  onKindChange: (value: string) => void;
}) {
  return (
    <section className="ink-paper-panel overflow-hidden rounded-card" aria-labelledby="knowledge-browse-title">
      <div className="border-b border-border-subtle bg-surface-sunken/45 px-comfortable py-base">
        <div className="flex flex-wrap items-end justify-between gap-base">
          <div className="min-w-0">
            <div className="flex items-center gap-tight">
              <Search className="h-5 w-5 text-brand-primary" aria-hidden="true" />
              <h2 id="knowledge-browse-title" className="text-h3 text-text-primary">浏览知识</h2>
            </div>
            <p className="mt-micro text-caption text-text-tertiary">搜索标题、摘要、标签或别名；只显示已经发布的内容。</p>
          </div>
          <Field label="搜索知识" className="w-full max-w-md">
            <Input
              type="search"
              value={searchQuery}
              onChange={(event) => onSearchChange(event.target.value)}
              placeholder="输入关键词"
              aria-label="搜索知识标题摘要标签或别名"
            />
          </Field>
        </div>
        <div className="mt-base flex flex-wrap gap-tight" role="group" aria-label="按语义分类浏览">
          {KIND_FILTERS.map((filter) => (
            <Button
              type="button"
              key={filter.value || 'all'}
              size="sm"
              variant={kindFilter === filter.value ? 'ghost' : 'secondary'}
              aria-pressed={kindFilter === filter.value}
              onClick={() => onKindChange(filter.value)}
            >
              {filter.label}
            </Button>
          ))}
        </div>
      </div>
    </section>
  );
}

function KnowledgeListSection({
  state,
  items,
  searchQuery,
  chatPath,
  loadingMore,
  loadMoreError,
  onRetry,
  onLoadMore,
  onOpen,
  onChat,
}: {
  state: KnowledgeListState<KnowledgeItem>;
  items: KnowledgeItem[];
  searchQuery: string;
  chatPath: string | null;
  loadingMore: boolean;
  loadMoreError?: string;
  onRetry: () => void;
  onLoadMore: () => void;
  onOpen: (item: KnowledgeItem) => void;
  onChat: () => void;
}) {
  const hasMore = state.kind === 'ready' && Boolean(state.value.next_cursor);
  const emptyState = knowledgeListEmptyState(searchQuery, hasMore);
  return (
    <section className="ink-paper-panel min-w-0 overflow-hidden rounded-card" aria-labelledby="knowledge-list-title">
      <div className="flex flex-wrap items-end justify-between gap-base border-b border-border-subtle bg-surface-sunken/45 px-comfortable py-base">
        <div>
          <div className="flex items-center gap-tight">
            <BookOpen className="h-5 w-5 text-brand-primary" aria-hidden="true" />
            <h2 id="knowledge-list-title" className="text-h3 text-text-primary">已发布知识</h2>
          </div>
          <p className="mt-micro text-caption text-text-tertiary">
            {searchQuery.trim() ? '当前显示搜索结果。' : '点击任意条目查看正文、来源、关联和版本。'}
          </p>
        </div>
        {state.kind === 'ready' ? <span className="text-caption tabular-nums text-text-tertiary">显示 {items.length} 条</span> : null}
      </div>
      {state.kind === 'loading' ? (
        <div className="grid gap-tight p-comfortable md:grid-cols-2" role="status" aria-label="已发布知识加载中">
          {Array.from({ length: 6 }, (_, index) => <Skeleton key={index} className="h-24 w-full rounded-button" />)}
        </div>
      ) : state.kind === 'error' ? (
        <InlineError message={state.message} onRetry={onRetry} />
      ) : items.length === 0 ? (
        <EmptyState
          icon={hasMore || searchQuery.trim() ? <Search className="h-5 w-5" aria-hidden="true" /> : <BookOpen className="h-5 w-5" aria-hidden="true" />}
          title={emptyState.title}
          description={emptyState.description}
          action={hasMore ? (
            <Button type="button" variant="secondary" onClick={onLoadMore} disabled={loadingMore}>
              <RefreshCw className={cx('h-4 w-4', loadingMore && 'animate-spin')} aria-hidden="true" />
              {loadingMore ? '加载中…' : '继续加载知识'}
            </Button>
          ) : !searchQuery.trim() && chatPath ? (
            <Button type="button" variant="primary" onClick={onChat}>
              <MessageCircle className="h-4 w-4" aria-hidden="true" />
              与知识管理员对话
            </Button>
          ) : null}
        />
      ) : (
        <>
          <div className="grid gap-tight p-comfortable md:grid-cols-2">
            {items.map((item) => <KnowledgeCard key={item.id} item={item} onOpen={onOpen} />)}
          </div>
          {state.kind === 'ready' && state.value.next_cursor ? (
            <div className="flex flex-wrap items-center justify-between gap-snug border-t border-border-subtle bg-surface-sunken/35 px-comfortable py-snug">
              <div className="min-w-0 text-caption text-text-tertiary">
                <p>还有更多已发布知识。</p>
                {loadMoreError ? <p className="mt-micro text-status-error" role="alert">{loadMoreError}</p> : null}
              </div>
              <Button type="button" size="sm" onClick={onLoadMore} disabled={loadingMore}>
                <RefreshCw className={cx('h-3.5 w-3.5', loadingMore && 'animate-spin')} aria-hidden="true" />
                {loadingMore ? '加载中…' : '加载更多'}
              </Button>
            </div>
          ) : null}
        </>
      )}
    </section>
  );
}

function KnowledgeCard({ item, onOpen }: { item: KnowledgeItem; onOpen: (item: KnowledgeItem) => void }) {
  return (
    <button
      type="button"
      onClick={() => onOpen(item)}
      className="min-w-0 rounded-card border border-border-subtle bg-surface-base/55 px-base py-snug text-left transition-colors duration-inkFast hover:border-border-strong hover:bg-surface-base focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-brand-primary/40"
      aria-label={'查看知识：' + item.title}
    >
      <div className="flex items-start justify-between gap-tight">
        <p className="line-clamp-2 min-w-0 text-body font-medium text-text-primary">{item.title}</p>
        <StatusPill className={knowledgeStatusClass('effective')}>已发布</StatusPill>
      </div>
      <p className="mt-tight line-clamp-3 text-caption text-text-secondary">{item.summary || '打开查看正文、来源和关联。'}</p>
      <div className="mt-tight flex items-center justify-between gap-tight text-caption text-text-tertiary">
        <span>{knowledgeKindLabel(item.kind)}</span>
        <time dateTime={item.updated_at}>{formatDateTime(item.updated_at)}</time>
      </div>
    </button>
  );
}

function KnowledgeDetailDrawer({
  workspaceId,
  item,
  onClose,
}: {
  workspaceId: string;
  item: KnowledgeItem;
  onClose: () => void;
}) {
  const [bundleState, setBundleState] = useState<AsyncState<KnowledgeItemDetails>>({ kind: 'loading' });
  const [versionsState, setVersionsState] = useState<KnowledgeListState<KnowledgeVersion>>({ kind: 'loading' });
  const [relationsState, setRelationsState] = useState<KnowledgeListState<KnowledgeRelation>>({ kind: 'loading' });
  const [selectedVersion, setSelectedVersion] = useState(String(item.current_version));
  const [versionLoading, setVersionLoading] = useState(false);
  const [versionError, setVersionError] = useState<string | undefined>();
  const [reloadNonce, setReloadNonce] = useState(0);
  const request = useRef(0);

  useEffect(() => {
    const scope = captureScope();
    const current = ++request.current;
    setBundleState({ kind: 'loading' });
    setVersionsState({ kind: 'loading' });
    setRelationsState({ kind: 'loading' });
    setSelectedVersion(String(item.current_version));
    setVersionError(undefined);
    void Promise.allSettled([
      getKnowledgeItem(workspaceId, item.id),
      listKnowledgeVersions(workspaceId, item.id),
      listKnowledgeRelations(workspaceId, item.id, 'both'),
    ]).then(([bundleResult, versionsResult, relationsResult]) => {
      if (current !== request.current || !isCurrent(scope)) return;
      if (bundleResult.status === 'fulfilled') setBundleState({ kind: 'ready', value: bundleResult.value });
      else setBundleState({ kind: 'error', message: errorMessage(bundleResult.reason, '知识正文读取失败') });
      if (versionsResult.status === 'fulfilled') setVersionsState({ kind: 'ready', value: versionsResult.value });
      else setVersionsState({ kind: 'error', message: errorMessage(versionsResult.reason, '知识版本读取失败') });
      if (relationsResult.status === 'fulfilled') setRelationsState({ kind: 'ready', value: relationsResult.value });
      else setRelationsState({ kind: 'error', message: errorMessage(relationsResult.reason, '知识关联读取失败') });
    });
  }, [item.current_version, item.id, reloadNonce, workspaceId]);

  const bundle = bundleState.kind === 'ready' ? bundleState.value : null;
  const versions = versionsState.kind === 'ready' ? sortKnowledgeVersions(versionsState.value.items) : [];
  const relations = relationsState.kind === 'ready' ? relationsState.value.items : [];

  const readVersion = async (version: string) => {
    if (!bundle || version === String(bundle.version.version) || versionLoading) return;
    const scope = captureScope();
    setVersionLoading(true);
    setVersionError(undefined);
    try {
      const next = await getKnowledgeVersion(workspaceId, item.id, version);
      if (!isCurrent(scope)) return;
      setBundleState({ kind: 'ready', value: next });
      setSelectedVersion(version);
    } catch (error) {
      if (isCurrent(scope)) setVersionError(errorMessage(error, '知识版本读取失败'));
    } finally {
      if (isCurrent(scope)) setVersionLoading(false);
    }
  };

  return (
    <Drawer open title="知识详情" onClose={onClose} width={560} ariaLabel={'知识详情：' + item.title}>
      <div className="space-y-comfortable p-comfortable">
        {bundleState.kind === 'loading' ? (
          <div className="space-y-snug">
            <Skeleton className="h-6 w-2/3" />
            <Skeleton className="h-24 w-full" />
            <Skeleton className="h-16 w-full" />
          </div>
        ) : bundleState.kind === 'error' ? (
          <InlineError message={bundleState.message} onRetry={() => setReloadNonce((value) => value + 1)} />
        ) : bundle ? (
          <>
            <div>
              <div className="flex flex-wrap items-center gap-tight">
                <StatusPill className={knowledgeStatusClass(bundle.version.status)}>{statusLabel(bundle.version.status)}</StatusPill>
                <span className="text-caption text-text-tertiary">{knowledgeKindLabel(bundle.version.kind)}</span>
              </div>
              <h2 className="mt-tight text-h2 text-text-primary">{bundle.version.title}</h2>
              {bundle.version.summary ? <p className="mt-tight text-body text-text-secondary">{bundle.version.summary}</p> : null}
              {bundle.version.scope && Object.keys(bundle.version.scope).length > 0 ? (
                <details className="mt-snug">
                  <summary className="cursor-pointer text-caption text-text-tertiary focus-visible:ring-2 focus-visible:ring-brand-primary/40">查看适用范围</summary>
                  <p className="mt-micro break-words font-mono text-caption text-text-tertiary">{formatKnowledgeScope(bundle.version.scope)}</p>
                </details>
              ) : null}
            </div>

            <Field label="查看版本">
              <Select
                value={selectedVersion}
                onChange={(event) => void readVersion(event.target.value)}
                disabled={versionLoading || versionsState.kind === 'loading'}
                aria-label="选择知识版本"
              >
                {versions.map((version) => (
                  <option key={version.id} value={String(version.version)}>
                    v{version.version} · {statusLabel(version.status)}
                  </option>
                ))}
                {versions.length === 0 ? <option value={String(bundle.version.version)}>v{bundle.version.version}</option> : null}
              </Select>
              {versionError ? <p className="mt-micro text-caption text-status-error" role="alert">{versionError}</p> : null}
            </Field>

            <section aria-labelledby="knowledge-body-title">
              <SectionHeading id="knowledge-body-title" icon={<FileText className="h-4 w-4" aria-hidden="true" />} title="正文" />
              <div className="mt-snug rounded-card border border-border-subtle bg-surface-base/55 px-base py-snug">
                {bundle.version.body_markdown ? <AgentOutput text={bundle.version.body_markdown} showCaret={false} /> : <p className="text-caption text-text-tertiary">该版本没有正文。</p>}
              </div>
            </section>

            <SourceList sources={bundle.sources} />
            <RelationList itemId={item.id} relations={relations} state={relationsState} onRetry={() => setReloadNonce((value) => value + 1)} />

            <details className="rounded-card border border-border-subtle bg-surface-base/45">
              <summary className="cursor-pointer list-none px-snug py-tight text-caption text-text-secondary focus-visible:ring-2 focus-visible:ring-brand-primary/40">
                查看版本信息
              </summary>
              <dl className="grid gap-snug border-t border-border-subtle px-snug py-snug text-caption md:grid-cols-2">
                <LineageField label="版本" value={'v' + bundle.version.version} />
                <LineageField label="创建时间" value={formatDateTime(bundle.version.created_at)} />
                <LineageField label="创建 Agent" value={bundle.version.created_by_agent_id} mono />
                <LineageField label="来源 Run" value={bundle.version.created_by_run_id} mono />
                <LineageField label="来源任务" value={bundle.version.created_by_work_item_id} mono />
                <LineageField label="内容摘要" value={bundle.version.content_digest} mono />
              </dl>
            </details>
          </>
        ) : null}
      </div>
    </Drawer>
  );
}

function sortKnowledgeVersions(versions: KnowledgeVersion[]): KnowledgeVersion[] {
  return [...versions].sort((left, right) => right.version - left.version);
}

function SourceList({ sources }: { sources: KnowledgeSource[] }) {
  const agents = useAgentsStore((state) => state.agents);
  return (
    <section aria-labelledby="knowledge-sources-title">
      <SectionHeading id="knowledge-sources-title" icon={<FileText className="h-4 w-4" aria-hidden="true" />} title="来源证据" />
      <div className="mt-snug space-y-tight">
        {sources.length === 0 ? <p className="text-caption text-text-tertiary">该版本没有登记来源证据。</p> : sources.map((source) => {
          const verification = sourceVerificationLabel(source);
          return (
            <div key={source.id} className="rounded-button border border-border-subtle bg-surface-sunken/45 px-snug py-tight">
              <div className="flex flex-wrap items-center gap-tight">
                <span className="text-caption text-text-secondary">{sourceKindLabel(source.kind)}</span>
                <StatusPill className={knowledgeStatusClass(verification.startsWith('已核验') ? 'complete' : 'missing')}>{verification}</StatusPill>
              </div>
              <p className="mt-micro break-words text-caption text-text-primary">{source.kind === 'agent' ? (agents.find((agent) => agent.id === source.ref)?.name ?? '团队智能体') : source.ref}</p>
              {source.kind === 'agent' && <details className="mt-micro text-caption text-text-tertiary"><summary className="cursor-pointer">查看来源标识</summary><p className="break-all">{source.ref}</p></details>}
              {source.locator ? <p className="mt-micro break-words text-caption text-text-tertiary">{source.locator}</p> : null}
              {source.excerpt ? <p className="mt-micro whitespace-pre-wrap text-caption text-text-secondary">{source.excerpt}</p> : null}
            </div>
          );
        })}
      </div>
    </section>
  );
}

function RelationList({
  itemId,
  relations,
  state,
  onRetry,
}: {
  itemId: string;
  relations: KnowledgeRelation[];
  state: KnowledgeListState<KnowledgeRelation>;
  onRetry: () => void;
}) {
  return (
    <section aria-labelledby="knowledge-relations-title">
      <div className="flex items-center justify-between gap-tight">
        <SectionHeading id="knowledge-relations-title" icon={<GitBranch className="h-4 w-4" aria-hidden="true" />} title="关联知识" />
        <span className="text-caption tabular-nums text-text-tertiary">{state.kind === 'loading' ? '读取中…' : relations.length + ' 条'}</span>
      </div>
      <p className="mt-tight text-caption text-text-tertiary">展示当前有效的正向和反向关联。</p>
      {state.kind === 'error' ? (
        <div className="mt-snug"><InlineError message={state.message} onRetry={onRetry} /></div>
      ) : relations.length === 0 ? (
        <p className="mt-snug text-caption text-text-tertiary">没有可见的关联知识。</p>
      ) : (
        <div className="mt-snug space-y-tight">
          {relations.map((relation) => {
            const outgoing = relation.from_item_id === itemId;
            const otherID = outgoing ? relation.to_item_id : relation.from_item_id;
            return (
              <div key={relation.id} className="rounded-button border border-border-subtle bg-surface-sunken/45 px-snug py-tight">
                <p className="text-body text-text-primary">
                  {outgoing ? '本条知识' : '另一条知识'} {relationLabel(relation.kind)} {outgoing ? '另一条知识' : '本条知识'}
                </p>
                {relation.condition ? <p className="mt-micro text-caption text-text-secondary">条件：{relation.condition}</p> : null}
                {relation.rationale ? <p className="mt-micro text-caption text-text-tertiary">依据：{relation.rationale}</p> : null}
                <details className="mt-tight">
                  <summary className="cursor-pointer text-caption text-text-tertiary focus-visible:ring-2 focus-visible:ring-brand-primary/40">查看关联条目</summary>
                  <p className="mt-micro break-words font-mono text-caption text-text-tertiary">{otherID}</p>
                </details>
              </div>
            );
          })}
        </div>
      )}
    </section>
  );
}

function LineageField({ label, value, mono = false }: { label: string; value?: string; mono?: boolean }) {
  return (
    <div className="min-w-0">
      <dt className="text-text-tertiary">{label}</dt>
      <dd className={cx('mt-micro break-words text-text-secondary', mono && 'font-mono')}>{value ?? '未记录'}</dd>
    </div>
  );
}

function SectionHeading({ id, icon, title }: { id: string; icon: ReactNode; title: string }) {
  return (
    <h3 id={id} className="flex items-center gap-tight text-body font-medium text-text-primary">
      <span className="text-brand-primary" aria-hidden="true">{icon}</span>
      {title}
    </h3>
  );
}

function InlineError({ message, onRetry }: { message: string; onRetry: () => void }) {
  return (
    <div className="flex flex-wrap items-center justify-between gap-base px-comfortable py-base" role="alert">
      <div className="flex min-w-0 items-start gap-tight">
        <AlertCircle className="mt-0.5 h-5 w-5 shrink-0 text-status-error" aria-hidden="true" />
        <p className="text-body text-text-secondary">{message}</p>
      </div>
      <Button type="button" size="sm" onClick={onRetry}>重试</Button>
    </div>
  );
}
