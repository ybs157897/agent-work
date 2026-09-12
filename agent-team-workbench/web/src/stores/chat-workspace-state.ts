import { useSyncExternalStore } from 'react';
import type { ChatSourceRef } from '../api/chat-sources';
import type { PublicationDraftReceipt } from '../api/task-publications';

/**
 * Browser recovery for the part of Chat that is intentionally local while a
 * Run is being created: the selected Agent/conversation, unsent composer and
 * queued messages. Every key contains the Workspace boundary and Agent ID so
 * switching A → B cannot reveal A's draft in B.
 */
export interface PersistedChatQueueItem {
  text: string;
  clientKey: string;
  sourceRefs?: ChatSourceRef[];
  attachmentKeys?: string[];
  outputContract?: 'languagegui/v1' | 'chat-analysis/v1';
}

export interface PersistedChatAnalysisDraft {
  version: number;
  revision: number;
  questionId: string;
  selectedOptionIds: string[];
  text: string;
  clientKey?: string;
}

export interface PersistedChatDecisionDraft {
  version: number;
  revision: number;
  itemId: string;
  outcome: 'confirmed' | 'rejected' | 'needs_clarification';
  conclusion: string;
  basis: string;
  productVersion: string;
  clientKey?: string;
}

export type PublicationDraftStatus = 'editing' | 'saving' | 'ready' | 'stale' | 'publishing' | 'published' | 'failed';

export interface PersistedPublicationDraft {
  expectedVersion: number;
  revision: number;
  itemIds: string[];
  title: string;
  description: string;
  acceptanceCriteria: string[];
  clientKey: string;
  publishClientKey?: string;
  draftId?: string;
  status: PublicationDraftStatus;
  lastOperation?: 'save' | 'publish';
  frozen?: PublicationDraftReceipt;
  taskId?: string;
  publishedAt?: string;
  error?: string;
}

export interface ChatComposerDraft {
  draft: string;
}

export interface ChatWorkspaceState {
  workspaceId: string;
  agentId: string | null;
  conversationId: string | null;
  composer: ChatComposerDraft;
  queue: PersistedChatQueueItem[];
  analysisDraft?: PersistedChatAnalysisDraft;
  decisionDraft?: PersistedChatDecisionDraft;
  publicationDraft?: PersistedPublicationDraft;
  updatedAt: string;
}

const PREFIX = 'chat:workspace-state:v1:';
const LEGACY_TASK_INTAKE_PREFIX = 'task-intake:v1:';
const LEGACY_TASK_INTAKE_PROJECT_PREFIX = 'task-intake:v2:';
const LEGACY_TASK_INTAKE_BINDING_PREFIX = 'task-intake:project-binding:v1:';

let storageNotice: string | null = null;
const storageListeners = new Set<() => void>();

function setStorageNotice(value: string | null): void {
  if (storageNotice === value) return;
  storageNotice = value;
  storageListeners.forEach((listener) => listener());
}

export function subscribeChatWorkspaceStorageNotice(listener: () => void): () => void {
  storageListeners.add(listener);
  return () => storageListeners.delete(listener);
}

export function getChatWorkspaceStorageNotice(): string | null {
  return storageNotice;
}

export function useChatWorkspaceStorageNotice(): string | null {
  return useSyncExternalStore(subscribeChatWorkspaceStorageNotice, getChatWorkspaceStorageNotice, () => null);
}

export interface LegacyTaskIntakeRecoveryEntry {
  projectKey: string | null;
  hasTerminalState: boolean;
  draft?: { title: string; description: string; acceptance_criteria: string[] };
  composerText?: string;
  messages: Array<{ role: 'user' | 'assistant'; content: string }>;
}

export interface LegacyTaskIntakeRecovery extends LegacyTaskIntakeRecoveryEntry {
  workspaceId: string;
  entries: LegacyTaskIntakeRecoveryEntry[];
  bindingPresent: boolean;
}

function key(workspaceId: string, agentId: string, conversationId: string | null): string {
  return `${PREFIX}${encodeURIComponent(workspaceId)}:${encodeURIComponent(agentId)}:${encodeURIComponent(conversationId ?? 'new')}`;
}

function storage(): Storage | null {
  if (typeof window === 'undefined') return null;
  try { return window.localStorage; } catch { return null; }
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null;
}

function normalizeComposer(value: unknown): ChatComposerDraft {
  if (!isRecord(value)) return { draft: '' };
  return { draft: typeof value.draft === 'string' ? value.draft : '' };
}

function normalizeQueue(value: unknown): PersistedChatQueueItem[] {
  if (!Array.isArray(value)) return [];
  return value.filter(isRecord).map((item) => {
    if (typeof item.text !== 'string' || typeof item.clientKey !== 'string' || !item.text.trim() || !item.clientKey.trim()) return null;
    const sourceRefs = Array.isArray(item.sourceRefs)
      ? item.sourceRefs.filter((ref): ref is ChatSourceRef => isRecord(ref) && typeof ref.source_id === 'string' && typeof ref.sha256 === 'string' && /^[0-9a-f]{64}$/.test(ref.sha256))
      : undefined;
    const attachmentKeys = Array.isArray(item.attachmentKeys)
      ? item.attachmentKeys.filter((key): key is string => typeof key === 'string' && key.trim().length > 0)
      : undefined;
    const outputContract = item.outputContract === 'languagegui/v1' || item.outputContract === 'chat-analysis/v1' ? item.outputContract : undefined;
    return { text: item.text, clientKey: item.clientKey, ...(sourceRefs?.length ? { sourceRefs } : {}), ...(attachmentKeys?.length ? { attachmentKeys } : {}), ...(outputContract ? { outputContract } : {}) };
  }).filter((item): item is PersistedChatQueueItem => item !== null);
}

function normalizeAnalysisDraft(value: unknown): PersistedChatAnalysisDraft | undefined {
  if (!isRecord(value) || typeof value.version !== 'number' || !Number.isSafeInteger(value.version) || value.version < 0
    || typeof value.revision !== 'number' || !Number.isSafeInteger(value.revision) || value.revision < 0
    || typeof value.questionId !== 'string' || !value.questionId.trim()
    || !Array.isArray(value.selectedOptionIds) || value.selectedOptionIds.some((id) => typeof id !== 'string' || !id.trim())
    || typeof value.text !== 'string' || value.text.length > 8_000) return undefined;
  return {
    version: value.version,
    revision: value.revision,
    questionId: value.questionId,
    selectedOptionIds: [...new Set(value.selectedOptionIds as string[])],
    text: value.text,
    ...(typeof value.clientKey === 'string' && value.clientKey.trim() ? { clientKey: value.clientKey } : {}),
  };
}

function normalizeDecisionDraft(value: unknown): PersistedChatDecisionDraft | undefined {
  if (!isRecord(value) || typeof value.version !== 'number' || !Number.isSafeInteger(value.version) || value.version < 0
    || typeof value.revision !== 'number' || !Number.isSafeInteger(value.revision) || value.revision < 0
    || typeof value.itemId !== 'string' || !value.itemId.trim()
    || !['confirmed', 'rejected', 'needs_clarification'].includes(String(value.outcome))
    || typeof value.conclusion !== 'string' || value.conclusion.length > 8_000
    || typeof value.basis !== 'string' || value.basis.length > 8_000
    || typeof value.productVersion !== 'string' || value.productVersion.length > 500) return undefined;
  return {
    version: value.version,
    revision: value.revision,
    itemId: value.itemId,
    outcome: value.outcome as PersistedChatDecisionDraft['outcome'],
    conclusion: value.conclusion,
    basis: value.basis,
    productVersion: value.productVersion,
    ...(typeof value.clientKey === 'string' && value.clientKey.trim() ? { clientKey: value.clientKey } : {}),
  };
}

function normalizePublicationDraft(value: unknown): PersistedPublicationDraft | undefined {
  if (!isRecord(value) || typeof value.expectedVersion !== 'number' || !Number.isSafeInteger(value.expectedVersion) || value.expectedVersion < 0
    || typeof value.revision !== 'number' || !Number.isSafeInteger(value.revision) || value.revision < 0
    || !Array.isArray(value.itemIds) || value.itemIds.some((item) => typeof item !== 'string' || !item.trim())
    || typeof value.title !== 'string' || value.title.length > 500
    || typeof value.description !== 'string' || value.description.length > 20_000
    || !Array.isArray(value.acceptanceCriteria) || value.acceptanceCriteria.some((item) => typeof item !== 'string' || item.length > 2_000)
    || typeof value.clientKey !== 'string' || !value.clientKey.trim()
    || !['editing', 'saving', 'ready', 'stale', 'publishing', 'published', 'failed'].includes(String(value.status))) return undefined;
  return {
    expectedVersion: value.expectedVersion,
    revision: value.revision,
    itemIds: [...new Set(value.itemIds as string[])],
    title: value.title,
    description: value.description,
    acceptanceCriteria: [...value.acceptanceCriteria] as string[],
    clientKey: value.clientKey,
    ...(typeof value.publishClientKey === 'string' && value.publishClientKey.trim() ? { publishClientKey: value.publishClientKey } : {}),
    ...(typeof value.draftId === 'string' && value.draftId.trim() ? { draftId: value.draftId } : {}),
    status: value.status as PersistedPublicationDraft['status'],
    ...(value.lastOperation === 'save' || value.lastOperation === 'publish' ? { lastOperation: value.lastOperation } : {}),
    ...(normalizePublicationReceipt(value.frozen) ? { frozen: normalizePublicationReceipt(value.frozen)! } : {}),
    ...(typeof value.taskId === 'string' && value.taskId.trim() ? { taskId: value.taskId } : {}),
    ...(typeof value.publishedAt === 'string' && value.publishedAt.trim() ? { publishedAt: value.publishedAt } : {}),
    ...(typeof value.error === 'string' && value.error.length <= 8_000 ? { error: value.error } : {}),
  };
}

function normalizePublicationReceipt(value: unknown): PublicationDraftReceipt | undefined {
  if (!isRecord(value) || typeof value.draft_id !== 'string' || typeof value.workspace_id !== 'string' || typeof value.chat_id !== 'string'
    || typeof value.agent_id !== 'string' || typeof value.analysis_revision !== 'number' || !Number.isSafeInteger(value.analysis_revision) || value.analysis_revision < 1
    || typeof value.version !== 'number' || !Number.isSafeInteger(value.version) || value.version < 1
    || !Array.isArray(value.item_ids) || value.item_ids.some((item) => typeof item !== 'string')
    || !Array.isArray(value.confirmation_ids) || value.confirmation_ids.some((item) => typeof item !== 'string')
    || typeof value.title !== 'string' || typeof value.description !== 'string' || typeof value.context_snapshot_id !== 'string' || typeof value.fingerprint !== 'string' || typeof value.client_key !== 'string'
    || !Array.isArray(value.acceptance_criteria) || value.acceptance_criteria.some((item) => typeof item !== 'string')
    || !Array.isArray(value.source_dependencies) || !isRecord(value.project_baseline) || typeof value.status !== 'string') return undefined;
  const sourceDependencies = value.source_dependencies.filter(isRecord).filter((item) => typeof item.kind === 'string' && typeof item.ref === 'string' && typeof item.sha256 === 'string');
  const baseline = value.project_baseline;
  if (sourceDependencies.length !== value.source_dependencies.length
    || typeof baseline.repository_identity !== 'string' || typeof baseline.ref_kind !== 'string' || typeof baseline.branch_name !== 'string' || typeof baseline.checkout_ref !== 'string' || typeof baseline.head !== 'string'
    || typeof baseline.staged_digest !== 'string' || typeof baseline.unstaged_digest !== 'string'
    || typeof baseline.untracked_digest !== 'string' || typeof baseline.content_digest !== 'string') return undefined;
  return {
    draft_id: value.draft_id,
    workspace_id: value.workspace_id,
    chat_id: value.chat_id,
    agent_id: value.agent_id,
    analysis_revision: value.analysis_revision,
    item_ids: [...value.item_ids] as string[],
    confirmation_ids: [...value.confirmation_ids] as string[],
    title: value.title,
    description: value.description,
    acceptance_criteria: [...value.acceptance_criteria] as string[],
    source_dependencies: sourceDependencies.map((item) => ({
      kind: item.kind as string,
      ref: item.ref as string,
      sha256: item.sha256 as string,
      ...(typeof item.version === 'number' ? { version: item.version } : {}),
    })),
    project_baseline: {
      repository_identity: baseline.repository_identity,
      ref_kind: baseline.ref_kind as string,
      branch_name: baseline.branch_name as string,
      checkout_ref: baseline.checkout_ref as string,
      head: baseline.head,
      staged_digest: baseline.staged_digest,
      unstaged_digest: baseline.unstaged_digest,
      untracked_digest: baseline.untracked_digest,
      content_digest: baseline.content_digest,
      ...(typeof baseline.branch_name === 'string' ? { branch_name: baseline.branch_name } : {}),
      ...(typeof baseline.checkout_ref === 'string' ? { checkout_ref: baseline.checkout_ref } : {}),
    },
    status: value.status as PublicationDraftReceipt['status'],
    context_snapshot_id: value.context_snapshot_id,
    fingerprint: value.fingerprint,
    client_key: value.client_key,
    version: value.version,
  };
}

export function readChatWorkspaceState(workspaceId: string, agentId: string, conversationId: string | null): ChatWorkspaceState | null {
  const store = storage();
  if (!store || !workspaceId || !agentId) return null;
  try {
    const raw = store.getItem(key(workspaceId, agentId, conversationId));
    if (!raw) return null;
    const value: unknown = JSON.parse(raw);
    if (!isRecord(value)) return null;
    const analysisDraft = normalizeAnalysisDraft(value.analysisDraft);
    const decisionDraft = normalizeDecisionDraft(value.decisionDraft);
    const publicationDraft = normalizePublicationDraft(value.publicationDraft);
    return {
      workspaceId,
      agentId,
      conversationId,
      composer: normalizeComposer(value.composer),
      queue: normalizeQueue(value.queue),
      ...(analysisDraft ? { analysisDraft } : {}),
      ...(decisionDraft ? { decisionDraft } : {}),
      ...(publicationDraft ? { publicationDraft } : {}),
      updatedAt: typeof value.updatedAt === 'string' ? value.updatedAt : '',
    };
  } catch {
    setStorageNotice('浏览器存储空间不足，当前对话输入仍保留在页面内；请先复制未发送内容后清理空间。');
    return null;
  }
}

export function writeChatWorkspaceState(
  workspaceId: string,
  agentId: string,
  conversationId: string | null,
  state: Pick<ChatWorkspaceState, 'composer' | 'queue'> & { analysisDraft?: PersistedChatAnalysisDraft | null; decisionDraft?: PersistedChatDecisionDraft | null; publicationDraft?: PersistedPublicationDraft | null },
): void {
  const store = storage();
  if (!store || !workspaceId || !agentId) return;
  const saved = readChatWorkspaceState(workspaceId, agentId, conversationId);
  const analysisDraft = state.analysisDraft === null ? undefined : state.analysisDraft ?? saved?.analysisDraft;
  const decisionDraft = state.decisionDraft === null ? undefined : state.decisionDraft ?? saved?.decisionDraft;
  const publicationDraft = state.publicationDraft === null ? undefined : state.publicationDraft ?? saved?.publicationDraft;
  try {
    store.setItem(key(workspaceId, agentId, conversationId), JSON.stringify({
      workspaceId,
      agentId,
      conversationId,
      composer: normalizeComposer(state.composer),
      queue: normalizeQueue(state.queue),
      ...(analysisDraft ? { analysisDraft: normalizeAnalysisDraft(analysisDraft) } : {}),
      ...(decisionDraft ? { decisionDraft: normalizeDecisionDraft(decisionDraft) } : {}),
      ...(publicationDraft ? { publicationDraft: normalizePublicationDraft(publicationDraft) } : {}),
      updatedAt: new Date().toISOString(),
    } satisfies ChatWorkspaceState));
    setStorageNotice(null);
  } catch {
    setStorageNotice('浏览器存储空间不足，当前对话输入仍保留在页面内；请先复制未发送内容后清理空间。');
  }
}

export function writeChatSelection(workspaceId: string, agentId: string | null, conversationId: string | null): void {
  if (!workspaceId || !agentId) return;
  const current = readChatWorkspaceState(workspaceId, agentId, conversationId);
  writeChatWorkspaceState(workspaceId, agentId, conversationId, current ?? { composer: { draft: '' }, queue: [] });
}

export function readChatSelection(workspaceId: string): { agentId: string; conversationId: string | null } | null {
  const store = storage();
  if (!store || !workspaceId) return null;
  const prefix = `${PREFIX}${encodeURIComponent(workspaceId)}:`;
  try {
    const entries: Array<{ updatedAt: string; agentId: string; conversationId: string | null }> = [];
    for (let index = 0; index < store.length; index += 1) {
      const storageKey = store.key(index);
      if (!storageKey?.startsWith(prefix)) continue;
      const raw = store.getItem(storageKey);
      if (!raw) continue;
      const value: unknown = JSON.parse(raw);
      if (!isRecord(value) || typeof value.agentId !== 'string' || !value.agentId.trim()) continue;
      const conversationId = typeof value.conversationId === 'string' && value.conversationId ? value.conversationId : null;
      entries.push({ updatedAt: typeof value.updatedAt === 'string' ? value.updatedAt : '', agentId: value.agentId, conversationId });
    }
    entries.sort((left, right) => right.updatedAt.localeCompare(left.updatedAt));
    const first = entries[0];
    return first ? { agentId: first.agentId, conversationId: first.conversationId } : null;
  } catch {
    return null;
  }
}

function parseLegacyTaskIntakeEntry(raw: string | null, projectKey: string | null, expectedVersion: 1 | 2): LegacyTaskIntakeRecoveryEntry | null {
  if (!raw) return null;
  try {
    const value: unknown = JSON.parse(raw);
    if (!isRecord(value) || value.version !== expectedVersion) return null;
    const messages = Array.isArray(value.messages) ? value.messages.filter(isRecord).map((item) => {
      if ((item.role !== 'user' && item.role !== 'assistant') || typeof item.content !== 'string') return null;
      return { role: item.role, content: item.content } as const;
    }).filter((item): item is { role: 'user' | 'assistant'; content: string } => item !== null) : [];
    const draftValue = isRecord(value.draft) ? value.draft : null;
    const pendingInput = isRecord(value.pendingPublication) && isRecord(value.pendingPublication.input) ? value.pendingPublication.input : null;
    const draft = draftValue && typeof draftValue.title === 'string' && typeof draftValue.description === 'string' && Array.isArray(draftValue.acceptance_criteria)
      ? { title: draftValue.title, description: draftValue.description, acceptance_criteria: draftValue.acceptance_criteria.filter((item): item is string => typeof item === 'string') }
      : pendingInput && typeof pendingInput.title === 'string' && typeof pendingInput.description === 'string' && Array.isArray(pendingInput.acceptance_criteria)
        ? { title: pendingInput.title, description: pendingInput.description, acceptance_criteria: pendingInput.acceptance_criteria.filter((item): item is string => typeof item === 'string') }
        : undefined;
    const composerText = typeof value.composerText === 'string' && value.composerText.trim() ? value.composerText : undefined;
    const hasTerminalState = Boolean(value.pendingPublication || value.publishedReceipt);
    if (!draft && !composerText && messages.length === 0 && !hasTerminalState) return null;
    return { projectKey, hasTerminalState, ...(draft ? { draft } : {}), ...(composerText ? { composerText } : {}), messages };
  } catch {
    return null;
  }
}

/** Read-only compatibility reader for both old /task-chat storage formats.
 * It never deletes, merges, or silently rebinds project buckets. Chat offers
 * one explicit recovery action per discovered bucket. */
export function readLegacyTaskIntakeRecovery(workspaceId: string): LegacyTaskIntakeRecovery | null {
  const store = storage();
  if (!store || !workspaceId) return null;
  try {
    const encodedWorkspaceId = encodeURIComponent(workspaceId);
    const entries: LegacyTaskIntakeRecoveryEntry[] = [];
    const legacy = parseLegacyTaskIntakeEntry(store.getItem(`${LEGACY_TASK_INTAKE_PREFIX}${encodedWorkspaceId}`), null, 1);
    if (legacy) entries.push(legacy);
    const projectPrefix = `${LEGACY_TASK_INTAKE_PROJECT_PREFIX}${encodedWorkspaceId}:`;
    for (let index = 0; index < store.length; index += 1) {
      const storageKey = store.key(index);
      if (!storageKey?.startsWith(projectPrefix)) continue;
      let projectKey: string | null = null;
      try { projectKey = decodeURIComponent(storageKey.slice(projectPrefix.length)); } catch { projectKey = '历史项目'; }
      const entry = parseLegacyTaskIntakeEntry(store.getItem(storageKey), projectKey, 2);
      if (entry) entries.push(entry);
    }
    const bindingPresent = Boolean(store.getItem(`${LEGACY_TASK_INTAKE_BINDING_PREFIX}${encodedWorkspaceId}`));
    if (entries.length === 0 && !bindingPresent) return null;
    const latest = entries[entries.length - 1] ?? { projectKey: null, hasTerminalState: false, messages: [] };
    return { workspaceId, ...latest, entries, bindingPresent };
  } catch {
    return null;
  }
}
