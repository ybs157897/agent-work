import { apiFetch } from './client';
import type { ChatAnalysisDecision, ChatAnalysisProjection, ChatDecisionOutcome } from './chat-analysis';

export interface SubmitChatDecisionInput {
  expected_version: number;
  revision: number;
  item_id: string;
  outcome: ChatDecisionOutcome;
  conclusion: string;
  basis: string;
  product_version: string;
  client_key: string;
}

export interface ChatDecisionHistoryEntry {
  id: string;
  kind: 'revision' | 'answer' | 'decision' | 'reopen';
  revision: number;
  item_id?: string;
  question_id?: string;
  outcome?: ChatDecisionOutcome;
  conclusion?: string;
  basis?: string;
  product_version?: string;
  reason?: string;
  created_at: string;
  client_key?: string;
}

interface ChatDecisionHistoryResponse {
  items?: unknown[];
  revisions?: unknown[];
  answers?: unknown[];
  decisions?: unknown[];
  reopens?: unknown[];
}

const analysisRoot = (chatId: string) => `/work-items/${encodeURIComponent(chatId)}/analysis`;

export const submitChatDecision = (chatId: string, input: SubmitChatDecisionInput) =>
  apiFetch<ChatAnalysisProjection>(`${analysisRoot(chatId)}/decisions`, {
    method: 'POST',
    body: input,
    idempotencyKey: input.client_key,
  });

function asRecord(value: unknown): Record<string, unknown> | null {
  return typeof value === 'object' && value !== null ? value as Record<string, unknown> : null;
}

function asRevision(value: unknown, fallback = 0): number {
  const record = asRecord(value);
  return typeof record?.revision === 'number' && Number.isSafeInteger(record.revision) && record.revision >= 0 ? record.revision : fallback;
}

function asCreatedAt(value: unknown): string {
  const record = asRecord(value);
  if (typeof record?.created_at === 'string' && record.created_at) return record.created_at;
  if (typeof record?.updated_at === 'string' && record.updated_at) return record.updated_at;
  return '';
}

function normalizeHistoryResponse(value: unknown): { items: ChatDecisionHistoryEntry[] } {
  const response = asRecord(value) as ChatDecisionHistoryResponse | null;
  if (!response) return { items: [] };
  const legacyItems = Array.isArray(response.items) ? response.items : [];
  const items: ChatDecisionHistoryEntry[] = [];
  for (const value of legacyItems) {
    const entry = asRecord(value);
    if (!entry || typeof entry.id !== 'string' || typeof entry.kind !== 'string' || !['revision', 'answer', 'decision', 'reopen'].includes(entry.kind)
      || typeof entry.revision !== 'number' || !Number.isSafeInteger(entry.revision) || entry.revision < 0 || typeof entry.created_at !== 'string') continue;
    items.push({
      id: entry.id,
      kind: entry.kind as ChatDecisionHistoryEntry['kind'],
      revision: entry.revision,
      ...(typeof entry.item_id === 'string' ? { item_id: entry.item_id } : {}),
      ...(typeof entry.question_id === 'string' ? { question_id: entry.question_id } : {}),
      ...(typeof entry.outcome === 'string' ? { outcome: entry.outcome as ChatDecisionOutcome } : {}),
      ...(typeof entry.conclusion === 'string' ? { conclusion: entry.conclusion } : {}),
      ...(typeof entry.basis === 'string' ? { basis: entry.basis } : {}),
      ...(typeof entry.product_version === 'string' ? { product_version: entry.product_version } : {}),
      ...(typeof entry.reason === 'string' ? { reason: entry.reason } : {}),
      created_at: entry.created_at,
      ...(typeof entry.client_key === 'string' ? { client_key: entry.client_key } : {}),
    });
  }
  const revisions = Array.isArray(response.revisions) ? response.revisions : [];
  for (const value of revisions) {
    const revision = asRevision(value);
    const createdAt = asCreatedAt(value);
    if (revision < 1 || !createdAt) continue;
    items.push({ id: `revision:${revision}`, kind: 'revision', revision, created_at: createdAt });
  }
  const answers = Array.isArray(response.answers) ? response.answers : [];
  for (const value of answers) {
    const answer = asRecord(value);
    const id = typeof answer?.id === 'string' ? answer.id : '';
    const revision = asRevision(value);
    const createdAt = asCreatedAt(value);
    if (!id || revision < 1 || !createdAt) continue;
    items.push({
      id, kind: 'answer', revision, created_at: createdAt,
      ...(typeof answer?.question_id === 'string' ? { question_id: answer.question_id } : {}),
      ...(typeof answer?.client_key === 'string' ? { client_key: answer.client_key } : {}),
    });
  }
  const decisions = Array.isArray(response.decisions) ? response.decisions : [];
  for (const value of decisions) {
    const decision = asRecord(value);
    if (!decision) continue;
    const id = typeof decision?.id === 'string' ? decision.id : decision?.decision_id;
    const revision = asRevision(value);
    const createdAt = asCreatedAt(value);
    if (typeof id !== 'string' || !id || revision < 1 || !createdAt) continue;
    items.push({
      id, kind: 'decision', revision, created_at: createdAt,
      ...(typeof decision.item_id === 'string' ? { item_id: decision.item_id } : {}),
      ...(typeof decision.outcome === 'string' ? { outcome: decision.outcome as ChatDecisionOutcome } : {}),
      ...(typeof decision.conclusion === 'string' ? { conclusion: decision.conclusion } : {}),
      ...(typeof decision.basis === 'string' ? { basis: decision.basis } : {}),
      ...(typeof decision.product_version === 'string' ? { product_version: decision.product_version } : {}),
      ...(typeof decision.review_reason === 'string' ? { reason: decision.review_reason } : {}),
      ...(typeof decision.client_key === 'string' ? { client_key: decision.client_key } : {}),
    });
  }
  const reopens = Array.isArray(response.reopens) ? response.reopens : [];
  for (const value of reopens) {
    const reopen = asRecord(value);
    if (!reopen) continue;
    const id = typeof reopen?.id === 'string' ? reopen.id : '';
    const revision = asRevision({ revision: reopen?.to_revision });
    const createdAt = asCreatedAt(value);
    if (!id || revision < 1 || !createdAt) continue;
    items.push({
      id, kind: 'reopen', revision, created_at: createdAt,
      ...(typeof reopen.item_id === 'string' ? { item_id: reopen.item_id } : {}),
      ...(typeof reopen.reason === 'string' ? { reason: reopen.reason } : {}),
    });
  }
  items.sort((left, right) => left.created_at.localeCompare(right.created_at) || left.revision - right.revision || left.id.localeCompare(right.id));
  return { items };
}

export const listChatDecisionHistory = async (chatId: string) =>
  normalizeHistoryResponse(await apiFetch<ChatDecisionHistoryResponse>(`${analysisRoot(chatId)}/history`));

export const recheckChatAnalysis = (chatId: string) =>
  apiFetch<ChatAnalysisProjection>(`${analysisRoot(chatId)}/recheck`, {
    method: 'POST',
    body: {},
  });

export type { ChatAnalysisDecision };
