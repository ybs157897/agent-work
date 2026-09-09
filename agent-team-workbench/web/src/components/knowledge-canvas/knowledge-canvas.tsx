import { lazy, Suspense, useCallback, useEffect, useMemo, useRef, useState, type SyntheticEvent } from 'react';
import {
  AlertCircle,
  BookOpen,
  ChevronLeft,
  ChevronRight,
  FileText,
  GitBranch,
  History,
  LayoutGrid,
  List,
  LoaderCircle,
  Quote,
  RefreshCw,
  Search,
  X,
} from 'lucide-react';
import { ApiError } from '../../api/client';
import {
  getKnowledgeItem,
  getKnowledgeVersion,
  listKnowledgeItems,
  listKnowledgeVersions,
  type KnowledgeItem,
  type KnowledgeItemsFilter,
  type KnowledgeItemDetails,
  type KnowledgeSource,
  type KnowledgeVersion,
} from '../../api/knowledge';
import { knowledgeHeadingId, knowledgeKindLabel, sourceKindLabel } from '../../pages/knowledge.page';
import type { KnowledgeCanvasReference } from '../../utils/agent-knowledge-canvas';
import { formatDateTime } from '../../utils/format';
import { AgentOutput } from '../chat/agent-output';
import { Button, EmptyState, Input, Skeleton, cx } from '../ui';
import './knowledge-canvas.css';

const KnowledgeGraph = lazy(() => import('./knowledge-graph').then(({ KnowledgeGraph: Graph }) => ({ default: Graph })));

export type KnowledgeCanvasView = 'documents' | 'graph';

/**
 * A reference is deliberately a quote-sized payload. The chat owner can turn it
 * into a removable draft chip without copying a whole document into a message.
 */
export type { KnowledgeCanvasReference } from '../../utils/agent-knowledge-canvas';

export interface KnowledgeCanvasProps {
  workspaceId: string;
  workspaceName?: string;
  agentId: string;
  agentName: string;
  requesterAgentId?: string;
  onReference: (reference: KnowledgeCanvasReference) => void;
  refreshKey?: string | number;
}

export function makeKnowledgeCanvasReference(
  workspaceId: string,
  agentId: string,
  reference: Omit<KnowledgeCanvasReference, 'workspaceId' | 'agentId'>,
): KnowledgeCanvasReference {
  return { workspaceId, agentId, ...reference };
}

type AsyncState<T> =
  | { kind: 'loading' }
  | { kind: 'ready'; value: T }
  | { kind: 'error'; message: string };

export interface KnowledgeCanvasItemsResult {
  items: KnowledgeItem[];
  nextCursor?: string;
  truncated: boolean;
}

type KnowledgeItemsResponse = Awaited<ReturnType<typeof listKnowledgeItems>>;
type KnowledgeItemsLoader = (workspaceId: string, filter: KnowledgeItemsFilter) => Promise<KnowledgeItemsResponse>;
type KnowledgeItemLoader = (workspaceId: string, itemId: string, requesterAgentId?: string) => Promise<KnowledgeItemDetails>;

/**
 * Read one owner-scoped page. A repeated cursor is surfaced as a partial
 * response rather than allowing the load-more action to loop indefinitely.
 */
export async function loadKnowledgeCanvasItems(
  workspaceId: string,
  agentId: string,
  requesterAgentId?: string,
  query?: string,
  cursor?: string,
  load: KnowledgeItemsLoader = listKnowledgeItems,
): Promise<KnowledgeCanvasItemsResult> {
  const items: KnowledgeItem[] = [];
  const response = await load(workspaceId, {
    q: query?.trim() || undefined,
    status: 'effective',
    owner_agent_id: agentId,
    agent_id: requesterAgentId,
    cursor,
    limit: 40,
  });
  const seenItemIds = new Set<string>();
  response.items.forEach((item) => {
    if (seenItemIds.has(item.id)) return;
    seenItemIds.add(item.id);
    items.push(item);
  });
  const nextCursor = response.next_cursor && response.next_cursor !== cursor ? response.next_cursor : undefined;
  return { items, nextCursor, truncated: Boolean(response.truncated) || Boolean(response.next_cursor && !nextCursor) };
}

export type KnowledgeCanvasRestoreResult =
  | { kind: 'restored'; details: KnowledgeItemDetails }
  | { kind: 'unavailable'; message: string }
  | { kind: 'error'; message: string };

/**
 * Recover one persisted document without scanning another list page. The
 * backend remains the authority for visibility; these checks fence a stale
 * or cross-agent response before it can enter the current canvas list.
 */
export async function restoreKnowledgeCanvasItem(
  workspaceId: string,
  agentId: string,
  requesterAgentId: string | undefined,
  itemId: string,
  load: KnowledgeItemLoader = getKnowledgeItem,
): Promise<KnowledgeCanvasRestoreResult> {
  try {
    const details = await load(workspaceId, itemId, requesterAgentId);
    const item = details.item;
    if (item.workspace_id !== workspaceId || item.owner_agent_id !== agentId) {
      return { kind: 'unavailable', message: '上次打开的文档已不再属于当前 Agent。' };
    }
    if (item.status !== 'effective' || details.version.status !== 'effective') {
      return { kind: 'unavailable', message: '上次打开的文档已不再发布。' };
    }
    return { kind: 'restored', details };
  } catch (error: unknown) {
    if (error instanceof ApiError && (error.status === 404 || error.code === 'not_found')) {
      return { kind: 'unavailable', message: '上次打开的文档已不存在或不可访问。' };
    }
    return { kind: 'error', message: `上次打开的文档恢复失败：${errorMessage(error, '请求失败')}。` };
  }
}

export type KnowledgeCanvasSelectionReconciliation =
  | { kind: 'in-page'; result: KnowledgeCanvasItemsResult }
  | { kind: 'restored'; result: KnowledgeCanvasItemsResult; item: KnowledgeItem }
  | { kind: 'unavailable'; result: KnowledgeCanvasItemsResult; message: string }
  | { kind: 'error'; result: KnowledgeCanvasItemsResult; message: string };

export function findKnowledgeCanvasVersionChange(
  previousItems: ReadonlyMap<string, KnowledgeItem>,
  nextItems: KnowledgeItem[],
  selectedItemId: string | null,
): KnowledgeItem | null {
  if (!selectedItemId) return null;
  const previous = previousItems.get(selectedItemId);
  const next = nextItems.find((item) => item.id === selectedItemId);
  return previous && next && next.current_version > previous.current_version ? next : null;
}

/** Keep a selected item visible across a bounded refresh without loading the rest of the list. */
export async function reconcileKnowledgeCanvasSelection(
  workspaceId: string,
  agentId: string,
  requesterAgentId: string | undefined,
  query: string | undefined,
  selectedItemId: string | null,
  result: KnowledgeCanvasItemsResult,
  load: KnowledgeItemLoader = getKnowledgeItem,
): Promise<KnowledgeCanvasSelectionReconciliation> {
  if (query?.trim() || !selectedItemId || result.items.some((item) => item.id === selectedItemId)) {
    return { kind: 'in-page', result };
  }
  const restored = await restoreKnowledgeCanvasItem(workspaceId, agentId, requesterAgentId, selectedItemId, load);
  if (restored.kind === 'restored') {
    const items = [restored.details.item, ...result.items];
    return { kind: 'restored', result: { ...result, items }, item: restored.details.item };
  }
  if (restored.kind === 'unavailable') {
    return { kind: 'unavailable', result, message: `${restored.message} 已回到当前首条文档。` };
  }
  return { kind: 'error', result, message: `${restored.message} 当前文档上下文已保留，请重试。` };
}

export interface KnowledgeCanvasRequestFence {
  begin: () => number;
  isCurrent: (token: number) => boolean;
}

/** A tiny fencing primitive used by list/detail effects and covered by async-switch tests. */
export function createKnowledgeCanvasRequestFence(): KnowledgeCanvasRequestFence {
  let current = 0;
  return {
    begin: () => {
      current += 1;
      return current;
    },
    isCurrent: (token) => token === current,
  };
}

export function knowledgeCanvasStorageKey(workspaceId: string, agentId: string): string {
  return `knowledge-canvas:view:${encodeURIComponent(workspaceId)}:${encodeURIComponent(agentId)}`;
}

interface StoredCanvasState {
  view: KnowledgeCanvasView;
  itemId: string | null;
}

function readStoredCanvasState(key: string): StoredCanvasState | null {
  if (typeof window === 'undefined') return null;
  try {
    const raw = window.localStorage.getItem(key);
    if (!raw) return null;
    const parsed = JSON.parse(raw) as Partial<StoredCanvasState>;
    return {
      view: parsed.view === 'graph' ? 'graph' : 'documents',
      itemId: typeof parsed.itemId === 'string' ? parsed.itemId : null,
    };
  } catch {
    return null;
  }
}

function writeStoredCanvasState(key: string, state: StoredCanvasState): void {
  if (typeof window === 'undefined') return;
  try {
    window.localStorage.setItem(key, JSON.stringify(state));
  } catch {
    // Private browsing and blocked storage should not break the reader.
  }
}

function errorMessage(error: unknown, fallback: string): string {
  if (error instanceof ApiError && error.message) return error.message;
  if (error instanceof Error && error.message) return error.message;
  return fallback;
}

export function normalizeKnowledgeQuote(value: string): string {
  const normalized = value.replace(/\s+/g, ' ').trim();
  const maxQuoteLength = 2400;
  return normalized.length > maxQuoteLength
    ? `${normalized.slice(0, maxQuoteLength)}\n（摘录已截至 ${maxQuoteLength} 字符，可读取该版本全文。）`
    : normalized;
}

function quoteHeading(block: HTMLElement, readerBody: HTMLElement): string | undefined {
  if (/^H[1-4]$/.test(block.tagName)) return normalizeKnowledgeQuote(block.textContent ?? '') || undefined;
  let heading: HTMLElement | undefined;
  readerBody.querySelectorAll<HTMLElement>('h1, h2, h3, h4').forEach((candidate) => {
    if (candidate.compareDocumentPosition(block) & Node.DOCUMENT_POSITION_FOLLOWING) heading = candidate;
  });
  return heading ? normalizeKnowledgeQuote(heading.textContent ?? '') || undefined : undefined;
}

function quoteBlock(target: EventTarget | null): HTMLElement | null {
  const element = target instanceof HTMLElement ? target : target instanceof Text ? target.parentElement : null;
  return element?.closest('p, h1, h2, h3, h4, li, blockquote, pre, table') ?? null;
}

function isInside(currentTarget: HTMLElement, node: Node | null): boolean {
  return Boolean(node && (node === currentTarget || currentTarget.contains(node)));
}

function statusLabel(item: KnowledgeItem): string {
  return item.current_version > 0 ? `v${item.current_version}` : '已发布';
}

interface KnowledgeDocumentBundle {
  details: KnowledgeItemDetails;
  versions: KnowledgeVersion[];
  versionsError?: string;
}

interface KnowledgeTocEntry {
  id: string;
  level: number;
  text: string;
}

function markdownHeadingText(value: string): string {
  return value.replace(/[*_`~]/g, '').replace(/\s+#+\s*$/, '').trim();
}

function buildKnowledgeToc(body: string): KnowledgeTocEntry[] {
  const seen = new Map<string, number>();
  return body.split(/\r?\n/).flatMap((line, index) => {
    const match = line.match(/^\s{0,3}(#{1,4})\s+(.+?)\s*$/);
    if (!match) return [];
    const text = markdownHeadingText(match[2] ?? '');
    if (!text) return [];
    const base = knowledgeHeadingId(text, index);
    const count = seen.get(base) ?? 0;
    seen.set(base, count + 1);
    return [{ id: count === 0 ? base : `${base}-${count + 1}`, level: match[1]?.length ?? 1, text }];
  });
}

function ListError({ message, onRetry }: { message: string; onRetry: () => void }) {
  return (
    <div className="knowledge-canvas-feedback" role="alert">
      <AlertCircle className="h-5 w-5 shrink-0 text-status-error" aria-hidden="true" />
      <div className="min-w-0 flex-1">
        <p className="text-body font-medium text-text-primary">知识列表读取失败</p>
        <p className="mt-micro break-words text-caption text-text-secondary">{message}</p>
      </div>
      <Button type="button" size="sm" onClick={onRetry}>
        <RefreshCw className="h-3.5 w-3.5" aria-hidden="true" />
        重试
      </Button>
    </div>
  );
}

function DocumentList({
  items,
  selectedItemId,
  query,
  onSelect,
}: {
  items: KnowledgeItem[];
  selectedItemId: string | null;
  query: string;
  onSelect: (item: KnowledgeItem) => void;
}) {
  if (items.length === 0) {
    return (
      <EmptyState
        className="knowledge-canvas-empty"
        icon={<FileText className="h-5 w-5" aria-hidden="true" />}
        title={query ? '没有匹配的已发布文档' : '还没有已发布文档'}
        description={query ? '换一个关键词后再试。' : '这个 Agent 的已发布知识会出现在这里。'}
      />
    );
  }

  return (
    <ul className="knowledge-canvas-document-list" aria-label="产品知识文档">
      {items.map((item) => (
        <li key={item.id}>
          <button
            type="button"
            className={cx('knowledge-canvas-document-row', selectedItemId === item.id && 'is-selected')}
            aria-current={selectedItemId === item.id ? 'page' : undefined}
            onClick={() => onSelect(item)}
          >
            <span className="knowledge-canvas-document-icon" aria-hidden="true">
              <FileText className="h-4 w-4" />
            </span>
            <span className="min-w-0 flex-1 text-left">
              <span className="knowledge-canvas-document-title">{item.title}</span>
              <span className="knowledge-canvas-document-summary">{item.summary || '暂无摘要'}</span>
            </span>
            <span className="knowledge-canvas-document-version">{statusLabel(item)}</span>
          </button>
        </li>
      ))}
    </ul>
  );
}

function DocumentView({
  detailsState,
  onRetry,
  onSelectVersion,
  onReference,
}: {
  detailsState: AsyncState<KnowledgeDocumentBundle> | { kind: 'empty' };
  onRetry: () => void;
  onSelectVersion: (version: number) => void;
  onReference: (reference: Omit<KnowledgeCanvasReference, 'workspaceId' | 'agentId'>) => void;
}) {
  const articleRef = useRef<HTMLDivElement>(null);
  const [pendingQuote, setPendingQuote] = useState<{ quote: string; heading?: string } | null>(null);
  const detailsKey = detailsState.kind === 'ready' ? `${detailsState.value.details.item.id}:${detailsState.value.details.version.version}` : detailsState.kind;

  useEffect(() => {
    setPendingQuote(null);
  }, [detailsKey]);

  const handleArticleSelection = useCallback((event: SyntheticEvent<HTMLDivElement>) => {
    const selection = typeof window !== 'undefined' ? window.getSelection() : null;
    if (!selection || selection.isCollapsed) return;
    if (!isInside(event.currentTarget, selection.anchorNode) || !isInside(event.currentTarget, selection.focusNode)) return;
    const quote = normalizeKnowledgeQuote(selection.toString());
    const block = quoteBlock(selection.anchorNode);
    if (quote && block) setPendingQuote({ quote, heading: quoteHeading(block, event.currentTarget) });
  }, []);

  const handleArticleClick = useCallback((event: SyntheticEvent<HTMLDivElement>) => {
    const block = quoteBlock(event.target);
    if (!block || !event.currentTarget.contains(block)) return;
    const selection = typeof window !== 'undefined' ? window.getSelection() : null;
    if (selection && !selection.isCollapsed && normalizeKnowledgeQuote(selection.toString())) return;
    const quote = normalizeKnowledgeQuote(block.textContent ?? '');
    if (quote) setPendingQuote({ quote, heading: quoteHeading(block, event.currentTarget) });
  }, []);

  const tocBody = detailsState.kind === 'ready' ? detailsState.value.details.version.body_markdown : '';
  const toc = useMemo(() => buildKnowledgeToc(tocBody), [tocBody]);
  const versionHistory = useMemo(() => {
    if (detailsState.kind !== 'ready') return [];
    const selectedVersion = detailsState.value.details.version;
    const all = detailsState.value.versions.some((entry) => entry.id === selectedVersion.id)
      ? detailsState.value.versions
      : [...detailsState.value.versions, selectedVersion];
    return [...all].sort((left, right) => right.version - left.version);
  }, [detailsState]);
  useEffect(() => {
    const headings = articleRef.current?.querySelectorAll<HTMLElement>('h1, h2, h3, h4');
    if (!headings) return;
    headings.forEach((heading, index) => {
      const entry = toc[index];
      if (entry) heading.id = entry.id;
    });
  }, [toc]);

  if (detailsState.kind === 'empty') {
    return (
      <EmptyState
        className="knowledge-canvas-reader-empty"
        icon={<BookOpen className="h-5 w-5" aria-hidden="true" />}
        title="选择一份文档开始阅读"
        description="点击正文段落后，可以选择“引用这段”带入右侧对话。"
      />
    );
  }
  if (detailsState.kind === 'loading') {
    return (
      <div className="knowledge-canvas-reader-loading" role="status" aria-label="文档正文加载中">
        <Skeleton className="h-4 w-24" />
        <Skeleton className="mt-base h-8 w-2/3" />
        <Skeleton className="mt-tight h-4 w-1/2" />
        <Skeleton className="mt-loose h-72 w-full rounded-card" />
      </div>
    );
  }
  if (detailsState.kind === 'error') {
    return (
      <div className="knowledge-canvas-feedback knowledge-canvas-reader-error" role="alert">
        <AlertCircle className="h-5 w-5 shrink-0 text-status-error" aria-hidden="true" />
        <div className="min-w-0 flex-1">
          <p className="text-body font-medium text-text-primary">文档正文读取失败</p>
          <p className="mt-micro break-words text-caption text-text-secondary">{detailsState.message}</p>
        </div>
        <Button type="button" size="sm" onClick={onRetry}>
          <RefreshCw className="h-3.5 w-3.5" aria-hidden="true" />
          重试
        </Button>
      </div>
    );
  }

  const { details, versionsError } = detailsState.value;
  const { item, version, sources } = details;
  const isHistorical = item.current_version !== version.version;

  return (
    <article className="knowledge-canvas-reader" aria-labelledby="knowledge-canvas-document-title">
      <header className="knowledge-canvas-reader-header">
        <div className="knowledge-canvas-reader-meta">
          <span className="knowledge-canvas-kicker">{knowledgeKindLabel(version.kind)}</span>
          <span className="knowledge-canvas-version-badge">v{version.version}</span>
          <span className="text-caption text-text-tertiary">{isHistorical ? '历史版本' : '当前版本'} · 更新于 {formatDateTime(version.created_at)}</span>
        </div>
        <h2 id="knowledge-canvas-document-title" className="knowledge-canvas-reader-title">{version.title}</h2>
        {version.summary ? <p className="knowledge-canvas-reader-summary">{version.summary}</p> : null}
        <details className="knowledge-canvas-lineage">
          <summary>查看版本信息</summary>
          <dl>
            <div><dt>条目标识</dt><dd>{item.id}</dd></div>
            <div><dt>当前版本</dt><dd>v{item.current_version}</dd></div>
            <div><dt>发布于</dt><dd>{formatDateTime(version.published_at ?? version.created_at)}</dd></div>
          </dl>
        </details>
      </header>
      {pendingQuote ? (
        <div className="knowledge-canvas-quote-bar" role="status">
          <Quote className="h-4 w-4 shrink-0 text-brand-primary" aria-hidden="true" />
          <span className="min-w-0 flex-1 truncate">已选中：{pendingQuote.quote}</span>
          <Button
            type="button"
            size="sm"
            variant="primary"
            onClick={() => {
              onReference({ itemId: item.id, version: version.version, title: version.title, quote: pendingQuote.quote, heading: pendingQuote.heading });
              setPendingQuote(null);
              if (typeof window !== 'undefined') window.getSelection()?.removeAllRanges();
            }}
          >
            <Quote className="h-3.5 w-3.5" aria-hidden="true" />
            引用这段
          </Button>
          <button type="button" className="knowledge-canvas-icon-button" aria-label="取消选择" onClick={() => setPendingQuote(null)}>
            <X className="h-4 w-4" aria-hidden="true" />
          </button>
        </div>
      ) : null}
      <div ref={articleRef} className="knowledge-canvas-reader-body" onMouseUp={handleArticleSelection} onKeyUp={handleArticleSelection} onClick={handleArticleClick}>
        <AgentOutput text={version.body_markdown} showCaret={false} />
      </div>
      <div className="knowledge-canvas-reader-support">
        <section className="knowledge-canvas-support-section" aria-labelledby="knowledge-canvas-toc-title">
          <div className="knowledge-canvas-support-heading"><BookOpen className="h-4 w-4 text-brand-primary" aria-hidden="true" /><h3 id="knowledge-canvas-toc-title">正文目录</h3></div>
          {toc.length === 0 ? <p className="knowledge-canvas-support-empty">这份正文没有可导航标题。</p> : <nav aria-label="正文目录">{toc.map((entry) => <button key={entry.id} type="button" className="knowledge-canvas-toc-item" style={{ paddingLeft: `${8 + Math.max(0, entry.level - 1) * 10}px` }} onClick={() => document.getElementById(entry.id)?.scrollIntoView({ behavior: 'smooth', block: 'start' })}>{entry.text}</button>)}</nav>}
        </section>
        <section className="knowledge-canvas-support-section" aria-labelledby="knowledge-canvas-history-title">
          <div className="knowledge-canvas-support-heading"><History className="h-4 w-4 text-brand-primary" aria-hidden="true" /><h3 id="knowledge-canvas-history-title">版本历史</h3></div>
          {versionsError ? <p className="knowledge-canvas-support-error" role="alert">{versionsError} <button type="button" onClick={onRetry}>重试</button></p> : null}
          {versionHistory.length === 0 ? <p className="knowledge-canvas-support-empty">没有可见版本记录。</p> : <ol className="knowledge-canvas-version-list">{versionHistory.map((entry) => <li key={entry.id}><button type="button" className={cx('knowledge-canvas-version-item', entry.version === version.version && 'is-selected')} aria-current={entry.version === version.version ? 'page' : undefined} onClick={() => onSelectVersion(entry.version)}><span className="knowledge-canvas-version-dot" aria-hidden="true" /><span className="min-w-0 flex-1"><span className="knowledge-canvas-version-label">v{entry.version}{entry.version === item.current_version ? ' · 当前' : ' · 历史'}</span><span className="knowledge-canvas-version-date">{formatDateTime(entry.published_at ?? entry.created_at)}</span></span><ChevronRight className="h-3.5 w-3.5 shrink-0" aria-hidden="true" /></button></li>)}</ol>}
        </section>
        <section className="knowledge-canvas-support-section" aria-labelledby="knowledge-canvas-sources-title">
          <div className="knowledge-canvas-support-heading"><GitBranch className="h-4 w-4 text-brand-primary" aria-hidden="true" /><h3 id="knowledge-canvas-sources-title">来源证据</h3></div>
          {sources.length === 0 ? <p className="knowledge-canvas-support-empty">该版本没有登记来源证据。</p> : <ul className="knowledge-canvas-source-list">{sources.map((source: KnowledgeSource) => <li key={source.id}><div className="knowledge-canvas-source-meta"><span>{sourceKindLabel(source.kind)}</span><span className="knowledge-canvas-source-ref">{source.ref}</span></div>{source.excerpt ? <p>{source.excerpt}</p> : null}{source.locator ? <p className="knowledge-canvas-source-locator">定位：{source.locator}</p> : null}</li>)}</ul>}
        </section>
      </div>
    </article>
  );
}

function CanvasLoading({ label }: { label: string }) {
  return (
    <div className="knowledge-canvas-loading" role="status" aria-label={label}>
      <LoaderCircle className="h-4 w-4 animate-spin text-brand-primary motion-reduce:animate-none" aria-hidden="true" />
      <span>{label}</span>
    </div>
  );
}

export function KnowledgeCanvas({
  workspaceId,
  workspaceName,
  agentId,
  agentName,
  requesterAgentId,
  onReference,
  refreshKey,
}: KnowledgeCanvasProps) {
  const storageKey = useMemo(() => knowledgeCanvasStorageKey(workspaceId, agentId), [agentId, workspaceId]);
  const initialStoredState = useMemo(() => readStoredCanvasState(storageKey), [storageKey]);
  const scopeKey = `${workspaceId}:${agentId}:${requesterAgentId ?? ''}`;
  const [view, setView] = useState<KnowledgeCanvasView>(initialStoredState?.view ?? 'documents');
  const [selectedItemId, setSelectedItemId] = useState<string | null>(initialStoredState?.itemId ?? null);
  const [listCollapsed, setListCollapsed] = useState(false);
  const [queryInput, setQueryInput] = useState('');
  const [query, setQuery] = useState('');
  const [itemsState, setItemsState] = useState<AsyncState<KnowledgeCanvasItemsResult>>({ kind: 'loading' });
  const [detailsState, setDetailsState] = useState<AsyncState<KnowledgeDocumentBundle> | { kind: 'empty' }>({ kind: 'empty' });
  const [newVersionNotice, setNewVersionNotice] = useState<{ itemId: string; title: string; version: number } | null>(null);
  const [retryNonce, setRetryNonce] = useState(0);
  const [isLoadingMore, setIsLoadingMore] = useState(false);
  const [loadMoreError, setLoadMoreError] = useState<string | null>(null);
  const [restoreState, setRestoreState] = useState<{ kind: 'idle' } | { kind: 'loading' } | { kind: 'unavailable' | 'error'; message: string }>({ kind: 'idle' });
  const listFence = useRef<KnowledgeCanvasRequestFence>(createKnowledgeCanvasRequestFence());
  const detailFence = useRef<KnowledgeCanvasRequestFence>(createKnowledgeCanvasRequestFence());
  const previousItems = useRef<Map<string, KnowledgeItem>>(new Map());
  const selectedItemIdRef = useRef(selectedItemId);
  const previousScope = useRef(scopeKey);
  const previousQuery = useRef(query);

  useEffect(() => {
    selectedItemIdRef.current = selectedItemId;
  }, [selectedItemId]);

  useEffect(() => {
    const stored = readStoredCanvasState(storageKey);
    setView(stored?.view ?? 'documents');
    setSelectedItemId(stored?.itemId ?? null);
    selectedItemIdRef.current = stored?.itemId ?? null;
    setQueryInput('');
    setQuery('');
    setItemsState({ kind: 'loading' });
    setDetailsState({ kind: 'empty' });
    setNewVersionNotice(null);
    setIsLoadingMore(false);
    setLoadMoreError(null);
    setRestoreState({ kind: 'idle' });
    listFence.current.begin();
    detailFence.current.begin();
    previousItems.current = new Map();
    previousScope.current = scopeKey;
    previousQuery.current = '';
  }, [scopeKey, storageKey]);

  useEffect(() => () => {
    listFence.current.begin();
    detailFence.current.begin();
  }, []);

  useEffect(() => {
    const timeout = window.setTimeout(() => setQuery(queryInput.trim()), 220);
    return () => window.clearTimeout(timeout);
  }, [queryInput]);

  useEffect(() => {
    const fence = listFence.current;
    const token = fence.begin();
    const scopeAtStart = scopeKey;
    const queryAtStart = query;
    const detectVersion = previousScope.current === scopeAtStart && previousQuery.current === queryAtStart && previousItems.current.size > 0;
    setItemsState({ kind: 'loading' });
    setLoadMoreError(null);
    void loadKnowledgeCanvasItems(workspaceId, agentId, requesterAgentId, queryAtStart)
      .then(async (result) => {
        if (!fence.isCurrent(token)) return;
        const selectedId = selectedItemIdRef.current;
        const needsSelectionReconciliation = !queryAtStart
          && Boolean(selectedId)
          && !result.items.some((item) => item.id === selectedId);
        if (needsSelectionReconciliation) setRestoreState({ kind: 'loading' });
        const reconciliation = await reconcileKnowledgeCanvasSelection(
          workspaceId,
          agentId,
          requesterAgentId,
          queryAtStart,
          selectedId,
          result,
        );
        if (!fence.isCurrent(token)) return;
        const reconciledResult = reconciliation.result;
        const nextMap = new Map(reconciledResult.items.map((item) => [item.id, item]));
        if (detectVersion) {
          const nextVersion = findKnowledgeCanvasVersionChange(previousItems.current, reconciledResult.items, selectedId);
          if (nextVersion) setNewVersionNotice({ itemId: nextVersion.id, title: nextVersion.title, version: nextVersion.current_version });
        }
        if (reconciliation.kind !== 'error') previousItems.current = nextMap;
        previousScope.current = scopeAtStart;
        previousQuery.current = queryAtStart;
        setItemsState({ kind: 'ready', value: reconciledResult });
        if (reconciliation.kind === 'unavailable') {
          setRestoreState({ kind: 'unavailable', message: reconciliation.message });
          setSelectedItemId(null);
          selectedItemIdRef.current = null;
          setDetailsState({ kind: 'empty' });
        } else if (reconciliation.kind === 'error') {
          setRestoreState({ kind: 'error', message: reconciliation.message });
        } else {
          setRestoreState({ kind: 'idle' });
        }
        if (queryAtStart && selectedId && !nextMap.has(selectedId)) {
          setSelectedItemId(null);
          selectedItemIdRef.current = null;
          setDetailsState({ kind: 'empty' });
        }
      })
      .catch((error: unknown) => {
        if (!fence.isCurrent(token)) return;
        setItemsState({ kind: 'error', message: errorMessage(error, '请检查网络或稍后重试。') });
      });
    return () => { fence.begin(); };
  }, [agentId, query, refreshKey, requesterAgentId, retryNonce, scopeKey, workspaceId]);

  useEffect(() => {
    writeStoredCanvasState(storageKey, { view, itemId: selectedItemId });
  }, [selectedItemId, storageKey, view]);

  const selectItem = useCallback((item: KnowledgeItem | null, options: { openDocument?: boolean; clearRestoreNotice?: boolean } = {}) => {
    if (!item) {
      setSelectedItemId(null);
      setDetailsState({ kind: 'empty' });
      setNewVersionNotice(null);
      return;
    }
    if (options.clearRestoreNotice !== false) {
      setRestoreState({ kind: 'idle' });
    }
    if (options.openDocument !== false) setView('documents');
    setSelectedItemId(item.id);
    setDetailsState({ kind: 'loading' });
    setNewVersionNotice(null);
    const token = detailFence.current.begin();
    void Promise.allSettled([
      getKnowledgeItem(workspaceId, item.id, requesterAgentId),
      listKnowledgeVersions(workspaceId, item.id, requesterAgentId),
    ])
      .then(([detailsResult, versionsResult]) => {
        if (!detailFence.current.isCurrent(token)) return;
        if (detailsResult.status === 'rejected') throw detailsResult.reason;
        const versions = versionsResult.status === 'fulfilled' ? versionsResult.value.items : [];
        setDetailsState({
          kind: 'ready',
          value: {
            details: detailsResult.value,
            versions,
            versionsError: versionsResult.status === 'rejected' ? errorMessage(versionsResult.reason, '版本历史读取失败。') : undefined,
          },
        });
      })
      .catch((error: unknown) => {
        if (!detailFence.current.isCurrent(token)) return;
        setDetailsState({ kind: 'error', message: errorMessage(error, '请检查网络或稍后重试。') });
      });
  }, [requesterAgentId, workspaceId]);

  const selectVersion = useCallback((versionNumber: number) => {
    if (detailsState.kind !== 'ready' || !selectedItemId) return;
    const previous = detailsState.value;
    const token = detailFence.current.begin();
    setDetailsState({ kind: 'loading' });
    void getKnowledgeVersion(workspaceId, selectedItemId, versionNumber, requesterAgentId)
      .then((details) => {
        if (!detailFence.current.isCurrent(token)) return;
        setDetailsState({ kind: 'ready', value: { details, versions: previous.versions, versionsError: previous.versionsError } });
      })
      .catch((error: unknown) => {
        if (!detailFence.current.isCurrent(token)) return;
        setDetailsState({ kind: 'error', message: errorMessage(error, '历史版本读取失败。') });
      });
  }, [detailsState, requesterAgentId, selectedItemId, workspaceId]);

  useEffect(() => {
    if (itemsState.kind !== 'ready' || detailsState.kind !== 'empty') return;
    const item = selectedItemIdRef.current
      ? itemsState.value.items.find((entry) => entry.id === selectedItemIdRef.current)
      : itemsState.value.items[0];
    if (item) selectItem(item, { openDocument: false, clearRestoreNotice: restoreState.kind !== 'unavailable' });
  }, [detailsState.kind, itemsState, restoreState.kind, selectItem, selectedItemId]);

  const retryList = useCallback(() => {
    setItemsState({ kind: 'loading' });
    setRetryNonce((current) => current + 1);
  }, []);

  const retryDetails = useCallback(() => {
    const item = itemsState.kind === 'ready' && selectedItemId ? itemsState.value.items.find((entry) => entry.id === selectedItemId) : undefined;
    if (item) selectItem(item);
  }, [itemsState, selectItem, selectedItemId]);

  const loadMore = useCallback(() => {
    if (itemsState.kind !== 'ready' || !itemsState.value.nextCursor || isLoadingMore) return;
    const token = listFence.current.begin();
    const cursor = itemsState.value.nextCursor;
    setIsLoadingMore(true);
    setLoadMoreError(null);
    void loadKnowledgeCanvasItems(workspaceId, agentId, requesterAgentId, query, cursor)
      .then((result) => {
        if (!listFence.current.isCurrent(token)) return;
        setItemsState((current) => {
          if (current.kind !== 'ready') return current;
          const seen = new Set(current.value.items.map((entry) => entry.id));
          const mergedItems = [...current.value.items, ...result.items.filter((entry) => !seen.has(entry.id))];
          previousItems.current = new Map(mergedItems.map((entry) => [entry.id, entry]));
          return { kind: 'ready', value: { items: mergedItems, nextCursor: result.nextCursor, truncated: current.value.truncated || result.truncated } };
        });
      })
      .catch((error: unknown) => {
        if (listFence.current.isCurrent(token)) setLoadMoreError(errorMessage(error, '继续加载失败，请重试。'));
      })
      .finally(() => {
        if (listFence.current.isCurrent(token)) setIsLoadingMore(false);
      });
  }, [agentId, isLoadingMore, itemsState, query, requesterAgentId, workspaceId]);

  const handleReference = useCallback((reference: Omit<KnowledgeCanvasReference, 'workspaceId' | 'agentId'>) => {
    onReference(makeKnowledgeCanvasReference(workspaceId, agentId, reference));
  }, [agentId, onReference, workspaceId]);

  const graphItems = useMemo(() => itemsState.kind === 'ready' ? itemsState.value.items.slice(0, 60) : [], [itemsState]);
  const versionNotice = newVersionNotice && itemsState.kind === 'ready' && itemsState.value.items.some((item) => item.id === newVersionNotice.itemId)
    ? newVersionNotice
    : null;

  return (
    <section className="knowledge-canvas" aria-label={`${agentName} 的知识画布`}>
      <header className="knowledge-canvas-toolbar">
        <div className="knowledge-canvas-toolbar-title">
          <span className="knowledge-canvas-toolbar-icon" aria-hidden="true"><BookOpen className="h-4 w-4" /></span>
          <div className="min-w-0">
            <p className="truncate text-caption text-text-tertiary">当前工作空间 · {workspaceName ?? workspaceId}</p>
            <h2 className="truncate text-body font-semibold text-text-primary">{agentName} 的知识</h2>
            <p className="truncate text-caption text-text-tertiary">{requesterAgentId ? '已发布内容' : '已发布共享内容'} · 只读画布</p>
          </div>
        </div>
        <div className="knowledge-canvas-toolbar-actions">
          <div className="knowledge-canvas-view-switch" role="group" aria-label="知识画布视图">
            <button type="button" className={cx('knowledge-canvas-view-button', view === 'documents' && 'is-active')} aria-pressed={view === 'documents'} onClick={() => setView('documents')}>
              <List className="h-3.5 w-3.5" aria-hidden="true" />文档
            </button>
            <button type="button" className={cx('knowledge-canvas-view-button', view === 'graph' && 'is-active')} aria-pressed={view === 'graph'} onClick={() => setView('graph')}>
              <LayoutGrid className="h-3.5 w-3.5" aria-hidden="true" />全景
            </button>
          </div>
          <button type="button" className="knowledge-canvas-icon-button" aria-label="刷新知识列表" onClick={retryList}>
            <RefreshCw className="h-4 w-4" aria-hidden="true" />
          </button>
          <button type="button" className="knowledge-canvas-icon-button" aria-label={listCollapsed ? '展开文档列表' : '收起文档列表'} aria-expanded={!listCollapsed} onClick={() => setListCollapsed((current) => !current)}>
            {listCollapsed ? <ChevronRight className="h-4 w-4" aria-hidden="true" /> : <ChevronLeft className="h-4 w-4" aria-hidden="true" />}
          </button>
        </div>
      </header>
      <div className={cx('knowledge-canvas-layout', listCollapsed && 'is-list-collapsed')}>
        <aside className="knowledge-canvas-sidebar" aria-label="知识文档列表">
          <div className="knowledge-canvas-search">
            <label htmlFor="knowledge-canvas-search" className="sr-only">搜索知识</label>
            <Search className="knowledge-canvas-search-icon h-4 w-4" aria-hidden="true" />
            <Input id="knowledge-canvas-search" value={queryInput} onChange={(event) => setQueryInput(event.target.value)} placeholder="搜索文档" />
            {queryInput ? <button type="button" className="knowledge-canvas-search-clear" aria-label="清除搜索" onClick={() => setQueryInput('')}><X className="h-3.5 w-3.5" aria-hidden="true" /></button> : null}
          </div>
          <div className="knowledge-canvas-sidebar-heading">
            <span>文档</span>
            {itemsState.kind === 'ready' ? <span className="tabular-nums text-text-tertiary">{itemsState.value.items.length}</span> : null}
          </div>
          <div className="knowledge-canvas-list-scroll">
            {itemsState.kind === 'loading' ? <div className="knowledge-canvas-list-skeleton" role="status" aria-label="知识列表加载中"><Skeleton className="h-14 w-full rounded-card" /><Skeleton className="h-14 w-full rounded-card" /><Skeleton className="h-14 w-full rounded-card" /></div> : null}
            {itemsState.kind === 'error' ? <ListError message={itemsState.message} onRetry={retryList} /> : null}
            {itemsState.kind === 'ready' ? <>
              <DocumentList items={itemsState.value.items} selectedItemId={selectedItemId} query={query} onSelect={selectItem} />
              {loadMoreError ? <p className="knowledge-canvas-load-more-error" role="alert">{loadMoreError}</p> : null}
              {itemsState.value.nextCursor ? <Button type="button" size="sm" className="knowledge-canvas-load-more" onClick={loadMore} disabled={isLoadingMore}>{isLoadingMore ? <LoaderCircle className="h-3.5 w-3.5 animate-spin motion-reduce:animate-none" aria-hidden="true" /> : null}{isLoadingMore ? '正在加载…' : '继续加载'}</Button> : null}
            </> : null}
          </div>
        </aside>
        <main className="knowledge-canvas-main">
          {restoreState.kind === 'loading' ? <div className="knowledge-canvas-version-notice" role="status"><LoaderCircle className="h-4 w-4 shrink-0 animate-spin text-brand-primary motion-reduce:animate-none" aria-hidden="true" /><span>正在恢复上次打开的文档…</span></div> : null}
          {restoreState.kind === 'unavailable' ? <div className="knowledge-canvas-version-notice" role="status"><AlertCircle className="h-4 w-4 shrink-0 text-status-warning" aria-hidden="true" /><span className="min-w-0">{restoreState.message}</span></div> : null}
          {restoreState.kind === 'error' ? <div className="knowledge-canvas-version-notice" role="alert"><AlertCircle className="h-4 w-4 shrink-0 text-status-error" aria-hidden="true" /><span className="min-w-0">{restoreState.message}</span></div> : null}
          {versionNotice ? <div className="knowledge-canvas-version-notice" role="status"><GitBranch className="h-4 w-4 shrink-0 text-status-info" aria-hidden="true" /><span className="min-w-0 flex-1">“{versionNotice.title}”已发布 v{versionNotice.version}，当前仍显示旧版本。</span><Button type="button" size="sm" onClick={() => { const item = itemsState.kind === 'ready' ? itemsState.value.items.find((entry) => entry.id === versionNotice.itemId) : undefined; if (item) selectItem(item); }}>查看新版本</Button></div> : null}
          {view === 'documents' ? <DocumentView detailsState={detailsState} onRetry={retryDetails} onSelectVersion={selectVersion} onReference={handleReference} /> : itemsState.kind === 'ready' && graphItems.length > 0 ? <Suspense fallback={<CanvasLoading label="正在准备全景画布…" />}><KnowledgeGraph items={graphItems} workspaceId={workspaceId} viewportStorageKey={storageKey} requesterAgentId={requesterAgentId} selectedItemId={selectedItemId} onSelectItem={selectItem} /></Suspense> : itemsState.kind === 'loading' ? <CanvasLoading label="正在加载知识全景…" /> : itemsState.kind === 'error' ? <EmptyState className="knowledge-canvas-reader-empty" icon={<AlertCircle className="h-5 w-5 text-status-error" aria-hidden="true" />} title="全景暂不可用" description="知识列表读取失败后，全景无法建立。" /> : <EmptyState className="knowledge-canvas-reader-empty" icon={<GitBranch className="h-5 w-5" aria-hidden="true" />} title="还没有可展示的知识关系" description="已发布文档和真实关联会出现在全景视图。" />}
          {itemsState.kind === 'ready' && itemsState.value.truncated ? <p className="knowledge-canvas-partial-notice" role="status">服务端返回的知识列表不完整，当前画布只展示已读取内容。</p> : null}
        </main>
      </div>
    </section>
  );
}
