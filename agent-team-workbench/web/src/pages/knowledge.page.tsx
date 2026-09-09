import {
  AlertCircle,
  ArrowLeft,
  BookOpen,
  Bot,
  Check,
  ChevronRight,
  Clock3,
  ExternalLink,
  FileText,
  GitBranch,
  History,
  Link2,
  MessageCircle,
  RefreshCw,
  Search,
  ShieldCheck,
} from 'lucide-react';
import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from 'react';
import { Link, useNavigate, useSearchParams } from 'react-router-dom';
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
import { Button, Card, EmptyState, Field, Input, Skeleton, StatusPill, cx } from '../components/ui';
import { useAgentsStore } from '../stores/agents.store';
import { captureScope, isCurrent } from '../stores/scope';
import { useWorkspaceStore } from '../stores/workspace.store';
import { formatDateTime } from '../utils/format';
import { isKnowledgeLibrarianAgent } from '../utils/agent-scope';
import { isSafeContentUrl } from '../utils/content-blocks';
import { parseSnippet } from '../utils/search-snippet';
import './knowledge-reading.css';

export { isKnowledgeLibrarianAgent } from '../utils/agent-scope';

type AsyncState<T> =
  | { kind: 'loading' }
  | { kind: 'ready'; value: T }
  | { kind: 'error'; message: string };

type KnowledgeListValue<T> = { items: T[]; next_cursor?: string | null; truncated?: boolean };
type KnowledgeListState<T> = AsyncState<KnowledgeListValue<T>>;

export interface KnowledgeUrlState {
  query: string;
  kind: string;
  itemId: string | null;
  version: number | null;
  invalidVersion: string | null;
}

export interface KnowledgePathOptions {
  workspaceId?: string;
  query?: string;
  kind?: string;
  itemId?: string;
  version?: number | string;
}

export type KnowledgeTocEntry = { id: string; level: number; text: string };
export type KnowledgeDiffLine = { kind: 'same' | 'added' | 'removed'; text: string };

function isKnowledgeVersionText(value: string): boolean {
  if (!/^[1-9]\d*$/.test(value)) return false;
  return Number.isSafeInteger(Number(value));
}

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

export const KNOWLEDGE_KIND_FILTERS = [
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

/** Read only the supported knowledge URL fields; unknown fields are preserved by callers. */
export function parseKnowledgeUrlState(params: URLSearchParams): KnowledgeUrlState {
  const rawVersion = params.get('version')?.trim() ?? '';
  const validVersion = isKnowledgeVersionText(rawVersion) ? Number(rawVersion) : null;
  return {
    query: params.get('q')?.trim() ?? '',
    kind: params.get('kind')?.trim() ?? '',
    itemId: params.get('item')?.trim() || null,
    version: validVersion !== null && Number.isSafeInteger(validVersion) ? validVersion : null,
    invalidVersion: rawVersion && validVersion === null ? rawVersion : null,
  };
}

/** Build a stable deep link for the reader. Empty filters are omitted. */
export function buildKnowledgePath(options: KnowledgePathOptions = {}): string {
  const params = new URLSearchParams();
  const workspaceId = options.workspaceId?.trim();
  const query = options.query?.trim();
  const kind = options.kind?.trim();
  const itemId = options.itemId?.trim();
  const version = typeof options.version === 'number' || typeof options.version === 'string'
    ? String(options.version).trim()
    : '';
  if (workspaceId) params.set('ws', workspaceId);
  if (query) params.set('q', query);
  if (kind) params.set('kind', kind);
  if (itemId) params.set('item', itemId);
  if (isKnowledgeVersionText(version)) params.set('version', version);
  const encoded = params.toString();
  return encoded ? '/knowledge?' + encoded : '/knowledge';
}

/** Build the existing ordinary chat URL while carrying an optional knowledge citation. */
export function buildKnowledgeChatPath(
  agentId: string | undefined,
  itemId?: string,
  version?: number | string,
  returnTo?: string,
  workspaceId?: string,
): string | null {
  const normalized = agentId?.trim();
  if (!normalized) return null;
  const params = new URLSearchParams({ agent: normalized });
  const workspace = workspaceId?.trim() || (returnTo ? new URLSearchParams(returnTo.split('?')[1] ?? '').get('ws') ?? '' : '');
  if (workspace) params.set('ws', workspace);
  if (itemId?.trim()) params.set('knowledge', itemId.trim());
  const versionText = version === undefined ? '' : String(version).trim();
  if (isKnowledgeVersionText(versionText)) params.set('version', versionText);
  if (returnTo?.trim()) params.set('return_to', returnTo.trim());
  return '/chat?' + params.toString();
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

/** Keep server excerpts readable while rendering every marker as text nodes. */
export function cleanKnowledgeExcerpt(value: string): string {
  const cleaned = value
    .replace(/[`*_~>#]/g, '')
    .replace(/!?(\[[^\]]*\])\([^)]*\)/g, '$1')
    .replace(/[ \t]+/g, ' ')
    .trim();
  const firstHit = cleaned.indexOf('[');
  if (firstHit <= 48) return cleaned;
  const context = Array.from(cleaned.slice(0, firstHit)).slice(-44).join('');
  return `…${context}${cleaned.slice(firstHit)}`;
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

export type KnowledgeSourceLink = { href: string; external: boolean; label: string } | null;

function metadataText(source: KnowledgeSource, key: string): string | undefined {
  const value = source.metadata?.[key];
  return typeof value === 'string' && value.trim() ? value.trim() : undefined;
}

/** Only routes with a real workbench destination or validated HTTP(S) document become links. */
export function buildKnowledgeSourceLink(source: KnowledgeSource): KnowledgeSourceLink {
  const ref = source.ref.trim();
  if (!ref) return null;
  const label = metadataText(source, 'label') ?? metadataText(source, 'title') ?? ref;
  if (source.kind === 'work_item' && /^wi_[A-Za-z0-9_:-]+$/.test(ref)) {
    return { href: '/tasks/' + encodeURIComponent(ref), external: false, label };
  }
  if (source.kind === 'run' && /^run_[A-Za-z0-9_:-]+$/.test(ref)) {
    return { href: '/runs/' + encodeURIComponent(ref) + '/journal', external: false, label };
  }
  if (source.kind === 'document') {
    const candidate = source.locator?.trim() || ref;
    if (/^https?:\/\//i.test(candidate) && isSafeContentUrl(candidate)) {
      return { href: candidate, external: true, label };
    }
  }
  return null;
}

function errorMessage(error: unknown, fallback: string): string {
  if (error instanceof ApiError) return error.message || fallback;
  if (error instanceof Error) return error.message || fallback;
  return fallback;
}

/** Slug used only for DOM anchors generated after AgentOutput has rendered. */
export function knowledgeHeadingId(text: string, index = 0): string {
  const normalized = text.trim().toLocaleLowerCase();
  const slug = normalized
    .replace(/[^\w\u3400-\u9fff\u3040-\u30ff-]+/g, '-')
    .replace(/^-+|-+$/g, '')
    .slice(0, 80);
  return 'knowledge-heading-' + (slug || String(index + 1));
}

/** Compute a real line-level LCS diff from the selected historical body to current body. */
export function buildKnowledgeDiff(selectedBody: string, currentBody: string): KnowledgeDiffLine[] {
  const selected = selectedBody.split(/\r?\n/);
  const current = currentBody.split(/\r?\n/);
  const maxLines = 1200;
  if (selected.length > maxLines || current.length > maxLines) {
    return [
      ...selected.slice(0, maxLines).map((text) => ({ kind: 'removed' as const, text })),
      { kind: 'same' as const, text: '差异内容过长，已停止展开更多行。' },
      ...current.slice(0, maxLines).map((text) => ({ kind: 'added' as const, text })),
    ];
  }
  const rows = Array.from({ length: selected.length + 1 }, () => new Uint16Array(current.length + 1));
  for (let i = selected.length - 1; i >= 0; i -= 1) {
    for (let j = current.length - 1; j >= 0; j -= 1) {
      rows[i]![j] = selected[i] === current[j]
        ? rows[i + 1]![j + 1]! + 1
        : Math.max(rows[i + 1]![j]!, rows[i]![j + 1]!);
    }
  }
  const result: KnowledgeDiffLine[] = [];
  let i = 0;
  let j = 0;
  while (i < selected.length || j < current.length) {
    if (i < selected.length && j < current.length && selected[i] === current[j]) {
      result.push({ kind: 'same', text: selected[i]! });
      i += 1;
      j += 1;
    } else if (i < selected.length && (j >= current.length || rows[i + 1]![j]! >= rows[i]![j + 1]!)) {
      result.push({ kind: 'removed', text: selected[i]! });
      i += 1;
    } else if (j < current.length) {
      result.push({ kind: 'added', text: current[j]! });
      j += 1;
    }
  }
  return result;
}

export default function KnowledgePage() {
  const workspace = useWorkspaceStore((state) => state.workspace);
  const workspaceId = workspace?.id ?? null;
  const agents = useAgentsStore((state) => state.agents);
  const navigate = useNavigate();
  const [searchParams, setSearchParams] = useSearchParams();
  const urlState = useMemo(() => parseKnowledgeUrlState(searchParams), [searchParams]);
  const [debouncedQuery, setDebouncedQuery] = useState(urlState.query);
  const [configState, setConfigState] = useState<AsyncState<KnowledgeConfig>>({ kind: 'loading' });
  const [itemsState, setItemsState] = useState<KnowledgeListState<KnowledgeItem>>({ kind: 'loading' });
  const [refreshing, setRefreshing] = useState(false);
  const [itemsLoadingMore, setItemsLoadingMore] = useState(false);
  const [itemsLoadMoreError, setItemsLoadMoreError] = useState<string | undefined>();
  const [readerRefreshNonce, setReaderRefreshNonce] = useState(0);
  const configRequest = useRef(0);
  const itemsRequest = useRef(0);

  useEffect(() => {
    const timer = window.setTimeout(() => setDebouncedQuery(urlState.query), 180);
    return () => window.clearTimeout(timer);
  }, [urlState.query]);

  const updateUrl = useCallback((updates: Partial<KnowledgeUrlState>, replace = true) => {
    setSearchParams((previous) => {
      const next = new URLSearchParams(previous);
      if (updates.query !== undefined) {
        if (updates.query.trim()) next.set('q', updates.query.trim());
        else next.delete('q');
      }
      if (updates.kind !== undefined) {
        if (updates.kind.trim()) next.set('kind', updates.kind.trim());
        else next.delete('kind');
      }
      if (updates.itemId !== undefined) {
        if (updates.itemId?.trim()) next.set('item', updates.itemId.trim());
        else next.delete('item');
      }
      if (updates.version !== undefined) {
        const version = updates.version;
        if (typeof version === 'number' ? Number.isSafeInteger(version) && version > 0 : typeof version === 'string' && isKnowledgeVersionText(version)) next.set('version', String(version));
        else next.delete('version');
      }
      if (workspaceId) next.set('ws', workspaceId);
      return next;
    }, { replace });
  }, [setSearchParams, workspaceId]);

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
  }, [workspaceId]);

  const loadItems = useCallback(async () => {
    if (!workspaceId) return;
    const scope = captureScope();
    const request = ++itemsRequest.current;
    setItemsLoadingMore(false);
    setItemsLoadMoreError(undefined);
    setItemsState({ kind: 'loading' });
    try {
      const value = await listKnowledgeItems(workspaceId, { q: debouncedQuery || undefined, status: 'effective', kind: urlState.kind || undefined, limit: 200 });
      if (request !== itemsRequest.current || !isCurrent(scope)) return;
      setItemsState({ kind: 'ready', value: { ...value, items: sortKnowledgeItems(value.items) } });
    } catch (error) {
      if (request !== itemsRequest.current || !isCurrent(scope)) return;
      setItemsState({ kind: 'error', message: errorMessage(error, '已发布知识读取失败') });
    }
  }, [debouncedQuery, urlState.kind, workspaceId]);

  const loadMoreItems = useCallback(async () => {
    if (!workspaceId || itemsLoadingMore || itemsState.kind !== 'ready') return;
    const cursor = itemsState.value.next_cursor;
    if (!cursor) return;
    const scope = captureScope();
    const request = ++itemsRequest.current;
    setItemsLoadingMore(true);
    setItemsLoadMoreError(undefined);
    try {
      const value = await listKnowledgeItems(workspaceId, { q: debouncedQuery || undefined, status: 'effective', kind: urlState.kind || undefined, cursor, limit: 200 });
      if (request !== itemsRequest.current || !isCurrent(scope)) return;
      setItemsState((state) => {
        if (state.kind !== 'ready') return state;
        const existing = new Set(state.value.items.map((item) => item.id));
        const appended = sortKnowledgeItems(value.items).filter((item) => !existing.has(item.id));
        return { kind: 'ready', value: { ...state.value, items: [...state.value.items, ...appended], next_cursor: value.next_cursor ?? null, truncated: value.truncated } };
      });
    } catch (error) {
      if (request !== itemsRequest.current || !isCurrent(scope)) return;
      setItemsLoadMoreError(errorMessage(error, '更多知识读取失败'));
    } finally {
      if (request === itemsRequest.current && isCurrent(scope)) setItemsLoadingMore(false);
    }
  }, [debouncedQuery, itemsLoadingMore, itemsState, urlState.kind, workspaceId]);

  useEffect(() => {
    if (!workspaceId) return;
    void loadConfig();
  }, [loadConfig, workspaceId]);

  useEffect(() => {
    if (!workspaceId || debouncedQuery !== urlState.query) return;
    void loadItems();
  }, [debouncedQuery, loadItems, urlState.query, workspaceId]);

  const librarianAgent = agents.find(isKnowledgeLibrarianAgent);
  const librarianID = configState.kind === 'ready' ? configState.value.librarian_agent_id : librarianAgent?.id;
  const genericChatPath = buildKnowledgeChatPath(librarianID ?? undefined, undefined, undefined, buildKnowledgePath({ workspaceId: workspaceId ?? undefined }), workspaceId ?? undefined);
  const pendingList = debouncedQuery !== urlState.query;
  const refreshAll = async () => {
    const scope = captureScope();
    setRefreshing(true);
    setReaderRefreshNonce((value) => value + 1);
    try {
      await Promise.all([loadConfig(), loadItems()]);
    } finally {
      if (isCurrent(scope)) setRefreshing(false);
    }
  };

  const clearSelection = () => updateUrl({ itemId: null, version: null }, false);
  const openItem = (itemId: string) => updateUrl({ itemId, version: null }, false);
  const openVersion = (version: number) => updateUrl({ version }, false);

  if (!workspaceId) {
    return (
      <main className="page-shell knowledge-reading-page" data-testid="knowledge-reading-layout" aria-label="知识库阅读工作区">
        <header className="page-header"><div><p className="text-caption font-medium uppercase tracking-wider text-brand-primary">知识库</p><h1 className="page-title">知识库</h1><p className="page-subtitle mt-1">工作区准备完成后，这里会显示团队已经确认的知识。</p></div></header>
        <Card padded><EmptyState icon={<BookOpen className="h-5 w-5" aria-hidden="true" />} title="等待工作区" description="工作区准备完成后，可以浏览已发布知识。" /></Card>
      </main>
    );
  }

  return (
    <main className={cx('page-shell knowledge-reading-page', urlState.itemId ? 'knowledge-reader-open' : 'knowledge-reader-closed')} data-testid="knowledge-reading-layout" aria-label="知识库阅读工作区">
      <header className="page-header">
        <div className="min-w-0"><p className="text-caption font-medium uppercase tracking-wider text-brand-primary">团队记忆</p><h1 className="page-title">知识库</h1><p className="page-subtitle mt-1">浏览已发布知识，沿着来源、关联和版本历史阅读上下文。</p></div>
        <div className="flex shrink-0 flex-wrap items-center gap-tight"><StatusPill title="当前工作区"><BookOpen className="h-3.5 w-3.5 text-brand-primary" aria-hidden="true" /><span className="max-w-44 truncate">{workspace?.name ?? workspaceId}</span></StatusPill><StatusPill title="当前 Agent"><Bot className="h-3.5 w-3.5 text-brand-primary" aria-hidden="true" /><span className="max-w-44 truncate">{librarianAgent?.name ?? '知识管理员'}</span></StatusPill><Button type="button" onClick={() => void refreshAll()} disabled={refreshing} aria-label="刷新知识库"><RefreshCw className={cx('h-4 w-4', refreshing && 'animate-spin')} aria-hidden="true" />刷新</Button></div>
      </header>
      <KnowledgeLibrarianBanner state={configState} agent={librarianAgent} chatPath={genericChatPath} onChat={() => { if (genericChatPath) navigate(genericChatPath); }} onRetry={() => void loadConfig()} />
      <div className="knowledge-reading-grid">
        <KnowledgeTypeNav kind={urlState.kind} onKindChange={(kind) => updateUrl({ kind })} />
        <div className="knowledge-results-column">
          <KnowledgeBrowserControls searchQuery={urlState.query} onSearchChange={(query) => updateUrl({ query })} onClear={() => updateUrl({ query: '' })} />
          <KnowledgeListSection state={pendingList ? { kind: 'loading' } : itemsState} searchQuery={urlState.query} loadingMore={itemsLoadingMore} loadMoreError={itemsLoadMoreError} onRetry={() => void loadItems()} onLoadMore={() => void loadMoreItems()} onOpen={openItem} selectedItemId={urlState.itemId} onChat={() => { if (genericChatPath) navigate(genericChatPath); }} chatPath={genericChatPath} />
        </div>
        <KnowledgeReader workspaceId={workspaceId} itemId={urlState.itemId} requestedVersion={urlState.version} invalidVersion={urlState.invalidVersion} query={urlState.query} kind={urlState.kind} librarianAgentId={librarianID ?? undefined} refreshNonce={readerRefreshNonce} onClose={clearSelection} onSelectItem={openItem} onSelectVersion={openVersion} />
      </div>
    </main>
  );
}

function KnowledgeLibrarianBanner({ state, agent, chatPath, onChat, onRetry }: { state: AsyncState<KnowledgeConfig>; agent?: AgentProfile; chatPath: string | null; onChat: () => void; onRetry: () => void }) {
  const stateLabel = state.kind === 'loading' ? '读取中…' : state.kind === 'error' ? '入口暂不可用' : state.value.enabled === false ? '未启用' : chatPath ? '可对话' : '入口待配置';
  const stateClass = state.kind === 'error' ? knowledgeStatusClass('missing') : chatPath ? knowledgeStatusClass('effective') : knowledgeStatusClass('partial');
  if (state.kind === 'error') {
    return <section className="knowledge-librarian-banner knowledge-librarian-banner-error" aria-labelledby="knowledge-librarian-title" role="status"><div className="flex min-w-0 items-center gap-snug"><div className="flex h-8 w-8 shrink-0 items-center justify-center rounded-button bg-brand-muted text-brand-primary" aria-hidden="true"><Bot className="h-4 w-4" /></div><div className="min-w-0"><p id="knowledge-librarian-title" className="text-caption text-text-tertiary">内置知识管理员</p><p className="truncate text-body font-medium text-text-primary">{agent?.name ?? '知识管理员'} · 入口暂不可用</p></div><StatusPill className={stateClass}>{stateLabel}</StatusPill></div><div className="flex flex-wrap items-center gap-tight"><Button type="button" size="sm" onClick={onRetry}>重试读取</Button></div><p className="w-full text-caption text-status-warning">当前只能浏览已发布知识；入口恢复后可从普通对话进入管理员。</p></section>;
  }
  return <section className="knowledge-librarian-banner knowledge-librarian-banner-compact" aria-labelledby="knowledge-librarian-title"><div className="flex min-w-0 items-center gap-tight"><Bot className="h-4 w-4 shrink-0 text-brand-primary" aria-hidden="true" /><p id="knowledge-librarian-title" className="truncate text-caption text-text-secondary">知识管理员</p><StatusPill className={stateClass}>{stateLabel}</StatusPill></div>{chatPath ? <Button type="button" size="sm" variant="ghost" onClick={onChat}><MessageCircle className="h-3.5 w-3.5" aria-hidden="true" />与管理员对话</Button> : null}</section>;
}

function KnowledgeTypeNav({ kind, onKindChange }: { kind: string; onKindChange: (kind: string) => void }) {
  return <aside className="knowledge-type-nav" data-testid="knowledge-type-nav" aria-label="知识类型导航"><div className="knowledge-panel-heading"><p className="text-caption font-medium uppercase tracking-wider text-text-tertiary">浏览</p><h2 className="mt-micro text-body font-semibold text-text-primary">知识类型</h2></div><nav className="knowledge-type-nav-list" aria-label="按类型筛选知识">{KNOWLEDGE_KIND_FILTERS.map((filter) => <button key={filter.value || 'all'} type="button" className={cx('knowledge-type-nav-item', kind === filter.value && 'knowledge-type-nav-item-active')} aria-current={kind === filter.value ? 'page' : undefined} onClick={() => onKindChange(filter.value)}><span>{filter.label}</span>{kind === filter.value ? <ChevronRight className="h-4 w-4" aria-hidden="true" /> : null}</button>)}</nav></aside>;
}

function KnowledgeBrowserControls({ searchQuery, onSearchChange, onClear }: { searchQuery: string; onSearchChange: (query: string) => void; onClear: () => void }) {
  return <section className="knowledge-results-toolbar" aria-labelledby="knowledge-browse-title"><div className="knowledge-results-toolbar-head"><div className="flex min-w-0 items-center gap-tight"><Search className="h-4 w-4 shrink-0 text-brand-primary" aria-hidden="true" /><div className="min-w-0"><h2 id="knowledge-browse-title" className="text-body font-semibold text-text-primary">查找知识</h2><p className="text-caption text-text-tertiary">已搜索全部可见知识，包含正文。</p></div></div>{searchQuery ? <Button type="button" size="sm" onClick={onClear}>清除</Button> : null}</div><Field label="搜索知识" className="knowledge-search-field"><Input type="search" value={searchQuery} onChange={(event) => onSearchChange(event.target.value)} placeholder="输入关键词" aria-label="搜索知识正文标题摘要标签或别名" className="knowledge-search-input" /></Field></section>;
}

function KnowledgeListSection({ state, searchQuery, loadingMore, loadMoreError, onRetry, onLoadMore, onOpen, selectedItemId, onChat, chatPath }: { state: KnowledgeListState<KnowledgeItem>; searchQuery: string; loadingMore: boolean; loadMoreError?: string; onRetry: () => void; onLoadMore: () => void; onOpen: (itemId: string) => void; selectedItemId: string | null; onChat: () => void; chatPath: string | null }) {
  const hasMore = state.kind === 'ready' && Boolean(state.value.next_cursor);
  const emptyState = knowledgeListEmptyState(searchQuery, hasMore);
  return <section className="knowledge-results-panel" data-testid="knowledge-results" aria-labelledby="knowledge-list-title"><div className="knowledge-panel-heading flex items-end justify-between gap-tight"><div><div className="flex items-center gap-tight"><BookOpen className="h-4 w-4 text-brand-primary" aria-hidden="true" /><h2 id="knowledge-list-title" className="text-body font-semibold text-text-primary">已发布知识</h2></div><p className="mt-micro text-caption text-text-tertiary">{searchQuery ? '已搜索全部可见知识。' : '选择条目开始阅读。'}</p></div>{state.kind === 'ready' ? <span className="text-caption tabular-nums text-text-tertiary">已加载 {state.value.items.length} 条</span> : null}</div>{state.kind === 'loading' ? <div className="space-y-tight p-base" role="status" aria-label="已发布知识加载中">{Array.from({ length: 7 }, (_, index) => <Skeleton key={index} className="h-16 w-full rounded-button" />)}</div> : state.kind === 'error' ? <InlineError message={state.message} onRetry={onRetry} /> : state.value.items.length === 0 ? <EmptyState icon={searchQuery || hasMore ? <Search className="h-5 w-5" aria-hidden="true" /> : <BookOpen className="h-5 w-5" aria-hidden="true" />} title={emptyState.title} description={emptyState.description} action={hasMore ? <Button type="button" onClick={onLoadMore} disabled={loadingMore}><RefreshCw className={cx('h-4 w-4', loadingMore && 'animate-spin')} aria-hidden="true" />继续加载知识</Button> : !searchQuery && chatPath ? <Button type="button" variant="primary" onClick={onChat}><MessageCircle className="h-4 w-4" aria-hidden="true" />与知识管理员对话</Button> : null} /> : <><ul className="knowledge-result-list">{state.value.items.map((item) => <li key={item.id}><KnowledgeCard item={item} query={searchQuery} selected={selectedItemId === item.id} onOpen={onOpen} /></li>)}</ul>{state.value.next_cursor ? <div className="knowledge-pagination"><div className="min-w-0 text-caption text-text-tertiary"><p>还有更多已发布知识。</p>{loadMoreError ? <p className="mt-micro text-status-error" role="alert">{loadMoreError}</p> : null}</div><Button type="button" size="sm" onClick={onLoadMore} disabled={loadingMore}><RefreshCw className={cx('h-3.5 w-3.5', loadingMore && 'animate-spin')} aria-hidden="true" />{loadingMore ? '加载中…' : '加载更多'}</Button></div> : null}</>}</section>;
}

function KnowledgeCard({ item, query, selected, onOpen }: { item: KnowledgeItem; query: string; selected: boolean; onOpen: (itemId: string) => void }) {
  const excerpt = query.trim() ? cleanKnowledgeExcerpt(item.search_excerpt?.trim() ?? '') : '';
  return <button type="button" onClick={() => onOpen(item.id)} className={cx('knowledge-result-row', selected && 'knowledge-result-row-active')} aria-current={selected ? 'page' : undefined} aria-label={'查看知识：' + item.title}><div className="flex min-w-0 items-start justify-between gap-tight"><p className="line-clamp-2 min-w-0 text-body font-medium text-text-primary">{item.title}</p><StatusPill className={knowledgeStatusClass(item.status)}>{statusLabel(item.status)}</StatusPill></div>{excerpt ? <p className="knowledge-result-excerpt mt-micro line-clamp-3 text-caption text-text-secondary"><KnowledgeSnippet text={excerpt} /></p> : <p className="mt-micro line-clamp-2 text-caption text-text-secondary">{item.summary || '打开查看正文、来源和关联。'}</p>}<div className="mt-tight flex items-center justify-between gap-tight text-caption text-text-tertiary"><span>{knowledgeKindLabel(item.kind)}</span><time dateTime={item.updated_at}>{formatDateTime(item.updated_at)}</time></div></button>;
}

function KnowledgeSnippet({ text }: { text: string }) {
  return <>{parseSnippet(text).map((part, index) => part.hit ? <mark key={index} className="rounded-sm bg-brand-primary/10 px-0.5 text-brand-accent">{part.text}</mark> : <span key={index}>{part.text}</span>)}</>;
}

function KnowledgeReader({ workspaceId, itemId, requestedVersion, invalidVersion, query, kind, librarianAgentId, refreshNonce, onClose, onSelectItem, onSelectVersion }: { workspaceId: string; itemId: string | null; requestedVersion: number | null; invalidVersion: string | null; query: string; kind: string; librarianAgentId?: string; refreshNonce: number; onClose: () => void; onSelectItem: (itemId: string) => void; onSelectVersion: (version: number) => void }) {
  const [bundleState, setBundleState] = useState<AsyncState<KnowledgeItemDetails>>({ kind: 'loading' });
  const [versionsState, setVersionsState] = useState<KnowledgeListState<KnowledgeVersion>>({ kind: 'loading' });
  const [relationsState, setRelationsState] = useState<KnowledgeListState<KnowledgeRelation>>({ kind: 'loading' });
  const [relationTargets, setRelationTargets] = useState<Record<string, KnowledgeItem>>({});
  const [relationsLoading, setRelationsLoading] = useState(false);
  const [readerError, setReaderError] = useState<string | undefined>();
  const [copied, setCopied] = useState(false);
  const [toc, setToc] = useState<KnowledgeTocEntry[]>([]);
  const [retryNonce, setRetryNonce] = useState(0);
  const articleRef = useRef<HTMLDivElement>(null);
  const request = useRef(0);

  useEffect(() => {
    const scope = captureScope();
    const current = ++request.current;
    setCopied(false);
    setToc([]);
    setRelationTargets({});
    setReaderError(invalidVersion ? `版本参数“${invalidVersion}”无效。` : undefined);
    if (!itemId) {
      setBundleState({ kind: 'ready', value: { item: {} as KnowledgeItem, version: {} as KnowledgeVersion, sources: [] } });
      setVersionsState({ kind: 'ready', value: { items: [] } });
      setRelationsState({ kind: 'ready', value: { items: [] } });
      return;
    }
    setBundleState({ kind: 'loading' });
    setVersionsState({ kind: 'loading' });
    setRelationsState({ kind: 'loading' });
    setRelationsLoading(false);
    const requestedPromise = requestedVersion === null || invalidVersion ? Promise.resolve<KnowledgeItemDetails | null>(null) : getKnowledgeVersion(workspaceId, itemId, requestedVersion);
    void Promise.allSettled([getKnowledgeItem(workspaceId, itemId), listKnowledgeVersions(workspaceId, itemId), listKnowledgeRelations(workspaceId, itemId, 'both'), requestedPromise]).then(async ([bundleResult, versionsResult, relationsResult, requestedResult]) => {
      if (current !== request.current || !isCurrent(scope)) return;
      if (bundleResult.status === 'rejected') {
        setBundleState({ kind: 'error', message: errorMessage(bundleResult.reason, '知识正文读取失败') });
        if (versionsResult.status === 'fulfilled') setVersionsState({ kind: 'ready', value: versionsResult.value });
        else setVersionsState({ kind: 'error', message: errorMessage(versionsResult.reason, '知识版本读取失败') });
        if (relationsResult.status === 'fulfilled') setRelationsState({ kind: 'ready', value: relationsResult.value });
        else setRelationsState({ kind: 'error', message: errorMessage(relationsResult.reason, '知识关联读取失败') });
        return;
      }
      const baseBundle = bundleResult.value;
      const selectedBundle = requestedVersion !== null ? requestedResult.status === 'fulfilled' && requestedResult.value ? requestedResult.value : null : baseBundle;
      if (!selectedBundle) setBundleState({ kind: 'error', message: requestedResult.status === 'rejected' ? errorMessage(requestedResult.reason, '固定版本读取失败') : '请求的知识版本不存在或不可访问。' });
      else setBundleState({ kind: 'ready', value: selectedBundle });
      if (versionsResult.status === 'fulfilled') setVersionsState({ kind: 'ready', value: versionsResult.value });
      else setVersionsState({ kind: 'error', message: errorMessage(versionsResult.reason, '知识版本读取失败') });
      if (relationsResult.status === 'fulfilled') setRelationsState({ kind: 'ready', value: relationsResult.value });
      else setRelationsState({ kind: 'error', message: errorMessage(relationsResult.reason, '知识关联读取失败') });
      if (relationsResult.status !== 'fulfilled') return;
      const relationIDs = [...new Set(relationsResult.value.items.map((relation) => relation.from_item_id === itemId ? relation.to_item_id : relation.from_item_id))];
      if (relationIDs.length === 0) return;
      setRelationsLoading(true);
      const targetResults = await Promise.allSettled(relationIDs.map((targetID) => getKnowledgeItem(workspaceId, targetID)));
      if (current !== request.current || !isCurrent(scope)) return;
      const targets: Record<string, KnowledgeItem> = {};
      targetResults.forEach((result, index) => { if (result.status === 'fulfilled') targets[relationIDs[index]!] = result.value.item; });
      setRelationTargets(targets);
      setRelationsLoading(false);
    });
  }, [invalidVersion, itemId, refreshNonce, requestedVersion, retryNonce, workspaceId]);

  useEffect(() => {
    const body = articleRef.current;
    if (!body || bundleState.kind !== 'ready' || !itemId) { setToc([]); return; }
    const headings = Array.from(body.querySelectorAll<HTMLElement>('h1, h2, h3, h4'));
    const seen = new Map<string, number>();
    const entries: KnowledgeTocEntry[] = [];
    headings.forEach((heading, index) => {
      const text = heading.textContent?.trim() ?? '';
      if (!text) return;
      const base = knowledgeHeadingId(text, index);
      const count = seen.get(base) ?? 0;
      seen.set(base, count + 1);
      const id = count === 0 ? base : `${base}-${count + 1}`;
      heading.id = id;
      entries.push({ id, level: Number(heading.tagName.slice(1)), text });
    });
    setToc(entries);
  }, [bundleState, itemId]);

  const bundle = bundleState.kind === 'ready' && itemId ? bundleState.value : null;
  const versions = useMemo(() => {
    if (versionsState.kind !== 'ready' || !bundle) return [];
    const all = [...versionsState.value.items];
    if (!all.some((version) => version.id === bundle.version.id)) all.push(bundle.version);
    return [...all].sort((left, right) => right.version - left.version);
  }, [bundle, versionsState]);
  const currentVersion = useMemo(() => versions.find((version) => version.version === bundle?.item.current_version) ?? versions.find((version) => version.status === 'effective'), [bundle?.item.current_version, versions]);
  const isHistorical = Boolean(bundle && bundle.item.current_version !== bundle.version.version);
  const currentPath = bundle ? buildKnowledgePath({ workspaceId, query, kind, itemId: bundle.item.id, version: bundle.version.version }) : '/knowledge';
  const chatPath = bundle && librarianAgentId ? buildKnowledgeChatPath(librarianAgentId, bundle.item.id, bundle.version.version, currentPath, workspaceId) : null;
  const share = async () => {
    if (!bundle || typeof window === 'undefined') return;
    try {
      await navigator.clipboard?.writeText(new URL(currentPath, window.location.origin).toString());
      setCopied(true);
      window.setTimeout(() => setCopied(false), 2200);
    } catch { setCopied(false); }
  };
  const scrollToHeading = (entry: KnowledgeTocEntry) => { document.getElementById(entry.id)?.scrollIntoView({ behavior: 'smooth', block: 'start' }); };

  if (!itemId) return <section className="knowledge-reader-panel knowledge-reader-empty" data-testid="knowledge-reader" aria-label="知识正文阅读区"><EmptyState icon={<BookOpen className="h-5 w-5" aria-hidden="true" />} title="选择一条知识开始阅读" description="正文、来源、关联和版本会在这里展开。" /></section>;
  const retryReader = () => setRetryNonce((value) => value + 1);
  const canRecoverVersion = Boolean(invalidVersion || requestedVersion !== null);
  return <section className="knowledge-reader-panel" data-testid="knowledge-reader" aria-label="知识正文阅读区"><div className="knowledge-reader-toolbar"><Button type="button" size="sm" className="knowledge-mobile-back" onClick={onClose}><ArrowLeft className="h-3.5 w-3.5" aria-hidden="true" />返回列表</Button><div className="flex min-w-0 flex-1 items-center gap-tight text-caption text-text-tertiary"><FileText className="h-3.5 w-3.5 shrink-0" aria-hidden="true" /><span>知识正文</span></div><div className="flex shrink-0 items-center gap-tight">{bundle ? <Button type="button" size="sm" onClick={() => void share()} aria-label="复制当前版本链接">{copied ? <Check className="h-3.5 w-3.5 text-status-success" aria-hidden="true" /> : <Link2 className="h-3.5 w-3.5" aria-hidden="true" />}{copied ? '已复制' : '分享版本'}</Button> : null}{chatPath ? <Link className="knowledge-chat-link" to={chatPath}><MessageCircle className="h-3.5 w-3.5" aria-hidden="true" />与管理员对话</Link> : null}</div></div>{bundleState.kind === 'loading' ? <div className="knowledge-reader-loading" role="status" aria-label="知识正文加载中"><Skeleton className="h-10 w-3/4" /><Skeleton className="h-5 w-1/2" /><Skeleton className="mt-base h-80 w-full rounded-card" /></div> : bundleState.kind === 'error' || readerError ? <div className="knowledge-reader-error"><InlineError message={readerError ?? (bundleState.kind === 'error' ? bundleState.message : '知识正文读取失败')} onRetry={retryReader} />{canRecoverVersion ? <Button type="button" onClick={() => onSelectVersion(bundle?.item.current_version ?? 1)}>回到当前版本</Button> : null}</div> : bundle ? <div className="knowledge-reader-body"><div className="knowledge-reader-main"><header className="knowledge-article-header"><div className="flex flex-wrap items-center gap-tight"><StatusPill className={knowledgeStatusClass(bundle.version.status)}>{statusLabel(bundle.version.status)}</StatusPill><span className="text-caption text-text-tertiary">v{bundle.version.version}</span>{isHistorical ? <span className="knowledge-history-mark"><History className="h-3 w-3" aria-hidden="true" />历史版本</span> : <span className="knowledge-current-mark"><Check className="h-3 w-3" aria-hidden="true" />当前版本</span>}</div><h2 className="mt-snug text-h1 text-text-primary">{bundle.version.title}</h2>{bundle.version.summary ? <p className="mt-tight max-w-3xl text-body-lg text-text-secondary">{bundle.version.summary}</p> : null}<div className="mt-base flex flex-wrap items-center gap-snug text-caption text-text-tertiary"><span className="inline-flex items-center gap-micro"><Clock3 className="h-3.5 w-3.5" aria-hidden="true" />更新于 {formatDateTime(bundle.item.updated_at)}</span><span>适用范围：{formatKnowledgeScope(bundle.version.scope)}</span></div></header><section className="knowledge-article-section" aria-labelledby="knowledge-body-title"><SectionHeading id="knowledge-body-title" icon={<FileText className="h-4 w-4" aria-hidden="true" />} title="正文" /><div ref={articleRef} className="knowledge-rendered-body" data-testid="knowledge-article"><AgentOutput text={bundle.version.body_markdown} showCaret={false} /></div></section><SourceList sources={bundle.sources} /><RelationList itemId={bundle.item.id} relations={relationsState.kind === 'ready' ? relationsState.value.items : []} state={relationsState} targets={relationTargets} targetsLoading={relationsLoading} onRetry={retryReader} onNavigate={onSelectItem} historical={isHistorical} /></div><aside className="knowledge-reader-aside" aria-label="知识阅读辅助信息"><KnowledgeToc entries={toc} onSelect={scrollToHeading} /><KnowledgeVersionHistory versions={versions} selectedVersion={bundle.version.version} currentVersion={currentVersion} onSelect={onSelectVersion} />{isHistorical && currentVersion ? <KnowledgeDiff selected={bundle.version} current={currentVersion} /> : null}<details className="knowledge-details-panel"><summary>版本元信息</summary><dl className="knowledge-details-grid"><LineageField label="条目标识" value={bundle.item.id} mono /><LineageField label="版本" value={'v' + bundle.version.version} /><LineageField label="创建时间" value={formatDateTime(bundle.version.created_at)} /><LineageField label="创建 Agent" value={bundle.version.created_by_agent_id} mono /><LineageField label="来源 Run" value={bundle.version.created_by_run_id} mono /><LineageField label="来源任务" value={bundle.version.created_by_work_item_id} mono /><LineageField label="内容摘要" value={bundle.version.content_digest} mono /></dl></details></aside></div> : null}</section>;
}

function KnowledgeToc({ entries, onSelect }: { entries: KnowledgeTocEntry[]; onSelect: (entry: KnowledgeTocEntry) => void }) {
  return <section className="knowledge-toc" data-testid="knowledge-toc" aria-labelledby="knowledge-toc-title"><div className="flex items-center gap-tight"><BookOpen className="h-3.5 w-3.5 text-brand-primary" aria-hidden="true" /><h3 id="knowledge-toc-title" className="text-caption font-semibold text-text-primary">正文目录</h3></div>{entries.length === 0 ? <p className="mt-tight text-caption text-text-tertiary">这份正文没有可导航标题。</p> : <nav className="mt-tight space-y-micro" aria-label="正文目录">{entries.map((entry) => <button key={entry.id} type="button" onClick={() => onSelect(entry)} className="knowledge-toc-item" style={{ paddingLeft: `${Math.max(0, entry.level - 1) * 8 + 8}px` }}>{entry.text}</button>)}</nav>}</section>;
}

function KnowledgeVersionHistory({ versions, selectedVersion, currentVersion, onSelect }: { versions: KnowledgeVersion[]; selectedVersion: number; currentVersion?: KnowledgeVersion; onSelect: (version: number) => void }) {
  return <section className="knowledge-version-history" aria-labelledby="knowledge-version-history-title"><div className="flex items-center gap-tight"><History className="h-3.5 w-3.5 text-brand-primary" aria-hidden="true" /><h3 id="knowledge-version-history-title" className="text-caption font-semibold text-text-primary">版本历史</h3></div>{versions.length === 0 ? <p className="mt-tight text-caption text-text-tertiary">没有可见版本记录。</p> : <ol className="mt-tight space-y-micro">{versions.map((version) => { const current = currentVersion?.version === version.version; const selected = selectedVersion === version.version; return <li key={version.id}><button type="button" className={cx('knowledge-version-item', selected && 'knowledge-version-item-selected')} aria-current={selected ? 'page' : undefined} onClick={() => onSelect(version.version)}><span className="knowledge-version-dot" aria-hidden="true" /><span className="min-w-0 flex-1"><span className="flex items-center gap-tight text-caption font-medium text-text-primary">v{version.version}{current ? <span className="knowledge-current-mark">当前</span> : <span className="knowledge-history-mark">历史</span>}</span><span className="mt-micro block truncate text-caption text-text-tertiary">{formatDateTime(version.created_at)} · {statusLabel(version.status)}</span></span><ChevronRight className="h-3.5 w-3.5 shrink-0 text-text-tertiary" aria-hidden="true" /></button></li>; })}</ol>}</section>;
}

function KnowledgeDiff({ selected, current }: { selected: KnowledgeVersion; current: KnowledgeVersion }) {
  const diff = useMemo(() => buildKnowledgeDiff(selected.body_markdown, current.body_markdown), [current.body_markdown, selected.body_markdown]);
  return <section className="knowledge-diff" aria-labelledby="knowledge-diff-title"><div className="flex items-center gap-tight"><GitBranch className="h-3.5 w-3.5 text-brand-primary" aria-hidden="true" /><h3 id="knowledge-diff-title" className="text-caption font-semibold text-text-primary">与当前版本的差异</h3></div><p className="mt-tight text-caption text-text-tertiary">历史 v{selected.version} → 当前 v{current.version}。绿色为当前新增，红色为历史移除。</p><pre className="knowledge-diff-code mt-tight" aria-label={`历史版本 v${selected.version} 与当前版本 v${current.version} 的差异`}>{diff.map((line, index) => <code key={`${line.kind}-${index}`} className={`knowledge-diff-line knowledge-diff-${line.kind}`}>{line.kind === 'added' ? '+ ' : line.kind === 'removed' ? '- ' : '  '}{line.text}{'\n'}</code>)}</pre></section>;
}

function SourceList({ sources }: { sources: KnowledgeSource[] }) {
  const agents = useAgentsStore((state) => state.agents);
  return <section className="knowledge-article-section" aria-labelledby="knowledge-sources-title"><SectionHeading id="knowledge-sources-title" icon={<ShieldCheck className="h-4 w-4" aria-hidden="true" />} title="来源证据" /><div className="mt-snug space-y-tight">{sources.length === 0 ? <p className="text-caption text-text-tertiary">该版本没有登记来源证据。</p> : sources.map((source) => { const verification = sourceVerificationLabel(source); const link = buildKnowledgeSourceLink(source); const display = source.kind === 'agent' ? (agents.find((agent) => agent.id === source.ref)?.name ?? '团队智能体') : (metadataText(source, 'label') ?? metadataText(source, 'title') ?? source.ref); return <div key={source.id} className="knowledge-source-card"><div className="flex flex-wrap items-center gap-tight"><span className="text-caption text-text-secondary">{sourceKindLabel(source.kind)}</span><StatusPill className={knowledgeStatusClass(verification.startsWith('已核验') ? 'complete' : 'missing')}>{verification}</StatusPill></div><div className="mt-micro flex min-w-0 items-center gap-tight text-body font-medium text-text-primary">{link && !link.external ? <Link className="knowledge-inline-link min-w-0 truncate" to={link.href}>{display}</Link> : link?.external ? <a className="knowledge-inline-link min-w-0 truncate" href={link.href} target="_blank" rel="noreferrer noopener">{display}<ExternalLink className="ml-micro inline h-3 w-3" aria-hidden="true" /></a> : <span className="min-w-0 break-words">{display}</span>}</div>{source.kind === 'agent' ? <details className="mt-micro text-caption text-text-tertiary"><summary className="cursor-pointer">查看来源标识</summary><p className="break-all">{source.ref}</p></details> : null}{source.locator && (!link || source.kind !== 'document') ? <p className="mt-micro break-words text-caption text-text-tertiary">定位：{source.locator}</p> : null}{source.excerpt ? <p className="mt-micro whitespace-pre-wrap text-caption text-text-secondary">{source.excerpt}</p> : null}</div>; })}</div></section>;
}

function RelationList({ itemId, relations, state, targets, targetsLoading, onRetry, onNavigate, historical }: { itemId: string; relations: KnowledgeRelation[]; state: KnowledgeListState<KnowledgeRelation>; targets: Record<string, KnowledgeItem>; targetsLoading: boolean; onRetry: () => void; onNavigate: (itemId: string) => void; historical: boolean }) {
  return <section className="knowledge-article-section" aria-labelledby="knowledge-relations-title"><div className="flex items-center justify-between gap-tight"><SectionHeading id="knowledge-relations-title" icon={<GitBranch className="h-4 w-4" aria-hidden="true" />} title="关联知识" /><span className="text-caption tabular-nums text-text-tertiary">{state.kind === 'loading' ? '读取中…' : relations.length + ' 条'}</span></div><p className="mt-tight text-caption text-text-tertiary">{historical ? '关联来自当前有效版本，和历史正文分开显示。' : '展示当前有效的正向和反向关联。'}</p>{state.kind === 'error' ? <div className="mt-snug"><InlineError message={state.message} onRetry={onRetry} /></div> : relations.length === 0 ? <p className="mt-snug text-caption text-text-tertiary">没有可见的关联知识。</p> : <div className="mt-snug space-y-tight">{relations.map((relation) => { const outgoing = relation.from_item_id === itemId; const otherID = outgoing ? relation.to_item_id : relation.from_item_id; const target = targets[otherID]; return <div key={relation.id} className="knowledge-relation-card"><div className="flex items-start gap-tight"><GitBranch className="mt-0.5 h-4 w-4 shrink-0 text-brand-primary" aria-hidden="true" /><div className="min-w-0 flex-1"><p className="text-body text-text-primary">{outgoing ? '本条知识' : '另一条知识'} <span className="font-medium">{relationLabel(relation.kind)}</span> {outgoing ? '另一条知识' : '本条知识'}</p>{target ? <button type="button" onClick={() => onNavigate(target.id)} className="knowledge-relation-target" aria-label={'打开关联知识：' + target.title}>{target.title}<ChevronRight className="h-3.5 w-3.5" aria-hidden="true" /></button> : <p className="mt-micro break-words text-caption text-text-tertiary">{targetsLoading ? '正在读取目标标题…' : '目标条目当前不可见 · ' + otherID}</p>}{relation.condition ? <p className="mt-micro text-caption text-text-secondary">条件：{relation.condition}</p> : null}{relation.rationale ? <p className="mt-micro text-caption text-text-tertiary">依据：{relation.rationale}</p> : null}</div></div></div>; })}</div>}</section>;
}

function LineageField({ label, value, mono = false }: { label: string; value?: string; mono?: boolean }) {
  return <div className="min-w-0"><dt className="text-text-tertiary">{label}</dt><dd className={cx('mt-micro break-words text-text-secondary', mono && 'font-mono')}>{value ?? '未记录'}</dd></div>;
}

function SectionHeading({ id, icon, title }: { id: string; icon: ReactNode; title: string }) {
  return <h3 id={id} className="flex items-center gap-tight text-body font-medium text-text-primary"><span className="text-brand-primary" aria-hidden="true">{icon}</span>{title}</h3>;
}

function InlineError({ message, onRetry }: { message: string; onRetry: () => void }) {
  return <div className="flex flex-wrap items-center justify-between gap-base px-comfortable py-base" role="alert"><div className="flex min-w-0 items-start gap-tight"><AlertCircle className="mt-0.5 h-5 w-5 shrink-0 text-status-error" aria-hidden="true" /><p className="text-body text-text-secondary">{message}</p></div><Button type="button" size="sm" onClick={onRetry}>重试</Button></div>;
}
