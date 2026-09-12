import { apiFetch } from './client';

/**
 * 资料库（知识管理员）HTTP 客户端。
 *
 * 契约：`notes/implemented/architecture/2026-09-12-knowledge-library-rebuild.md` §5，
 * 前缀 `/api/v1/workspaces/{workspace_id}/library`；列表端点统一返回 `{ items: [...] }`。
 * 写命令的幂等键优先复用请求体里的 `client_key`，重复提交不会产生第二个事件。
 */

export interface LibrarySummary {
  workspace_id: string;
  root_path: string;
  enabled: boolean;
  current_release: Release | null;
  release_count: number;
  document_count: number;
  source_count: number;
  index_revision: number;
  updated_at: string;
  queue: { pending: number; blocked: number; head_seq: number | null; running: TaskSummary | null };
}

/**
 * `artifact` 的来路。`declared` 是默认值：人工登记的值是**声明**，不是构建
 * 实际解析到的版本；只有真实构建/依赖解析才能标 `resolved`，没有映射则 `unknown`。
 */
export type ArtifactResolution = 'declared' | 'resolved' | 'unknown';

/**
 * 一个逻辑来源的一次具体使用：哪个消费方（业务服务或模块）依赖哪个制品版本。
 * 共享库（common）通常有多条 usage，各消费方的依赖版本可以不同。
 */
export interface SourceUsage {
  consumer: string;
  artifact: string;
  environment?: string;
  /**
   * 有效状态。本版本没有依赖解析能力，所以 resolved 永远不是有效状态：
   * 服务端只会给出 declared / unknown。
   */
  artifact_resolution?: ArtifactResolution;
  /** 调用方声明的状态（可能强于有效状态），仅用于展示。 */
  claimed_resolution?: ArtifactResolution;
  /** 调用方给出的引用；未经解析核实，只作登记备注保留。 */
  resolution_ref?: string;
  /** 服务端是否把声明降级为登记值。 */
  downgraded?: boolean;
}

export interface Source {
  id: string;
  name: string;
  kind: 'service' | 'common' | 'documents' | 'other';
  repo_path: string;
  default_ref: string;
  /** 空数组 = 一个隐式使用方（后端语义）；读取路径统一归一化为真数组。 */
  usages: SourceUsage[];
  enabled: boolean;
  version: number;
  created_at: string;
  updated_at: string;
}

export interface LibraryEvent {
  id: string;
  event_type: string;
  source: string;
  subject: Record<string, unknown>;
  content_ref: string;
  status: 'accepted' | 'queued' | 'processing' | 'completed' | 'failed' | 'blocked';
  task_id?: string;
  received_at: string;
  updated_at: string;
  client_key: string;
}

/**
 * 覆盖状态由服务端判定：pending / in_progress / not_computed / reported。
 * 只有 reported 才能说“已报告无缺口”；not_computed 表示资料库没有记录覆盖。
 */
export type CoverageState = 'pending' | 'in_progress' | 'not_computed' | 'reported';

export interface TaskSummary {
  coverage_state?: CoverageState;
  id: string;
  seq: number;
  kind: string;
  status: string;
  view_id: string;
  attempt: number;
  max_attempts: number;
  repair_attempt: number;
  max_repair_attempts: number;
  base_release_id?: string;
  target_release_id?: string;
  snapshot_id?: string;
  turn_seq: number;
  last_error: string;
  blocked_reason: string;
  created_at: string;
  updated_at: string;
  finished_at?: string;
}

export interface TaskDetail extends TaskSummary {
  staging_path: string;
  plan: Record<string, unknown>;
  coverage: Coverage;
  turns: TurnSummary[];
  diagnostics: string[];
}

export interface TurnSummary {
  id: string;
  turn_seq: number;
  run_id: string;
  purpose: string;
  status: 'pending' | 'completed' | 'failed';
  error_message: string;
  created_at: string;
  updated_at: string;
}

export interface Coverage {
  sources_read?: string[];
  sources_missed?: string[];
  notes?: string;
  gaps?: string[];
}

export interface Release {
  id: string;
  seq: number;
  snapshot_id: string;
  task_id?: string;
  parent_release_id?: string;
  projection_digest: string;
  document_count: number;
  assertion_count: number;
  relation_count: number;
  evidence_count: number;
  coverage: Coverage;
  coverage_state?: CoverageState;
  notes: string;
  status: 'published' | 'superseded';
  published_at: string;
}

/** 发布详情：除发布本身外还带回该发布的文档清单。 */
export interface ReleaseDetail extends Release {
  documents: DocumentSummary[];
}

export interface DocumentSummary {
  id: string;
  path: string;
  kind: string;
  title: string;
  summary: string;
  domains: string[];
  status: 'active' | 'removed' | 'renamed';
  renamed_from: string;
  version: number;
  release_id?: string;
  updated_at: string;
}

export interface EvidenceRef {
  evidence_id: string;
  role: 'supports' | 'contradicts' | 'context';
}

export interface Scope {
  conditions: string[];
  environments: string[];
  valid_from?: string;
  valid_until?: string;
}

export interface Assertion {
  id: string;
  document_id: string;
  document_version_id: string;
  heading: string;
  about: string[];
  perspective: 'normative' | 'descriptive';
  basis: 'source_statement' | 'code_static' | 'runtime_observed' | 'inferred';
  statement: string;
  scope: Scope;
  evidence: EvidenceRef[];
  unknown_notes: string;
}

export interface Endpoint {
  kind: 'entity' | 'document' | 'assertion';
  id: string;
}

export interface Relation {
  id: string;
  document_id: string;
  from: Endpoint;
  predicate: string;
  to: Endpoint;
  to_resolved: boolean;
  to_raw: string;
  perspective: 'normative' | 'descriptive';
  basis: string;
  condition: string;
  evidence: EvidenceRef[];
}

export interface DocumentVersionMeta {
  id: string;
  version: number;
  content_digest: string;
  created_at: string;
}

export interface DocumentDetail {
  document: DocumentSummary;
  version: { id: string; version: number; content_digest: string; created_at: string; content_markdown: string };
  assertions: Assertion[];
  relations: Relation[];
  versions: DocumentVersionMeta[];
}

export interface EvidenceBinding {
  source_name: string;
  source_kind: string;
  repo_path: string;
  git_ref: string;
  commit_sha: string;
  dirty: boolean;
  artifact: string;
  consumer: string;
}

export interface EvidenceRepresentation {
  path: string;
  media_type: string;
  encoding: string;
  digest_algo: string;
  content_digest: string;
  byte_size: number;
  coverage: string;
  origin: 'committed' | 'worktree';
  stored_path: string;
}

export interface Evidence {
  id: string;
  snapshot_id: string;
  binding_id: string;
  representation_id: string;
  locator: Record<string, unknown>;
  locator_kind: string;
  excerpt: string;
  excerpt_digest: string;
  match_count: number;
  availability: 'available' | 'unavailable';
  collected_at: string;
  binding: EvidenceBinding;
  representation: EvidenceRepresentation;
}

export interface QueryResult {
  assertion: Assertion;
  document: { id: string; path: string; title: string };
  score: number;
  snippet: string;
  evidence: Evidence[];
}

export interface QueryResponse {
  release: Release | null;
  results: QueryResult[];
  coverage: { status: 'complete' | 'partial' | 'not_ready'; truncated: boolean; scanned_versions: number; notes: string[] };
  freshness: {
    release_id?: string;
    published_at?: string;
    pending_events: number;
    newer_release_available: boolean;
    stale_sources: string[];
  };
  unknowns: string[];
  expand_handles: { kind: string; id: string; label: string }[];
}

export interface GraphResponse {
  release_id: string;
  entities: { id: string; kind: string; namespace: string; canonical_name: string; aliases: string[]; document_id?: string }[];
  relations: Relation[];
  documents: DocumentSummary[];
}

export interface BridgeView {
  id: string;
  release_id: string;
  base_release_id?: string;
  anchor_entity_id: string;
  title: string;
  summary: string;
  steps: unknown[];
  participants: unknown[];
  coverage: Coverage;
  gaps: string[];
  builder_version: string;
  content_digest: string;
  created_at: string;
}

export interface LibraryStatus {
  library: LibrarySummary;
  head_task: TaskSummary | null;
  blocked_tasks: TaskSummary[];
  recent_errors: { task_id: string; seq: number; kind: string; last_error: string; blocked_reason: string; updated_at: string }[];
  last_release: Release | null;
  pending_events: number;
  index_revision: number;
  freshness: QueryResponse['freshness'];
}

export interface Paged<T> {
  items: T[];
  next_cursor?: string;
}

export type SourceKind = Source['kind'];

export interface CreateSourceInput {
  name: string;
  kind: SourceKind;
  repo_path: string;
  default_ref?: string;
  /** 空数组表示一个隐式使用方；缺省字段与空数组同义。 */
  usages?: SourceUsage[];
  include_globs?: string[];
  exclude_globs?: string[];
}

export interface UpdateSourceInput extends Partial<CreateSourceInput> {
  enabled?: boolean;
  expected_version: number;
}

export interface SubmitEventInput {
  event_type: string;
  source: string;
  subject?: Record<string, unknown>;
  content_ref?: string;
  payload?: Record<string, unknown>;
  client_key: string;
}

export interface InitializeLibraryInput {
  client_key: string;
  view_id?: string;
  reason?: string;
}

export interface QueryLibraryInput {
  question: string;
  terms?: string[];
  release_id?: string;
  limit?: number;
}

/** `/expand` 的响应体未在契约中固定字段，按原样透传由调用方展示。 */
export type LibraryReceipt = Record<string, unknown>;

/**
 * 索引重建的回执：重建是写入队列里的一次写入，接口只返回排队结果，
 * 不代表索引已经与已发布版本一致。
 */
export interface ReindexReceipt {
  task_id: string;
  accepted: boolean;
  queue_seq: number;
  status: string;
  enqueued_at?: string;
}

const segment = (value: string): string => encodeURIComponent(value);

const libraryRoot = (workspaceId: string): string => `/workspaces/${segment(workspaceId)}/library`;

function withQuery(path: string, values: Record<string, string | number | undefined>): string {
  const params = new URLSearchParams();
  Object.entries(values).forEach(([key, value]) => {
    if (value !== undefined && value !== '') params.set(key, String(value));
  });
  const query = params.toString();
  return query ? `${path}?${query}` : path;
}

export const getLibrary = async (workspaceId: string): Promise<LibrarySummary> =>
  normalizeLibrarySummary(await apiFetch<LibrarySummary>(libraryRoot(workspaceId)));

export const listSources = async (workspaceId: string): Promise<Paged<Source>> =>
  normalizeSourceList(await apiFetch<Paged<Source>>(`${libraryRoot(workspaceId)}/sources`));

export const createSource = async (workspaceId: string, body: CreateSourceInput): Promise<Source> =>
  normalizeSource(await apiFetch<Source>(`${libraryRoot(workspaceId)}/sources`, { method: 'POST', body }));

export const updateSource = async (workspaceId: string, sourceId: string, body: UpdateSourceInput): Promise<Source> =>
  normalizeSource(
    await apiFetch<Source>(`${libraryRoot(workspaceId)}/sources/${segment(sourceId)}`, { method: 'PATCH', body }),
  );

export const deleteSource = (workspaceId: string, sourceId: string) =>
  apiFetch<void>(`${libraryRoot(workspaceId)}/sources/${segment(sourceId)}`, { method: 'DELETE' });

export const listEvents = (workspaceId: string, filter: { status?: string; limit?: number } = {}) =>
  apiFetch<Paged<LibraryEvent>>(
    withQuery(`${libraryRoot(workspaceId)}/events`, { status: filter.status, limit: filter.limit }),
  );

export const submitEvent = (workspaceId: string, body: SubmitEventInput) =>
  apiFetch<LibraryEvent>(`${libraryRoot(workspaceId)}/events`, {
    method: 'POST',
    body,
    idempotencyKey: body.client_key,
  });

export const initializeLibrary = (workspaceId: string, body: InitializeLibraryInput) =>
  apiFetch<LibraryEvent>(`${libraryRoot(workspaceId)}/initialize`, {
    method: 'POST',
    body,
    idempotencyKey: body.client_key,
  });

export const listTasks = async (
  workspaceId: string,
  filter: { status?: string; limit?: number } = {},
): Promise<Paged<TaskSummary>> =>
  normalizePaged(
    await apiFetch<Paged<TaskSummary>>(
      withQuery(`${libraryRoot(workspaceId)}/tasks`, { status: filter.status, limit: filter.limit }),
    ),
  );

export const getTask = async (workspaceId: string, taskId: string): Promise<TaskDetail> =>
  normalizeTaskDetail(await apiFetch<TaskDetail>(`${libraryRoot(workspaceId)}/tasks/${segment(taskId)}`));

export const retryTask = (workspaceId: string, taskId: string) =>
  apiFetch<void>(`${libraryRoot(workspaceId)}/tasks/${segment(taskId)}/retry`, { method: 'POST' });

export const cancelTask = (workspaceId: string, taskId: string) =>
  apiFetch<void>(`${libraryRoot(workspaceId)}/tasks/${segment(taskId)}/cancel`, { method: 'POST' });

export const listReleases = async (
  workspaceId: string,
  filter: { limit?: number } = {},
): Promise<Paged<Release>> =>
  normalizePaged(await apiFetch<Paged<Release>>(withQuery(`${libraryRoot(workspaceId)}/releases`, { limit: filter.limit })));

export const getRelease = async (workspaceId: string, releaseId: string): Promise<ReleaseDetail> =>
  normalizeReleaseDetail(await apiFetch<ReleaseDetail>(`${libraryRoot(workspaceId)}/releases/${segment(releaseId)}`));

export const listDocuments = (
  workspaceId: string,
  filter: { release_id?: string; q?: string; kind?: string; limit?: number; cursor?: string } = {},
) =>
  apiFetch<Paged<DocumentSummary>>(
    withQuery(`${libraryRoot(workspaceId)}/documents`, {
      release_id: filter.release_id,
      q: filter.q,
      kind: filter.kind,
      limit: filter.limit,
      cursor: filter.cursor,
    }),
  );

export const getDocument = (workspaceId: string, documentId: string, releaseId?: string) =>
  apiFetch<DocumentDetail>(
    withQuery(`${libraryRoot(workspaceId)}/documents/${segment(documentId)}`, { release_id: releaseId }),
  );

export const getEvidence = async (workspaceId: string, evidenceId: string): Promise<Evidence> =>
  normalizeEvidence(await apiFetch<Evidence>(`${libraryRoot(workspaceId)}/evidence/${segment(evidenceId)}`));

export const getGraph = (workspaceId: string, releaseId?: string) =>
  apiFetch<GraphResponse>(withQuery(`${libraryRoot(workspaceId)}/graph`, { release_id: releaseId }));

export const listBridges = async (workspaceId: string, releaseId?: string): Promise<Paged<BridgeView>> =>
  normalizePaged(
    await apiFetch<Paged<BridgeView>>(withQuery(`${libraryRoot(workspaceId)}/bridges`, { release_id: releaseId })),
  );

/**
 * 读接口的归一化：把 null / 缺字段 / 形状不一致的响应收敛成契约里的形状，
 * 让页面在“空结果”和“结构化诊断”下都能渲染，而不是白屏。
 * 这些函数对真实接口返回的每个列表字段都成立，不是只为理想 mock 写的。
 */
const asArray = <T,>(value: T[] | null | undefined): T[] => (Array.isArray(value) ? value : []);

const asStrings = (value: unknown): string[] => {
  if (Array.isArray(value)) {
    return value.map((item) => (typeof item === 'string' ? item : JSON.stringify(item)));
  }
  if (value === null || value === undefined) return [];
  return [typeof value === 'string' ? value : JSON.stringify(value)];
};

const asObject = <T extends object>(value: T | null | undefined): T => (value && typeof value === 'object' ? value : ({} as T));

export const normalizeTaskSummary = (raw: TaskSummary): TaskSummary => ({
  ...raw,
  coverage_state: raw?.coverage_state,
});

export const normalizeTaskDetail = (raw: TaskDetail): TaskDetail => ({
  ...normalizeTaskSummary(raw),
  staging_path: raw?.staging_path ?? '',
  plan: asObject(raw?.plan),
  coverage: asObject(raw?.coverage) as TaskDetail['coverage'],
  // 后端可能返回 []、对象或字符串；这里统一成字符串数组。
  diagnostics: asStrings(raw?.diagnostics),
  turns: asArray(raw?.turns),
});

export const normalizeRelease = (raw: Release): Release => ({
  ...raw,
  coverage: asObject(raw?.coverage) as Release['coverage'],
  coverage_state: raw?.coverage_state,
  notes: raw?.notes ?? '',
});

export const normalizeReleaseDetail = (raw: ReleaseDetail): ReleaseDetail => ({
  ...normalizeRelease(raw),
  documents: asArray(raw?.documents),
});

export const normalizeEvidence = (raw: Evidence): Evidence => ({
  ...raw,
  locator: asObject(raw?.locator),
  excerpt: raw?.excerpt ?? '',
  binding: asObject(raw?.binding) as Evidence['binding'],
  representation: asObject(raw?.representation) as Evidence['representation'],
});

export const normalizeFreshness = (raw: QueryResponse['freshness'] | null | undefined): QueryResponse['freshness'] => ({
  release_id: raw?.release_id,
  published_at: raw?.published_at,
  pending_events: raw?.pending_events ?? 0,
  newer_release_available: Boolean(raw?.newer_release_available),
  stale_sources: asArray(raw?.stale_sources),
});

export const normalizeLibrarySummary = (raw: LibrarySummary | null | undefined): LibrarySummary =>
  ({
    ...(raw ?? {}),
    queue: {
      pending: raw?.queue?.pending ?? 0,
      blocked: raw?.queue?.blocked ?? 0,
      head_seq: raw?.queue?.head_seq ?? null,
      running: raw?.queue?.running ?? null,
    },
    current_release: raw?.current_release ? normalizeRelease(raw.current_release) : null,
  }) as LibrarySummary;

export const normalizePaged = <T,>(raw: Paged<T> | null | undefined): Paged<T> => ({
  ...(raw ?? ({} as Paged<T>)),
  items: asArray(raw?.items),
});

/**
 * 解析标注：缺省与非法值一律落到 `declared`，`resolved` 也不会被当成有效状态——
 * 本版本没有依赖解析能力，人工登记的值不能被呈现为已核实。
 */
export function artifactResolutionOf(
  usage: { artifact_resolution?: ArtifactResolution } | null | undefined,
): ArtifactResolution {
  const raw = usage?.artifact_resolution;
  return raw === 'resolved' || raw === 'unknown' ? raw : 'declared';
}

function normalizeUsage(usage: SourceUsage | null | undefined): SourceUsage {
  const raw: Partial<SourceUsage> = usage ?? {};
  return {
    consumer: typeof raw.consumer === 'string' ? raw.consumer : '',
    artifact: typeof raw.artifact === 'string' ? raw.artifact : '',
    environment: typeof raw.environment === 'string' ? raw.environment : '',
    artifact_resolution: artifactResolutionOf(raw),
    claimed_resolution: raw.claimed_resolution,
    resolution_ref: typeof raw.resolution_ref === 'string' ? raw.resolution_ref : '',
    downgraded: raw.downgraded,
  };
}

/**
 * 来源读取路径的统一归一化：`usages` 永远是真正的数组（空数组 = 一个隐式使用方），
 * 缺字段的旧记录不会让表格或表单在渲染时炸掉。
 */
export function normalizeSource(raw: Source): Source {
  return { ...raw, usages: asArray(raw?.usages).map(normalizeUsage) };
}

/** 列表端点：`items` 与其中每条来源的 `usages` 一起归一化。 */
export function normalizeSourceList(raw: Paged<Source>): Paged<Source> {
  return { ...raw, items: asArray(raw?.items).map(normalizeSource) };
}

export const normalizeQueryResponse = (raw: QueryResponse): QueryResponse => ({
  ...raw,
  release: raw?.release ?? null,
  results: asArray(raw?.results),
  coverage: {
    status: raw?.coverage?.status ?? 'not_ready',
    truncated: Boolean(raw?.coverage?.truncated),
    scanned_versions: raw?.coverage?.scanned_versions ?? 0,
    notes: asArray(raw?.coverage?.notes),
  },
  freshness: {
    release_id: raw?.freshness?.release_id,
    published_at: raw?.freshness?.published_at,
    pending_events: raw?.freshness?.pending_events ?? 0,
    newer_release_available: Boolean(raw?.freshness?.newer_release_available),
    stale_sources: asArray(raw?.freshness?.stale_sources),
  },
  unknowns: asArray(raw?.unknowns),
  expand_handles: asArray(raw?.expand_handles),
});

export const queryLibrary = async (workspaceId: string, body: QueryLibraryInput): Promise<QueryResponse> =>
  normalizeQueryResponse(await apiFetch<QueryResponse>(`${libraryRoot(workspaceId)}/query`, { method: 'POST', body }));

/**
 * 展开一个句柄。releaseId 必须传当前正在查看的发布：展开是固定版本的读取，
 * 不传就会落到当前发布，把历史问题回答成今天的知识。
 */
export const expandHandle = (workspaceId: string, kind: string, id: string, releaseId?: string) =>
  apiFetch<LibraryReceipt>(withQuery(`${libraryRoot(workspaceId)}/expand`, { kind, id, release_id: releaseId }));

export const getStatus = async (workspaceId: string): Promise<LibraryStatus> => {
  const raw = await apiFetch<LibraryStatus>(`${libraryRoot(workspaceId)}/status`);
  return {
    ...raw,
    library: normalizeLibrarySummary(raw?.library),
    head_task: raw?.head_task ? normalizeTaskSummary(raw.head_task) : null,
    blocked_tasks: asArray<TaskSummary>(raw?.blocked_tasks).map(normalizeTaskSummary),
    recent_errors: asArray(raw?.recent_errors),
    last_release: raw?.last_release ? normalizeRelease(raw.last_release) : null,
    pending_events: raw?.pending_events ?? 0,
    index_revision: raw?.index_revision ?? 0,
    freshness: normalizeFreshness(raw?.freshness),
  };
};

export const reindex = async (workspaceId: string): Promise<ReindexReceipt> => {
  const raw = await apiFetch<ReindexReceipt>(`${libraryRoot(workspaceId)}/reindex`, { method: 'POST' });
  return {
    ...raw,
    task_id: typeof raw?.task_id === 'string' ? raw.task_id : '',
    accepted: raw?.accepted === true,
    queue_seq: typeof raw?.queue_seq === 'number' ? raw.queue_seq : 0,
    status: typeof raw?.status === 'string' ? raw.status : 'queued',
  };
};
