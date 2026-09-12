import {
  AlertCircle,
  ArrowLeftRight,
  BookOpen,
  Check,
  ChevronRight,
  Circle,
  CircleAlert,
  Clock3,
  Copy,
  Database,
  FileText,
  Inbox,
  Info,
  Link2,
  ListChecks,
  Package,
  Play,
  Plus,
  RefreshCw,
  RotateCcw,
  Search,
  Server,
  ShieldCheck,
  Trash2,
  XOctagon,
} from 'lucide-react';
import { useCallback, useEffect, useMemo, useState, type ReactNode } from 'react';
import { useSearchParams } from 'react-router-dom';
import { ApiError, newIdempotencyKey } from '../api/client';
import {
  artifactResolutionOf,
  cancelTask,
  createSource,
  deleteSource,
  expandHandle,
  getDocument,
  getEvidence,
  getStatus,
  getTask,
  initializeLibrary,
  listDocuments,
  listEvents,
  listReleases,
  listSources,
  listTasks,
  queryLibrary,
  reindex,
  retryTask,
  submitEvent,
  updateSource,
  type ArtifactResolution,
  type Assertion,
  type Coverage,
  type CreateSourceInput,
  type DocumentDetail,
  type DocumentSummary,
  type Evidence,
  type EvidenceRef,
  type LibraryEvent,
  type LibraryReceipt,
  type LibraryStatus,
  type LibrarySummary,
  type Paged,
  type QueryResponse,
  type Relation,
  type Release,
  type Source,
  type SourceKind,
  type SourceUsage,
  type TaskDetail,
  type TaskSummary,
  type UpdateSourceInput,
} from '../api/knowledge-library';
import { Drawer } from '../components/drawer';
import { Button, Card, EmptyState, Field, FieldError, Input, Select, Skeleton, StatusPill, Textarea, cx } from '../components/ui';
import { captureScope, isCurrent } from '../stores/scope';
import { toast } from '../stores/toast.store';
import { useWorkspaceStore } from '../stores/workspace.store';
import { formatDateTime } from '../utils/format';

/**
 * 资料库（知识管理员）管理页。
 *
 * 语义：一个工作区只有**一个**资料库，收录全部服务仓库与 common；跨服务关系是库内关系。
 * 写入是异步的：页面提交事件只拿到「已受理」回执，真正的更新由后台队列完成，
 * 因此队列、轮次与诊断放在「版本与队列」，业务调用方不需要等待。
 */

// ---------------------------------------------------------------------------
// 状态与文案模型
// ---------------------------------------------------------------------------

export type Async<T> = { kind: 'loading' } | { kind: 'ready'; value: T } | { kind: 'error'; message: string };

export type KnowledgeTab = 'library' | 'sync' | 'browse' | 'query' | 'releases';

export const KNOWLEDGE_TABS: { id: KnowledgeTab; label: string }[] = [
  { id: 'library', label: '资料库与来源' },
  { id: 'sync', label: '初始化与更新' },
  { id: 'browse', label: '知识浏览' },
  { id: 'query', label: '查询与展开' },
  { id: 'releases', label: '版本与队列' },
];

export const KNOWLEDGE_EVENT_TYPES = [
  { value: 'workspace.connected', label: '工作区接入（workspace.connected）' },
  { value: 'code.pulled', label: '代码更新（code.pulled）' },
  { value: 'requirement.imported', label: '需求导入（requirement.imported）' },
  { value: 'document.revised', label: '文档修订（document.revised）' },
  { value: 'code.changed', label: '代码变更（code.changed）' },
];

export const SOURCE_KIND_OPTIONS: { value: SourceKind; label: string }[] = [
  { value: 'service', label: '服务仓库（service）' },
  { value: 'common', label: '公共仓库（common）' },
  { value: 'documents', label: '文档仓库（documents）' },
  // 需求输入源由系统在受理 requirement.imported 时登记，不在表单里手工创建。
  { value: 'requirement', label: '需求输入（requirement，系统登记）' },
  { value: 'other', label: '其他（other）' },
];

/**
 * 制品版本的来路：登记的表单只能产生 `declared`（人工声明值），
 * 构建解析结果由真实构建写入，两者在界面上必须能分辨。
 */
export const ARTIFACT_RESOLUTION_OPTIONS: { value: ArtifactResolution; label: string; hint: string }[] = [
  {
    value: 'declared',
    label: '登记声明值',
    hint: '登记声明值：人工填写，未经过构建/依赖解析核实',
  },
  {
    value: 'resolved',
    label: '声明为构建解析值（本版本无法核实）',
    hint: '本版本没有依赖解析能力：即使填写引用，资料库也只能按「登记声明值」保存，引用只作备注',
  },
  {
    value: 'unknown',
    label: '解析未知',
    hint: '解析未知：没有已知的依赖版本映射',
  },
];

/**
 * 有效状态说明。本版本没有依赖解析能力，所以任何「已解析」声明都只能落成
 * 登记声明值；引用不论是否填写都只是备注，不构成核实。
 */
export function effectiveResolutionLabel(resolution: ArtifactResolution, resolutionRef: string): string {
  if (resolution === 'resolved') {
    return resolutionRef.trim() === ''
      ? '实际保存为：登记声明值（本版本无法核实，且未填引用）'
      : '实际保存为：登记声明值（本版本无法核实；引用仅作备注）';
  }
  return artifactResolutionHint(resolution);
}

export function artifactResolutionLabel(value: ArtifactResolution): string {
  return ARTIFACT_RESOLUTION_OPTIONS.find((option) => option.value === value)?.label ?? value;
}

export function artifactResolutionHint(value: ArtifactResolution): string {
  return ARTIFACT_RESOLUTION_OPTIONS.find((option) => option.value === value)?.hint ?? '';
}

/** 没有声明使用方时的诚实说法：后端把空 usages 当作一个隐式使用方。 */
export const SINGLE_IMPLICIT_USAGE_LABEL = '单一使用方（未声明依赖版本）';

/**
 * 一条使用方的单行摘要：`order-service · com.example:common:1.4.2（登记声明值）`。
 * 没填制品时只说“未声明依赖版本”，不给一个空的解析标注。
 */
export function usageLine(usage: SourceUsage): string {
  const consumer = usage.consumer?.trim() || '未声明使用方';
  const artifact = usage.artifact?.trim() ?? '';
  if (!artifact) return `${consumer}（未声明依赖版本）`;
  return `${consumer} · ${artifact}（${artifactResolutionLabel(artifactResolutionOf(usage))}）`;
}

export function parseKnowledgeTab(raw: string | null): KnowledgeTab {
  const hit = KNOWLEDGE_TABS.find((candidate) => candidate.id === raw);
  return hit ? hit.id : 'library';
}

type Tone = 'success' | 'warning' | 'info' | 'error' | 'neutral';

const TONE_CLASSES: Record<Tone, string> = {
  success: 'border-status-success/30 bg-status-success/10 text-status-success',
  warning: 'border-status-warning/35 bg-status-warning/10 text-status-warning',
  info: 'border-status-info/30 bg-status-info/10 text-status-info',
  error: 'border-status-error/35 bg-status-error/10 text-status-error',
  neutral: 'border-border-subtle bg-surface-sunken text-text-secondary',
};

const TONE_ICONS = {
  success: Check,
  warning: AlertCircle,
  info: Info,
  error: XOctagon,
  neutral: Circle,
} as const;

const EVENT_STATUS: Record<LibraryEvent['status'], { label: string; tone: Tone }> = {
  accepted: { label: '已受理', tone: 'info' },
  queued: { label: '已排队', tone: 'info' },
  processing: { label: '处理中', tone: 'warning' },
  completed: { label: '已完成', tone: 'success' },
  failed: { label: '失败', tone: 'error' },
  blocked: { label: '已阻塞', tone: 'error' },
};

const TASK_STATUS: Record<string, { label: string; tone: Tone }> = {
  queued: { label: '排队中', tone: 'info' },
  running: { label: '执行中', tone: 'warning' },
  succeeded: { label: '已完成', tone: 'success' },
  failed: { label: '失败', tone: 'error' },
  blocked: { label: '已阻塞', tone: 'error' },
  cancelled: { label: '已取消', tone: 'neutral' },
  retry_wait: { label: '等待重试', tone: 'warning' },
  awaiting_agent: { label: '等待资料员', tone: 'warning' },
};

const RELEASE_STATUS: Record<Release['status'], { label: string; tone: Tone }> = {
  published: { label: '当前发布', tone: 'success' },
  superseded: { label: '已被取代', tone: 'neutral' },
};

const PERSPECTIVE_LABELS: Record<Assertion['perspective'], string> = {
  normative: '规范性',
  descriptive: '描述性',
};

const BASIS_LABELS: Record<Assertion['basis'], string> = {
  source_statement: '来源陈述',
  code_static: '代码静态事实',
  runtime_observed: '运行时观测',
  inferred: '推断',
};

const COVERAGE_LABELS: Record<QueryResponse['coverage']['status'], { label: string; tone: Tone }> = {
  complete: { label: '覆盖完整', tone: 'success' },
  partial: { label: '部分覆盖', tone: 'warning' },
  not_ready: { label: '尚不可用', tone: 'neutral' },
};

const EVIDENCE_ROLE_LABELS: Record<EvidenceRef['role'], string> = {
  supports: '支持',
  contradicts: '反证',
  context: '上下文',
};

export function eventStatusLabel(status: string): string {
  return EVENT_STATUS[status as LibraryEvent['status']]?.label ?? status;
}

export function eventStatusTone(status: string): Tone {
  return EVENT_STATUS[status as LibraryEvent['status']]?.tone ?? 'neutral';
}

export function taskStatusLabel(status: string): string {
  return TASK_STATUS[status]?.label ?? status;
}

export function taskStatusTone(status: string): Tone {
  return TASK_STATUS[status]?.tone ?? 'neutral';
}

export function releaseStatusLabel(status: string): string {
  return RELEASE_STATUS[status as Release['status']]?.label ?? status;
}

export function releaseStatusTone(status: string): Tone {
  return RELEASE_STATUS[status as Release['status']]?.tone ?? 'neutral';
}

export function perspectiveLabel(perspective: string): string {
  return PERSPECTIVE_LABELS[perspective as Assertion['perspective']] ?? perspective;
}

export function basisLabel(basis: string): string {
  return BASIS_LABELS[basis as Assertion['basis']] ?? basis;
}

export function evidenceRoleLabel(role: string): string {
  return EVIDENCE_ROLE_LABELS[role as EvidenceRef['role']] ?? role;
}

export function scopeText(scope: Assertion['scope'] | undefined): string {
  if (!scope) return '未限定适用范围';
  const parts = [
    ...(scope.conditions ?? []).map((condition) => `条件：${condition}`),
    ...(scope.environments ?? []).map((environment) => `环境：${environment}`),
    ...(scope.valid_from ? [`生效：${scope.valid_from}`] : []),
    ...(scope.valid_until ? [`失效：${scope.valid_until}`] : []),
  ];
  return parts.length > 0 ? parts.join(' · ') : '未限定适用范围';
}

/**
 * 覆盖状态：只有资料库真正报告过覆盖时才能说“无缺口”。
 * 空覆盖对象 = 尚未统计，不等于已经验证没有缺口。
 */
export type CoverageState = 'pending' | 'in_progress' | 'not_computed' | 'reported';

/**
 * 取消只对「还在队列里」的任务成立：queued / retry_wait / blocked。
 * 模型轮次进行中（running / awaiting_agent）与已结束状态都无法取消，
 * 界面必须禁用而不是提供一个点了必失败的操作。
 */
export function taskCancelState(status: string): { cancellable: boolean; hint: string } {
  switch (status) {
    case 'queued':
      return { cancellable: true, hint: '取消这个排队任务' };
    case 'retry_wait':
      return { cancellable: true, hint: '取消这个等待重试的任务' };
    case 'blocked':
      return { cancellable: true, hint: '取消这个已阻塞的任务' };
    case 'running':
    case 'awaiting_agent':
      return { cancellable: false, hint: '模型轮次正在进行，无法取消；请等待它到达终态' };
    case 'completed':
      return { cancellable: false, hint: '任务已发布，如需变更请提交一次新的知识变更' };
    default:
      return { cancellable: false, hint: '任务已结束，无法取消' };
  }
}

export function coverageLine(coverage: Coverage | undefined, state: CoverageState = 'reported'): string {
  if (state === 'pending') return '尚未收到覆盖报告（覆盖未知）';
  if (state === 'in_progress') return '统计中（任务仍在处理，覆盖范围未定）';
  if (state === 'not_computed' || !coverage) return '覆盖未知（资料库没有记录本轮读了哪些来源）';
  const read = coverage.sources_read?.length ?? 0;
  const missed = coverage.sources_missed?.length ?? 0;
  const gaps = coverage.gaps?.length ?? 0;
  const notes = coverage.notes?.trim();
  if (read === 0 && missed === 0 && gaps === 0 && !notes) {
    return '覆盖未知（资料库没有记录本轮读了哪些来源）';
  }
  const parts = [`已读来源 ${read}`, missed > 0 ? `未读来源 ${missed}` : gaps > 0 ? `缺口 ${gaps}` : '已报告无缺口'];
  if (notes) parts.push(notes);
  return parts.join(' · ');
}

export function coverageGaps(coverage: Coverage | undefined): string[] {
  if (!coverage) return [];
  return [...(coverage.sources_missed ?? []), ...(coverage.gaps ?? [])].filter((item) => item.trim() !== '');
}

export function sourceKindLabel(kind: string): string {
  return SOURCE_KIND_OPTIONS.find((option) => option.value === kind)?.label ?? kind;
}

export function formatBytes(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes < 0) return String(bytes);
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}

export function shortDigest(digest: string, length = 12): string {
  const value = digest?.trim() ?? '';
  if (value.length <= length) return value;
  return value.slice(0, length);
}

export function formatLocator(locator: Record<string, unknown> | undefined): string {
  if (!locator) return '无定位信息';
  const entries = Object.entries(locator).filter(([, value]) => value !== undefined && value !== null && value !== '');
  if (entries.length === 0) return '无定位信息';
  return entries.map(([key, value]) => `${key}=${typeof value === 'string' ? value : JSON.stringify(value)}`).join(' · ');
}

export function evidenceChipLabel(ref: EvidenceRef): string {
  return `查看证据 ${ref.evidence_id}（${evidenceRoleLabel(ref.role)}）`;
}

function apiErrorMessage(error: unknown, fallback: string): string {
  if (error instanceof ApiError) return error.message || fallback;
  if (error instanceof Error) return error.message || fallback;
  return fallback;
}

/** 读取资源：key 变化即重新取数；Workspace 切换后旧响应一律丢弃。 */
function useAsyncData<T>(key: string | null, load: (() => Promise<T>) | null): Async<T> {
  const [state, setState] = useState<Async<T>>({ kind: 'loading' });
  useEffect(() => {
    if (!key || !load) return;
    const scope = captureScope();
    let cancelled = false;
    setState({ kind: 'loading' });
    load()
      .then((value) => {
        if (!cancelled && isCurrent(scope)) setState({ kind: 'ready', value });
      })
      .catch((error: unknown) => {
        if (!cancelled && isCurrent(scope)) setState({ kind: 'error', message: apiErrorMessage(error, '读取失败') });
      });
    return () => {
      cancelled = true;
    };
  }, [key, load]);
  return state;
}

async function copyText(text: string): Promise<boolean> {
  try {
    await navigator.clipboard?.writeText(text);
    return true;
  } catch {
    return false;
  }
}

// ---------------------------------------------------------------------------
// 通用展示件
// ---------------------------------------------------------------------------

export function StateBadge({ tone, children, className }: { tone: Tone; children: ReactNode; className?: string }) {
  const Icon = TONE_ICONS[tone];
  return (
    <span
      className={cx(
        'inline-flex items-center gap-micro rounded-full border px-tight py-micro text-caption',
        TONE_CLASSES[tone],
        className,
      )}
    >
      <Icon className="h-3 w-3 shrink-0" aria-hidden />
      {children}
    </span>
  );
}

export function InlineError({ message, onRetry }: { message: string; onRetry?: () => void }) {
  return (
    <div
      role="alert"
      className="flex flex-wrap items-center justify-between gap-tight rounded-card border border-status-error/30 bg-status-error/5 px-snug py-tight"
    >
      <span className="flex min-w-0 items-center gap-tight text-body text-status-error">
        <AlertCircle className="h-4 w-4 shrink-0" aria-hidden />
        {message}
      </span>
      {onRetry ? (
        <Button size="sm" onClick={onRetry}>
          重试
        </Button>
      ) : null}
    </div>
  );
}

export function PanelSection({
  title,
  hint,
  actions,
  children,
  className,
}: {
  title: string;
  hint?: string;
  actions?: ReactNode;
  children: ReactNode;
  className?: string;
}) {
  return (
    <section className={cx('workbench-panel rounded-card p-comfortable', className)}>
      <header className="flex flex-wrap items-end justify-between gap-tight">
        <div className="min-w-0">
          <h3 className="text-h3 text-text-primary">{title}</h3>
          {hint ? <p className="mt-micro text-caption text-text-tertiary">{hint}</p> : null}
        </div>
        {actions}
      </header>
      <div className="mt-snug">{children}</div>
    </section>
  );
}

export function PanelLoading({ label, rows = 5 }: { label: string; rows?: number }) {
  return (
    <div className="space-y-tight" role="status" aria-label={label}>
      {Array.from({ length: rows }, (_, index) => (
        <Skeleton key={index} className="h-12 w-full rounded-card" />
      ))}
    </div>
  );
}

function DataTable({ caption, children }: { caption: string; children: ReactNode }) {
  return (
    <div className="overflow-x-auto">
      <table className="w-full min-w-[42rem] border-collapse text-body">
        <caption className="sr-only">{caption}</caption>
        {children}
      </table>
    </div>
  );
}

function Th({ children, className }: { children: ReactNode; className?: string }) {
  return (
    <th
      scope="col"
      className={cx(
        'border-b border-border-subtle px-tight py-tight text-left text-caption font-medium text-text-tertiary',
        className,
      )}
    >
      {children}
    </th>
  );
}

function Td({ children, className, mono }: { children: ReactNode; className?: string; mono?: boolean }) {
  return (
    <td
      className={cx(
        'border-b border-border-subtle px-tight py-tight align-top text-text-secondary',
        mono && 'font-mono text-caption',
        className,
      )}
    >
      {children}
    </td>
  );
}

function MetaItem({ label, value, mono }: { label: string; value: ReactNode; mono?: boolean }) {
  return (
    <div className="min-w-0">
      <dt className="text-caption text-text-tertiary">{label}</dt>
      <dd className={cx('truncate text-body text-text-primary', mono && 'font-mono text-caption')}>{value}</dd>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Tab 1 · 资料库与来源
// ---------------------------------------------------------------------------

function LibraryFactsCard({ summary, onRefresh }: { summary: LibrarySummary; onRefresh: () => void }) {
  const [copied, setCopied] = useState(false);
  const copyRoot = async () => {
    const ok = await copyText(summary.root_path);
    setCopied(ok);
    if (ok) window.setTimeout(() => setCopied(false), 2000);
  };
  return (
    <Card padded>
      <div className="flex flex-wrap items-start justify-between gap-tight">
        <div className="min-w-0">
          <p className="text-caption uppercase tracking-widest text-text-tertiary">库根路径</p>
          <p className="mt-micro break-all font-mono text-body text-text-primary">{summary.root_path}</p>
        </div>
        <div className="flex shrink-0 items-center gap-tight">
          <StateBadge tone={summary.enabled ? 'success' : 'neutral'}>
            {summary.enabled ? '已启用' : '未启用'}
          </StateBadge>
          <Button size="sm" onClick={() => void copyRoot()}>
            {copied ? <Check className="h-3.5 w-3.5" aria-hidden /> : <Copy className="h-3.5 w-3.5" aria-hidden />}
            {copied ? '已复制' : '复制路径'}
          </Button>
        </div>
      </div>
      <dl className="mt-base grid grid-cols-2 gap-snug md:grid-cols-4">
        <MetaItem label="发布数" value={summary.release_count} mono />
        <MetaItem label="文档数" value={summary.document_count} mono />
        <MetaItem label="来源数" value={summary.source_count} mono />
        <MetaItem label="索引修订" value={summary.index_revision} mono />
        <MetaItem label="待处理事件" value={summary.queue.pending} mono />
        <MetaItem label="阻塞任务" value={summary.queue.blocked} mono />
        <MetaItem label="队头序号" value={summary.queue.head_seq ?? '—'} mono />
        <MetaItem label="更新时间" value={formatDateTime(summary.updated_at)} />
      </dl>
      <div className="mt-base flex flex-wrap items-center justify-between gap-tight rounded-card border border-border-subtle bg-surface-sunken/45 px-snug py-tight">
        <p className="flex min-w-0 items-center gap-tight text-caption text-text-secondary">
          <Info className="h-3.5 w-3.5 shrink-0 text-brand-primary" aria-hidden />
          一个资料库收录本工作区全部服务仓库与 common；跨服务关系是库内关系，不需要按服务拆库。
        </p>
        <Button size="sm" onClick={onRefresh}>
          <RefreshCw className="h-3.5 w-3.5" aria-hidden />
          刷新
        </Button>
      </div>
    </Card>
  );
}

export function CurrentReleaseCard({ release }: { release: Release | null }) {
  if (!release) {
    return (
      <Card padded>
        <h3 className="text-h3 text-text-primary">当前发布</h3>
        <p className="mt-tight text-body text-text-secondary">
          还没有发布版本。先提交一次初始化，等待后台队列完成第一次发布。
        </p>
      </Card>
    );
  }
  return (
    <Card padded>
      <div className="flex flex-wrap items-center justify-between gap-tight">
        <h3 className="text-h3 text-text-primary">当前发布</h3>
        <div className="flex items-center gap-tight">
          <StateBadge tone={releaseStatusTone(release.status)}>{releaseStatusLabel(release.status)}</StateBadge>
          <span className="font-mono text-caption text-text-tertiary">
            #{release.seq} · {release.id}
          </span>
        </div>
      </div>
      <dl className="mt-snug grid grid-cols-2 gap-snug md:grid-cols-4">
        <MetaItem label="文档（该发布包含）" value={release.document_count} mono />
        <MetaItem label="断言（该发布包含）" value={release.assertion_count} mono />
        <MetaItem label="关系（该发布包含）" value={release.relation_count} mono />
        <MetaItem label="证据（该发布包含）" value={release.evidence_count} mono />
      </dl>
      <p className="mt-micro text-caption text-text-tertiary">
        本次发布写入：文档 {release.written_document_count} · 断言 {release.written_assertion_count} · 关系{' '}
        {release.written_relation_count}；其余为上一版本沿用（carry-forward）。
      </p>
      <p className="mt-snug text-caption text-text-secondary">
        覆盖：{coverageLine(release.coverage, (release.coverage_state ?? 'reported') as CoverageState)}
      </p>
      <p className="mt-micro text-caption text-text-tertiary">
        发布时间 {formatDateTime(release.published_at)} · 快照{' '}
        <span className="font-mono">{shortDigest(release.snapshot_id, 16)}</span> · 投影摘要{' '}
        <span className="font-mono">{shortDigest(release.projection_digest, 16)}</span>
      </p>
      {release.notes ? <p className="mt-micro text-caption text-text-secondary">备注：{release.notes}</p> : null}
    </Card>
  );
}

export interface SourceUsageDraft {
  consumer: string;
  artifact: string;
  environment: string;
  artifact_resolution: ArtifactResolution;
  /** resolved 的可核查依据；为空时服务端降级为 declared。 */
  resolution_ref: string;
}

export interface SourceDraft {
  name: string;
  kind: SourceKind;
  repo_path: string;
  default_ref: string;
  usages: SourceUsageDraft[];
}

export function emptySourceDraft(): SourceDraft {
  return { name: '', kind: 'service', repo_path: '', default_ref: '', usages: [] };
}

export function emptyUsageDraft(): SourceUsageDraft {
  return { consumer: '', artifact: '', environment: '', artifact_resolution: 'declared', resolution_ref: '' };
}

export function usageDraftFrom(usage: SourceUsage): SourceUsageDraft {
  return {
    consumer: usage.consumer ?? '',
    artifact: usage.artifact ?? '',
    environment: usage.environment ?? '',
    artifact_resolution: artifactResolutionOf(usage),
    resolution_ref: usage.resolution_ref ?? '',
  };
}

/**
 * 编辑态由已加载来源的使用方种子化；只改无关字段时保存必须原样带回这些条目，
 * 所以这里不做任何过滤或合并。
 */
export function sourceDraftFrom(source: Source): SourceDraft {
  return {
    name: source.name,
    kind: source.kind,
    repo_path: source.repo_path,
    default_ref: source.default_ref,
    usages: (source.usages ?? []).map(usageDraftFrom),
  };
}

/**
 * 编辑器行 → 请求体；逐行原样回传，空环境不占字段。
 * resolved 必须带依据一起回传，否则服务端会（正确地）把它降级为声明值。
 */
export function sourceUsagesFromDraft(usages: SourceUsageDraft[]): SourceUsage[] {
  return usages.map((usage) => {
    const environment = usage.environment.trim();
    const resolutionRef = usage.resolution_ref.trim();
    return {
      consumer: usage.consumer.trim(),
      artifact: usage.artifact.trim(),
      ...(environment ? { environment } : {}),
      artifact_resolution: usage.artifact_resolution,
      ...(resolutionRef ? { resolution_ref: resolutionRef } : {}),
    };
  });
}

export function sourceCreatePayload(draft: SourceDraft): CreateSourceInput {
  return {
    name: draft.name.trim(),
    kind: draft.kind,
    repo_path: draft.repo_path.trim(),
    default_ref: draft.default_ref.trim() || undefined,
    usages: sourceUsagesFromDraft(draft.usages),
  };
}

/** 修改请求始终带上编辑器里的全部使用方；没有条目时显式提交 `usages: []`。 */
export function sourceUpdatePayload(source: Source, draft: SourceDraft): UpdateSourceInput {
  return {
    name: draft.name.trim(),
    kind: draft.kind,
    repo_path: draft.repo_path.trim(),
    default_ref: draft.default_ref.trim(),
    usages: sourceUsagesFromDraft(draft.usages),
    expected_version: source.version,
  };
}

export function sourceDraftError(draft: SourceDraft): string {
  if (!draft.name.trim()) return '填写来源名称，用于在队列和证据里标识它';
  if (!draft.repo_path.trim()) return '填写仓库绝对路径，资料员只能在该目录内取快照';
  return '';
}

export function SourceForm({
  draft,
  onChange,
  onSubmit,
  onCancel,
  busy,
  submitLabel,
}: {
  draft: SourceDraft;
  onChange: (draft: SourceDraft) => void;
  onSubmit: () => void;
  onCancel?: () => void;
  busy: boolean;
  submitLabel: string;
}) {
  const [touched, setTouched] = useState(false);
  const error = sourceDraftError(draft);
  const patch = (next: Partial<SourceDraft>) => onChange({ ...draft, ...next });
  const patchUsage = (index: number, next: Partial<SourceUsageDraft>) =>
    patch({ usages: draft.usages.map((usage, i) => (i === index ? { ...usage, ...next } : usage)) });
  const addUsage = () => patch({ usages: [...draft.usages, emptyUsageDraft()] });
  const removeUsage = (index: number) => patch({ usages: draft.usages.filter((_, i) => i !== index) });
  return (
    <form
      className="rounded-card border border-border-subtle bg-surface-sunken/45 p-base"
      onSubmit={(event) => {
        event.preventDefault();
        setTouched(true);
        if (!error) onSubmit();
      }}
    >
      <div className="grid grid-cols-1 gap-snug md:grid-cols-2 xl:grid-cols-3">
        <Field label="名称">
          <Input
            value={draft.name}
            onChange={(event) => patch({ name: event.target.value })}
            placeholder="订单服务"
            invalid={touched && !draft.name.trim()}
          />
        </Field>
        <Field label="类型">
          <Select value={draft.kind} onChange={(event) => patch({ kind: event.target.value as SourceKind })}>
            {SOURCE_KIND_OPTIONS.map((option) => (
              <option key={option.value} value={option.value}>
                {option.label}
              </option>
            ))}
          </Select>
        </Field>
        <Field label="仓库路径" hint="绝对路径；资料员只在该目录内取快照">
          <Input
            value={draft.repo_path}
            onChange={(event) => patch({ repo_path: event.target.value })}
            placeholder="/Users/me/repos/order-service"
            invalid={touched && !draft.repo_path.trim()}
            className="font-mono text-caption"
          />
        </Field>
        <Field label="默认 ref" hint="留空由服务端取默认分支">
          <Input
            value={draft.default_ref}
            onChange={(event) => patch({ default_ref: event.target.value })}
            placeholder="main"
            className="font-mono text-caption"
          />
        </Field>
      </div>

      <fieldset className="mt-base rounded-card border border-border-subtle bg-surface-raised/50 p-snug">
        <legend className="px-micro text-caption font-medium text-text-secondary">
          使用方（依赖这份来源的业务服务或模块）
        </legend>
        <p className="text-caption text-text-tertiary">
          消费方是依赖这份来源的业务服务或模块（共享库 common 常被多个服务以不同版本依赖），不是来查询资料的 agent。
          逐条登记后，同一份资料的不同依赖版本才分得清。
        </p>
        {draft.usages.length === 0 ? (
          <p className="mt-snug text-caption text-text-tertiary">
            还没有使用方条目；保存时提交空列表，后端按「{SINGLE_IMPLICIT_USAGE_LABEL}」处理。
          </p>
        ) : (
          <ul className="mt-snug space-y-snug">
            {draft.usages.map((usage, index) => (
              <li
                key={index}
                className="rounded-card border border-border-subtle bg-surface-sunken/40 p-snug"
              >
                <div className="flex items-center justify-between gap-tight">
                  <span className="font-mono text-caption text-text-tertiary">使用方 {index + 1}</span>
                  <Button
                    type="button"
                    size="sm"
                    variant="danger-outline"
                    disabled={busy}
                    aria-label={`移除使用方 ${index + 1}`}
                    onClick={() => removeUsage(index)}
                  >
                    <Trash2 className="h-3.5 w-3.5" aria-hidden />
                    移除
                  </Button>
                </div>
                <div className="mt-snug grid grid-cols-1 gap-snug md:grid-cols-2 xl:grid-cols-4">
                  <Field label="消费方（业务服务或模块）" hint="依赖这份来源的服务/模块，不是查询资料的 agent">
                    <Input
                      value={usage.consumer}
                      onChange={(event) => patchUsage(index, { consumer: event.target.value })}
                      placeholder="order-service"
                      className="font-mono text-caption"
                    />
                  </Field>
                  <Field label="依赖制品（版本）" hint="形如 group:artifact:version；未登记可留空">
                    <Input
                      value={usage.artifact}
                      onChange={(event) => patchUsage(index, { artifact: event.target.value })}
                      placeholder="com.example:common:1.4.2"
                      className="font-mono text-caption"
                    />
                  </Field>
                  <Field label="环境" hint="可选，例如 prod / staging">
                    <Input
                      value={usage.environment}
                      onChange={(event) => patchUsage(index, { environment: event.target.value })}
                      placeholder="prod"
                      className="font-mono text-caption"
                    />
                  </Field>
                  <Field
                    label="制品版本来源"
                    hint={effectiveResolutionLabel(usage.artifact_resolution, usage.resolution_ref)}
                  >
                    <Select
                      value={usage.artifact_resolution}
                      onChange={(event) =>
                        patchUsage(index, { artifact_resolution: event.target.value as ArtifactResolution })
                      }
                    >
                      {ARTIFACT_RESOLUTION_OPTIONS.map((option) => (
                        <option key={option.value} value={option.value}>
                          {option.label}
                        </option>
                      ))}
                    </Select>
                  </Field>
                  {usage.artifact_resolution === 'resolved' ? (
                    <Field
                      label="解析依据"
                      hint="可选备注：例如 mvn dependency:tree 的输出路径。本版本没有解析能力，填写引用也不会变成已核实"
                    >
                      <Input
                        value={usage.resolution_ref}
                        onChange={(event) => patchUsage(index, { resolution_ref: event.target.value })}
                        placeholder="mvn-dependency-tree:order-service/target/tree.txt"
                        className="font-mono text-caption"
                      />
                    </Field>
                  ) : null}
                </div>
              </li>
            ))}
          </ul>
        )}
        <div className="mt-snug">
          <Button type="button" size="sm" onClick={addUsage} disabled={busy}>
            <Plus className="h-3.5 w-3.5" aria-hidden />
            添加使用方
          </Button>
        </div>
      </fieldset>

      {touched && error ? <FieldError>{error}</FieldError> : null}
      <div className="mt-snug flex justify-end gap-tight">
        {onCancel ? (
          <Button type="button" onClick={onCancel} disabled={busy}>
            取消
          </Button>
        ) : null}
        <Button type="submit" variant="primary" disabled={busy || !!error}>
          {busy ? '提交中…' : submitLabel}
        </Button>
      </div>
    </form>
  );
}

/**
 * 使用方摘要：一条来源可以被多个业务服务以不同依赖版本使用（共享库的常态）。
 * 多使用方时用状态徽标 + 缩进列表把「不止一个消费方」显式说出来，
 * 而不是把两条记录挤成一行。
 */
export function SourceUsagesCell({ usages }: { usages: SourceUsage[] }) {
  const list = usages ?? [];
  if (list.length === 0) {
    return <span className="text-caption text-text-tertiary">{SINGLE_IMPLICIT_USAGE_LABEL}</span>;
  }
  const multiple = list.length > 1;
  return (
    <div className="space-y-micro">
      {multiple ? <StateBadge tone="info">多使用方 · {list.length}</StateBadge> : null}
      <ul className={cx('space-y-micro', multiple && 'border-l-2 border-brand-primary/30 pl-snug')}>
        {list.map((usage, index) => (
          <li key={`${usage.consumer}#${usage.artifact}#${usage.environment ?? ''}#${index}`} className="text-caption">
            <span className="text-text-primary">{usageLine(usage)}</span>
            {usage.environment ? (
              <span className="mt-micro block text-caption text-text-tertiary">环境 {usage.environment}</span>
            ) : null}
          </li>
        ))}
      </ul>
    </div>
  );
}

export function SourcesTable({
  sources,
  onEdit,
  onDisable,
  busy,
}: {
  sources: Source[];
  onEdit: (source: Source) => void;
  onDisable: (source: Source) => void;
  busy: boolean;
}) {
  return (
    <DataTable caption="已登记来源">
      <thead>
        <tr>
          <Th>名称</Th>
          <Th>类型</Th>
          <Th>仓库路径</Th>
          <Th>默认 ref</Th>
          <Th>使用方 / 依赖版本</Th>
          <Th>状态</Th>
          <Th className="text-right">操作</Th>
        </tr>
      </thead>
      <tbody>
        {sources.map((source) => (
          <tr key={source.id}>
            <Td>
              <span className="text-text-primary">{source.name}</span>
              <span className="mt-micro block font-mono text-caption text-text-tertiary">{source.id}</span>
            </Td>
            <Td>{sourceKindLabel(source.kind)}</Td>
            <Td mono>{source.repo_path || '—'}</Td>
            <Td mono>{source.default_ref || '默认'}</Td>
            <Td>
              <SourceUsagesCell usages={source.usages} />
            </Td>
            <Td>
              <StateBadge tone={source.enabled ? 'success' : 'neutral'}>{source.enabled ? '启用' : '停用'}</StateBadge>
              <span className="mt-micro block font-mono text-caption text-text-tertiary">v{source.version}</span>
            </Td>
            <Td className="text-right">
              <div className="inline-flex items-center gap-tight">
                <Button size="sm" onClick={() => onEdit(source)}>
                  编辑
                </Button>
                <Button
                  size="sm"
                  variant="danger-outline"
                  onClick={() => onDisable(source)}
                  disabled={busy || !source.enabled}
                >
                  <Trash2 className="h-3.5 w-3.5" aria-hidden />
                  停用
                </Button>
              </div>
            </Td>
          </tr>
        ))}
      </tbody>
    </DataTable>
  );
}

export function LibraryPanel({
  summary,
  sources,
  onCreate,
  onUpdate,
  onDisable,
  onRefresh,
  busy,
}: {
  summary: Async<LibrarySummary>;
  sources: Async<Paged<Source>>;
  onCreate: (draft: SourceDraft) => Promise<boolean>;
  onUpdate: (source: Source, draft: SourceDraft) => Promise<boolean>;
  onDisable: (source: Source) => void;
  onRefresh: () => void;
  busy: boolean;
}) {
  const [draft, setDraft] = useState<SourceDraft>(emptySourceDraft);
  const [editing, setEditing] = useState<{ source: Source; draft: SourceDraft } | null>(null);

  return (
    <div className="space-y-comfortable">
      {summary.kind === 'loading' ? (
        <PanelLoading label="资料库总览加载中" rows={3} />
      ) : summary.kind === 'error' ? (
        <InlineError message={summary.message} onRetry={onRefresh} />
      ) : (
        <>
          <LibraryFactsCard summary={summary.value} onRefresh={onRefresh} />
          <CurrentReleaseCard release={summary.value.current_release} />
        </>
      )}

      <PanelSection
        title="来源登记"
        hint="登记来源后资料员按默认 ref 取快照；共享库（common）在同一条来源下登记多个使用方，各消费方的依赖版本分别记录；停用保留来源账本，不删除历史证据。"
      >
        <SourceForm
          draft={draft}
          onChange={setDraft}
          busy={busy}
          submitLabel="登记来源"
          onSubmit={() => {
            void onCreate(draft).then((ok) => {
              if (ok) setDraft(emptySourceDraft());
            });
          }}
        />
        <div className="mt-base">
          {sources.kind === 'loading' ? (
            <PanelLoading label="来源列表加载中" rows={4} />
          ) : sources.kind === 'error' ? (
            <InlineError message={sources.message} onRetry={onRefresh} />
          ) : sources.value.items.length === 0 ? (
            <EmptyState
              icon={<Server className="h-5 w-5" aria-hidden />}
              title="还没有登记来源"
              description="登记本工作区的服务仓库与 common，资料库才能取到它们的代码与文档。"
            />
          ) : (
            <SourcesTable
              sources={sources.value.items}
              busy={busy}
              onEdit={(source) => setEditing({ source, draft: sourceDraftFrom(source) })}
              onDisable={onDisable}
            />
          )}
        </div>
      </PanelSection>

      {editing ? (
        <Card padded className="border-brand-primary/30">
          <div className="flex items-center justify-between gap-tight">
            <h3 className="text-h3 text-text-primary">编辑来源 · {editing.source.name}</h3>
            <Button size="sm" onClick={() => setEditing(null)}>
              关闭
            </Button>
          </div>
          <div className="mt-snug">
            <SourceForm
              draft={editing.draft}
              onChange={(next) => setEditing({ ...editing, draft: next })}
              busy={busy}
              submitLabel="保存来源"
              onCancel={() => setEditing(null)}
              onSubmit={() => {
                void onUpdate(editing.source, editing.draft).then((ok) => {
                  if (ok) setEditing(null);
                });
              }}
            />
          </div>
        </Card>
      ) : null}
    </div>
  );
}

// ---------------------------------------------------------------------------
// Tab 2 · 初始化与更新
// ---------------------------------------------------------------------------

export function ReceiptCard({ receipt }: { receipt: LibraryEvent }) {
  return (
    <div className="rounded-card border border-border-subtle bg-surface-sunken/45 p-base" role="status">
      <div className="flex flex-wrap items-center gap-tight">
        <StateBadge tone={eventStatusTone(receipt.status)}>{eventStatusLabel(receipt.status)}</StateBadge>
        <span className="font-mono text-caption text-text-secondary">{receipt.event_type}</span>
        <span className="font-mono text-caption text-text-tertiary">{receipt.id}</span>
      </div>
      <p className="mt-tight text-body text-text-secondary">
        {receipt.status === 'accepted' || receipt.status === 'queued'
          ? '事件已受理并进入队列，资料更新在后台异步进行；这里只是回执，不代表知识已经更新。'
          : receipt.status === 'completed'
            ? '队列已完成这次更新，可以在「版本与队列」里查看发布。'
            : '这次更新没有完成，请到「版本与队列」查看阻塞原因与最后错误。'}
      </p>
      {receipt.task_id ? (
        <p className="mt-micro font-mono text-caption text-text-tertiary">关联任务 {receipt.task_id}</p>
      ) : null}
    </div>
  );
}

export function EventsTable({ events }: { events: LibraryEvent[] }) {
  return (
    <DataTable caption="最近事件">
      <thead>
        <tr>
          <Th>受理时间</Th>
          <Th>事件</Th>
          <Th>来源</Th>
          <Th>内容引用</Th>
          <Th>回执状态</Th>
          <Th>任务</Th>
        </tr>
      </thead>
      <tbody>
        {events.map((event) => (
          <tr key={event.id}>
            <Td mono>{formatDateTime(event.received_at)}</Td>
            <Td>
              <span className="font-mono text-caption text-text-primary">{event.event_type}</span>
              <span className="mt-micro block font-mono text-caption text-text-tertiary">{event.id}</span>
            </Td>
            <Td>{event.source || '—'}</Td>
            <Td mono>{event.content_ref || '—'}</Td>
            <Td>
              <StateBadge tone={eventStatusTone(event.status)}>{eventStatusLabel(event.status)}</StateBadge>
            </Td>
            <Td mono>{event.task_id || '—'}</Td>
          </tr>
        ))}
      </tbody>
    </DataTable>
  );
}

export function SyncPanel({
  events,
  eventStatusFilter,
  onEventStatusFilterChange,
  onInitialize,
  onSubmitEvent,
  receipt,
  busy,
  onRefresh,
}: {
  events: Async<Paged<LibraryEvent>>;
  eventStatusFilter: string;
  onEventStatusFilterChange: (status: string) => void;
  onInitialize: (reason: string) => void;
  onSubmitEvent: (input: { eventType: string; source: string; contentRef: string; payload: string }) => void;
  receipt: LibraryEvent | null;
  busy: boolean;
  onRefresh: () => void;
}) {
  const [eventType, setEventType] = useState(KNOWLEDGE_EVENT_TYPES[1]!.value);
  const [source, setSource] = useState('');
  const [contentRef, setContentRef] = useState('');
  const [payload, setPayload] = useState('');
  const [payloadError, setPayloadError] = useState('');
  const [reason, setReason] = useState('');

  const submit = () => {
    let parseError = '';
    if (payload.trim()) {
      try {
        const parsed: unknown = JSON.parse(payload);
        if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) parseError = '载荷必须是 JSON 对象';
      } catch {
        parseError = '载荷不是合法 JSON';
      }
    }
    setPayloadError(parseError);
    if (parseError) return;
    onSubmitEvent({ eventType, source, contentRef, payload });
  };

  return (
    <div className="space-y-comfortable">
      <PanelSection title="初始化资料库" hint="首次建库只需一次；重复提交由 client_key 幂等，不会产生第二个任务。">
        <div className="flex flex-wrap items-end gap-snug">
          <Field label="初始化原因（可选）" className="min-w-[16rem] flex-1">
            <Input value={reason} onChange={(event) => setReason(event.target.value)} placeholder="首次建立资料库" />
          </Field>
          <Button variant="primary" onClick={() => onInitialize(reason)} disabled={busy}>
            <Play className="h-4 w-4" aria-hidden />
            提交初始化
          </Button>
        </div>
        <p className="mt-snug flex items-start gap-tight rounded-card border border-border-subtle bg-surface-sunken/45 px-snug py-tight text-caption text-text-secondary">
          <Info className="h-3.5 w-3.5 shrink-0 text-brand-primary" aria-hidden />
          提交后立即返回回执，后台队列再依次执行；业务调用方不需要等待，也不要按「提交成功」判断知识已经更新。
        </p>
      </PanelSection>

      <PanelSection
        title="提交更新事件"
        hint="代码拉取、需求导入、文档修订都通过事件通知资料库；一次事件一次受理。"
      >
        <form
          className="space-y-snug"
          onSubmit={(event) => {
            event.preventDefault();
            submit();
          }}
        >
          <div className="grid grid-cols-1 gap-snug md:grid-cols-2 xl:grid-cols-3">
            <Field label="事件类型">
              <Select value={eventType} onChange={(event) => setEventType(event.target.value)}>
                {KNOWLEDGE_EVENT_TYPES.map((option) => (
                  <option key={option.value} value={option.value}>
                    {option.label}
                  </option>
                ))}
              </Select>
            </Field>
            <Field label="来源" hint="来源名称或仓库路径；留空表示工作区整体事件">
              <Input value={source} onChange={(event) => setSource(event.target.value)} placeholder="订单服务" />
            </Field>
            <Field label="内容引用（可选）" hint="如 commit sha、文档路径或需求编号">
              <Input
                value={contentRef}
                onChange={(event) => setContentRef(event.target.value)}
                placeholder="commit:9f2c1a"
                className="font-mono text-caption"
              />
            </Field>
          </div>
          <Field label="载荷 JSON（可选）" error={payloadError}>
            <Textarea
              value={payload}
              onChange={(event) => setPayload(event.target.value)}
              rows={4}
              invalid={!!payloadError}
              placeholder='{"branch":"main","sha":"9f2c1a"}'
              className="font-mono text-caption"
            />
          </Field>
          <div className="flex justify-end">
            <Button type="submit" variant="primary" disabled={busy}>
              <Inbox className="h-4 w-4" aria-hidden />
              {busy ? '提交中…' : '提交事件'}
            </Button>
          </div>
        </form>
        {receipt ? (
          <div className="mt-snug">
            <p className="mb-tight text-caption font-medium text-text-tertiary">最近一次提交的回执</p>
            <ReceiptCard receipt={receipt} />
          </div>
        ) : null}
      </PanelSection>

      <PanelSection
        title="最近事件"
        hint="「已受理」只说明事件进入了队列；「已完成」才代表这次更新落到了发布版本。"
        actions={
          <div className="flex items-end gap-tight">
            <Field label="状态筛选">
              <Select value={eventStatusFilter} onChange={(event) => onEventStatusFilterChange(event.target.value)}>
                <option value="">全部</option>
                {Object.entries(EVENT_STATUS).map(([value, meta]) => (
                  <option key={value} value={value}>
                    {meta.label}
                  </option>
                ))}
              </Select>
            </Field>
            <Button size="sm" onClick={onRefresh}>
              <RefreshCw className="h-3.5 w-3.5" aria-hidden />
              刷新
            </Button>
          </div>
        }
      >
        {events.kind === 'loading' ? (
          <PanelLoading label="事件列表加载中" rows={5} />
        ) : events.kind === 'error' ? (
          <InlineError message={events.message} onRetry={onRefresh} />
        ) : events.value.items.length === 0 ? (
          <EmptyState
            icon={<Inbox className="h-5 w-5" aria-hidden />}
            title="还没有事件"
            description="提交初始化或一次更新事件后，这里会显示受理回执与关联任务。"
          />
        ) : (
          <>
            <EventsTable events={events.value.items} />
            {events.value.next_cursor ? (
              <p className="mt-tight text-caption text-text-tertiary" role="status">
                只显示最近 {events.value.items.length} 条；用状态筛选收窄范围。
              </p>
            ) : null}
          </>
        )}
      </PanelSection>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Tab 3 · 知识浏览
// ---------------------------------------------------------------------------

export function releaseOptionLabel(release: Release): string {
  return `#${release.seq} · ${formatDateTime(release.published_at)} · 文档 ${release.document_count}`;
}

export function DocumentFrontmatter({ detail }: { detail: DocumentDetail }) {
  const { document, version } = detail;
  return (
    <dl className="grid grid-cols-2 gap-snug md:grid-cols-4">
      <MetaItem label="文档 ID" value={document.id} mono />
      <MetaItem label="路径" value={document.path} mono />
      <MetaItem label="类型" value={document.kind || '—'} mono />
      <MetaItem label="状态" value={document.status} mono />
      <MetaItem label="版本" value={`v${document.version}`} mono />
      <MetaItem label="发布" value={document.release_id ?? '当前发布'} mono />
      <MetaItem label="领域" value={document.domains.length > 0 ? document.domains.join(' · ') : '未标注'} />
      <MetaItem label="内容摘要" value={<span className="font-mono">{shortDigest(version.content_digest, 20)}</span>} />
      <MetaItem label="更新时间" value={formatDateTime(document.updated_at)} />
      <MetaItem label="正文生成时间" value={formatDateTime(version.created_at)} />
    </dl>
  );
}

export function AssertionCard({
  assertion,
  onSelectEvidence,
}: {
  assertion: Assertion;
  onSelectEvidence?: (evidenceId: string) => void;
}) {
  return (
    <article className="rounded-card border border-border-subtle bg-surface-raised p-base">
      <header className="flex flex-wrap items-center gap-tight">
        <StateBadge tone={assertion.perspective === 'normative' ? 'info' : 'neutral'}>
          {perspectiveLabel(assertion.perspective)}
        </StateBadge>
        <StateBadge tone="neutral">{basisLabel(assertion.basis)}</StateBadge>
        {assertion.heading ? <span className="text-caption text-text-secondary">{assertion.heading}</span> : null}
        <span className="font-mono text-caption text-text-tertiary">{assertion.id}</span>
      </header>
      <p className="mt-tight whitespace-pre-wrap text-body text-text-primary">{assertion.statement}</p>
      <p className="mt-tight text-caption text-text-secondary">适用范围：{scopeText(assertion.scope)}</p>
      {assertion.about.length > 0 ? (
        <p className="mt-micro text-caption text-text-tertiary">关于：{assertion.about.join(' · ')}</p>
      ) : null}
      {assertion.evidence.length > 0 ? (
        <div className="mt-tight flex flex-wrap items-center gap-tight">
          <span className="text-caption text-text-tertiary">证据</span>
          {assertion.evidence.map((ref) => (
            <button
              key={`${ref.evidence_id}:${ref.role}`}
              type="button"
              onClick={() => onSelectEvidence?.(ref.evidence_id)}
              aria-label={evidenceChipLabel(ref)}
              className="inline-flex min-h-8 items-center gap-micro rounded-button border border-border-strong bg-surface-base px-tight text-caption text-text-secondary transition-colors hover:border-brand-primary/40 hover:text-brand-primary focus-visible:ring-2 focus-visible:ring-brand-primary/40"
            >
              <ShieldCheck className="h-3.5 w-3.5" aria-hidden />
              <span className="font-mono">{shortDigest(ref.evidence_id, 14)}</span>
              <span className="text-text-tertiary">{evidenceRoleLabel(ref.role)}</span>
            </button>
          ))}
        </div>
      ) : (
        <p className="mt-tight text-caption text-text-tertiary">这条断言没有登记证据。</p>
      )}
      {assertion.unknown_notes ? (
        <p className="mt-tight rounded-card border border-status-warning/30 bg-status-warning/5 px-snug py-tight text-caption text-status-warning">
          未知：{assertion.unknown_notes}
        </p>
      ) : null}
    </article>
  );
}

export function RelationList({ relations }: { relations: Relation[] }) {
  if (relations.length === 0) {
    return <p className="text-body text-text-tertiary">这份文档没有登记关系。</p>;
  }
  return (
    <ul className="space-y-tight">
      {relations.map((relation) => (
        <li key={relation.id} className="rounded-card border border-border-subtle bg-surface-raised px-snug py-tight">
          <div className="flex flex-wrap items-center gap-tight text-body">
            <span className="font-mono text-caption text-text-secondary">
              {relation.from.kind}:{relation.from.id}
            </span>
            <ArrowLeftRight className="h-3.5 w-3.5 shrink-0 text-brand-primary" aria-hidden />
            <span className="text-text-primary">{relation.predicate}</span>
            <ArrowLeftRight className="h-3.5 w-3.5 shrink-0 text-brand-primary" aria-hidden />
            {relation.to_resolved ? (
              <span className="font-mono text-caption text-text-secondary">
                {relation.to.kind}:{relation.to.id}
              </span>
            ) : (
              <>
                <span className="font-mono text-caption text-text-tertiary">{relation.to_raw || relation.to.id}</span>
                <StateBadge tone="warning">端点未解析</StateBadge>
              </>
            )}
          </div>
          <p className="mt-micro text-caption text-text-tertiary">
            {perspectiveLabel(relation.perspective)} · 依据 {relation.basis || '未标注'}
            {relation.condition ? ` · 条件 ${relation.condition}` : ''}
          </p>
        </li>
      ))}
    </ul>
  );
}

export function VersionHistory({
  versions,
  currentVersion,
  selectedVersion,
  onSelect,
}: {
  versions: DocumentDetail['versions'];
  currentVersion: number;
  selectedVersion: number | null;
  onSelect: (version: number) => void;
}) {
  const ordered = useMemo(() => [...versions].sort((left, right) => right.version - left.version), [versions]);
  if (ordered.length === 0) {
    return <p className="text-body text-text-tertiary">没有版本记录。</p>;
  }
  return (
    <ol className="space-y-micro">
      {ordered.map((version) => {
        const current = version.version === currentVersion;
        const selected = version.version === selectedVersion;
        return (
          <li key={version.id}>
            <button
              type="button"
              onClick={() => onSelect(version.version)}
              aria-current={current ? 'page' : undefined}
              aria-pressed={selected}
              className={cx(
                'flex min-h-8 w-full items-center justify-between gap-tight rounded-button border px-snug py-micro text-left text-caption transition-colors',
                selected
                  ? 'border-brand-primary/40 bg-brand-muted/40 text-text-primary'
                  : 'border-border-subtle bg-surface-base text-text-secondary hover:border-border-strong hover:text-text-primary',
              )}
            >
              <span className="flex items-center gap-tight">
                <span className="font-mono">v{version.version}</span>
                {current ? <StateBadge tone="success">当前</StateBadge> : <StateBadge tone="neutral">历史</StateBadge>}
              </span>
              <span className="flex min-w-0 items-center gap-tight text-text-tertiary">
                <span className="truncate">{formatDateTime(version.created_at)}</span>
                <span className="font-mono">{shortDigest(version.content_digest, 10)}</span>
                <ChevronRight className="h-3.5 w-3.5 shrink-0" aria-hidden />
              </span>
            </button>
          </li>
        );
      })}
    </ol>
  );
}

export function HistoricalVersionNotice({ detail, version }: { detail: DocumentDetail; version: number }) {
  const meta = detail.versions.find((candidate) => candidate.version === version);
  return (
    <div className="rounded-card border border-border-subtle bg-surface-sunken/45 p-base">
      <div className="flex flex-wrap items-center gap-tight">
        <StateBadge tone="neutral">历史版本只读</StateBadge>
        <span className="font-mono text-caption text-text-secondary">v{version}</span>
      </div>
      <p className="mt-tight text-body text-text-secondary">
        服务端按发布版本提供正文；要看历史版本正文，请在「发布版本」里固定到包含它的 release 后打开。
      </p>
      {meta ? (
        <dl className="mt-snug grid grid-cols-2 gap-snug md:grid-cols-3">
          <MetaItem label="版本 ID" value={meta.id} mono />
          <MetaItem label="内容摘要" value={<span className="font-mono">{meta.content_digest}</span>} />
          <MetaItem label="生成时间" value={formatDateTime(meta.created_at)} />
        </dl>
      ) : (
        <p className="mt-tight text-caption text-text-tertiary">版本列表里没有这条记录。</p>
      )}
    </div>
  );
}

export function DocumentDetailView({
  detail,
  selectedVersion,
  onSelectVersion,
  onSelectEvidence,
}: {
  detail: DocumentDetail;
  selectedVersion: number | null;
  onSelectVersion: (version: number) => void;
  onSelectEvidence: (evidenceId: string) => void;
}) {
  const historical = selectedVersion !== null && selectedVersion !== detail.document.version;
  return (
    <div className="space-y-comfortable">
      <PanelSection title={detail.document.title || detail.document.path} hint={detail.document.summary || '未提供摘要'}>
        <DocumentFrontmatter detail={detail} />
        {detail.document.renamed_from ? (
          <p className="mt-snug text-caption text-text-tertiary">由 {detail.document.renamed_from} 重命名而来。</p>
        ) : null}
      </PanelSection>

      {historical && selectedVersion !== null ? (
        <HistoricalVersionNotice detail={detail} version={selectedVersion} />
      ) : (
        <>
          <PanelSection title="断言" hint={`共 ${detail.assertions.length} 条；每条给出视角、依据、适用范围与证据。`}>
            {detail.assertions.length === 0 ? (
              <EmptyState
                icon={<ListChecks className="h-5 w-5" aria-hidden />}
                title="这份文档还没有断言"
                description="断言来自正文里的稳定 ID 块；发布后才会出现在这里。"
              />
            ) : (
              <div className="space-y-snug">
                {detail.assertions.map((assertion) => (
                  <AssertionCard key={assertion.id} assertion={assertion} onSelectEvidence={onSelectEvidence} />
                ))}
              </div>
            )}
          </PanelSection>

          <PanelSection title="关系" hint="from → predicate → to；端点未解析表示目标还没进入同一发布。">
            <RelationList relations={detail.relations} />
          </PanelSection>

          <PanelSection title="正文" hint="Markdown 是知识正文的真相源。">
            <pre className="max-h-[32rem] overflow-auto whitespace-pre-wrap rounded-card border border-border-subtle bg-surface-sunken/45 p-base font-mono text-caption text-text-secondary">
              {detail.version.content_markdown}
            </pre>
          </PanelSection>
        </>
      )}

      <PanelSection title="版本历史" hint="当前版本可读正文；历史版本按发布固定读取。">
        <VersionHistory
          versions={detail.versions}
          currentVersion={detail.document.version}
          selectedVersion={selectedVersion}
          onSelect={onSelectVersion}
        />
        {historical ? (
          <div className="mt-snug">
            <Button size="sm" onClick={() => onSelectVersion(detail.document.version)}>
              回到当前版本 v{detail.document.version}
            </Button>
          </div>
        ) : null}
      </PanelSection>
    </div>
  );
}

export function BrowsePanel({
  releases,
  documents,
  detail,
  selectedReleaseId,
  onSelectRelease,
  query,
  kind,
  onQueryChange,
  onKindChange,
  onSearch,
  selectedDocumentId,
  onSelectDocument,
  onSelectEvidence,
  onRefresh,
}: {
  releases: Async<Paged<Release>>;
  documents: Async<Paged<DocumentSummary>>;
  detail: Async<DocumentDetail> | null;
  selectedReleaseId: string;
  onSelectRelease: (releaseId: string) => void;
  query: string;
  kind: string;
  onQueryChange: (value: string) => void;
  onKindChange: (value: string) => void;
  onSearch: () => void;
  selectedDocumentId: string | null;
  onSelectDocument: (documentId: string) => void;
  onSelectEvidence: (evidenceId: string) => void;
  onRefresh: () => void;
}) {
  const [selectedVersion, setSelectedVersion] = useState<number | null>(null);

  useEffect(() => {
    setSelectedVersion(null);
  }, [selectedDocumentId]);

  return (
    <div className="space-y-comfortable">
      <PanelSection
        title="浏览范围"
        hint="默认读当前发布；固定到某个 release 可以复现当时的文档与断言。"
        actions={
          <Button size="sm" onClick={onRefresh}>
            <RefreshCw className="h-3.5 w-3.5" aria-hidden />
            刷新
          </Button>
        }
      >
        <form
          className="grid grid-cols-1 gap-snug md:grid-cols-4"
          onSubmit={(event) => {
            event.preventDefault();
            onSearch();
          }}
        >
          <Field label="发布版本">
            <Select value={selectedReleaseId} onChange={(event) => onSelectRelease(event.target.value)}>
              <option value="">当前发布（默认）</option>
              {releases.kind === 'ready'
                ? releases.value.items.map((release) => (
                    <option key={release.id} value={release.id}>
                      {releaseOptionLabel(release)}
                    </option>
                  ))
                : null}
            </Select>
          </Field>
          <Field label="搜索文档">
            <Input
              value={query}
              onChange={(event) => onQueryChange(event.target.value)}
              placeholder="标题、路径或摘要关键词"
              type="search"
            />
          </Field>
          <Field label="类型" hint="如 rule / flow / component / contract">
            <Input
              value={kind}
              onChange={(event) => onKindChange(event.target.value)}
              placeholder="全部类型"
              className="font-mono text-caption"
            />
          </Field>
          <div className="flex items-end">
            <Button type="submit" className="w-full">
              <Search className="h-4 w-4" aria-hidden />
              搜索
            </Button>
          </div>
        </form>
        {releases.kind === 'error' ? (
          <div className="mt-snug">
            <InlineError message={releases.message} onRetry={onRefresh} />
          </div>
        ) : null}
      </PanelSection>

      <div className="grid grid-cols-1 gap-comfortable xl:grid-cols-[20rem_minmax(0,1fr)]">
        <PanelSection title="文档" hint={`已加载 ${documents.kind === 'ready' ? documents.value.items.length : 0} 份`}>
          {documents.kind === 'loading' ? (
            <PanelLoading label="文档列表加载中" rows={6} />
          ) : documents.kind === 'error' ? (
            <InlineError message={documents.message} onRetry={onRefresh} />
          ) : documents.value.items.length === 0 ? (
            <EmptyState
              icon={<FileText className="h-5 w-5" aria-hidden />}
              title="没有匹配的文档"
              description="换一个关键词，或清空类型筛选后重试。"
            />
          ) : (
            <>
              {documents.value.next_cursor ? (
                <p className="mb-tight text-caption text-text-tertiary" role="status">
                  只显示前 {documents.value.items.length} 份；用搜索或类型筛选收窄范围。
                </p>
              ) : null}
              <ul className="max-h-[36rem] space-y-micro overflow-y-auto">
                {documents.value.items.map((document) => (
                  <li key={document.id}>
                    <button
                      type="button"
                      onClick={() => onSelectDocument(document.id)}
                      aria-current={selectedDocumentId === document.id ? 'page' : undefined}
                      className={cx(
                        'w-full rounded-button border px-snug py-tight text-left transition-colors',
                        selectedDocumentId === document.id
                          ? 'border-brand-primary/40 bg-brand-muted/40'
                          : 'border-border-subtle bg-surface-base hover:border-border-strong',
                      )}
                    >
                      <span className="flex items-center justify-between gap-tight">
                        <span className="truncate text-body font-medium text-text-primary">
                          {document.title || document.path}
                        </span>
                        <span className="shrink-0 font-mono text-caption text-text-tertiary">v{document.version}</span>
                      </span>
                      <span className="mt-micro block truncate font-mono text-caption text-text-tertiary">
                        {document.path}
                      </span>
                      <span className="mt-micro flex items-center gap-tight text-caption text-text-tertiary">
                        <span>{document.kind || '未分类'}</span>
                        {document.status !== 'active' ? <StateBadge tone="warning">{document.status}</StateBadge> : null}
                      </span>
                    </button>
                  </li>
                ))}
              </ul>
            </>
          )}
        </PanelSection>

        <div className="min-w-0">
          {!selectedDocumentId ? (
            <Card padded>
              <EmptyState
                icon={<BookOpen className="h-5 w-5" aria-hidden />}
                title="选择一份文档开始阅读"
                description="正文、断言、关系与版本历史会在这里展开。"
              />
            </Card>
          ) : detail === null || detail.kind === 'loading' ? (
            <PanelLoading label="文档详情加载中" rows={6} />
          ) : detail.kind === 'error' ? (
            <InlineError message={detail.message} onRetry={onRefresh} />
          ) : (
            <DocumentDetailView
              detail={detail.value}
              selectedVersion={selectedVersion}
              onSelectVersion={setSelectedVersion}
              onSelectEvidence={onSelectEvidence}
            />
          )}
        </div>
      </div>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Tab 4 · 查询与展开
// ---------------------------------------------------------------------------

/** 需求输入的表示来源不是代码仓库，术语必须跟着来源类型走。 */
export function isRequirementEvidence(evidence: Evidence): boolean {
  return evidence.binding.source_kind === 'requirement';
}

export function representationOriginLabel(origin: string): string {
  switch (origin) {
    case 'worktree':
      return '工作树字节';
    case 'frozen_requirement':
      return '受理时冻结的需求原文';
    default:
      return '已提交字节';
  }
}

export function EvidenceDetailCard({ evidence }: { evidence: Evidence }) {
  return (
    <div className="space-y-snug">
      <div className="flex flex-wrap items-center gap-tight">
        <StateBadge tone={evidence.availability === 'available' ? 'success' : 'warning'}>
          {evidence.availability === 'available' ? '可取用' : '不可取用'}
        </StateBadge>
        <StateBadge tone="neutral">{evidence.locator_kind || '未知定位类型'}</StateBadge>
        <span className="font-mono text-caption text-text-tertiary">{evidence.id}</span>
      </div>

      <dl className="grid grid-cols-1 gap-snug md:grid-cols-2">
        <MetaItem label="来源" value={evidence.binding.source_name || evidence.binding.source_kind || '—'} />
        {isRequirementEvidence(evidence) ? (
          <>
            {/* 需求输入不是 Git 仓库：commit_sha 是受理原文的 sha256，说成「提交」会误导。 */}
            <MetaItem label="需求版本" value={evidence.binding.git_ref || '—'} mono />
            <MetaItem label="受理原文摘要" value={evidence.binding.commit_sha || '—'} mono />
            <MetaItem label="冻结文本" value="受理时固化的需求原文" />
            <MetaItem label="来源类型" value="需求输入（非代码仓库）" />
          </>
        ) : (
          <>
            <MetaItem label="仓库路径" value={evidence.binding.repo_path || '—'} mono />
            <MetaItem label="git ref" value={evidence.binding.git_ref || '—'} mono />
            <MetaItem label="commit" value={evidence.binding.commit_sha || '—'} mono />
            <MetaItem
              label="工作树状态"
              value={
                evidence.binding.dirty ? (
                  <StateBadge tone="warning">包含未提交改动</StateBadge>
                ) : (
                  <StateBadge tone="success">干净</StateBadge>
                )
              }
            />
            <MetaItem
              label="制品 / 消费方"
              value={`${evidence.binding.artifact || '—'} · ${evidence.binding.consumer || '—'}`}
            />
          </>
        )}
        <MetaItem label="定位" value={formatLocator(evidence.locator)} mono />
        <MetaItem
          label="表示"
          value={`${evidence.representation.media_type || '未知类型'} · ${representationOriginLabel(
            evidence.representation.origin,
          )} · ${formatBytes(evidence.representation.byte_size)}`}
        />
        <MetaItem
          label="内容摘要"
          value={
            <span className="font-mono">
              {shortDigest(evidence.representation.content_digest, 24)}（{evidence.representation.digest_algo}）
            </span>
          }
        />
        <MetaItem label="存储路径" value={evidence.representation.stored_path || '—'} mono />
        <MetaItem label="摘录摘要" value={<span className="font-mono">{shortDigest(evidence.excerpt_digest, 24)}</span>} />
        <MetaItem label="命中次数" value={evidence.match_count} mono />
      </dl>

      <div>
        <p className="text-caption text-text-tertiary">摘录</p>
        <pre className="mt-micro max-h-80 overflow-auto whitespace-pre-wrap rounded-card border border-border-subtle bg-surface-sunken/45 p-snug font-mono text-caption text-text-secondary">
          {evidence.excerpt || '这条证据没有摘录。'}
        </pre>
      </div>
    </div>
  );
}

export function QueryCoverageCard({ response }: { response: QueryResponse }) {
  const meta = COVERAGE_LABELS[response.coverage.status];
  return (
    <PanelSection title="覆盖与新鲜度" hint={`扫描版本 ${response.coverage.scanned_versions}`}>
      {response.coverage.status === 'not_ready' && !response.release ? (
        <div
          role="status"
          className="mb-snug rounded-card border border-status-warning/35 bg-status-warning/10 px-snug py-tight"
        >
          <p className="flex items-center gap-micro text-body font-medium text-status-warning">
            <CircleAlert className="h-4 w-4 shrink-0" aria-hidden="true" />
            资料尚未就绪
          </p>
          <p className="mt-micro text-caption text-text-secondary">
            这个资料库还没有发布任何版本，因此现在没有可以引用的知识。初始化任务完成后即可查询；业务任务不需要等待它。
          </p>
        </div>
      ) : null}
      <div className="flex flex-wrap items-center gap-tight">
        <StateBadge tone={meta.tone}>{meta.label}</StateBadge>
        {/* 检索维度与知识维度分开显示：命中没被截断，不等于知识没有缺口。 */}
        <StateBadge tone={response.coverage.truncated ? 'warning' : 'neutral'}>
          {response.coverage.truncated ? '检索被截断' : '检索未截断'}
        </StateBadge>
        {response.coverage.gap_count > 0 ? (
          <StateBadge tone="warning">覆盖缺口 {response.coverage.gap_count}</StateBadge>
        ) : null}
        {response.coverage.evidence_missing > 0 ? (
          <StateBadge tone="warning">无证据条目 {response.coverage.evidence_missing}</StateBadge>
        ) : null}
        {response.coverage.unknown_count > 0 ? (
          <StateBadge tone="warning">未知 {response.coverage.unknown_count}</StateBadge>
        ) : null}
        {response.freshness.newer_release_available ? <StateBadge tone="warning">已有更新的发布</StateBadge> : null}
        <StateBadge tone={response.freshness.pending_events > 0 ? 'warning' : 'success'}>
          待处理事件 {response.freshness.pending_events}
        </StateBadge>
      </div>
      {response.coverage.truncated ? (
        <p className="mt-tight text-caption text-status-warning">结果达到上限被截断，缩小问题范围或降低 limit 后再看。</p>
      ) : null}
      {response.freshness.pending_events > 0 ? (
        <p className="mt-tight text-caption text-status-warning">
          还有 {response.freshness.pending_events} 个事件没有处理完，当前答案可能落后于最新代码与文档。
        </p>
      ) : null}
      {response.freshness.newer_release_available ? (
        <p className="mt-tight text-caption text-status-warning">库里有更新的发布；如需最新结论，请重新提交查询。</p>
      ) : null}
      {response.freshness.stale_sources.length > 0 ? (
        <p className="mt-tight text-caption text-status-warning">
          来源已过期：{response.freshness.stale_sources.join(' · ')}
        </p>
      ) : null}
      {response.coverage.notes.length > 0 ? (
        <ul className="mt-tight space-y-micro">
          {response.coverage.notes.map((note, index) => (
            <li key={index} className="text-caption text-text-secondary">
              · {note}
            </li>
          ))}
        </ul>
      ) : null}
      {response.release ? (
        <p className="mt-tight font-mono text-caption text-text-tertiary">
          固定发布 #{response.release.seq} · {response.release.id} · {formatDateTime(response.release.published_at)}
        </p>
      ) : (
        <p className="mt-tight text-caption text-text-tertiary">当前没有可查询的发布版本。</p>
      )}
    </PanelSection>
  );
}

export function QueryResultCard({
  result,
  onSelectEvidence,
}: {
  result: QueryResponse['results'][number];
  onSelectEvidence: (evidenceId: string) => void;
}) {
  return (
    <article className="rounded-card border border-border-subtle bg-surface-raised p-base">
      <header className="flex flex-wrap items-center justify-between gap-tight">
        <div className="min-w-0">
          <p className="truncate text-body font-medium text-text-primary">
            {result.document.title || result.document.path}
          </p>
          <p className="truncate font-mono text-caption text-text-tertiary">{result.document.path}</p>
        </div>
        <StateBadge tone="neutral">相关度 {result.score.toFixed(3)}</StateBadge>
      </header>
      <div className="mt-snug">
        <AssertionCard assertion={result.assertion} onSelectEvidence={onSelectEvidence} />
      </div>
      {result.snippet ? (
        <p className="mt-tight whitespace-pre-wrap rounded-card border border-border-subtle bg-surface-sunken/45 px-snug py-tight text-caption text-text-secondary">
          {result.snippet}
        </p>
      ) : null}
    </article>
  );
}

export function QueryPanel({
  releases,
  state,
  onQuery,
  onSelectEvidence,
  onExpand,
  expanded,
  onRefresh,
}: {
  releases: Async<Paged<Release>>;
  state: { kind: 'idle' } | Async<QueryResponse>;
  onQuery: (input: { question: string; terms: string; releaseId: string; limit: string }) => void;
  onSelectEvidence: (evidenceId: string) => void;
  onExpand: (kind: string, id: string) => void;
  expanded: Record<string, LibraryReceipt>;
  onRefresh: () => void;
}) {
  const [question, setQuestion] = useState('');
  const [terms, setTerms] = useState('');
  const [releaseId, setReleaseId] = useState('');
  const [limit, setLimit] = useState('10');
  const loading = state.kind === 'loading';

  return (
    <div className="space-y-comfortable">
      <PanelSection title="查询知识" hint="查询固定读一个已发布版本，并区分条件、依据、覆盖缺口、未知与新鲜度。">
        <form
          className="space-y-snug"
          onSubmit={(event) => {
            event.preventDefault();
            onQuery({ question, terms, releaseId, limit });
          }}
        >
          <Field label="问题">
            <Textarea
              value={question}
              onChange={(event) => setQuestion(event.target.value)}
              rows={3}
              placeholder="退款窗口是多久？在什么条件下生效？"
            />
          </Field>
          <div className="grid grid-cols-1 gap-snug md:grid-cols-3">
            <Field label="关键词（可选）" hint="逗号或空格分隔，用于收紧召回">
              <Input value={terms} onChange={(event) => setTerms(event.target.value)} placeholder="退款, 窗口" />
            </Field>
            <Field label="固定发布版本">
              <Select value={releaseId} onChange={(event) => setReleaseId(event.target.value)}>
                <option value="">当前发布（默认）</option>
                {releases.kind === 'ready'
                  ? releases.value.items.map((release) => (
                      <option key={release.id} value={release.id}>
                        {releaseOptionLabel(release)}
                      </option>
                    ))
                  : null}
              </Select>
            </Field>
            <Field label="返回条数上限">
              <Input
                value={limit}
                onChange={(event) => setLimit(event.target.value)}
                inputMode="numeric"
                className="font-mono"
              />
            </Field>
          </div>
          <div className="flex justify-end">
            <Button type="submit" variant="primary" disabled={loading || !question.trim()}>
              <Search className="h-4 w-4" aria-hidden />
              {loading ? '查询中…' : '查询'}
            </Button>
          </div>
        </form>
      </PanelSection>

      {state.kind === 'idle' ? (
        <Card padded>
          <EmptyState
            icon={<Search className="h-5 w-5" aria-hidden />}
            title="还没有查询结果"
            description="提交一个问题后，这里会给出断言、条件、依据、证据与覆盖缺口。"
          />
        </Card>
      ) : state.kind === 'loading' ? (
        <PanelLoading label="查询进行中" rows={4} />
      ) : state.kind === 'error' ? (
        <InlineError message={state.message} onRetry={onRefresh} />
      ) : (
        <>
          <QueryCoverageCard response={state.value} />

          <PanelSection title="结果" hint={`${state.value.results.length} 条断言命中`}>
            {state.value.results.length === 0 ? (
              <EmptyState
                icon={<Search className="h-5 w-5" aria-hidden />}
                title="没有命中"
                description="换一种问法，或先确认相关来源已经在「初始化与更新」里提交过事件。"
              />
            ) : (
              <div className="space-y-snug">
                {state.value.results.map((result, index) => (
                  <QueryResultCard
                    key={`${result.document.id}:${result.assertion.id}:${index}`}
                    result={result}
                    onSelectEvidence={onSelectEvidence}
                  />
                ))}
              </div>
            )}
          </PanelSection>

          <PanelSection title="未知" hint="资料库明确标注「还不知道」的部分；不要把它们当成结论。">
            {state.value.unknowns.length === 0 ? (
              <p className="text-body text-text-tertiary">这次查询没有登记未知项。</p>
            ) : (
              <ul className="space-y-micro">
                {state.value.unknowns.map((unknown, index) => (
                  <li key={index} className="flex items-start gap-tight text-body text-text-secondary">
                    <AlertCircle className="mt-micro h-3.5 w-3.5 shrink-0 text-status-warning" aria-hidden />
                    {unknown}
                  </li>
                ))}
              </ul>
            )}
          </PanelSection>

          <PanelSection title="展开句柄" hint="按句柄展开原文证据或条目；展开结果按服务端返回原样展示。">
            {state.value.expand_handles.length === 0 ? (
              <p className="text-body text-text-tertiary">这次查询没有返回可展开句柄。</p>
            ) : (
              <div className="flex flex-wrap gap-tight">
                {state.value.expand_handles.map((handle) => (
                  <Button key={`${handle.kind}:${handle.id}`} size="sm" onClick={() => onExpand(handle.kind, handle.id)}>
                    <Link2 className="h-3.5 w-3.5" aria-hidden />
                    {handle.label || `${handle.kind}:${handle.id}`}
                  </Button>
                ))}
              </div>
            )}
            {Object.keys(expanded).length > 0 ? (
              <div className="mt-snug space-y-tight">
                {Object.entries(expanded).map(([key, value]) => (
                  <div key={key} className="rounded-card border border-border-subtle bg-surface-sunken/45 p-snug">
                    <p className="font-mono text-caption text-text-tertiary">{key}</p>
                    <pre className="mt-micro max-h-64 overflow-auto whitespace-pre-wrap font-mono text-caption text-text-secondary">
                      {JSON.stringify(value, null, 2)}
                    </pre>
                  </div>
                ))}
              </div>
            ) : null}
          </PanelSection>
        </>
      )}
    </div>
  );
}

// ---------------------------------------------------------------------------
// Tab 5 · 版本与队列
// ---------------------------------------------------------------------------

export function ReleasesTable({ releases }: { releases: Release[] }) {
  return (
    <DataTable caption="发布版本">
      <thead>
        <tr>
          <Th>序号</Th>
          <Th>发布</Th>
          <Th>文档 / 断言 / 关系 / 证据</Th>
          <Th>包含 / 本次写入</Th>
          <Th>状态</Th>
          <Th>发布时间</Th>
        </tr>
      </thead>
      <tbody>
        {releases.map((release) => (
          <tr key={release.id}>
            <Td mono>#{release.seq}</Td>
            <Td>
              <span className="font-mono text-caption text-text-primary">{release.id}</span>
              <span className="mt-micro block font-mono text-caption text-text-tertiary">
                快照 {shortDigest(release.snapshot_id, 14)} · 投影 {shortDigest(release.projection_digest, 14)}
              </span>
              {release.parent_release_id ? (
                <span className="mt-micro block font-mono text-caption text-text-tertiary">
                  父发布 {shortDigest(release.parent_release_id, 14)}
                </span>
              ) : null}
            </Td>
            <Td mono>
              <span className="block">
                {release.document_count} / {release.assertion_count} / {release.relation_count} /{' '}
                {release.evidence_count}
              </span>
              <span className="block text-caption text-text-tertiary">
                本次写入 文档 {release.written_document_count} · 断言 {release.written_assertion_count}
              </span>
            </Td>
            <Td>
              <span className="block text-caption text-text-secondary">
                {coverageLine(release.coverage, (release.coverage_state ?? 'reported') as CoverageState)}
              </span>
              {coverageGaps(release.coverage).length > 0 ? (
                <span className="mt-micro block text-caption text-status-warning">
                  缺口：{coverageGaps(release.coverage).join(' · ')}
                </span>
              ) : null}
              {release.notes ? (
                <span className="mt-micro block text-caption text-text-tertiary">{release.notes}</span>
              ) : null}
            </Td>
            <Td>
              <StateBadge tone={releaseStatusTone(release.status)}>{releaseStatusLabel(release.status)}</StateBadge>
            </Td>
            <Td mono>{formatDateTime(release.published_at)}</Td>
          </tr>
        ))}
      </tbody>
    </DataTable>
  );
}

export function TasksTable({
  tasks,
  busyTaskId,
  onOpen,
  onRetry,
  onCancel,
}: {
  tasks: TaskSummary[];
  busyTaskId: string | null;
  onOpen: (taskId: string) => void;
  onRetry: (taskId: string) => void;
  onCancel: (taskId: string) => void;
}) {
  return (
    <DataTable caption="资料写队列">
      <thead>
        <tr>
          <Th>序号</Th>
          <Th>类型</Th>
          <Th>状态</Th>
          <Th>尝试</Th>
          <Th>阻塞原因 / 最后错误</Th>
          <Th>更新时间</Th>
          <Th className="text-right">操作</Th>
        </tr>
      </thead>
      <tbody>
        {tasks.map((task) => {
          const busy = busyTaskId === task.id;
          const cancelState = taskCancelState(task.status);
          return (
            <tr key={task.id}>
              <Td mono>#{task.seq}</Td>
              <Td>
                <span className="font-mono text-caption text-text-primary">{task.kind}</span>
                <span className="mt-micro block font-mono text-caption text-text-tertiary">{task.id}</span>
              </Td>
              <Td>
                <StateBadge tone={taskStatusTone(task.status)}>{taskStatusLabel(task.status)}</StateBadge>
              </Td>
              <Td mono>
                {task.attempt}/{task.max_attempts}
                {task.max_repair_attempts > 0 ? ` · 修复 ${task.repair_attempt}/${task.max_repair_attempts}` : ''}
                {task.turn_seq > 0 ? ` · 轮次 ${task.turn_seq}` : ''}
              </Td>
              <Td>
                {task.blocked_reason ? <span className="block text-status-error">{task.blocked_reason}</span> : null}
                {task.last_error ? (
                  <span className="mt-micro block break-words font-mono text-caption text-text-tertiary">
                    {task.last_error}
                  </span>
                ) : null}
                {!task.blocked_reason && !task.last_error ? <span className="text-text-tertiary">—</span> : null}
              </Td>
              <Td mono>{formatDateTime(task.updated_at)}</Td>
              <Td className="text-right">
                <div className="inline-flex items-center gap-tight">
                  <Button size="sm" onClick={() => onOpen(task.id)} disabled={busy}>
                    详情
                  </Button>
                  <Button size="sm" onClick={() => onRetry(task.id)} disabled={busy || !task.blocked_reason}>
                    <RotateCcw className="h-3.5 w-3.5" aria-hidden />
                    重试
                  </Button>
                  <Button
                    size="sm"
                    variant="danger-outline"
                    onClick={() => onCancel(task.id)}
                    disabled={busy || !cancelState.cancellable}
                    title={cancelState.hint}
                  >
                    取消
                  </Button>
                </div>
              </Td>
            </tr>
          );
        })}
      </tbody>
    </DataTable>
  );
}

export function TaskDetailView({ detail }: { detail: TaskDetail }) {
  return (
    <div className="space-y-comfortable p-comfortable">
      <div className="flex flex-wrap items-center gap-tight">
        <StateBadge tone={taskStatusTone(detail.status)}>{taskStatusLabel(detail.status)}</StateBadge>
        <span className="font-mono text-caption text-text-secondary">
          #{detail.seq} · {detail.id}
        </span>
      </div>

      <dl className="grid grid-cols-2 gap-snug">
        <MetaItem label="类型" value={detail.kind} mono />
        <MetaItem label="视图" value={detail.view_id || '—'} mono />
        <MetaItem label="尝试" value={`${detail.attempt}/${detail.max_attempts}`} mono />
        <MetaItem label="修复尝试" value={`${detail.repair_attempt}/${detail.max_repair_attempts}`} mono />
        <MetaItem label="基础发布" value={detail.base_release_id ?? '—'} mono />
        <MetaItem label="目标发布" value={detail.target_release_id ?? '—'} mono />
        <MetaItem label="快照" value={detail.snapshot_id ?? '—'} mono />
        <MetaItem label="staging" value={detail.staging_path || '—'} mono />
        <MetaItem label="创建" value={formatDateTime(detail.created_at)} />
        <MetaItem label="更新" value={formatDateTime(detail.updated_at)} />
        <MetaItem label="结束" value={detail.finished_at ? formatDateTime(detail.finished_at) : '未结束'} />
      </dl>

      {detail.blocked_reason ? (
        <p
          className="rounded-card border border-status-error/30 bg-status-error/5 px-snug py-tight text-body text-status-error"
          role="alert"
        >
          阻塞原因：{detail.blocked_reason}
        </p>
      ) : null}
      {detail.last_error ? (
        <p className="break-words rounded-card border border-status-warning/30 bg-status-warning/5 px-snug py-tight font-mono text-caption text-status-warning">
          最后错误：{detail.last_error}
        </p>
      ) : null}

      <section>
        <h4 className="text-body font-semibold text-text-primary">轮次</h4>
        {detail.turns.length === 0 ? (
          <p className="mt-tight text-caption text-text-tertiary">还没有轮次记录。</p>
        ) : (
          <ol className="mt-tight space-y-micro">
            {detail.turns.map((turn) => (
              <li key={turn.id} className="rounded-card border border-border-subtle bg-surface-base px-snug py-tight">
                <div className="flex flex-wrap items-center gap-tight">
                  <span className="font-mono text-caption text-text-secondary">轮次 {turn.turn_seq}</span>
                  <StateBadge
                    tone={turn.status === 'completed' ? 'success' : turn.status === 'failed' ? 'error' : 'info'}
                  >
                    {turn.status === 'completed' ? '已完成' : turn.status === 'failed' ? '失败' : '进行中'}
                  </StateBadge>
                  <span className="text-caption text-text-tertiary">{turn.purpose || '未标注用途'}</span>
                </div>
                <p className="mt-micro font-mono text-caption text-text-tertiary">
                  run {turn.run_id} · {formatDateTime(turn.created_at)}
                </p>
                {turn.error_message ? (
                  <p className="mt-micro text-caption text-status-error">{turn.error_message}</p>
                ) : null}
              </li>
            ))}
          </ol>
        )}
      </section>

      <section>
        <h4 className="text-body font-semibold text-text-primary">覆盖</h4>
        <p className="mt-tight text-caption text-text-secondary">
          {coverageLine(detail.coverage, (detail.coverage_state ?? 'not_computed') as CoverageState)}
        </p>
        {coverageGaps(detail.coverage).length > 0 ? (
          <ul className="mt-micro space-y-micro">
            {coverageGaps(detail.coverage).map((gap, index) => (
              <li key={index} className="text-caption text-status-warning">
                · {gap}
              </li>
            ))}
          </ul>
        ) : null}
      </section>

      <section>
        <h4 className="text-body font-semibold text-text-primary">诊断</h4>
        {detail.diagnostics.length === 0 ? (
          <p className="mt-tight text-caption text-text-tertiary">没有诊断输出。</p>
        ) : (
          <ul className="mt-tight space-y-micro">
            {detail.diagnostics.map((item, index) => (
              <li key={index} className="break-words font-mono text-caption text-text-secondary">
                {item}
              </li>
            ))}
          </ul>
        )}
      </section>

      {Object.keys(detail.plan).length > 0 ? (
        <details className="rounded-card border border-border-subtle bg-surface-sunken/45 p-snug">
          <summary className="cursor-pointer text-caption text-text-secondary">诊断 · plan.json</summary>
          <pre className="mt-tight max-h-64 overflow-auto whitespace-pre-wrap font-mono text-caption text-text-tertiary">
            {JSON.stringify(detail.plan, null, 2)}
          </pre>
        </details>
      ) : null}
    </div>
  );
}

export function ReleasesPanel({
  status,
  releases,
  tasks,
  busyTaskId,
  onOpenTask,
  onRetryTask,
  onCancelTask,
  onReindex,
  onRefresh,
  reindexing,
}: {
  status: Async<LibraryStatus>;
  releases: Async<Paged<Release>>;
  tasks: Async<Paged<TaskSummary>>;
  busyTaskId: string | null;
  onOpenTask: (taskId: string) => void;
  onRetryTask: (taskId: string) => void;
  onCancelTask: (taskId: string) => void;
  onReindex: () => void;
  onRefresh: () => void;
  reindexing: boolean;
}) {
  return (
    <div className="space-y-comfortable">
      <PanelSection
        title="索引与运维"
        hint="索引是可重建投影；重建只从正式 Markdown 与来源账本恢复，不会改正文。"
        actions={
          <div className="flex items-center gap-tight">
            <Button size="sm" onClick={onRefresh}>
              <RefreshCw className="h-3.5 w-3.5" aria-hidden />
              刷新
            </Button>
            <Button size="sm" variant="ghost" onClick={onReindex} disabled={reindexing}>
              <Database className="h-3.5 w-3.5" aria-hidden />
              {reindexing ? '提交中…' : '重建索引'}
            </Button>
          </div>
        }
      >
        {status.kind === 'loading' ? (
          <PanelLoading label="队列观察面加载中" rows={2} />
        ) : status.kind === 'error' ? (
          <InlineError message={status.message} onRetry={onRefresh} />
        ) : (
          <dl className="grid grid-cols-2 gap-snug md:grid-cols-4">
            <MetaItem label="索引修订" value={status.value.index_revision} mono />
            <MetaItem label="待处理事件" value={status.value.pending_events} mono />
            <MetaItem
              label="队头任务"
              value={
                status.value.head_task
                  ? `#${status.value.head_task.seq} · ${taskStatusLabel(status.value.head_task.status)}`
                  : '空闲'
              }
            />
            <MetaItem label="阻塞任务" value={status.value.blocked_tasks.length} mono />
          </dl>
        )}
      </PanelSection>

      {status.kind === 'ready' && status.value.recent_errors.length > 0 ? (
        <PanelSection title="最近错误" hint="来自队列观察面；修好原因后可对阻塞任务重试。">
          <ul className="space-y-tight">
            {status.value.recent_errors.map((error) => (
              <li
                key={error.task_id}
                className="rounded-card border border-status-error/30 bg-status-error/5 px-snug py-tight"
              >
                <div className="flex flex-wrap items-center gap-tight">
                  <StateBadge tone="error">
                    #{error.seq} · {error.kind}
                  </StateBadge>
                  <span className="font-mono text-caption text-text-tertiary">{error.task_id}</span>
                  <span className="text-caption text-text-tertiary">{formatDateTime(error.updated_at)}</span>
                </div>
                {error.blocked_reason ? (
                  <p className="mt-micro text-body text-status-error">{error.blocked_reason}</p>
                ) : null}
                {error.last_error ? (
                  <p className="mt-micro break-words font-mono text-caption text-text-secondary">{error.last_error}</p>
                ) : null}
              </li>
            ))}
          </ul>
        </PanelSection>
      ) : null}

      <PanelSection title="发布版本" hint="每次发布都带覆盖清单；缺口会明确列出，不会假装完整。">
        {releases.kind === 'loading' ? (
          <PanelLoading label="发布列表加载中" rows={4} />
        ) : releases.kind === 'error' ? (
          <InlineError message={releases.message} onRetry={onRefresh} />
        ) : releases.value.items.length === 0 ? (
          <EmptyState
            icon={<Package className="h-5 w-5" aria-hidden />}
            title="还没有发布版本"
            description="第一次发布完成后，这里会显示文档、断言、关系与证据计数。"
          />
        ) : (
          <>
            <ReleasesTable releases={releases.value.items} />
            {releases.value.next_cursor ? (
              <p className="mt-tight text-caption text-text-tertiary" role="status">
                只显示最近 {releases.value.items.length} 个发布。
              </p>
            ) : null}
          </>
        )}
      </PanelSection>

      <PanelSection title="写队列" hint="同一时刻只有一个写入任务在执行；其余按序号等待。">
        {tasks.kind === 'loading' ? (
          <PanelLoading label="任务队列加载中" rows={5} />
        ) : tasks.kind === 'error' ? (
          <InlineError message={tasks.message} onRetry={onRefresh} />
        ) : tasks.value.items.length === 0 ? (
          <EmptyState
            icon={<Clock3 className="h-5 w-5" aria-hidden />}
            title="队列是空的"
            description="提交初始化或更新事件后，任务会按序号出现在这里。"
          />
        ) : (
          <>
            <TasksTable
              tasks={tasks.value.items}
              busyTaskId={busyTaskId}
              onOpen={onOpenTask}
              onRetry={onRetryTask}
              onCancel={onCancelTask}
            />
            {tasks.value.next_cursor ? (
              <p className="mt-tight text-caption text-text-tertiary" role="status">
                只显示最近 {tasks.value.items.length} 个任务。
              </p>
            ) : null}
          </>
        )}
      </PanelSection>
    </div>
  );
}

// ---------------------------------------------------------------------------
// 页面
// ---------------------------------------------------------------------------

export function KnowledgeTabList({
  active,
  onSelect,
}: {
  active: KnowledgeTab;
  onSelect: (tab: KnowledgeTab) => void;
}) {
  const move = (delta: number) => {
    const index = KNOWLEDGE_TABS.findIndex((tab) => tab.id === active);
    const next = KNOWLEDGE_TABS[(index + delta + KNOWLEDGE_TABS.length) % KNOWLEDGE_TABS.length];
    if (next) onSelect(next.id);
  };
  return (
    <div
      role="tablist"
      aria-label="资料库管理"
      className="flex flex-wrap gap-micro border-b border-border-subtle"
      onKeyDown={(event) => {
        if (event.key === 'ArrowRight') {
          event.preventDefault();
          move(1);
        }
        if (event.key === 'ArrowLeft') {
          event.preventDefault();
          move(-1);
        }
      }}
    >
      {KNOWLEDGE_TABS.map((tab) => {
        const selected = tab.id === active;
        return (
          <button
            key={tab.id}
            type="button"
            role="tab"
            id={`knowledge-tab-${tab.id}`}
            aria-selected={selected}
            aria-controls={`knowledge-panel-${tab.id}`}
            tabIndex={selected ? 0 : -1}
            onClick={() => onSelect(tab.id)}
            className={cx(
              'inline-flex min-h-8 items-center gap-micro rounded-t-button border-b-2 px-snug py-tight text-body transition-colors focus-visible:ring-2 focus-visible:ring-brand-primary/40',
              selected
                ? 'border-brand-primary text-text-primary'
                : 'border-transparent text-text-secondary hover:bg-surface-sunken hover:text-text-primary',
            )}
          >
            {tab.label}
          </button>
        );
      })}
    </div>
  );
}

export default function KnowledgePage() {
  const workspaceId = useWorkspaceStore((state) => state.workspace?.id ?? null);
  const workspaceName = useWorkspaceStore((state) => state.workspace?.name);
  const [searchParams, setSearchParams] = useSearchParams();
  const tab = parseKnowledgeTab(searchParams.get('tab'));
  const [refreshNonce, setRefreshNonce] = useState(0);
  const refresh = useCallback(() => setRefreshNonce((value) => value + 1), []);

  const [eventFilter, setEventFilter] = useState('');
  const [releaseFilter, setReleaseFilter] = useState('');
  const [docQuery, setDocQuery] = useState('');
  const [docKind, setDocKind] = useState('');
  const [docSearch, setDocSearch] = useState({ q: '', kind: '' });
  const [selectedDocumentId, setSelectedDocumentId] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [busyTaskId, setBusyTaskId] = useState<string | null>(null);
  const [receipt, setReceipt] = useState<LibraryEvent | null>(null);
  const [reindexing, setReindexing] = useState(false);
  const [queryState, setQueryState] = useState<{ kind: 'idle' } | Async<QueryResponse>>({ kind: 'idle' });
  const [expanded, setExpanded] = useState<Record<string, LibraryReceipt>>({});
  const [evidenceId, setEvidenceId] = useState<string | null>(null);
  const [evidenceState, setEvidenceState] = useState<Async<Evidence>>({ kind: 'loading' });
  const [taskId, setTaskId] = useState<string | null>(null);
  const [taskState, setTaskState] = useState<Async<TaskDetail>>({ kind: 'loading' });

  const statusKey = workspaceId ? `status:${workspaceId}:${refreshNonce}` : null;
  const sourcesKey = workspaceId && tab === 'library' ? `sources:${workspaceId}:${refreshNonce}` : null;
  const eventsKey = workspaceId && tab === 'sync' ? `events:${workspaceId}:${eventFilter}:${refreshNonce}` : null;
  const releasesKey =
    workspaceId && (tab === 'browse' || tab === 'query' || tab === 'releases')
      ? `releases:${workspaceId}:${refreshNonce}`
      : null;
  const documentsKey =
    workspaceId && tab === 'browse'
      ? `documents:${workspaceId}:${releaseFilter}:${docSearch.q}:${docSearch.kind}:${refreshNonce}`
      : null;
  const documentKey =
    workspaceId && tab === 'browse' && selectedDocumentId
      ? `document:${workspaceId}:${selectedDocumentId}:${releaseFilter}:${refreshNonce}`
      : null;
  const tasksKey = workspaceId && tab === 'releases' ? `tasks:${workspaceId}:${refreshNonce}` : null;

  const loadStatus = useCallback(() => getStatus(workspaceId!), [workspaceId]);
  const loadSources = useCallback(() => listSources(workspaceId!), [workspaceId]);
  const loadEvents = useCallback(
    () => listEvents(workspaceId!, { status: eventFilter || undefined, limit: 50 }),
    [workspaceId, eventFilter],
  );
  const loadReleases = useCallback(() => listReleases(workspaceId!, { limit: 50 }), [workspaceId]);
  const loadDocuments = useCallback(
    () =>
      listDocuments(workspaceId!, {
        release_id: releaseFilter || undefined,
        q: docSearch.q || undefined,
        kind: docSearch.kind || undefined,
        limit: 100,
      }),
    [workspaceId, releaseFilter, docSearch],
  );
  const loadDocument = useCallback(
    () => getDocument(workspaceId!, selectedDocumentId!, releaseFilter || undefined),
    [workspaceId, selectedDocumentId, releaseFilter],
  );
  const loadTasks = useCallback(() => listTasks(workspaceId!, { limit: 50 }), [workspaceId]);

  const status = useAsyncData(statusKey, workspaceId ? loadStatus : null);
  const sources = useAsyncData(sourcesKey, workspaceId && tab === 'library' ? loadSources : null);
  const events = useAsyncData(eventsKey, workspaceId && tab === 'sync' ? loadEvents : null);
  const releases = useAsyncData(releasesKey, workspaceId ? loadReleases : null);
  const documents = useAsyncData(documentsKey, workspaceId && tab === 'browse' ? loadDocuments : null);
  const document = useAsyncData(documentKey, workspaceId && selectedDocumentId ? loadDocument : null);
  const tasks = useAsyncData(tasksKey, workspaceId && tab === 'releases' ? loadTasks : null);

  useEffect(() => {
    setSelectedDocumentId(null);
    setDocSearch({ q: '', kind: '' });
    setDocQuery('');
    setDocKind('');
    setReleaseFilter('');
  }, [workspaceId]);

  useEffect(() => {
    if (tab !== 'browse') setSelectedDocumentId(null);
  }, [tab]);

  useEffect(() => {
    if (!evidenceId || !workspaceId) return;
    const scope = captureScope();
    let cancelled = false;
    setEvidenceState({ kind: 'loading' });
    getEvidence(workspaceId, evidenceId)
      .then((value) => {
        if (!cancelled && isCurrent(scope)) setEvidenceState({ kind: 'ready', value });
      })
      .catch((error: unknown) => {
        if (!cancelled && isCurrent(scope)) {
          setEvidenceState({ kind: 'error', message: apiErrorMessage(error, '证据读取失败') });
        }
      });
    return () => {
      cancelled = true;
    };
  }, [evidenceId, workspaceId]);

  useEffect(() => {
    if (!taskId || !workspaceId) return;
    const scope = captureScope();
    let cancelled = false;
    setTaskState({ kind: 'loading' });
    getTask(workspaceId, taskId)
      .then((value) => {
        if (!cancelled && isCurrent(scope)) setTaskState({ kind: 'ready', value });
      })
      .catch((error: unknown) => {
        if (!cancelled && isCurrent(scope)) {
          setTaskState({ kind: 'error', message: apiErrorMessage(error, '任务详情读取失败') });
        }
      });
    return () => {
      cancelled = true;
    };
  }, [taskId, workspaceId]);

  const selectTab = (next: KnowledgeTab) => {
    setSearchParams(
      (previous) => {
        const params = new URLSearchParams(previous);
        if (next === 'library') params.delete('tab');
        else params.set('tab', next);
        return params;
      },
      { replace: true },
    );
  };

  /** 返回是否成功：失败时调用方保留用户输入，不清空表单。 */
  const runAction = async (action: () => Promise<void>, successMessage: string, fallback: string): Promise<boolean> => {
    setBusy(true);
    try {
      await action();
      toast.success(successMessage);
      refresh();
      return true;
    } catch (error) {
      toast.error(apiErrorMessage(error, fallback));
      return false;
    } finally {
      setBusy(false);
    }
  };

  if (!workspaceId) {
    return (
      <main className="page-shell" aria-label="资料库管理">
        <header className="page-header">
          <h1 className="page-title">资料库</h1>
          <p className="page-subtitle mt-1">工作区准备完成后，这里会显示资料库根路径与来源。</p>
        </header>
        <Card padded>
          <EmptyState
            icon={<Database className="h-5 w-5" aria-hidden />}
            title="等待工作区"
            description="选中一个工作区后即可管理它的资料库。"
          />
        </Card>
      </main>
    );
  }

  return (
    <main className="page-shell" aria-label="资料库管理">
      <header className="page-header">
        <div className="min-w-0">
          <p className="text-caption font-medium uppercase tracking-wider text-brand-primary">知识管理员</p>
          <h1 className="page-title">资料库</h1>
          <p className="page-subtitle mt-1">
            管理 {workspaceName ?? workspaceId} 的资料库：来源、异步更新、知识浏览、查询与队列。
          </p>
        </div>
        <div className="flex shrink-0 flex-wrap items-center gap-tight">
          {status.kind === 'ready' ? (
            <>
              <StateBadge tone={status.value.library.enabled ? 'success' : 'neutral'}>
                {status.value.library.enabled ? '资料库已启用' : '资料库未启用'}
              </StateBadge>
              <StateBadge tone={status.value.blocked_tasks.length > 0 ? 'error' : 'success'}>
                阻塞 {status.value.blocked_tasks.length}
              </StateBadge>
              <StateBadge tone={status.value.pending_events > 0 ? 'warning' : 'neutral'}>
                待处理事件 {status.value.pending_events}
              </StateBadge>
            </>
          ) : status.kind === 'error' ? (
            <StateBadge tone="error">观察面读取失败</StateBadge>
          ) : (
            <StatusPill>
              <Clock3 className="h-3.5 w-3.5 text-brand-primary" aria-hidden />
              观察面读取中
            </StatusPill>
          )}
          <Button onClick={refresh}>
            <RefreshCw className="h-4 w-4" aria-hidden />
            刷新
          </Button>
        </div>
      </header>

      <KnowledgeTabList active={tab} onSelect={selectTab} />

      <div
        role="tabpanel"
        id={`knowledge-panel-${tab}`}
        aria-labelledby={`knowledge-tab-${tab}`}
        tabIndex={-1}
        className="mt-comfortable outline-none"
      >
        {tab === 'library' ? (
          <LibraryPanel
            summary={status.kind === 'ready' ? { kind: 'ready', value: status.value.library } : status}
            sources={sources}
            busy={busy}
            onRefresh={refresh}
            onCreate={(draft) =>
              runAction(
                async () => {
                  await createSource(workspaceId, sourceCreatePayload(draft));
                },
                `已登记来源「${draft.name.trim()}」`,
                '登记来源失败',
              )
            }
            onUpdate={(source, draft) =>
              runAction(
                async () => {
                  await updateSource(workspaceId, source.id, sourceUpdatePayload(source, draft));
                },
                `已保存来源「${draft.name.trim()}」`,
                '保存来源失败',
              )
            }
            onDisable={(source) =>
              void runAction(
                async () => {
                  await deleteSource(workspaceId, source.id);
                },
                `已停用来源「${source.name}」`,
                '停用来源失败',
              )
            }
          />
        ) : null}

        {tab === 'sync' ? (
          <SyncPanel
            events={events}
            busy={busy}
            receipt={receipt}
            eventStatusFilter={eventFilter}
            onEventStatusFilterChange={setEventFilter}
            onRefresh={refresh}
            onInitialize={(reason) =>
              void runAction(
                async () => {
                  const event = await initializeLibrary(workspaceId, {
                    client_key: newIdempotencyKey(),
                    reason: reason.trim() || undefined,
                  });
                  setReceipt(event);
                },
                '初始化已提交，后台队列会异步执行',
                '提交初始化失败',
              )
            }
            onSubmitEvent={(input) =>
              void runAction(
                async () => {
                  const event = await submitEvent(workspaceId, {
                    event_type: input.eventType,
                    source: input.source.trim(),
                    content_ref: input.contentRef.trim() || undefined,
                    payload: input.payload.trim() ? (JSON.parse(input.payload) as Record<string, unknown>) : undefined,
                    client_key: newIdempotencyKey(),
                  });
                  setReceipt(event);
                },
                '事件已受理（已受理 ≠ 已更新）',
                '提交事件失败',
              )
            }
          />
        ) : null}

        {tab === 'browse' ? (
          <BrowsePanel
            releases={releases}
            documents={documents}
            detail={selectedDocumentId ? document : null}
            selectedReleaseId={releaseFilter}
            onSelectRelease={(releaseId) => {
              setReleaseFilter(releaseId);
              setSelectedDocumentId(null);
            }}
            query={docQuery}
            kind={docKind}
            onQueryChange={setDocQuery}
            onKindChange={setDocKind}
            onSearch={() => setDocSearch({ q: docQuery.trim(), kind: docKind.trim() })}
            selectedDocumentId={selectedDocumentId}
            onSelectDocument={setSelectedDocumentId}
            onSelectEvidence={setEvidenceId}
            onRefresh={refresh}
          />
        ) : null}

        {tab === 'query' ? (
          <QueryPanel
            releases={releases}
            state={queryState}
            expanded={expanded}
            onRefresh={refresh}
            onSelectEvidence={setEvidenceId}
            onExpand={(kind, id) => {
              // Expand inside the release the answer is pinned to: a handle
              // from a historical release must open that release's object.
              const pinnedReleaseId =
                queryState.kind === 'ready' ? queryState.value.release?.id : undefined;
              const key = `${pinnedReleaseId ?? 'current'}:${kind}:${id}`;
              void (async () => {
                try {
                  const value = await expandHandle(workspaceId, kind, id, pinnedReleaseId);
                  setExpanded((current) => ({ ...current, [key]: value }));
                } catch (error) {
                  toast.error(apiErrorMessage(error, '展开句柄失败'));
                }
              })();
            }}
            onQuery={({ question, terms, releaseId, limit }) => {
              const trimmed = question.trim();
              if (!trimmed) return;
              const parsedLimit = Number.parseInt(limit, 10);
              void (async () => {
                setQueryState({ kind: 'loading' });
                try {
                  const value = await queryLibrary(workspaceId, {
                    question: trimmed,
                    terms: terms
                      .split(/[,，\s]+/)
                      .map((term) => term.trim())
                      .filter(Boolean),
                    release_id: releaseId || undefined,
                    limit: parsedLimit > 0 ? parsedLimit : undefined,
                  });
                  setQueryState({ kind: 'ready', value });
                  // Expansions belong to the release that was queried; a new
                  // answer must not leave another release's objects on screen.
                  setExpanded({});
                } catch (error) {
                  setQueryState({ kind: 'error', message: apiErrorMessage(error, '查询失败') });
                }
              })();
            }}
          />
        ) : null}

        {tab === 'releases' ? (
          <ReleasesPanel
            status={status}
            releases={releases}
            tasks={tasks}
            busyTaskId={busyTaskId}
            reindexing={reindexing}
            onRefresh={refresh}
            onOpenTask={setTaskId}
            onRetryTask={(id) => {
              setBusyTaskId(id);
              void (async () => {
                try {
                  await retryTask(workspaceId, id);
                  toast.success('已提交重试');
                  refresh();
                } catch (error) {
                  toast.error(apiErrorMessage(error, '重试失败'));
                } finally {
                  setBusyTaskId(null);
                }
              })();
            }}
            onCancelTask={(id) => {
              setBusyTaskId(id);
              void (async () => {
                try {
                  await cancelTask(workspaceId, id);
                  toast.success('已取消任务');
                  refresh();
                } catch (error) {
                  toast.error(apiErrorMessage(error, '取消失败'));
                } finally {
                  setBusyTaskId(null);
                }
              })();
            }}
            onReindex={() => {
              setReindexing(true);
              void (async () => {
                try {
                  const receipt = await reindex(workspaceId);
                  const seq = typeof receipt.queue_seq === 'number' ? receipt.queue_seq : null;
                  toast.success(
                    seq === null ? '索引重建已进入写入队列' : `索引重建已排队（第 ${seq} 位），轮到后重建`,
                  );
                  refresh();
                } catch (error) {
                  toast.error(apiErrorMessage(error, '提交索引重建失败'));
                } finally {
                  setReindexing(false);
                }
              })();
            }}
          />
        ) : null}
      </div>

      {evidenceId !== null ? (
        <Drawer open onClose={() => setEvidenceId(null)} title="证据详情" width={560}>
          {evidenceState.kind === 'loading' ? (
            <PanelLoading label="证据加载中" rows={6} />
          ) : evidenceState.kind === 'error' ? (
            <div className="p-comfortable">
              <InlineError message={evidenceState.message} />
            </div>
          ) : (
            <div className="p-comfortable">
              <EvidenceDetailCard evidence={evidenceState.value} />
            </div>
          )}
        </Drawer>
      ) : null}

      {taskId !== null ? (
        <Drawer open onClose={() => setTaskId(null)} title="任务详情" width={620}>
          {taskState.kind === 'loading' ? (
            <PanelLoading label="任务详情加载中" rows={6} />
          ) : taskState.kind === 'error' ? (
            <div className="p-comfortable">
              <InlineError message={taskState.message} />
            </div>
          ) : (
            <TaskDetailView detail={taskState.value} />
          )}
        </Drawer>
      ) : null}
    </main>
  );
}
