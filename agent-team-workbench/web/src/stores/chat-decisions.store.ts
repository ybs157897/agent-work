import { create } from 'zustand';
import { ApiError } from '../api/client';
import {
  parseChatAnalysisProjection,
  type ChatAnalysisItem,
  type ChatAnalysisProjection,
  type ChatDecisionOutcome,
} from '../api/chat-analysis';
import { listChatDecisionHistory, recheckChatAnalysis, submitChatDecision, type ChatDecisionHistoryEntry, type SubmitChatDecisionInput } from '../api/chat-decisions';
import { readChatWorkspaceState, writeChatWorkspaceState, type PersistedChatDecisionDraft } from './chat-workspace-state';
import { useChatAnalysisStore } from './chat-analysis.store';
import { captureScope, isCurrent, registerWorkspaceScopedReset, type Scope } from './scope';

export type ChatDecisionDraft = PersistedChatDecisionDraft;
export const CHAT_ANALYSIS_CHANGE_INSTRUCTION = '用户正在补充需求变更或新的资料。请重新核对受影响的需求事项、正常与异常场景、冲突和来源版本；保留未受影响的事项与已有产品结论，明确标记需要重新回填的事项，并在机器文档中保留完整待确认问题队列。普通正文不要枚举全部问题，不要自动确认或发布任务。';

interface ChatDecisionsStore {
  workspaceId: string | null;
  agentId: string | null;
  conversationId: string | null;
  history: ChatDecisionHistoryEntry[];
  selectedItemId: string | null;
  draft: ChatDecisionDraft | null;
  loading: boolean;
  historyLoading: boolean;
  submitting: boolean;
  error: string | null;
  refresh: (workspaceId: string, agentId: string, conversationId: string) => Promise<void>;
  syncProjection: (projection: ChatAnalysisProjection | null) => void;
  selectItem: (itemId: string) => void;
  recheck: () => Promise<boolean>;
  setOutcome: (outcome: ChatDecisionOutcome) => void;
  setConclusion: (conclusion: string) => void;
  setBasis: (basis: string) => void;
  setProductVersion: (productVersion: string) => void;
  submit: () => Promise<boolean>;
  restoreDraftText: () => void;
  discardDraft: () => void;
  reset: () => void;
}

function decisionClientKey(chatId: string, itemId: string, revision: number): string {
  const uuid = typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function'
    ? crypto.randomUUID()
    : `${Date.now().toString(36)}-${Math.random().toString(36).slice(2)}`;
  return `chat-decision:${chatId}:${itemId}:${revision}:${uuid}`;
}

function normalizeHistory(value: unknown): ChatDecisionHistoryEntry[] {
  if (!Array.isArray(value)) return [];
  return value.filter((entry): entry is ChatDecisionHistoryEntry => {
    if (!entry || typeof entry !== 'object') return false;
    const item = entry as Record<string, unknown>;
    return typeof item.id === 'string' && typeof item.kind === 'string' && ['revision', 'answer', 'decision', 'reopen'].includes(item.kind)
      && typeof item.revision === 'number' && typeof item.created_at === 'string';
  });
}

function scopeMatches(scope: Scope, workspaceId: string, agentId: string, conversationId: string): boolean {
  const state = useChatDecisionsStore.getState();
  return isCurrent(scope) && captureScope().workspaceId === workspaceId
    && state.workspaceId === workspaceId && state.agentId === agentId && state.conversationId === conversationId;
}

function projectionMatchesScope(projection: ChatAnalysisProjection | null, workspaceId: string, agentId: string, conversationId: string): projection is ChatAnalysisProjection {
  return Boolean(projection && projection.workspace_id === workspaceId && projection.agent_id === agentId && projection.chat_id === conversationId);
}

function chooseItemId(projection: ChatAnalysisProjection | null, selectedItemId: string | null): string | null {
  const items = projection?.document?.items ?? [];
  if (selectedItemId && items.some((item) => item.id === selectedItemId)) return selectedItemId;
  return items.find((item) => projection?.decisions.some((decision) => decision.item_id === item.id))?.id ?? items[0]?.id ?? null;
}

function currentItem(projection: ChatAnalysisProjection | null, selectedItemId: string | null): ChatAnalysisItem | null {
  const itemId = chooseItemId(projection, selectedItemId);
  return projection?.document?.items.find((item) => item.id === itemId) ?? null;
}

function draftMatches(draft: ChatDecisionDraft | null | undefined, itemId: string, revision: number): draft is ChatDecisionDraft {
  return draft?.itemId === itemId && draft.revision === revision;
}

function writeDecisionDraft(state: ChatDecisionsStore, draft: ChatDecisionDraft | null): void {
  if (!state.workspaceId || !state.agentId || !state.conversationId) return;
  const saved = readChatWorkspaceState(state.workspaceId, state.agentId, state.conversationId);
  writeChatWorkspaceState(state.workspaceId, state.agentId, state.conversationId, {
    composer: saved?.composer ?? { draft: '' },
    queue: saved?.queue ?? [],
    decisionDraft: draft,
  });
}

let refreshSequence = 0;
let submitSequence = 0;
const settledDecisionClientKeys = new Set<string>();

export const useChatDecisionsStore = create<ChatDecisionsStore>()((set, get) => ({
  workspaceId: null,
  agentId: null,
  conversationId: null,
  history: [],
  selectedItemId: null,
  draft: null,
  loading: false,
  historyLoading: false,
  submitting: false,
  error: null,

  syncProjection: (projection) => {
    const state = get();
    if (!projectionMatchesScope(projection, state.workspaceId ?? '', state.agentId ?? '', state.conversationId ?? '')) return;
    set({ selectedItemId: chooseItemId(projection, state.selectedItemId) });
  },

  refresh: async (workspaceId, agentId, conversationId) => {
    const scope = captureScope();
    if (scope.workspaceId !== workspaceId || !isCurrent(scope)) return;
    const sequence = ++refreshSequence;
    const existing = get();
    const sameScope = existing.workspaceId === workspaceId && existing.agentId === agentId && existing.conversationId === conversationId;
    set({ workspaceId, agentId, conversationId, loading: true, historyLoading: true, submitting: sameScope && existing.submitting, error: null });
    try {
      let projection = useChatAnalysisStore.getState().projection;
      if (!projectionMatchesScope(projection, workspaceId, agentId, conversationId)) {
        await useChatAnalysisStore.getState().refresh(workspaceId, agentId, conversationId);
        projection = useChatAnalysisStore.getState().projection;
      }
      const rawHistory = await listChatDecisionHistory(conversationId);
      const persistedAtResponse = readChatWorkspaceState(workspaceId, agentId, conversationId)?.decisionDraft;
      const receiptClientKey = get().draft?.clientKey ?? existing.draft?.clientKey ?? persistedAtResponse?.clientKey;
      const projectionReceipt = projection?.decisions.some((decision) => decision.client_key && decision.client_key === receiptClientKey) ?? false;
      const historyReceipt = rawHistory.items.some((entry) => entry.client_key && entry.client_key === receiptClientKey);
      // Record a receipt before the response-order guard. A slower request may
      // return after a newer refresh and still be the first response that proves
      // the server committed the idempotent decision.
      if (receiptClientKey && (projectionReceipt || historyReceipt)) {
        settledDecisionClientKeys.add(receiptClientKey);
        if (scopeMatches(scope, workspaceId, agentId, conversationId) && get().draft?.clientKey === receiptClientKey) {
          set({ draft: null });
          writeDecisionDraft(get(), null);
        }
      }
      if (sequence !== refreshSequence || !scopeMatches(scope, workspaceId, agentId, conversationId)) return;
      const latestProjection = useChatAnalysisStore.getState().projection;
      if (projectionMatchesScope(latestProjection, workspaceId, agentId, conversationId)) projection = latestProjection;
      if (!projectionMatchesScope(projection, workspaceId, agentId, conversationId)) throw new Error('产品结论状态与当前会话不匹配');
      const persisted = persistedAtResponse;
      const latestState = get();
      const latestInMemoryDraft = latestState.workspaceId === workspaceId && latestState.agentId === agentId && latestState.conversationId === conversationId
        ? latestState.draft
        : null;
      // The request may have started before submit() minted a client key. Use
      // the newest in-memory/persisted draft at response time so that an old
      // refresh cannot write its keyless closure back over a pending retry.
      const draft = latestInMemoryDraft?.clientKey
        ? latestInMemoryDraft
        : persisted?.clientKey
          ? persisted
          : latestInMemoryDraft ?? persisted ?? null;
      const savedClientKey = projection.decisions.some((decision) => decision.client_key && decision.client_key === draft?.clientKey)
        || rawHistory.items.some((entry) => entry.client_key && entry.client_key === draft?.clientKey)
        || Boolean(draft?.clientKey && settledDecisionClientKeys.has(draft.clientKey));
      const latestSelection = get().selectedItemId;
      const draftItemId = draft?.itemId && projection.document?.items.some((item) => item.id === draft.itemId) ? draft.itemId : null;
      const selectedItemId = draftItemId ?? chooseItemId(projection, latestSelection ?? existing.selectedItemId);
      set({ selectedItemId, history: normalizeHistory(rawHistory.items), draft: savedClientKey ? null : draft, loading: false, historyLoading: false, error: null });
      writeDecisionDraft(get(), savedClientKey ? null : draft);
    } catch (error) {
      if (sequence !== refreshSequence || !scopeMatches(scope, workspaceId, agentId, conversationId)) return;
      set({ loading: false, historyLoading: false, error: error instanceof Error ? error.message : '产品结论状态读取失败' });
    }
  },

  selectItem: (itemId) => {
    const state = get();
    const projection = useChatAnalysisStore.getState().projection;
    if (!projectionMatchesScope(projection, state.workspaceId ?? '', state.agentId ?? '', state.conversationId ?? '')
      || !projection.document?.items.some((item) => item.id === itemId) || state.submitting) return;
    set({ selectedItemId: itemId, error: null });
  },

  recheck: async () => {
    const state = get();
    if (!state.workspaceId || !state.agentId || !state.conversationId || state.submitting) return false;
    const scope = captureScope();
    try {
      const projection = parseChatAnalysisProjection(await recheckChatAnalysis(state.conversationId));
      if (!scopeMatches(scope, state.workspaceId, state.agentId, state.conversationId)) return false;
      if (!projectionMatchesScope(projection, state.workspaceId, state.agentId, state.conversationId)) throw new Error('复核结果与当前会话不匹配');
      useChatAnalysisStore.getState().acceptProjection(projection);
      get().syncProjection(projection);
      set({ error: null });
      return true;
    } catch (error) {
      if (scopeMatches(scope, state.workspaceId, state.agentId, state.conversationId)) set({ error: error instanceof Error ? error.message : '材料复核失败' });
      return false;
    }
  },

  setOutcome: (outcome) => {
    const state = get();
    const projection = useChatAnalysisStore.getState().projection;
    const item = currentItem(projection, state.selectedItemId);
    if (!item || !projection || state.submitting || !state.workspaceId || !state.agentId || !state.conversationId) return;
    const currentDraft = draftMatches(state.draft, item.id, projection.revision) ? state.draft : null;
    const next = { version: projection.version, revision: projection.revision, itemId: item.id, outcome, conclusion: currentDraft?.conclusion ?? '', basis: currentDraft?.basis ?? '', productVersion: currentDraft?.productVersion ?? '', ...(currentDraft?.clientKey ? { clientKey: currentDraft.clientKey } : {}) } satisfies ChatDecisionDraft;
    set({ draft: next, error: null });
    writeDecisionDraft(get(), next);
  },

  setConclusion: (conclusion) => {
    const state = get();
    const projection = useChatAnalysisStore.getState().projection;
    const item = currentItem(projection, state.selectedItemId);
    if (!item || !projection || state.submitting || !state.workspaceId || !state.agentId || !state.conversationId) return;
    const currentDraft = draftMatches(state.draft, item.id, projection.revision) ? state.draft : null;
    const next = { version: projection.version, revision: projection.revision, itemId: item.id, outcome: currentDraft?.outcome ?? 'needs_clarification', conclusion, basis: currentDraft?.basis ?? '', productVersion: currentDraft?.productVersion ?? '', ...(currentDraft?.clientKey ? { clientKey: currentDraft.clientKey } : {}) } satisfies ChatDecisionDraft;
    set({ draft: next, error: null });
    writeDecisionDraft(get(), next);
  },

  setBasis: (basis) => {
    const state = get();
    const projection = useChatAnalysisStore.getState().projection;
    const item = currentItem(projection, state.selectedItemId);
    if (!item || !projection || state.submitting || !state.workspaceId || !state.agentId || !state.conversationId) return;
    const currentDraft = draftMatches(state.draft, item.id, projection.revision) ? state.draft : null;
    const next = { version: projection.version, revision: projection.revision, itemId: item.id, outcome: currentDraft?.outcome ?? 'needs_clarification', conclusion: currentDraft?.conclusion ?? '', basis, productVersion: currentDraft?.productVersion ?? '', ...(currentDraft?.clientKey ? { clientKey: currentDraft.clientKey } : {}) } satisfies ChatDecisionDraft;
    set({ draft: next, error: null });
    writeDecisionDraft(get(), next);
  },

  setProductVersion: (productVersion) => {
    const state = get();
    const projection = useChatAnalysisStore.getState().projection;
    const item = currentItem(projection, state.selectedItemId);
    if (!item || !projection || state.submitting || !state.workspaceId || !state.agentId || !state.conversationId) return;
    const currentDraft = draftMatches(state.draft, item.id, projection.revision) ? state.draft : null;
    const next = { version: projection.version, revision: projection.revision, itemId: item.id, outcome: currentDraft?.outcome ?? 'needs_clarification', conclusion: currentDraft?.conclusion ?? '', basis: currentDraft?.basis ?? '', productVersion, ...(currentDraft?.clientKey ? { clientKey: currentDraft.clientKey } : {}) } satisfies ChatDecisionDraft;
    set({ draft: next, error: null });
    writeDecisionDraft(get(), next);
  },

  submit: async () => {
    const state = get();
    let projection = useChatAnalysisStore.getState().projection;
    const item = currentItem(projection, state.selectedItemId);
    if (!projection || !item || !state.workspaceId || !state.agentId || !state.conversationId || state.submitting) return false;
    if (!state.draft || state.draft.itemId !== item.id || state.draft.revision !== projection.revision) {
      set({ error: '当前事项或材料版本已变化，请重新填写产品结论。' });
      return false;
    }
    if (!state.draft.conclusion.trim() || !state.draft.basis.trim() || !state.draft.productVersion.trim()) {
      set({ error: '结论、依据和产品版本都必须填写。' });
      return false;
    }
    const rechecked = await get().recheck();
    if (!rechecked) return false;
    projection = useChatAnalysisStore.getState().projection;
    const latest = get();
    const latestItem = currentItem(projection, latest.selectedItemId);
    const chatId = latest.conversationId;
    const workspaceId = latest.workspaceId;
    const agentId = latest.agentId;
    if (!chatId || !workspaceId || !agentId) {
      set({ error: '当前 Chat 已切换，请重新加载产品结论。' });
      return false;
    }
    const chatIdChecked: string = chatId;
    const workspaceIdChecked: string = workspaceId;
    const agentIdChecked: string = agentId;
    if (!projection || !latestItem || !latest.draft || latest.draft.itemId !== latestItem.id || latest.draft.revision !== projection.revision) {
      set({ error: '材料复核后事项已变化，请重新填写产品结论。' });
      return false;
    }
    const clientKey = latest.draft.clientKey ?? decisionClientKey(chatIdChecked, latestItem.id, projection.revision);
    const pending = { ...latest.draft, version: projection.version, revision: projection.revision, clientKey };
    const scope = captureScope();
    const sequence = ++submitSequence;
    set({ draft: pending, submitting: true, error: null });
    writeDecisionDraft(get(), pending);
    const input: SubmitChatDecisionInput = {
      expected_version: projection.version, revision: projection.revision,
      item_id: latestItem.id, outcome: pending.outcome, conclusion: pending.conclusion,
      basis: pending.basis, product_version: pending.productVersion, client_key: clientKey,
    };
    try {
      const returnedProjection = parseChatAnalysisProjection(await submitChatDecision(chatIdChecked, input));
      if (sequence !== submitSequence || !scopeMatches(scope, workspaceIdChecked, agentIdChecked, chatIdChecked)) return false;
      if (!projectionMatchesScope(returnedProjection, workspaceIdChecked, agentIdChecked, chatIdChecked)) throw new Error('产品结论结果与当前会话不匹配');
      settledDecisionClientKeys.add(clientKey);
      useChatAnalysisStore.getState().acceptProjection(returnedProjection);
      get().syncProjection(returnedProjection);
      set({ draft: null, submitting: false, error: null });
      writeDecisionDraft(get(), null);
      return true;
    } catch (error) {
      if (sequence !== submitSequence || !scopeMatches(scope, workspaceIdChecked, agentIdChecked, chatIdChecked)) return false;
      if (error instanceof ApiError && error.status === 409) {
        set({ submitting: false, error: '产品结论所依据的材料已变化，请核对后重新填写。' });
        await useChatAnalysisStore.getState().refresh(workspaceIdChecked, agentIdChecked, chatIdChecked);
        await get().refresh(workspaceIdChecked, agentIdChecked, chatIdChecked);
        if (scopeMatches(scope, workspaceIdChecked, agentIdChecked, chatIdChecked)) {
          const latestAfterConflict = get();
          const latestAfterProjection = useChatAnalysisStore.getState().projection;
          const latestAfterItem = currentItem(latestAfterProjection, latestAfterConflict.selectedItemId);
          const sameRevision = latestAfterProjection?.revision === pending.revision && latestAfterItem?.id === pending.itemId;
          const preserved = sameRevision && latestAfterProjection
            ? { ...pending, version: latestAfterProjection.version, clientKey: undefined }
            : pending;
          set({ draft: preserved, submitting: false, error: '产品结论所依据的材料已变化，请核对后重新填写。' });
          writeDecisionDraft(get(), preserved);
        }
      } else {
        set({ submitting: false, error: error instanceof Error ? error.message : '产品结论保存失败，请重试。' });
      }
      return false;
    }
  },

  restoreDraftText: () => {
    const state = get();
    const projection = useChatAnalysisStore.getState().projection;
    const item = currentItem(projection, state.selectedItemId);
    if (!projection || !item || !state.draft || state.submitting || !state.workspaceId || !state.agentId || !state.conversationId) return;
    const next: ChatDecisionDraft = { version: projection.version, revision: projection.revision, itemId: item.id, outcome: 'needs_clarification', conclusion: state.draft.conclusion, basis: '', productVersion: '' };
    set({ draft: next, error: null });
    writeDecisionDraft(get(), next);
  },

  discardDraft: () => {
    if (get().submitting) return;
    set({ draft: null, error: null });
    writeDecisionDraft(get(), null);
  },

  reset: () => {
    refreshSequence += 1;
    submitSequence += 1;
    settledDecisionClientKeys.clear();
    set({ workspaceId: null, agentId: null, conversationId: null, history: [], selectedItemId: null, draft: null, loading: false, historyLoading: false, submitting: false, error: null });
  },
}));

registerWorkspaceScopedReset(() => useChatDecisionsStore.getState().reset());
