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
  getDocument,
  getGraph,
  listDocuments,
  type Assertion,
  type DocumentDetail,
  type DocumentSummary,
  type DocumentVersionMeta,
  type GraphResponse,
  type Relation,
} from '../../api/knowledge-library';
import type { KnowledgeCanvasReference } from '../../utils/agent-knowledge-canvas';
import { formatDateTime } from '../../utils/format';
import { AgentOutput } from '../chat/agent-output';
import { Button, EmptyState, Input, Skeleton, cx } from '../ui';
import './knowledge-canvas.css';

const KnowledgeGraph = lazy(() => import('./knowledge-graph').then(({ KnowledgeGraph: Graph }) => ({ default: Graph })));

export type KnowledgeCanvasView = 'documents' | 'graph';

export type { KnowledgeCanvasReference } from '../../utils/agent-knowledge-canvas';

/**
 * 只读的产品知识画布：文档列表 + 正文阅读 + 版本历史 + 服务端全景。
 * 数据全部来自资料库当前发布的 release，画布不写知识、不做 owner 过滤。
 */
export interface KnowledgeCanvasProps {
  workspaceId: string;
  workspaceName?: string;
  agentId: string;
  agentName: string;
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
  items: DocumentSummary[];
  nextCursor?: string;
  truncated: boolean;
}

type DocumentsResponse = Awaited<ReturnType<typeof listDocuments>>;
type DocumentsLoader = (workspaceId: string, filter: { q?: string; limit?: number; cursor?: string }) => Promise<DocumentsResponse>;
type DocumentLoader = (workspaceId: string, documentId: string, releaseId?: string, version?: number) => Promise<DocumentDetail>;

/**
 * Read one page of the published document list. A repeated cursor is surfaced as
 * a partial response rather than letting the load-more action loop forever.
 */
export async function loadKnowledgeCanvasItems(
  workspaceId: string,
  query?: string,
  cursor?: string,
  load: DocumentsLoader = listDocuments,
): Promise<KnowledgeCanvasItemsResult> {
  const items: DocumentSummary[] = [];
  const response = await load(workspaceId, { q: query?.trim() || undefined, cursor, limit: 40 });
  const seen = new Set<string>();
  (response.items ?? []).forEach((item) => {
    if (seen.has(item.id)) return;
    seen.add(item.id);
    items.push(item);
  });
  const nextCursor = response.next_cursor && response.next_cursor !== cursor ? response.next_cursor : undefined;
  return { items, nextCursor, truncated: Boolean(response.next_cursor && !nextCursor) };
}

export type KnowledgeCanvasRestoreResult =
  | { kind: 'restored'; detail: DocumentDetail }
  | { kind: 'unavailable'; message: string }
  | { kind: 'error'; message: string };

/**
 * Recover the persisted selection with one bounded detail read. The server stays
 * the authority for what the release publishes; this only fences a document that
 * was removed or renamed away between two visits.
 */
export async function restoreKnowledgeCanvasDocument(
  workspaceId: string,
  documentId: string,
  releaseId: string | undefined,
  load: DocumentLoader = getDocument,
): Promise<KnowledgeCanvasRestoreResult> {
  try {
    const detail = await load(workspaceId, documentId, releaseId);
    if (detail.document.status !== 'active') {
      return { kind: 'unavailable', message: '上次打开的文档已不在当前发布的资料库里。' };
    }
    return { kind: 'restored', detail };
  } catch (error: unknown) {
    if (error instanceof ApiError && (error.status === 404 || error.code === 'not_found')) {
      return { kind: 'unavailable', message: '上次打开的文档已不存在或不可访问。' };
    }
    return { kind: 'error', message: `上次打开的文档恢复失败：${errorMessage(error, '请求失败')}。` };
  }
}

export function findKnowledgeCanvasVersionChange(
  previous: ReadonlyMap<string, DocumentSummary>,
  next: DocumentSummary[],
  selectedDocumentId: string | null,
): DocumentSummary | null {
  if (!selectedDocumentId) return null;
  const before = previous.get(selectedDocumentId);
  const after = next.find((document) => document.id === selectedDocumentId);
  return before && after && after.version > before.version ? after : null;
}

export type KnowledgeCanvasSelectionReconciliation =
  | { kind: 'in-page'; result: KnowledgeCanvasItemsResult }
  | { kind: 'restored'; result: KnowledgeCanvasItemsResult; document: DocumentSummary }
  | { kind: 'unavailable'; result: KnowledgeCanvasItemsResult; message: string }
  | { kind: 'error'; result: KnowledgeCanvasItemsResult; message: string };

/** Keep the selected document visible across a bounded refresh without reading the rest of the list. */
export async function reconcileKnowledgeCanvasSelection(
  workspaceId: string,
  query: string | undefined,
  selectedDocumentId: string | null,
  result: KnowledgeCanvasItemsResult,
  load: DocumentLoader = getDocument,
): Promise<KnowledgeCanvasSelectionReconciliation> {
  if (query?.trim() || !selectedDocumentId || result.items.some((document) => document.id === selectedDocumentId)) {
    return { kind: 'in-page', result };
  }
  const restored = await restoreKnowledgeCanvasDocument(workspaceId, selectedDocumentId, undefined, load);
  if (restored.kind === 'restored') {
    const document = restored.detail.document;
    return { kind: 'restored', result: { ...result, items: [document, ...result.items] }, document };
  }
  if (restored.kind === 'unavailable') {
    return { kind: 'unavailable', result, message: `${restored.message} 已回到当前首份文档。` };
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
  documentId: string | null;
}

function readStoredCanvasState(key: string): StoredCanvasState | null {
  if (typeof window === 'undefined') return null;
  try {
    const raw = window.localStorage.getItem(key);
    if (!raw) return null;
    const parsed = JSON.parse(raw) as Partial<StoredCanvasState>;
    return {
      view: parsed.view === 'graph' ? 'graph' : 'documents',
      documentId: typeof parsed.documentId === 'string' ? parsed.documentId : null,
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

/** 文档 kind 的展示名（旧知识页 helper 已随旧模型删除，这里内联替代，未知 kind 如实显示原值）。 */
const DOCUMENT_KIND_LABELS: Record<string, string> = {
  service: '服务知识',
  requirement: '需求知识',
  decision: '决策记录',
  runbook: '运维手册',
  overview: '总览',
};

export function knowledgeDocumentKindLabel(kind: string): string {
  return DOCUMENT_KIND_LABELS[kind] ?? (kind || '文档');
}

const BASIS_LABELS: Record<string, string> = {
  source_statement: '来源陈述',
  code_static: '静态代码',
  runtime_observed: '运行观测',
  inferred: '推断',
};

const PERSPECTIVE_LABELS: Record<string, string> = { normative: '规范', descriptive: '描述' };

const MAX_SUPPORT_ASSERTIONS = 20;

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

function statusLabel(document: DocumentSummary): string {
  return document.version > 0 ? `v${document.version}` : '已发布';
}

interface KnowledgeDocumentBundle {
  detail: DocumentDetail;
  versions: DocumentVersionMeta[];
}

interface KnowledgeTocEntry {
  id: string;
  level: number;
  text: string;
}

/**
 * 资料库正文以 YAML frontmatter 开头（schema_version/id 等真相源字段），管理页原样展示；
 * 画布是面向产品的阅读面，只剥掉**开头**那一块 `---...---`，正文原样渲染。
 * 只有首行是 `---` 且能找到独立成行的结束 `---` 时才剥；否则（例如正文首行就是水平线）不动。
 */
export function stripKnowledgeFrontmatter(markdown: string): string {
  const lines = markdown.split('\n');
  if (lines[0]?.trim() !== '---') return markdown;
  const end = lines.findIndex((line, index) => index > 0 && line.trim() === '---');
  if (end < 0) return markdown;
  let start = end + 1;
  while (start < lines.length && !lines[start].trim()) start += 1;
  return lines.slice(start).join('\n');
}

/** 正文标题锚点：中文标题也要能生成稳定 id（旧 knowledge.page helper 已不存在）。 */
export function knowledgeHeadingId(text: string, index = 0): string {
  const slug = text.trim().toLocaleLowerCase()
    .replace(/[^\w\u3400-\u9fff\u3040-\u30ff-]+/g, '-')
    .replace(/^-+|-+$/g, '')
    .slice(0, 80);
  return `knowledge-heading-${slug || String(index + 1)}`;
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
        <p className="text-body font-medium text-text-primary">文档列表读取失败</p>
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
  documents,
  selectedDocumentId,
  query,
  onSelect,
}: {
  documents: DocumentSummary[];
  selectedDocumentId: string | null;
  query: string;
  onSelect: (document: DocumentSummary) => void;
}) {
  if (documents.length === 0) {
    return (
      <EmptyState
        className="knowledge-canvas-empty"
        icon={<FileText className="h-5 w-5" aria-hidden="true" />}
        title={query ? '没有匹配的已发布文档' : '还没有已发布文档'}
        description={query ? '换一个关键词后再试。' : '资料库发布后，文档会出现在这里。'}
      />
    );
  }

  return (
    <ul className="knowledge-canvas-document-list" aria-label="产品知识文档">
      {documents.map((document) => (
        <li key={document.id}>
          <button
            type="button"
            className={cx('knowledge-canvas-document-row', selectedDocumentId === document.id && 'is-selected')}
            aria-current={selectedDocumentId === document.id ? 'page' : undefined}
            onClick={() => onSelect(document)}
          >
            <span className="knowledge-canvas-document-icon" aria-hidden="true">
              <FileText className="h-4 w-4" />
            </span>
            <span className="min-w-0 flex-1 text-left">
              <span className="knowledge-canvas-document-title">{document.title}</span>
              <span className="knowledge-canvas-document-summary">{document.summary || document.path || '暂无摘要'}</span>
            </span>
            <span className="knowledge-canvas-document-version">{statusLabel(document)}</span>
          </button>
        </li>
      ))}
    </ul>
  );
}

function AssertionSupport({ assertions, relations }: { assertions: Assertion[]; relations: Relation[] }) {
  if (assertions.length === 0 && relations.length === 0) {
    return <p className="knowledge-canvas-support-empty">该版本没有登记断言或关系。</p>;
  }
  const shown = assertions.slice(0, MAX_SUPPORT_ASSERTIONS);
  return (
    <>
      {shown.length > 0 && (
        <ul className="knowledge-canvas-source-list">
          {shown.map((assertion) => (
            <li key={assertion.id}>
              <div className="knowledge-canvas-source-meta">
                <span>{PERSPECTIVE_LABELS[assertion.perspective] ?? assertion.perspective}</span>
                <span className="knowledge-canvas-source-ref">{assertion.heading || '（无标题）'}</span>
              </div>
              <p>{assertion.statement}</p>
              <p className="knowledge-canvas-source-locator">依据：{BASIS_LABELS[assertion.basis] ?? assertion.basis}</p>
            </li>
          ))}
        </ul>
      )}
      {assertions.length > shown.length ? <p className="knowledge-canvas-support-empty">另有 {assertions.length - shown.length} 条断言未展开。</p> : null}
      {relations.length > 0 ? <p className="knowledge-canvas-support-empty">本版本登记了 {relations.length} 条关系，可在「全景」里查看。</p> : null}
    </>
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
  const detailsKey = detailsState.kind === 'ready'
    ? `${detailsState.value.detail.document.id}:${detailsState.value.detail.version.version}`
    : detailsState.kind;

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

  const body = detailsState.kind === 'ready' ? stripKnowledgeFrontmatter(detailsState.value.detail.version.content_markdown) : '';
  const toc = useMemo(() => buildKnowledgeToc(body), [body]);
  const versionHistory = useMemo(() => {
    if (detailsState.kind !== 'ready') return [] as DocumentVersionMeta[];
    const current = detailsState.value.detail.version;
    const listed = detailsState.value.versions;
    const all = listed.some((entry) => entry.version === current.version)
      ? listed
      : [{ id: current.id, version: current.version, content_digest: current.content_digest, created_at: current.created_at }, ...listed];
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

  const { detail } = detailsState.value;
  const { document: doc, version, assertions, relations } = detail;
  const releaseId = detail.release_id ?? doc.release_id ?? '';
  const isHistorical = doc.version !== version.version;

  return (
    <article className="knowledge-canvas-reader" aria-labelledby="knowledge-canvas-document-title">
      <header className="knowledge-canvas-reader-header">
        <div className="knowledge-canvas-reader-meta">
          <span className="knowledge-canvas-kicker">{knowledgeDocumentKindLabel(doc.kind)}</span>
          <span className="knowledge-canvas-version-badge">v{version.version}</span>
          <span className="text-caption text-text-tertiary">{isHistorical ? '历史版本' : '当前版本'} · 更新于 {formatDateTime(version.created_at)}</span>
        </div>
        <h2 id="knowledge-canvas-document-title" className="knowledge-canvas-reader-title">{doc.title}</h2>
        {doc.summary ? <p className="knowledge-canvas-reader-summary">{doc.summary}</p> : null}
        <details className="knowledge-canvas-lineage">
          <summary>查看文档信息</summary>
          <dl>
            <div><dt>文档标识</dt><dd>{doc.id}</dd></div>
            <div><dt>路径</dt><dd>{doc.path || '—'}</dd></div>
            <div><dt>最新版本</dt><dd>v{doc.version}</dd></div>
            <div><dt>发布版本</dt><dd>{releaseId || '当前发布'}</dd></div>
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
              onReference({
                documentId: doc.id,
                version: version.version,
                releaseId,
                title: doc.title,
                quote: pendingQuote.quote,
                heading: pendingQuote.heading,
              });
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
        <AgentOutput text={stripKnowledgeFrontmatter(version.content_markdown)} showCaret={false} />
      </div>
      <div className="knowledge-canvas-reader-support">
        <section className="knowledge-canvas-support-section" aria-labelledby="knowledge-canvas-toc-title">
          <div className="knowledge-canvas-support-heading"><BookOpen className="h-4 w-4 text-brand-primary" aria-hidden="true" /><h3 id="knowledge-canvas-toc-title">正文目录</h3></div>
          {toc.length === 0 ? <p className="knowledge-canvas-support-empty">这份正文没有可导航标题。</p> : <nav aria-label="正文目录">{toc.map((entry) => <button key={entry.id} type="button" className="knowledge-canvas-toc-item" style={{ paddingLeft: `${8 + Math.max(0, entry.level - 1) * 10}px` }} onClick={() => window.document.getElementById(entry.id)?.scrollIntoView({ behavior: 'smooth', block: 'start' })}>{entry.text}</button>)}</nav>}
        </section>
        <section className="knowledge-canvas-support-section" aria-labelledby="knowledge-canvas-history-title">
          <div className="knowledge-canvas-support-heading"><History className="h-4 w-4 text-brand-primary" aria-hidden="true" /><h3 id="knowledge-canvas-history-title">版本历史</h3></div>
          {versionHistory.length === 0 ? <p className="knowledge-canvas-support-empty">没有可见版本记录。</p> : <ol className="knowledge-canvas-version-list">{versionHistory.map((entry) => <li key={entry.id}><button type="button" className={cx('knowledge-canvas-version-item', entry.version === version.version && 'is-selected')} aria-current={entry.version === version.version ? 'page' : undefined} onClick={() => onSelectVersion(entry.version)}><span className="knowledge-canvas-version-dot" aria-hidden="true" /><span className="min-w-0 flex-1"><span className="knowledge-canvas-version-label">v{entry.version}{entry.version === doc.version ? ' · 最新' : ' · 历史'}</span><span className="knowledge-canvas-version-date">{formatDateTime(entry.created_at)}</span></span><ChevronRight className="h-3.5 w-3.5 shrink-0" aria-hidden="true" /></button></li>)}</ol>}
        </section>
        <section className="knowledge-canvas-support-section" aria-labelledby="knowledge-canvas-assertions-title">
          <div className="knowledge-canvas-support-heading"><GitBranch className="h-4 w-4 text-brand-primary" aria-hidden="true" /><h3 id="knowledge-canvas-assertions-title">已登记断言</h3></div>
          <AssertionSupport assertions={assertions ?? []} relations={relations ?? []} />
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
  onReference,
  refreshKey,
}: KnowledgeCanvasProps) {
  const storageKey = useMemo(() => knowledgeCanvasStorageKey(workspaceId, agentId), [agentId, workspaceId]);
  const initialStoredState = useMemo(() => readStoredCanvasState(storageKey), [storageKey]);
  const scopeKey = `${workspaceId}:${agentId}`;
  const [view, setView] = useState<KnowledgeCanvasView>(initialStoredState?.view ?? 'documents');
  const [selectedDocumentId, setSelectedDocumentId] = useState<string | null>(initialStoredState?.documentId ?? null);
  const [listCollapsed, setListCollapsed] = useState(false);
  const [queryInput, setQueryInput] = useState('');
  const [query, setQuery] = useState('');
  const [documentsState, setDocumentsState] = useState<AsyncState<KnowledgeCanvasItemsResult>>({ kind: 'loading' });
  const [detailsState, setDetailsState] = useState<AsyncState<KnowledgeDocumentBundle> | { kind: 'empty' }>({ kind: 'empty' });
  const [graphState, setGraphState] = useState<AsyncState<GraphResponse>>({ kind: 'loading' });
  const [newVersionNotice, setNewVersionNotice] = useState<{ documentId: string; title: string; version: number } | null>(null);
  const [retryNonce, setRetryNonce] = useState(0);
  const [isLoadingMore, setIsLoadingMore] = useState(false);
  const [loadMoreError, setLoadMoreError] = useState<string | null>(null);
  const [restoreState, setRestoreState] = useState<{ kind: 'idle' } | { kind: 'loading' } | { kind: 'unavailable' | 'error'; message: string }>({ kind: 'idle' });
  const listFence = useRef<KnowledgeCanvasRequestFence>(createKnowledgeCanvasRequestFence());
  const detailFence = useRef<KnowledgeCanvasRequestFence>(createKnowledgeCanvasRequestFence());
  const graphFence = useRef<KnowledgeCanvasRequestFence>(createKnowledgeCanvasRequestFence());
  const previousDocuments = useRef<Map<string, DocumentSummary>>(new Map());
  const selectedDocumentIdRef = useRef(selectedDocumentId);
  const previousScope = useRef(scopeKey);
  const previousQuery = useRef(query);

  useEffect(() => {
    selectedDocumentIdRef.current = selectedDocumentId;
  }, [selectedDocumentId]);

  useEffect(() => {
    const stored = readStoredCanvasState(storageKey);
    setView(stored?.view ?? 'documents');
    setSelectedDocumentId(stored?.documentId ?? null);
    selectedDocumentIdRef.current = stored?.documentId ?? null;
    setQueryInput('');
    setQuery('');
    setDocumentsState({ kind: 'loading' });
    setDetailsState({ kind: 'empty' });
    setGraphState({ kind: 'loading' });
    setNewVersionNotice(null);
    setIsLoadingMore(false);
    setLoadMoreError(null);
    setRestoreState({ kind: 'idle' });
    listFence.current.begin();
    detailFence.current.begin();
    graphFence.current.begin();
    previousDocuments.current = new Map();
    previousScope.current = scopeKey;
    previousQuery.current = '';
  }, [scopeKey, storageKey]);

  useEffect(() => () => {
    listFence.current.begin();
    detailFence.current.begin();
    graphFence.current.begin();
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
    const detectVersion = previousScope.current === scopeAtStart && previousQuery.current === queryAtStart && previousDocuments.current.size > 0;
    setDocumentsState({ kind: 'loading' });
    setLoadMoreError(null);
    void loadKnowledgeCanvasItems(workspaceId, queryAtStart)
      .then(async (result) => {
        if (!fence.isCurrent(token)) return;
        const selectedId = selectedDocumentIdRef.current;
        const needsReconciliation = !queryAtStart && Boolean(selectedId) && !result.items.some((document) => document.id === selectedId);
        if (needsReconciliation) setRestoreState({ kind: 'loading' });
        const reconciliation = await reconcileKnowledgeCanvasSelection(workspaceId, queryAtStart, selectedId, result);
        if (!fence.isCurrent(token)) return;
        const reconciled = reconciliation.result;
        const nextMap = new Map(reconciled.items.map((document) => [document.id, document]));
        if (detectVersion) {
          const changed = findKnowledgeCanvasVersionChange(previousDocuments.current, reconciled.items, selectedId);
          if (changed) setNewVersionNotice({ documentId: changed.id, title: changed.title, version: changed.version });
        }
        if (reconciliation.kind !== 'error') previousDocuments.current = nextMap;
        previousScope.current = scopeAtStart;
        previousQuery.current = queryAtStart;
        setDocumentsState({ kind: 'ready', value: reconciled });
        if (reconciliation.kind === 'unavailable') {
          setRestoreState({ kind: 'unavailable', message: reconciliation.message });
          setSelectedDocumentId(null);
          selectedDocumentIdRef.current = null;
          setDetailsState({ kind: 'empty' });
        } else if (reconciliation.kind === 'error') {
          setRestoreState({ kind: 'error', message: reconciliation.message });
        } else {
          setRestoreState({ kind: 'idle' });
        }
        if (queryAtStart && selectedId && !nextMap.has(selectedId)) {
          setSelectedDocumentId(null);
          selectedDocumentIdRef.current = null;
          setDetailsState({ kind: 'empty' });
        }
      })
      .catch((error: unknown) => {
        if (!fence.isCurrent(token)) return;
        setDocumentsState({ kind: 'error', message: errorMessage(error, '请检查网络或稍后重试。') });
      });
    return () => { fence.begin(); };
  }, [query, refreshKey, retryNonce, scopeKey, workspaceId]);

  useEffect(() => {
    if (view !== 'graph') return;
    const fence = graphFence.current;
    const token = fence.begin();
    setGraphState({ kind: 'loading' });
    void getGraph(workspaceId)
      .then((graph) => {
        if (!fence.isCurrent(token)) return;
        setGraphState({ kind: 'ready', value: graph });
      })
      .catch((error: unknown) => {
        if (!fence.isCurrent(token)) return;
        setGraphState({ kind: 'error', message: errorMessage(error, '请检查网络或稍后重试。') });
      });
    return () => { fence.begin(); };
  }, [refreshKey, retryNonce, view, workspaceId]);

  useEffect(() => {
    writeStoredCanvasState(storageKey, { view, documentId: selectedDocumentId });
  }, [selectedDocumentId, storageKey, view]);

  const loadDocument = useCallback((documentId: string, versionNumber?: number) => {
    const token = detailFence.current.begin();
    setDetailsState({ kind: 'loading' });
    setNewVersionNotice(null);
    void getDocument(workspaceId, documentId, undefined, versionNumber)
      .then((detail) => {
        if (!detailFence.current.isCurrent(token)) return;
        setDetailsState({ kind: 'ready', value: { detail, versions: detail.versions ?? [] } });
      })
      .catch((error: unknown) => {
        if (!detailFence.current.isCurrent(token)) return;
        setDetailsState({ kind: 'error', message: errorMessage(error, '请检查网络或稍后重试。') });
      });
  }, [workspaceId]);

  const selectDocument = useCallback((document: DocumentSummary | null, options: { openDocument?: boolean; clearRestoreNotice?: boolean } = {}) => {
    if (!document) {
      setSelectedDocumentId(null);
      setDetailsState({ kind: 'empty' });
      setNewVersionNotice(null);
      return;
    }
    if (options.clearRestoreNotice !== false) setRestoreState({ kind: 'idle' });
    if (options.openDocument !== false) setView('documents');
    setSelectedDocumentId(document.id);
    setNewVersionNotice(null);
    loadDocument(document.id);
  }, [loadDocument]);

  const selectVersion = useCallback((versionNumber: number) => {
    if (detailsState.kind !== 'ready') return;
    loadDocument(detailsState.value.detail.document.id, versionNumber);
  }, [detailsState, loadDocument]);

  useEffect(() => {
    if (documentsState.kind !== 'ready' || detailsState.kind !== 'empty') return;
    const document = selectedDocumentIdRef.current
      ? documentsState.value.items.find((entry) => entry.id === selectedDocumentIdRef.current)
      : documentsState.value.items[0];
    if (document) selectDocument(document, { openDocument: false, clearRestoreNotice: restoreState.kind !== 'unavailable' });
  }, [detailsState.kind, documentsState, restoreState.kind, selectDocument, selectedDocumentId]);

  const retryList = useCallback(() => {
    setDocumentsState({ kind: 'loading' });
    setRetryNonce((current) => current + 1);
  }, []);

  const retryDetails = useCallback(() => {
    const document = documentsState.kind === 'ready' && selectedDocumentId
      ? documentsState.value.items.find((entry) => entry.id === selectedDocumentId)
      : undefined;
    if (document) selectDocument(document);
  }, [documentsState, selectDocument, selectedDocumentId]);

  const loadMore = useCallback(() => {
    if (documentsState.kind !== 'ready' || !documentsState.value.nextCursor || isLoadingMore) return;
    const token = listFence.current.begin();
    const cursor = documentsState.value.nextCursor;
    setIsLoadingMore(true);
    setLoadMoreError(null);
    void loadKnowledgeCanvasItems(workspaceId, query, cursor)
      .then((result) => {
        if (!listFence.current.isCurrent(token)) return;
        setDocumentsState((current) => {
          if (current.kind !== 'ready') return current;
          const seen = new Set(current.value.items.map((entry) => entry.id));
          const merged = [...current.value.items, ...result.items.filter((entry) => !seen.has(entry.id))];
          previousDocuments.current = new Map(merged.map((entry) => [entry.id, entry]));
          return { kind: 'ready', value: { items: merged, nextCursor: result.nextCursor, truncated: current.value.truncated || result.truncated } };
        });
      })
      .catch((error: unknown) => {
        if (listFence.current.isCurrent(token)) setLoadMoreError(errorMessage(error, '继续加载失败，请重试。'));
      })
      .finally(() => {
        if (listFence.current.isCurrent(token)) setIsLoadingMore(false);
      });
  }, [documentsState, isLoadingMore, query, workspaceId]);

  const handleReference = useCallback((reference: Omit<KnowledgeCanvasReference, 'workspaceId' | 'agentId'>) => {
    onReference(makeKnowledgeCanvasReference(workspaceId, agentId, reference));
  }, [agentId, onReference, workspaceId]);

  const openDocumentById = useCallback((documentId: string) => {
    setView('documents');
    setSelectedDocumentId(documentId);
    loadDocument(documentId);
  }, [loadDocument]);

  const versionNotice = newVersionNotice && documentsState.kind === 'ready' && documentsState.value.items.some((document) => document.id === newVersionNotice.documentId)
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
            <p className="truncate text-caption text-text-tertiary">已发布内容 · 只读画布</p>
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
          <button type="button" className="knowledge-canvas-icon-button" aria-label="刷新文档列表" onClick={retryList}>
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
            {documentsState.kind === 'ready' ? <span className="tabular-nums text-text-tertiary">{documentsState.value.items.length}</span> : null}
          </div>
          <div className="knowledge-canvas-list-scroll">
            {documentsState.kind === 'loading' ? <div className="knowledge-canvas-list-skeleton" role="status" aria-label="文档列表加载中"><Skeleton className="h-14 w-full rounded-card" /><Skeleton className="h-14 w-full rounded-card" /><Skeleton className="h-14 w-full rounded-card" /></div> : null}
            {documentsState.kind === 'error' ? <ListError message={documentsState.message} onRetry={retryList} /> : null}
            {documentsState.kind === 'ready' ? <>
              <DocumentList documents={documentsState.value.items} selectedDocumentId={selectedDocumentId} query={query} onSelect={selectDocument} />
              {loadMoreError ? <p className="knowledge-canvas-load-more-error" role="alert">{loadMoreError}</p> : null}
              {documentsState.value.nextCursor ? <Button type="button" size="sm" className="knowledge-canvas-load-more" onClick={loadMore} disabled={isLoadingMore}>{isLoadingMore ? <LoaderCircle className="h-3.5 w-3.5 animate-spin motion-reduce:animate-none" aria-hidden="true" /> : null}{isLoadingMore ? '正在加载…' : '继续加载'}</Button> : null}
            </> : null}
          </div>
        </aside>
        <main className="knowledge-canvas-main">
          {restoreState.kind === 'loading' ? <div className="knowledge-canvas-version-notice" role="status"><LoaderCircle className="h-4 w-4 shrink-0 animate-spin text-brand-primary motion-reduce:animate-none" aria-hidden="true" /><span>正在恢复上次打开的文档…</span></div> : null}
          {restoreState.kind === 'unavailable' ? <div className="knowledge-canvas-version-notice" role="status"><AlertCircle className="h-4 w-4 shrink-0 text-status-warning" aria-hidden="true" /><span className="min-w-0">{restoreState.message}</span></div> : null}
          {restoreState.kind === 'error' ? <div className="knowledge-canvas-version-notice" role="alert"><AlertCircle className="h-4 w-4 shrink-0 text-status-error" aria-hidden="true" /><span className="min-w-0">{restoreState.message}</span></div> : null}
          {versionNotice ? <div className="knowledge-canvas-version-notice" role="status"><GitBranch className="h-4 w-4 shrink-0 text-status-info" aria-hidden="true" /><span className="min-w-0 flex-1">“{versionNotice.title}”已发布 v{versionNotice.version}，当前仍显示旧版本。</span><Button type="button" size="sm" onClick={() => { const document = documentsState.kind === 'ready' ? documentsState.value.items.find((entry) => entry.id === versionNotice.documentId) : undefined; if (document) selectDocument(document); }}>查看新版本</Button></div> : null}
          {view === 'documents'
            ? <DocumentView detailsState={detailsState} onRetry={retryDetails} onSelectVersion={selectVersion} onReference={handleReference} />
            : <Suspense fallback={<CanvasLoading label="正在准备全景画布…" />}>
                <KnowledgeGraph
                  graph={graphState.kind === 'ready' ? graphState.value : null}
                  loading={graphState.kind === 'loading'}
                  error={graphState.kind === 'error' ? graphState.message : null}
                  onRetry={() => setRetryNonce((current) => current + 1)}
                  selectedDocumentId={selectedDocumentId}
                  onOpenDocument={openDocumentById}
                  viewportStorageKey={storageKey}
                />
              </Suspense>}
          {documentsState.kind === 'ready' && documentsState.value.truncated ? <p className="knowledge-canvas-partial-notice" role="status">服务端返回的文档列表不完整，当前画布只展示已读取内容。</p> : null}
        </main>
      </div>
    </section>
  );
}
