import { create } from 'zustand';
import { ApiError } from '../api/client';
import {
  createPublicationDraft,
  recheckPublicationDraft,
  publishPublicationDraft,
} from '../api/task-publications';
import type { ChatAnalysisItem, ChatAnalysisProjection } from '../api/chat-analysis';
import {
  readChatWorkspaceState,
  writeChatWorkspaceState,
  type PersistedPublicationDraft,
} from './chat-workspace-state';
import { useChatAnalysisStore } from './chat-analysis.store';
import { captureScope, isCurrent, registerWorkspaceScopedReset, type Scope } from './scope';

interface PublicationStore {
  workspaceId: string | null;
  agentId: string | null;
  conversationId: string | null;
  draft: PersistedPublicationDraft | null;
  loading: boolean;
  saving: boolean;
  publishing: boolean;
  error: string | null;
  hydrate: (workspaceId: string, agentId: string, conversationId: string) => void;
  refresh: () => Promise<void>;
  reconcile: (projection: ChatAnalysisProjection | null) => void;
  openFromAnalysis: () => void;
  toggleItem: (itemId: string) => void;
  setTitle: (title: string) => void;
  startNewDraft: () => void;
  saveDraft: () => Promise<boolean>;
  publish: () => Promise<boolean>;
  reset: () => void;
}

let saveSequence = 0;
let publishSequence = 0;
let refreshSequence = 0;

function randomKey(prefix: string, chatId: string, revision: number): string {
  const uuid = typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function'
    ? crypto.randomUUID()
    : `${Date.now().toString(36)}-${Math.random().toString(36).slice(2)}`;
  return `${prefix}:${chatId}:${revision}:${uuid}`;
}

function projectionMatchesScope(projection: ChatAnalysisProjection | null, workspaceId: string | null, agentId: string | null, conversationId: string | null): projection is ChatAnalysisProjection {
  return Boolean(projection && workspaceId && agentId && conversationId
    && projection.workspace_id === workspaceId && projection.agent_id === agentId && projection.chat_id === conversationId);
}

function eligibleItems(projection: ChatAnalysisProjection | null): ChatAnalysisItem[] {
  if (!projection || projection.status === 'stale' || projection.status === 'analyzing' || projection.status === 'failed' || !projection.document) return [];
  const itemIds = new Set(projection.document.items.map((item) => item.id));
  const selected = new Set(projection.decisions
    .filter((decision) => decision.revision === projection.revision && decision.status === 'valid' && decision.outcome === 'confirmed' && itemIds.has(decision.item_id))
    .map((decision) => decision.item_id));
  return projection.document.items.filter((item) => selected.has(item.id));
}

function eligibleItemIds(projection: ChatAnalysisProjection | null): Set<string> {
  return new Set(eligibleItems(projection).map((item) => item.id));
}

function writeDraft(state: PublicationStore, draft: PersistedPublicationDraft | null): void {
  if (!state.workspaceId || !state.agentId || !state.conversationId) return;
  const saved = readChatWorkspaceState(state.workspaceId, state.agentId, state.conversationId);
  writeChatWorkspaceState(state.workspaceId, state.agentId, state.conversationId, {
    composer: saved?.composer ?? { draft: '' },
    queue: saved?.queue ?? [],
    publicationDraft: draft,
  });
}

function currentScopeMatches(scope: Scope, state: PublicationStore, workspaceId: string, agentId: string, conversationId: string): boolean {
  return isCurrent(scope) && state.workspaceId === workspaceId && state.agentId === agentId && state.conversationId === conversationId;
}

function rotateDraft(draft: PersistedPublicationDraft, chatId: string, patch: Partial<Pick<PersistedPublicationDraft, 'itemIds' | 'title'>>): PersistedPublicationDraft {
  const changed = patch.itemIds !== undefined
    ? patch.itemIds.join('\u0000') !== draft.itemIds.join('\u0000')
    : patch.title !== undefined && patch.title !== draft.title;
  if (!changed) return { ...draft, ...patch, error: undefined };
  return {
    ...draft,
    ...patch,
    clientKey: randomKey('draft', chatId, draft.revision),
    description: '',
    acceptanceCriteria: [],
    draftId: undefined,
    publishClientKey: undefined,
    frozen: undefined,
    taskId: undefined,
    publishedAt: undefined,
    status: 'editing',
    lastOperation: undefined,
    error: undefined,
  };
}

function validateDraft(projection: ChatAnalysisProjection | null, draft: PersistedPublicationDraft | null): string | null {
  if (!projection) return '需求分析尚未加载完成，请稍后重试。';
  if (projection.status === 'stale' || projection.status === 'analyzing' || projection.status === 'failed') return '当前分析材料需要先完成复核，暂不能形成任务草案。';
  if (!projection.document) return '当前没有可用于形成任务的分析文档。';
  if (!draft || draft.itemIds.length === 0) return '至少选择一个已确认事项。';
  if (!draft.title.trim()) return '请填写任务标题。';
  if (draft.revision !== projection.revision) return '分析版本已变化，请重新核对任务草案。';
  const allowed = eligibleItemIds(projection);
  if (draft.itemIds.some((itemId) => !allowed.has(itemId))) return '选中的事项已失效或尚未获得当前产品确认。';
  return null;
}

function retryablePublishError(error: unknown): boolean {
  if (!(error instanceof ApiError)) return true;
  return error.retryable || error.status >= 500 || error.status === 429 || error.code === 'idempotency_in_progress';
}

export const usePublicationStore = create<PublicationStore>()((set, get) => ({
  workspaceId: null,
  agentId: null,
  conversationId: null,
  draft: null,
  loading: false,
  saving: false,
  publishing: false,
  error: null,

  hydrate: (workspaceId, agentId, conversationId) => {
    const persisted = readChatWorkspaceState(workspaceId, agentId, conversationId)?.publicationDraft ?? null;
    set({ workspaceId, agentId, conversationId, draft: persisted, loading: false, saving: false, publishing: false, error: persisted?.error ?? null });
    get().reconcile(useChatAnalysisStore.getState().projection);
  },

  refresh: async () => {
    const state = get();
    if (!state.workspaceId || !state.agentId || !state.conversationId || !state.draft?.draftId) return;
    const scope = captureScope();
    const draftId = state.draft.draftId;
    const sequence = ++refreshSequence;
    set({ loading: true });
    try {
      const receipt = await recheckPublicationDraft(state.conversationId, draftId, state.draft.expectedVersion);
      if (sequence !== refreshSequence || !currentScopeMatches(scope, get(), state.workspaceId, state.agentId, state.conversationId)) return;
      const next = {
        ...get().draft!,
        expectedVersion: receipt.version,
        revision: receipt.analysis_revision,
        itemIds: [...receipt.item_ids],
        title: receipt.title,
        description: receipt.description,
        acceptanceCriteria: [...receipt.acceptance_criteria],
        frozen: receipt,
        status: receipt.status === 'published' ? 'published' as const : receipt.status === 'stale' ? 'stale' as const : get().draft!.status,
        lastOperation: receipt.status === 'published' ? undefined : get().draft!.lastOperation,
        taskId: receipt.task_id ?? get().draft!.taskId,
        error: receipt.status === 'stale' ? '分析、确认或项目基线已变化，请重新核对草案。' : undefined,
      };
      set({ draft: next, loading: false, error: next.error ?? null });
      writeDraft(get(), next);
    } catch (error) {
      if (sequence !== refreshSequence || !currentScopeMatches(scope, get(), state.workspaceId, state.agentId, state.conversationId)) return;
      const message = error instanceof ApiError && error.status === 409 ? '任务草案已过期，请重新核对。' : error instanceof Error ? error.message : '任务草案刷新失败';
      set({ loading: false, error: message });
    }
  },

  reconcile: (projection) => {
    const state = get();
    if (!state.draft || !projectionMatchesScope(projection, state.workspaceId, state.agentId, state.conversationId)) return;
    if (state.draft.status === 'editing' && !state.draft.draftId) return;
    const reason = validateDraft(projection, state.draft);
    if (!reason || state.draft.status === 'published') return;
    const next = { ...state.draft, status: 'stale' as const, error: reason };
    set({ draft: next, error: reason });
    writeDraft(get(), next);
  },

  openFromAnalysis: () => {
    const state = get();
    const projection = useChatAnalysisStore.getState().projection;
    if (!projectionMatchesScope(projection, state.workspaceId, state.agentId, state.conversationId) || state.draft) return;
    const next: PersistedPublicationDraft = {
      expectedVersion: projection.version,
      revision: projection.revision,
      itemIds: [],
      title: '',
      description: '',
      acceptanceCriteria: [],
      clientKey: randomKey('draft', state.conversationId!, projection.revision),
      status: 'editing',
    };
    set({ draft: next, error: null });
    writeDraft(get(), next);
  },

  toggleItem: (itemId) => {
    const state = get();
    const projection = useChatAnalysisStore.getState().projection;
    if (state.saving || state.publishing || state.draft?.status === 'published' || state.draft?.status === 'failed' || state.draft?.status === 'publishing' || !projectionMatchesScope(projection, state.workspaceId, state.agentId, state.conversationId)) return;
    const allowed = eligibleItemIds(projection);
    if (!allowed.has(itemId)) return;
    const current = state.draft ?? {
      expectedVersion: projection.version,
      revision: projection.revision,
      itemIds: [],
      title: '',
      description: '',
      acceptanceCriteria: [],
      clientKey: randomKey('draft', state.conversationId!, projection.revision),
      status: 'editing' as const,
    } satisfies PersistedPublicationDraft;
    const itemIds = current.itemIds.includes(itemId) ? current.itemIds.filter((id) => id !== itemId) : [...current.itemIds, itemId];
    const next = rotateDraft(current, state.conversationId!, { itemIds });
    set({ draft: next, error: null });
    writeDraft(get(), next);
  },

  setTitle: (title) => {
    const state = get();
    const projection = useChatAnalysisStore.getState().projection;
    if (state.saving || state.publishing || state.draft?.status === 'published' || state.draft?.status === 'failed' || state.draft?.status === 'publishing' || !projectionMatchesScope(projection, state.workspaceId, state.agentId, state.conversationId)) return;
    const current = state.draft ?? {
      expectedVersion: projection.version,
      revision: projection.revision,
      itemIds: [],
      title: '',
      description: '',
      acceptanceCriteria: [],
      clientKey: randomKey('draft', state.conversationId!, projection.revision),
      status: 'editing' as const,
    } satisfies PersistedPublicationDraft;
    const next = rotateDraft(current, state.conversationId!, { title });
    set({ draft: next, error: null });
    writeDraft(get(), next);
  },

  startNewDraft: () => {
    const state = get();
    const projection = useChatAnalysisStore.getState().projection;
    if (!state.draft || state.draft.status !== 'published' || !state.draft.taskId
      || !projectionMatchesScope(projection, state.workspaceId, state.agentId, state.conversationId)) return;
    const next: PersistedPublicationDraft = {
      expectedVersion: projection.version,
      revision: projection.revision,
      itemIds: [],
      title: '',
      description: '',
      acceptanceCriteria: [],
      clientKey: randomKey('draft', state.conversationId!, projection.revision),
      status: 'editing',
    };
    set({ draft: next, error: null });
    writeDraft(get(), next);
  },

  saveDraft: async () => {
    const state = get();
    const projection = useChatAnalysisStore.getState().projection;
    if (state.saving || state.publishing || !state.workspaceId || !state.agentId || !state.conversationId) return false;
    const draft = state.draft;
    const validationError = validateDraft(projection, draft);
    if (validationError) {
      set({ error: validationError, draft: draft ? { ...draft, status: 'stale', error: validationError } : draft });
      if (draft) writeDraft(get(), { ...draft, status: 'stale', error: validationError });
      return false;
    }
    const checkedProjection = projection!;
    const pending = { ...draft!, expectedVersion: checkedProjection.version, revision: checkedProjection.revision, status: 'saving' as const, lastOperation: 'save' as const, error: undefined };
    const scope = captureScope();
    const sequence = ++saveSequence;
    set({ draft: pending, saving: true, error: null });
    writeDraft(get(), pending);
    try {
      const receipt = await createPublicationDraft(state.conversationId, {
        expected_version: checkedProjection.version,
        revision: checkedProjection.revision,
        item_ids: [...pending.itemIds],
        title: pending.title.trim(),
        client_key: pending.clientKey,
      });
      if (sequence !== saveSequence || !currentScopeMatches(scope, get(), state.workspaceId, state.agentId, state.conversationId)) return false;
      if (receipt.workspace_id !== state.workspaceId || receipt.chat_id !== state.conversationId || receipt.agent_id !== state.agentId || receipt.analysis_revision !== checkedProjection.revision || receipt.version < 1
        || [...receipt.item_ids].sort().join('\u0000') !== [...pending.itemIds].sort().join('\u0000')) throw new Error('任务草案结果与当前会话不匹配');
      const next = {
        ...pending,
        expectedVersion: receipt.version,
        draftId: receipt.draft_id,
        description: receipt.description,
        acceptanceCriteria: [...receipt.acceptance_criteria],
        frozen: receipt,
        status: 'ready' as const,
        lastOperation: undefined,
        error: undefined,
      };
      set({ draft: next, saving: false, error: null });
      writeDraft(get(), next);
      return true;
    } catch (error) {
      if (sequence !== saveSequence || !currentScopeMatches(scope, get(), state.workspaceId, state.agentId, state.conversationId)) return false;
      const message = error instanceof ApiError && error.status === 409 ? '分析、确认或项目基线已变化，请重新核对草案。' : error instanceof Error ? error.message : '任务草案保存失败，请重试。';
      const next = { ...pending, status: error instanceof ApiError && error.status === 409 ? 'stale' as const : 'failed' as const, error: message };
      set({ draft: next, saving: false, error: message });
      writeDraft(get(), next);
      return false;
    }
  },

  publish: async () => {
    const state = get();
    const projection = useChatAnalysisStore.getState().projection;
    const draft = state.draft;
    if (state.saving || state.publishing || !state.workspaceId || !state.agentId || !state.conversationId || !draft?.draftId) return false;
    const retryingFailedPublish = draft.status === 'failed' && draft.lastOperation === 'publish' && Boolean(draft.publishClientKey);
    const validationError = validateDraft(projection, draft);
    if (validationError || (draft.status !== 'ready' && !retryingFailedPublish)) {
      const message = validationError ?? '请先保存最新任务草案。';
      const next = { ...draft, status: 'stale' as const, error: message };
      set({ draft: next, error: message });
      writeDraft(get(), next);
      return false;
    }
    const checkedProjection = projection!;
    const publishClientKey = draft.publishClientKey ?? randomKey('publish', draft.draftId, checkedProjection.revision);
    const pending = { ...draft, publishClientKey, status: 'publishing' as const, lastOperation: 'publish' as const, error: undefined };
    const scope = captureScope();
    const sequence = ++publishSequence;
    set({ draft: pending, publishing: true, error: null });
    writeDraft(get(), pending);
    try {
      const receipt = await publishPublicationDraft(state.conversationId, draft.draftId, { expected_version: draft.expectedVersion, client_key: publishClientKey });
      if (sequence !== publishSequence || !currentScopeMatches(scope, get(), state.workspaceId, state.agentId, state.conversationId)) return false;
      if (receipt.task.workspace_id !== state.workspaceId || receipt.task.record_kind !== 'task' || receipt.draft.draft_id !== pending.draftId) throw new Error('发布结果与当前 Workspace/Task 不匹配');
      const next = {
        ...pending,
        expectedVersion: receipt.draft.version,
        description: receipt.draft.description,
        acceptanceCriteria: [...receipt.draft.acceptance_criteria],
        frozen: receipt.draft,
        status: 'published' as const,
        taskId: receipt.task.id,
        publishedAt: new Date().toISOString(),
        lastOperation: undefined,
        error: undefined,
      };
      set({ draft: next, publishing: false, error: null });
      writeDraft(get(), next);
      return true;
    } catch (error) {
      if (sequence !== publishSequence || !currentScopeMatches(scope, get(), state.workspaceId, state.agentId, state.conversationId)) return false;
      const retryable = retryablePublishError(error);
      const message = retryable ? '发布结果暂未确认，请使用同一请求重试。' : error instanceof ApiError && error.status === 409 ? '项目、材料或产品确认已变化，请重新核对草案。' : error instanceof Error ? error.message : '发布失败，请重新形成草案。';
      const next = { ...pending, status: retryable ? 'failed' as const : 'stale' as const, lastOperation: retryable ? 'publish' as const : undefined, error: message };
      set({ draft: next, publishing: false, error: message });
      writeDraft(get(), next);
      return false;
    }
  },

  reset: () => {
    saveSequence += 1;
    publishSequence += 1;
    refreshSequence += 1;
    set({ workspaceId: null, agentId: null, conversationId: null, draft: null, loading: false, saving: false, publishing: false, error: null });
  },
}));

registerWorkspaceScopedReset(() => usePublicationStore.getState().reset());
