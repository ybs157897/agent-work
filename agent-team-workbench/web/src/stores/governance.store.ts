import { create } from 'zustand';
import { ApiError } from '../api/client';
import {
  getGovernanceGoalForWorkItem,
  getGovernanceGoalQuota,
  getGovernanceProjection,
  getGovernanceTurnReceipt,
  listGovernanceGoalEvidence,
  listGovernanceGoalHandoffs,
  listGovernanceTodos,
} from '../api/endpoints';
import type {
  CanonicalEvent,
  GovernanceGoal,
  GovernanceGoalProjection,
  GovernanceGoalQuota,
  GovernanceHandoff,
  GovernanceTodo,
  GovernanceTurnReceipt,
} from '../api/types';
import { captureScope, isCurrent, registerWorkspaceScopedReset } from './scope';

export interface GovernanceView {
  goal: GovernanceGoal;
  todos: GovernanceTodo[];
  quota: GovernanceGoalQuota;
  latest_receipt?: GovernanceTurnReceipt;
  evidence: GovernanceGoal['completion_evidence_summary'];
  handoffs: GovernanceHandoff[];
  projection?: GovernanceGoalProjection;
}

export type GovernanceResolution = 'idle' | 'loading' | 'ready' | 'legacy' | 'error';

interface GovernanceStore {
  byWorkItem: Record<string, GovernanceView>;
  statusByWorkItem: Record<string, GovernanceResolution>;
  errorByWorkItem: Record<string, string | undefined>;
  refreshFor: (workItemId: string, workspaceId: string) => Promise<void>;
  applyEvent: (event: CanonicalEvent) => boolean;
  reset: () => void;
}

const requestVersions = new Map<string, number>();
const pendingRefresh = new Map<string, ReturnType<typeof setTimeout>>();

function governanceEvent(event: CanonicalEvent): boolean {
  return event.type.startsWith('goal.') || event.type.startsWith('todo.') || event.type === 'turn.receipt_appended'
    || event.type === 'handoff.created' || event.type === 'handoff.state_changed'
    || event.type === 'goal.evidence_added' || event.type === 'validation.result_recorded'
    || event.type === 'projection.updated' || event.type === 'projection.repair_state_changed'
    || event.type === 'quota.reservation_changed' || event.type === 'quota.spend_recorded' || event.type === 'quota.gap_reconciled'
    || event.type === 'delivery_brief.snapshot_created';
}

function errorMessage(err: unknown): string {
  if (err instanceof ApiError) return err.message || '治理状态加载失败，请重试';
  return '治理状态加载失败，请重试';
}

export const useGovernanceStore = create<GovernanceStore>()((set, get) => ({
  byWorkItem: {},
  statusByWorkItem: {},
  errorByWorkItem: {},

  refreshFor: async (workItemId, workspaceId) => {
    const scope = captureScope();
    if (!workspaceId || scope.workspaceId !== workspaceId) return;
    const requestVersion = (requestVersions.get(workItemId) ?? 0) + 1;
    requestVersions.set(workItemId, requestVersion);
    set((state) => ({
      statusByWorkItem: {
        ...state.statusByWorkItem,
        [workItemId]: state.byWorkItem[workItemId] ? 'ready' : 'loading',
      },
      errorByWorkItem: { ...state.errorByWorkItem, [workItemId]: undefined },
    }));
    try {
      // The server resolves the root ancestry and Goal. Do not let the browser
      // scan all workspace Goals and guess which one owns this Task.
      const goal = await getGovernanceGoalForWorkItem(workspaceId, workItemId);
      const [todoPage, quota, evidencePage, handoffPage, projection] = await Promise.all([
        listGovernanceTodos(workspaceId, goal.id),
        getGovernanceGoalQuota(workspaceId, goal.id),
        listGovernanceGoalEvidence(workspaceId, goal.id),
        listGovernanceGoalHandoffs(workspaceId, goal.id),
        getGovernanceProjection(workspaceId, goal.id).catch((err) => {
          if (err instanceof ApiError && err.status === 404) return undefined;
          throw err;
        }),
      ]);
      let latestReceipt: GovernanceTurnReceipt | undefined;
      const currentTodo = goal.current_todo_id
        ? todoPage.items.find((todo) => todo.id === goal.current_todo_id)
        : undefined;
      if (currentTodo && currentTodo.last_turn_seq > 0) {
        try {
          latestReceipt = await getGovernanceTurnReceipt(
            workspaceId,
            goal.id,
            currentTodo.id,
            currentTodo.last_turn_seq,
          );
        } catch (err) {
          // A newly admitted turn may not have its first phase visible yet;
          // preserve the rest of the read model and retry on the next event.
          if (!(err instanceof ApiError && err.status === 404)) throw err;
        }
      }
      if (
        requestVersions.get(workItemId) !== requestVersion ||
        !isCurrent(scope) ||
        goal.workspace_id !== scope.workspaceId ||
        quota.goal_id !== goal.id ||
        todoPage.items.some((todo) => todo.goal_id !== goal.id) ||
        evidencePage.items.some((item) => item.source_id.trim() === '') ||
        handoffPage.items.some((handoff) => handoff.goal_id !== goal.id) ||
        (projection !== undefined && projection.goal_id !== goal.id)
      ) return;
      set((state) => ({
        byWorkItem: {
          ...state.byWorkItem,
          [workItemId]: {
            goal,
            todos: todoPage.items,
            quota,
            evidence: evidencePage.items,
            handoffs: handoffPage.items,
            ...(projection ? { projection } : {}),
            ...(latestReceipt ? { latest_receipt: latestReceipt } : {}),
          },
        },
        statusByWorkItem: { ...state.statusByWorkItem, [workItemId]: 'ready' },
        errorByWorkItem: { ...state.errorByWorkItem, [workItemId]: undefined },
      }));
    } catch (err) {
      if (requestVersions.get(workItemId) !== requestVersion || !isCurrent(scope)) return;
      if (err instanceof ApiError && err.status === 404) {
        set((state) => {
          const byWorkItem = { ...state.byWorkItem };
          delete byWorkItem[workItemId];
          return {
            byWorkItem,
            statusByWorkItem: { ...state.statusByWorkItem, [workItemId]: 'legacy' },
            errorByWorkItem: { ...state.errorByWorkItem, [workItemId]: undefined },
          };
        });
        return;
      }
      set((state) => ({
        statusByWorkItem: { ...state.statusByWorkItem, [workItemId]: 'error' },
        errorByWorkItem: { ...state.errorByWorkItem, [workItemId]: errorMessage(err) },
      }));
    }
  },

  applyEvent: (event) => {
    if (!governanceEvent(event)) return false;
    const workspaceId = captureScope().workspaceId;
    if (!workspaceId || event.workspace_id !== workspaceId) return true;
    for (const workItemId of Object.keys(get().byWorkItem)) {
      const oldTimer = pendingRefresh.get(workItemId);
      if (oldTimer) clearTimeout(oldTimer);
      pendingRefresh.set(
        workItemId,
        setTimeout(() => {
          pendingRefresh.delete(workItemId);
          void get().refreshFor(workItemId, workspaceId);
        }, 200),
      );
    }
    return true;
  },

  reset: () => {
    for (const timer of pendingRefresh.values()) clearTimeout(timer);
    pendingRefresh.clear();
    requestVersions.clear();
    set({ byWorkItem: {}, statusByWorkItem: {}, errorByWorkItem: {} });
  },
}));

registerWorkspaceScopedReset(() => useGovernanceStore.getState().reset());
