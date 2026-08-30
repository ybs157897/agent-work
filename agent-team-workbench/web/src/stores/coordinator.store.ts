import { create } from 'zustand';
import { getCoordinatorSnapshot, listCoordinatorEvents } from '../api/endpoints';
import type { CanonicalEvent, CoordinatorSnapshot } from '../api/types';

interface CoordinatorStore {
  /** work_item_id → task control-line snapshot. Chat records are never cached here. */
  byWorkItem: Record<string, CoordinatorSnapshot>;
  /** work_item_id → latest request error; an error must not become a fake empty state. */
  errorByWorkItem: Record<string, string | undefined>;
  refreshFor: (workItemId: string) => Promise<void>;
  applyEvent: (event: CanonicalEvent) => boolean;
  clear: (workItemId: string) => void;
}

const requestVersions = new Map<string, number>();
const pendingRefresh = new Map<string, ReturnType<typeof setTimeout>>();

function eventWorkItemId(event: CanonicalEvent): string {
  const root = event.data?.root_work_item_id;
  if (typeof root === 'string' && root.length > 0) return root;
  const hinted = event.data?.work_item_id;
  if (typeof hinted === 'string' && hinted.length > 0) return hinted;
  return event.aggregate.type === 'work_item' ? event.aggregate.id : '';
}

function isTaskEvent(event: CanonicalEvent): boolean {
  return event.data?.record_kind === 'task';
}

export const useCoordinatorStore = create<CoordinatorStore>()((set, get) => ({
  byWorkItem: {},
  errorByWorkItem: {},

  refreshFor: async (workItemId) => {
    const requestVersion = (requestVersions.get(workItemId) ?? 0) + 1;
    requestVersions.set(workItemId, requestVersion);
    try {
      const [snapshot, events] = await Promise.all([
        getCoordinatorSnapshot(workItemId),
        listCoordinatorEvents(workItemId),
      ]);
      if (requestVersions.get(workItemId) !== requestVersion) return;
      const timeline = events.items.length > 0 ? events.items : snapshot.timeline;
      set((state) => {
        const errorByWorkItem = { ...state.errorByWorkItem };
        delete errorByWorkItem[workItemId];
        return {
          byWorkItem: {
            ...state.byWorkItem,
            [workItemId]: { ...snapshot, timeline },
          },
          errorByWorkItem,
        };
      });
    } catch {
      if (requestVersions.get(workItemId) !== requestVersion) return;
      set((state) => ({
        errorByWorkItem: {
          ...state.errorByWorkItem,
          [workItemId]: 'Coordinator 状态加载失败，请重试；任务仍由控制面继续执行。',
        },
      }));
    }
  },

  applyEvent: (event) => {
    if (!event.type.startsWith('coordinator.')) return false;
    // Coordinator events without an explicit Task kind are intentionally ignored.
    if (!isTaskEvent(event)) return true;
    const workItemId = eventWorkItemId(event);
    if (!workItemId) return true;
    const oldTimer = pendingRefresh.get(workItemId);
    if (oldTimer) clearTimeout(oldTimer);
    pendingRefresh.set(
      workItemId,
      setTimeout(() => {
        pendingRefresh.delete(workItemId);
        void get().refreshFor(workItemId);
      }, 200),
    );
    return true;
  },

  clear: (workItemId) => {
    const timer = pendingRefresh.get(workItemId);
    if (timer) clearTimeout(timer);
    pendingRefresh.delete(workItemId);
    set((state) => {
      const byWorkItem = { ...state.byWorkItem };
      const errorByWorkItem = { ...state.errorByWorkItem };
      delete byWorkItem[workItemId];
      delete errorByWorkItem[workItemId];
      return { byWorkItem, errorByWorkItem };
    });
  },
}));
