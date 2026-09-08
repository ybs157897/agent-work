import { apiFetch } from './client';

// The knowledge tab is a read-only library. Agent writes use Run-bound tools.

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
export type KnowledgeScope = Record<string, unknown>;

export interface KnowledgeConfig {
  workspace_id: string;
  librarian_agent_id: string;
  enabled: boolean;
  auto_collect: boolean;
  version: number;
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
  /** Query-only excerpt returned by server-side q search. */
  search_excerpt?: string;
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

export interface KnowledgeListResponse<T> {
  items: T[];
  next_cursor?: string | null;
  truncated?: boolean;
}

export interface KnowledgeItemsFilter {
  /** Server-side full-text query. Empty queries are omitted from the URL. */
  q?: string;
  status?: KnowledgeStatus;
  scope?: string;
  kind?: string;
  visibility?: KnowledgeVisibility;
  agent_id?: string;
  cursor?: string;
  limit?: number;
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

export const listKnowledgeItems = (workspaceId: string, filter: KnowledgeItemsFilter = {}) =>
  apiFetch<KnowledgeListResponse<KnowledgeItem>>(
    withQuery(workspaceRoot(workspaceId) + '/items', {
      q: filter.q,
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
