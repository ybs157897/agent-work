import { create } from 'zustand';
import { ApiError } from '../api/client';
import {
  getChatAnalysis,
  parseChatAnalysisProjection,
  submitChatAnalysisAnswer,
  type ChatAnalysisProjection,
  type ChatAnalysisQuestion,
  type SubmitChatAnalysisAnswerInput,
} from '../api/chat-analysis';
import {
  readChatWorkspaceState,
  writeChatWorkspaceState,
  type PersistedChatAnalysisDraft,
} from './chat-workspace-state';
import { captureScope, isCurrent, registerWorkspaceScopedReset, type Scope } from './scope';

export const CHAT_ANALYSIS_OUTPUT_CONTRACT = 'chat-analysis/v1' as const;
export const CHAT_ANALYSIS_INSTRUCTION = '请整理当前对话中的需求材料，区分已观察事实、正常场景、异常场景、材料冲突和待确认项；为每项标注来源、影响和建议。在机器文档的 questions 中保留全部仍需确认的问题队列，由界面一次只显示一题；普通正文不要枚举全部问题。不要自动确认、发布任务或修改源码。';

export type ChatAnalysisDraft = PersistedChatAnalysisDraft;

interface ChatAnalysisStore {
  workspaceId: string | null;
  agentId: string | null;
  conversationId: string | null;
  projection: ChatAnalysisProjection | null;
  draft: ChatAnalysisDraft | null;
  loading: boolean;
  submitting: boolean;
  error: string | null;
  refresh: (workspaceId: string, agentId: string, conversationId: string) => Promise<void>;
  acceptProjection: (projection: ChatAnalysisProjection) => void;
  setSelection: (selectedOptionIds: string[]) => void;
  setText: (text: string) => void;
  submitAnswer: (disposition: 'answered' | 'deferred') => Promise<boolean>;
  restoreDraftText: () => void;
  discardDraft: () => void;
  reset: () => void;
}

function answerClientKey(chatId: string, questionId: string, revision: number): string {
  const uuid = typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function'
    ? crypto.randomUUID()
    : `${Date.now().toString(36)}-${Math.random().toString(36).slice(2)}`;
  return `chat-analysis-answer:${chatId}:${questionId}:${revision}:${uuid}`;
}

function draftForQuestion(
  draft: PersistedChatAnalysisDraft | null | undefined,
  projection: ChatAnalysisProjection,
): ChatAnalysisDraft | null {
  if (!draft) return null;
  if (draft.clientKey && projection.answers.some((answer) => answer.client_key === draft.clientKey)) return null;
  if (!projection.current_question) return draft;
  if (draft.questionId !== projection.current_question.id || draft.revision !== projection.revision) return draft;
  const sameVersion = draft.version === projection.version;
  const clientKey = sameVersion ? draft.clientKey : undefined;
  return {
    version: projection.version,
    revision: projection.revision,
    questionId: draft.questionId,
    selectedOptionIds: [...draft.selectedOptionIds],
    text: draft.text,
    ...(clientKey ? { clientKey } : {}),
  };
}

function writeDraft(state: ChatAnalysisStore, draft: ChatAnalysisDraft | null): void {
  if (!state.workspaceId || !state.agentId || !state.conversationId) return;
  const saved = readChatWorkspaceState(state.workspaceId, state.agentId, state.conversationId);
  writeChatWorkspaceState(state.workspaceId, state.agentId, state.conversationId, {
    composer: saved?.composer ?? { draft: '', reference: null },
    queue: saved?.queue ?? [],
    analysisDraft: draft,
  });
}

function currentQuestion(projection: ChatAnalysisProjection | null): ChatAnalysisQuestion | null {
  return projection?.current_question ?? null;
}

function projectionMatchesScope(projection: ChatAnalysisProjection, workspaceId: string, agentId: string, conversationId: string): boolean {
  return projection.workspace_id === workspaceId && projection.agent_id === agentId && projection.chat_id === conversationId;
}

function projectionIsOlder(candidate: ChatAnalysisProjection, current: ChatAnalysisProjection | null): boolean {
  if (!current) return false;
  return candidate.version < current.version || (candidate.version === current.version && candidate.revision < current.revision);
}

function scopeMatches(scope: Scope, workspaceId: string, agentId: string, conversationId: string): boolean {
  const current = captureScope();
  return isCurrent(scope) && current.workspaceId === workspaceId && useChatAnalysisStore.getState().workspaceId === workspaceId
    && useChatAnalysisStore.getState().agentId === agentId && useChatAnalysisStore.getState().conversationId === conversationId;
}

let refreshSequence = 0;
let answerSequence = 0;

export const useChatAnalysisStore = create<ChatAnalysisStore>()((set, get) => ({
  workspaceId: null,
  agentId: null,
  conversationId: null,
  projection: null,
  draft: null,
  loading: false,
  submitting: false,
  error: null,

  acceptProjection: (projection) => {
    const state = get();
    if (!state.workspaceId || !state.agentId || !state.conversationId
      || !projectionMatchesScope(projection, state.workspaceId, state.agentId, state.conversationId)
      || projectionIsOlder(projection, state.projection)) return;
    set({ projection, error: null });
  },

  refresh: async (workspaceId, agentId, conversationId) => {
    const scope = captureScope();
    if (scope.workspaceId !== workspaceId || !isCurrent(scope)) return;
    const sequence = ++refreshSequence;
    const existing = get();
    const sameScope = existing.workspaceId === workspaceId && existing.agentId === agentId && existing.conversationId === conversationId;
    set({ workspaceId, agentId, conversationId, loading: true, submitting: sameScope && existing.submitting, error: null });
    try {
      const projection = parseChatAnalysisProjection(await getChatAnalysis(conversationId));
      if (sequence !== refreshSequence || !scopeMatches(scope, workspaceId, agentId, conversationId)) return;
      if (!projectionMatchesScope(projection, workspaceId, agentId, conversationId)) throw new Error('需求分析状态与当前会话不匹配');
      const persisted = readChatWorkspaceState(workspaceId, agentId, conversationId)?.analysisDraft;
      const draft = draftForQuestion(existing.workspaceId === workspaceId && existing.agentId === agentId && existing.conversationId === conversationId ? existing.draft : persisted, projection)
        ?? draftForQuestion(persisted, projection);
      set({ projection, draft, loading: false, error: null });
      writeDraft({ ...get(), projection }, draft);
    } catch (error) {
      if (sequence !== refreshSequence || !scopeMatches(scope, workspaceId, agentId, conversationId)) return;
      set({ loading: false, error: error instanceof Error ? error.message : '需求分析状态读取失败' });
    }
  },

  setSelection: (selectedOptionIds) => {
    const state = get();
    const question = currentQuestion(state.projection);
    if (!question || state.submitting || !state.workspaceId || !state.agentId || !state.conversationId) return;
    const allowed = new Set(question.options.map((option) => option.id));
    const selected = [...new Set(selectedOptionIds.filter((id) => allowed.has(id)))];
    const next = {
      version: state.projection?.version ?? 0,
      revision: state.projection?.revision ?? 0,
      questionId: question.id,
      selectedOptionIds: question.selection === 'single' ? selected.slice(0, 1) : selected,
      text: state.draft?.questionId === question.id && state.draft.revision === state.projection?.revision ? state.draft.text : '',
    } satisfies ChatAnalysisDraft;
    set({ draft: next, error: null });
    writeDraft(get(), next);
  },

  setText: (text) => {
    const state = get();
    const question = currentQuestion(state.projection);
    if (!question || state.submitting || !state.workspaceId || !state.agentId || !state.conversationId) return;
    const next = {
      version: state.projection?.version ?? 0,
      revision: state.projection?.revision ?? 0,
      questionId: question.id,
      selectedOptionIds: state.draft?.questionId === question.id && state.draft.revision === state.projection?.revision ? [...state.draft.selectedOptionIds] : [],
      text,
    } satisfies ChatAnalysisDraft;
    set({ draft: next, error: null });
    writeDraft(get(), next);
  },

  submitAnswer: async (disposition) => {
    const state = get();
    const question = currentQuestion(state.projection);
    if (!state.projection || !question || !state.workspaceId || !state.agentId || !state.conversationId || state.submitting) return false;
    const draft = state.draft?.questionId === question.id && state.draft.revision === state.projection.revision
      ? state.draft
      : { version: state.projection.version, revision: state.projection.revision, questionId: question.id, selectedOptionIds: [], text: '' } satisfies ChatAnalysisDraft;
    if (disposition === 'answered' && question.selection !== 'text' && draft.selectedOptionIds.length === 0 && !draft.text.trim()) {
      set({ error: '请选择一个选项或填写补充说明后再保存。' });
      return false;
    }
    if (disposition === 'answered' && question.selection === 'text' && !draft.text.trim()) {
      set({ error: '请填写回答后再保存。' });
      return false;
    }
    const clientKey = draft.clientKey ?? answerClientKey(state.conversationId, question.id, state.projection.revision);
    const pendingDraft = { ...draft, version: state.projection.version, revision: state.projection.revision, clientKey };
    const scope = captureScope();
    const sequence = ++answerSequence;
    set({ draft: pendingDraft, submitting: true, error: null });
    writeDraft(get(), pendingDraft);
    const input: SubmitChatAnalysisAnswerInput = {
      expected_version: state.projection.version,
      revision: state.projection.revision,
      question_id: question.id,
      selected_option_ids: disposition === 'deferred' ? [] : [...draft.selectedOptionIds],
      text: draft.text,
      disposition,
      client_key: clientKey,
    };
    try {
      const result = await submitChatAnalysisAnswer(state.conversationId, input);
      if (sequence !== answerSequence || !scopeMatches(scope, state.workspaceId, state.agentId, state.conversationId)) return false;
      const returnedProjection = result && typeof result === 'object' && 'projection' in result
        ? result.projection
        : result;
      const projection = parseChatAnalysisProjection(returnedProjection);
      if (!projectionMatchesScope(projection, state.workspaceId, state.agentId, state.conversationId)) throw new Error('需求分析状态与当前会话不匹配');
      set({ projection, draft: null, submitting: false, error: null });
      writeDraft(get(), null);
      return true;
    } catch (error) {
      if (sequence !== answerSequence || !scopeMatches(scope, state.workspaceId, state.agentId, state.conversationId)) return false;
      if (error instanceof ApiError && error.status === 409) {
        const oldDraft = pendingDraft;
        set({ submitting: false, error: '分析内容已更新，请核对保留的回答后再提交。' });
        await get().refresh(state.workspaceId, state.agentId, state.conversationId);
        if (scopeMatches(scope, state.workspaceId, state.agentId, state.conversationId)) {
          const latest = get();
          const preserved = latest.projection?.current_question?.id === oldDraft.questionId
            ? { ...oldDraft, version: latest.projection.version, revision: latest.projection.revision, clientKey: undefined }
            : oldDraft;
          set({ draft: preserved, submitting: false, error: '分析内容已更新，请核对保留的回答后再提交。' });
          writeDraft(get(), preserved);
        }
      } else {
        set({ submitting: false, error: error instanceof Error ? error.message : '保存回答失败，请重试。' });
      }
      return false;
    }
  },

  restoreDraftText: () => {
    const state = get();
    const question = currentQuestion(state.projection);
    if (!state.projection || !question || !state.draft || state.submitting || !state.workspaceId || !state.agentId || !state.conversationId) return;
    const next: ChatAnalysisDraft = {
      version: state.projection.version,
      revision: state.projection.revision,
      questionId: question.id,
      selectedOptionIds: [],
      text: state.draft.text,
    };
    set({ draft: next, error: null });
    writeDraft(get(), next);
  },

  discardDraft: () => {
    const state = get();
    if (state.submitting) return;
    set({ draft: null, error: null });
    writeDraft(get(), null);
  },

  reset: () => {
    refreshSequence += 1;
    answerSequence += 1;
    set({ workspaceId: null, agentId: null, conversationId: null, projection: null, draft: null, loading: false, submitting: false, error: null });
  },
}));

registerWorkspaceScopedReset(() => useChatAnalysisStore.getState().reset());
