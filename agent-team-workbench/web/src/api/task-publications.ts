import { apiFetch } from './client';
import type { WorkItem } from './types';

export interface CreatePublicationDraftInput {
  expected_version: number;
  revision: number;
  item_ids: string[];
  title: string;
  client_key: string;
}

export interface PublicationSourceDependency {
  kind: string;
  ref: string;
  sha256: string;
  version?: number;
}

export interface PublicationProjectBaseline {
  repository_identity: string;
  ref_kind: string;
  branch_name: string;
  checkout_ref: string;
  head: string;
  staged_digest: string;
  unstaged_digest: string;
  untracked_digest: string;
  content_digest: string;
}

export type PublicationDraftStatus = 'ready' | 'stale' | 'published';

export interface PublicationDraftReceipt {
  draft_id: string;
  workspace_id: string;
  chat_id: string;
  agent_id: string;
  analysis_revision: number;
  item_ids: string[];
  confirmation_ids: string[];
  title: string;
  description: string;
  acceptance_criteria: string[];
  source_dependencies: PublicationSourceDependency[];
  project_baseline: PublicationProjectBaseline;
  status: PublicationDraftStatus;
  task_id?: string | null;
  context_snapshot_id: string;
  fingerprint: string;
  client_key: string;
  version: number;
}

export interface PublishPublicationInput {
  expected_version: number;
  client_key: string;
}

export interface PublishPublicationReceipt {
  draft: PublicationDraftReceipt;
  task: WorkItem;
}

const analysisRoot = (chatId: string) => `/work-items/${encodeURIComponent(chatId)}/analysis`;

function record(value: unknown): Record<string, unknown> | null {
  return typeof value === 'object' && value !== null ? value as Record<string, unknown> : null;
}

function requiredString(value: unknown, field: string): string {
  if (typeof value !== 'string' || !value.trim()) throw new Error(`任务草案响应格式无效：${field}`);
  return value;
}

export function parsePublicationDraftReceipt(value: unknown): PublicationDraftReceipt {
  const raw = record(value);
  const baseline = record(raw?.project_baseline);
  if (!raw || !Array.isArray(raw.item_ids) || raw.item_ids.some((item) => typeof item !== 'string' || !item.trim())
    || !Array.isArray(raw.confirmation_ids) || raw.confirmation_ids.some((item) => typeof item !== 'string')
    || !Array.isArray(raw.source_dependencies) || !baseline
    || typeof raw.analysis_revision !== 'number' || !Number.isSafeInteger(raw.analysis_revision) || raw.analysis_revision < 1
    || typeof raw.version !== 'number' || !Number.isSafeInteger(raw.version) || raw.version < 1
    || !['ready', 'stale', 'published'].includes(String(raw.status))) throw new Error('任务草案响应格式无效：字段');
  const sourceDependencies = raw.source_dependencies.map((dependency) => {
    const item = record(dependency);
    if (!item || typeof item.kind !== 'string' || !item.kind.trim() || typeof item.ref !== 'string' || !item.ref.trim() || typeof item.sha256 !== 'string' || !/^[0-9a-f]{64}$/.test(item.sha256)) throw new Error('任务草案响应格式无效：source_dependencies');
    if (item.version !== undefined && (typeof item.version !== 'number' || !Number.isSafeInteger(item.version) || item.version < 0)) throw new Error('任务草案响应格式无效：source_dependencies.version');
    return { kind: item.kind, ref: item.ref, sha256: item.sha256, ...(typeof item.version === 'number' ? { version: item.version } : {}) };
  });
  const baselineFields = ['repository_identity', 'ref_kind', 'branch_name', 'checkout_ref', 'head', 'staged_digest', 'unstaged_digest', 'untracked_digest', 'content_digest'] as const;
  if (baselineFields.some((field) => typeof baseline[field] !== 'string' || !String(baseline[field]).trim())) throw new Error('任务草案响应格式无效：project_baseline');
  if (!Array.isArray(raw.acceptance_criteria) || raw.acceptance_criteria.some((item) => typeof item !== 'string')) throw new Error('任务草案响应格式无效：acceptance_criteria');
  return {
    draft_id: requiredString(raw.draft_id, 'draft_id'),
    workspace_id: requiredString(raw.workspace_id, 'workspace_id'),
    chat_id: requiredString(raw.chat_id, 'chat_id'),
    agent_id: requiredString(raw.agent_id, 'agent_id'),
    analysis_revision: raw.analysis_revision,
    item_ids: [...raw.item_ids] as string[],
    confirmation_ids: [...raw.confirmation_ids] as string[],
    title: requiredString(raw.title, 'title'),
    description: typeof raw.description === 'string' ? raw.description : '',
    acceptance_criteria: [...raw.acceptance_criteria] as string[],
    source_dependencies: sourceDependencies,
    project_baseline: {
      repository_identity: baseline.repository_identity as string,
      ref_kind: baseline.ref_kind as string,
      branch_name: baseline.branch_name as string,
      checkout_ref: baseline.checkout_ref as string,
      head: baseline.head as string,
      staged_digest: baseline.staged_digest as string,
      unstaged_digest: baseline.unstaged_digest as string,
      untracked_digest: baseline.untracked_digest as string,
      content_digest: baseline.content_digest as string,
    },
    status: raw.status as PublicationDraftStatus,
    ...(raw.task_id === null || typeof raw.task_id === 'string' ? { task_id: raw.task_id as string | null } : {}),
    context_snapshot_id: requiredString(raw.context_snapshot_id, 'context_snapshot_id'),
    fingerprint: requiredString(raw.fingerprint, 'fingerprint'),
    client_key: requiredString(raw.client_key, 'client_key'),
    version: raw.version,
  };
}

export function parsePublishPublicationReceipt(value: unknown): PublishPublicationReceipt {
  const raw = record(value);
  const task = record(raw?.task);
  if (!raw || !task || !task.id || typeof task.id !== 'string' || task.record_kind !== 'task' || typeof task.workspace_id !== 'string' || typeof task.title !== 'string' || typeof task.description !== 'string' || typeof task.status !== 'string' || typeof task.priority !== 'string' || (typeof task.due_date !== 'string' && task.due_date !== null) || typeof task.version !== 'number') throw new Error('任务发布响应格式无效：task');
  return {
    draft: parsePublicationDraftReceipt(raw.draft),
    task: task as unknown as WorkItem,
  };
}

export const createPublicationDraft = async (chatId: string, input: CreatePublicationDraftInput) =>
  parsePublicationDraftReceipt(await apiFetch<unknown>(`${analysisRoot(chatId)}/drafts`, {
    method: 'POST',
    body: input,
    idempotencyKey: input.client_key,
  }));

export const getPublicationDraft = async (chatId: string, draftId: string) =>
  parsePublicationDraftReceipt(await apiFetch<unknown>(`${analysisRoot(chatId)}/drafts/${encodeURIComponent(draftId)}`));

export const recheckPublicationDraft = async (chatId: string, draftId: string, expectedVersion: number) =>
  parsePublicationDraftReceipt(await apiFetch<unknown>(`${analysisRoot(chatId)}/drafts/${encodeURIComponent(draftId)}/recheck`, {
    method: 'POST',
    body: { expected_version: expectedVersion },
  }));

export const publishPublicationDraft = async (chatId: string, draftId: string, input: PublishPublicationInput) =>
  parsePublishPublicationReceipt(await apiFetch<unknown>(`${analysisRoot(chatId)}/drafts/${encodeURIComponent(draftId)}/publish`, {
    method: 'POST',
    body: input,
    idempotencyKey: input.client_key,
  }));
