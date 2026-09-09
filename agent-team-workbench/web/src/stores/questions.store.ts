import { create } from 'zustand';
import type { CanonicalEvent } from '../api/types';
import { listNativeQuestions, resolveNativeQuestion, type NativeQuestion, type NativeQuestionResponse } from '../api/questions';
import { captureScope, isCurrent, registerWorkspaceScopedReset } from './scope';

interface QuestionsState {
  itemsByRun: Record<string, NativeQuestion[]>;
  loadingByRun: Record<string, boolean>;
  errorByRun: Record<string, string | undefined>;
  submittingByQuestion: Record<string, boolean>;
  refresh: (runId: string) => Promise<void>;
  applyEvent: (event: CanonicalEvent) => void;
  resolve: (runId: string, questionId: string, response: NativeQuestionResponse) => Promise<void>;
  clear: (runId?: string) => void;
}

const refreshRequests = new Map<string, number>();
const terminalRuns = new Set<string>();
const terminalStatuses = new Set(['succeeded', 'failed', 'interrupted', 'cancelled', 'lost']);
let nextRequest = 0;

export const useNativeQuestionsStore = create<QuestionsState>((set, get) => ({
  itemsByRun: {},
  loadingByRun: {},
  errorByRun: {},
  submittingByQuestion: {},
  refresh: async (runId) => {
    if (!runId || terminalRuns.has(runId)) return;
    const scope = captureScope();
    const request = ++nextRequest;
    refreshRequests.set(runId, request);
    set((state) => ({ loadingByRun: { ...state.loadingByRun, [runId]: true }, errorByRun: { ...state.errorByRun, [runId]: undefined } }));
    try {
      const result = await listNativeQuestions(runId);
      if (!isCurrent(scope) || refreshRequests.get(runId) !== request || terminalRuns.has(runId)) return;
      set((state) => ({ itemsByRun: { ...state.itemsByRun, [runId]: result.items }, loadingByRun: { ...state.loadingByRun, [runId]: false } }));
    } catch (error) {
      if (!isCurrent(scope) || refreshRequests.get(runId) !== request || terminalRuns.has(runId)) return;
      set((state) => ({ loadingByRun: { ...state.loadingByRun, [runId]: false }, errorByRun: { ...state.errorByRun, [runId]: error instanceof Error ? error.message : '读取待回答问题失败' } }));
    }
  },
  applyEvent: (event) => {
    const runId = typeof event.data?.run_id === 'string'
      ? event.data.run_id
      : event.aggregate.type === 'execution_run' ? event.aggregate.id : '';
    if (!runId) return;
    if (['run.completed', 'run.failed', 'run.cancelled', 'run.lost'].includes(event.type)
      || (event.type === 'run.status_changed' && terminalStatuses.has(String(event.data?.status)))) {
      terminalRuns.add(runId);
      get().clear(runId);
      return;
    }
    void get().refresh(runId);
  },
  resolve: async (runId, questionId, response) => {
    if (terminalRuns.has(runId)) throw new Error('本次运行已结束，不能继续回答。');
    if (get().submittingByQuestion[questionId]) return;
    const scope = captureScope();
    set((state) => ({ submittingByQuestion: { ...state.submittingByQuestion, [questionId]: true } }));
    try {
      await resolveNativeQuestion(runId, questionId, response);
      if (isCurrent(scope) && !terminalRuns.has(runId)) await get().refresh(runId);
    } finally {
      if (isCurrent(scope)) set((state) => ({ submittingByQuestion: { ...state.submittingByQuestion, [questionId]: false } }));
    }
  },
  clear: (runId) => {
    if (!runId) {
      refreshRequests.clear();
      terminalRuns.clear();
      set({ itemsByRun: {}, loadingByRun: {}, errorByRun: {}, submittingByQuestion: {} });
    } else {
      refreshRequests.delete(runId);
      set((state) => ({ itemsByRun: { ...state.itemsByRun, [runId]: [] }, loadingByRun: { ...state.loadingByRun, [runId]: false }, errorByRun: { ...state.errorByRun, [runId]: undefined } }));
    }
  },
}));

registerWorkspaceScopedReset(() => useNativeQuestionsStore.getState().clear());
