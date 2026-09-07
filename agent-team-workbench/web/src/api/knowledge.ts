import { apiFetch } from './client';

export type KnowledgeStatus = 'candidate' | 'draft' | 'effective' | 'superseded' | 'repealed';
export type KnowledgeVisibility = 'private' | 'workspace';
export type KnowledgeRelationDirection = 'both' | 'out' | 'in';
export type KnowledgeRelationKind =
  | 'related_to'
  | 'depends_on'
  | 'impacts'
  | 'triggers'
  | 'calls'
  | 'subscribes_to'
  | 'shares_state'
  | 'constrained_by'
  | 'conflicts_with'
  | 'supersedes';
export type KnowledgeSourceKind = 'run' | 'artifact' | 'work_item' | 'document' | 'code' | 'test' | 'user' | 'agent';
export type KnowledgeSubmissionStatus =
  | 'received'
  | 'processing'
  | 'accepted'
  | 'merged'
  | 'rejected'
  | 'needs_review';
export type KnowledgeCoverageStatus = 'complete' | 'partial' | 'missing' | 'conflict';
export type KnowledgeJobMode = 'inquiry' | 'curation';
export type KnowledgeJobStatus =
  | 'queued'
  | 'running'
  | 'waiting_retry'
  | 'completed'
  | 'incomplete'
  | 'conflict'
  | 'cancelled'
  | 'failed';

export type KnowledgeScope = Record<string, unknown>;

export interface KnowledgeConfig {
  workspace_id: string;
  librarian_agent_id: string;
  enabled: boolean;
  auto_collect: boolean;
  version: number;
}

export interface PatchKnowledgeConfigInput {
  expected_version: number;
  librarian_agent_id?: string | null;
  enabled?: boolean;
  auto_collect?: boolean;
}

export interface RepealKnowledgeItemInput {
  expected_version: number;
  reason: string;
}

export interface KnowledgeItem {
  id: string;
  workspace_id: string;
  owner_agent_id?: string;
  visibility: KnowledgeVisibility;
  kind: string;
  title: string;
  summary?: string;
  tags?: string[];
  aliases?: string[];
  scope?: KnowledgeScope;
  current_version_id?: string;
  current_version: number;
  status: KnowledgeStatus;
  version: number;
  created_at: string;
  updated_at: string;
}

export interface KnowledgeVersion {
  id: string;
  item_id: string;
  version: number;
  base_version: number;
  status: KnowledgeStatus;
  kind: string;
  title: string;
  summary?: string;
  body_markdown: string;
  tags?: string[];
  aliases?: string[];
  scope?: KnowledgeScope;
  metadata?: Record<string, unknown>;
  content_digest: string;
  created_by_agent_id: string;
  created_by_run_id?: string;
  created_by_work_item_id?: string;
  supersedes_version_id?: string;
  published_at?: string;
  created_at: string;
}

export interface KnowledgeSource {
  id: string;
  workspace_id: string;
  submitted_by_agent_id: string;
  kind: KnowledgeSourceKind;
  ref: string;
  locator?: string;
  excerpt?: string;
  digest?: string;
  metadata?: Record<string, unknown>;
  created_at: string;
}

export interface KnowledgeVersionSource {
  version_id: string;
  source_id: string;
  role?: string;
}

export interface KnowledgeVersionDetails extends KnowledgeVersion {
  sources?: KnowledgeSource[];
  relations?: KnowledgeRelation[];
}

export interface KnowledgeRelation {
  id: string;
  workspace_id: string;
  source_version_id: string;
  from_item_id: string;
  to_item_id: string;
  kind: KnowledgeRelationKind;
  condition?: string;
  rationale?: string;
  source_ids?: string[];
  sources?: KnowledgeSource[];
  created_at: string;
}

export interface KnowledgeChange {
  item_id?: string;
  base_version: number;
  owner_agent_id?: string;
  visibility?: KnowledgeVisibility;
  title: string;
  body: string;
  kind: string;
  scope?: KnowledgeScope;
  summary?: string;
  tags?: string[];
  aliases?: string[];
  sources?: KnowledgeSourceInput[];
  relations?: KnowledgeRelationInput[];
}

export interface KnowledgeSourceInput {
  kind: KnowledgeSourceKind;
  ref: string;
  locator?: string;
  excerpt?: string;
  digest?: string;
  metadata?: Record<string, unknown>;
  role?: string;
}

export interface KnowledgeRelationInput {
  from_item_id?: string;
  to_item_id: string;
  kind: KnowledgeRelationKind;
  condition?: string;
  rationale?: string;
  source_ids?: string[];
}

export interface KnowledgeSubmissionRequest {
  workspace_id: string;
  agent_id: string;
  run_id?: string;
  work_item_id?: string;
  client_key: string;
  changes?: KnowledgeChange[];
  no_change?: boolean;
}

export interface KnowledgeSubmitCandidateInput {
  agent_id: string;
  run_id?: string;
  work_item_id?: string;
  client_key: string;
  changes: KnowledgeChange[];
  no_change?: boolean;
}

export interface KnowledgeSubmission {
  id: string;
  workspace_id: string;
  agent_id: string;
  run_id?: string;
  work_item_id?: string;
  client_key: string;
  request?: KnowledgeSubmissionRequest;
  request_digest?: string;
  status: KnowledgeSubmissionStatus;
  result_item_ids?: string[];
  result_version_ids?: string[];
  error_message?: string;
  version: number;
  created_at: string;
  updated_at: string;
  coverage?: KnowledgeCoverage;
  message?: string;
}

export interface KnowledgeBudget {
  max_results: number;
  max_depth: number;
  max_nodes: number;
  max_bytes: number;
  max_searches: number;
  max_relations: number;
}

export interface KnowledgeJobBudget extends KnowledgeBudget {
  max_turns: number;
  max_input_tokens: number;
  max_output_tokens: number;
  max_duration_seconds: number;
}

export interface KnowledgeJobUsage {
  turns: number;
  searches: number;
  reads: number;
  relations: number;
  bytes: number;
  input_tokens: number;
  output_tokens: number;
}

export interface KnowledgeCoverageEntry {
  subject: string;
  status: KnowledgeCoverageStatus;
  evidence_ids?: string[];
  related_item_ids?: string[];
  missing?: string[];
  note?: string;
}

export interface KnowledgeCoverage {
  status: KnowledgeCoverageStatus;
  entries: KnowledgeCoverageEntry[];
  visited_nodes: number;
  visited_relations: number;
  truncated: boolean;
  budget: KnowledgeBudget;
}

export interface KnowledgeJobFrontier {
  subject: string;
  depth: number;
  kind?: string;
  reason?: string;
}

export interface KnowledgeJobRequiredRelation {
  id: string;
  source_version_id?: string;
  from_item_id: string;
  to_item_id: string;
  kind: KnowledgeRelationKind;
  source_ids?: string[];
}

export interface KnowledgeCitation {
  item_id?: string;
  knowledge_id?: string;
  version_id?: string;
  source_id?: string;
  title?: string;
  locator?: string;
  excerpt?: string;
  status?: KnowledgeStatus | string;
}

export interface KnowledgeInquiryResult {
  answer?: string;
  summary?: string;
  knowledge_package?: Record<string, unknown> | string;
  package?: Record<string, unknown> | string;
  citations?: KnowledgeCitation[];
  gaps?: string[];
  relations?: KnowledgeRelation[];
  coverage?: KnowledgeCoverage | KnowledgeCoverageEntry[];
  [key: string]: unknown;
}

export interface KnowledgeJob {
  id: string;
  workspace_id: string;
  requesting_agent_id?: string;
  source_run_id?: string;
  submission_id?: string;
  agent_profile_id: string;
  work_item_id: string;
  current_run_id?: string;
  mode: KnowledgeJobMode;
  status: KnowledgeJobStatus;
  question: string;
  context?: string;
  scope?: KnowledgeScope;
  budget: KnowledgeJobBudget;
  used: KnowledgeJobUsage;
  coverage: KnowledgeCoverage;
  evidence_ids?: string[];
  visited_version_ids?: string[];
  required_item_ids?: string[];
  required_relations?: KnowledgeJobRequiredRelation[];
  index_revision: number;
  snapshot_ids?: string[];
  frontier?: KnowledgeJobFrontier[];
  observations?: string[];
  result?: KnowledgeInquiryResult | null;
  turn_seq: number;
  retry_count: number;
  repair_attempt: number;
  next_action_at?: string;
  last_decision_digest?: string;
  last_error?: string;
  client_key: string;
  version: number;
  created_at: string;
  updated_at: string;
  finished_at?: string;
}

export interface KnowledgeListResponse<T> {
  items: T[];
  next_cursor?: string | null;
  truncated?: boolean;
}

export interface KnowledgeInquiryInput {
  question: string;
  context?: string;
  scope?: KnowledgeScope;
  budget?: KnowledgeJobBudget;
  client_key?: string;
}

export interface KnowledgeItemsFilter {
  status?: KnowledgeStatus;
  scope?: string;
  kind?: string;
  visibility?: KnowledgeVisibility;
  agent_id?: string;
  cursor?: string;
  limit?: number;
}

export interface KnowledgeSubmissionsFilter {
  status?: KnowledgeSubmissionStatus;
  agent_id?: string;
}

export interface KnowledgeJobsFilter {
  agent_id?: string;
}

const segment = (value: string): string => encodeURIComponent(value);
const workspaceRoot = (workspaceId: string): string => '/workspaces/' + segment(workspaceId) + '/knowledge';

function withQuery(path: string, values: Record<string, string | number | undefined>): string {
  const params = new URLSearchParams();
  Object.entries(values).forEach(([key, value]) => {
    if (value !== undefined && value !== '') params.set(key, String(value));
  });
  const query = params.toString();
  return query ? path + '?' + query : path;
}

export const getKnowledgeConfig = (workspaceId: string) =>
  apiFetch<KnowledgeConfig>(workspaceRoot(workspaceId) + '/config');

export const patchKnowledgeConfig = (workspaceId: string, input: PatchKnowledgeConfigInput) =>
  apiFetch<KnowledgeConfig>(workspaceRoot(workspaceId) + '/config', {
    method: 'PATCH',
    body: input,
  });

export const listKnowledgeItems = (workspaceId: string, filter: KnowledgeItemsFilter = {}) =>
  apiFetch<KnowledgeListResponse<KnowledgeItem>>(
    withQuery(workspaceRoot(workspaceId) + '/items', {
      status: filter.status,
      scope: filter.scope,
      kind: filter.kind,
      visibility: filter.visibility,
      agent_id: filter.agent_id,
      cursor: filter.cursor,
      limit: filter.limit,
    }),
  );

export interface KnowledgeItemDetails {
  item: KnowledgeItem;
  version: KnowledgeVersion;
  sources: KnowledgeSource[];
}

export const getKnowledgeItem = (workspaceId: string, itemId: string, agentId?: string) =>
  apiFetch<KnowledgeItemDetails>(
    withQuery(workspaceRoot(workspaceId) + '/items/' + segment(itemId), { agent_id: agentId }),
  );

export const repealKnowledgeItem = (
  workspaceId: string,
  itemId: string,
  input: RepealKnowledgeItemInput,
  agentId?: string,
  clientKey: string = crypto.randomUUID(),
) =>
  apiFetch<KnowledgeItem>(
    withQuery(workspaceRoot(workspaceId) + '/items/' + segment(itemId) + '/repeal', { agent_id: agentId }),
    { method: 'POST', body: input, idempotencyKey: clientKey },
  );

export const getKnowledgeVersion = (workspaceId: string, itemId: string, version: number | string, agentId?: string) =>
  apiFetch<KnowledgeItemDetails>(
    withQuery(
      workspaceRoot(workspaceId) + '/items/' + segment(itemId) + '/versions/' + segment(String(version)),
      { agent_id: agentId },
    ),
  );

export const listKnowledgeVersions = (workspaceId: string, itemId: string, agentId?: string) =>
  apiFetch<KnowledgeListResponse<KnowledgeVersion>>(
    withQuery(workspaceRoot(workspaceId) + '/items/' + segment(itemId) + '/versions', { agent_id: agentId }),
  );

export const listKnowledgeRelations = (
  workspaceId: string,
  itemId: string,
  direction: KnowledgeRelationDirection = 'both',
  agentId?: string,
) =>
  apiFetch<KnowledgeListResponse<KnowledgeRelation>>(
    withQuery(workspaceRoot(workspaceId) + '/items/' + segment(itemId) + '/relations', {
      direction,
      agent_id: agentId,
    }),
  );

export const listKnowledgeSubmissions = (
  workspaceId: string,
  filter: KnowledgeSubmissionsFilter = {},
) =>
  apiFetch<KnowledgeListResponse<KnowledgeSubmission>>(
    withQuery(workspaceRoot(workspaceId) + '/submissions', {
      status: filter.status,
      agent_id: filter.agent_id,
    }),
  );

export const submitKnowledgeCandidate = (
  workspaceId: string,
  input: KnowledgeSubmitCandidateInput,
) =>
  apiFetch<KnowledgeSubmission>(
    workspaceRoot(workspaceId) + '/submissions',
    { method: 'POST', body: input, idempotencyKey: input.client_key },
  );

export const curateKnowledgeSubmission = (workspaceId: string, submissionId: string, clientKey: string = crypto.randomUUID()) =>
  apiFetch<KnowledgeJob>(
    workspaceRoot(workspaceId) + '/submissions/' + segment(submissionId) + '/curate',
    { method: 'POST', body: {}, idempotencyKey: clientKey },
  );

export const publishKnowledgeSubmission = (workspaceId: string, submissionId: string, clientKey: string = crypto.randomUUID()) =>
  apiFetch<KnowledgeSubmission>(
    workspaceRoot(workspaceId) + '/submissions/' + segment(submissionId) + '/publish',
    { method: 'POST', body: {}, idempotencyKey: clientKey },
  );

export const createKnowledgeInquiry = (
  workspaceId: string,
  input: KnowledgeInquiryInput,
  agentId?: string,
  clientKey = input.client_key ?? crypto.randomUUID(),
) =>
  apiFetch<KnowledgeJob>(withQuery(workspaceRoot(workspaceId) + '/inquiries', { agent_id: agentId }), {
    method: 'POST',
    body: { ...input, client_key: clientKey },
    idempotencyKey: clientKey,
  });

export const listKnowledgeJobs = (workspaceId: string, filter: KnowledgeJobsFilter = {}) =>
  apiFetch<KnowledgeListResponse<KnowledgeJob>>(
    withQuery(workspaceRoot(workspaceId) + '/jobs', {
      agent_id: filter.agent_id,
    }),
  );

export const getKnowledgeJob = (workspaceId: string, jobId: string) =>
  apiFetch<KnowledgeJob>(workspaceRoot(workspaceId) + '/jobs/' + segment(jobId));

export const cancelKnowledgeJob = (workspaceId: string, jobId: string, clientKey: string = crypto.randomUUID()) =>
  apiFetch<KnowledgeJob>(
    workspaceRoot(workspaceId) + '/jobs/' + segment(jobId) + '/cancel',
    { method: 'POST', body: {}, idempotencyKey: clientKey },
  );
