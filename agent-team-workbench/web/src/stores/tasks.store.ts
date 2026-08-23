import { create } from 'zustand';
import { ApiError } from '../api/client';
import { listWorkItems, moveWorkItem } from '../api/endpoints';
import type { Priority, WorkItem, WorkItemStatus } from '../api/types';
import { createRequestGuard } from './request-guard';
import { useWorkspaceStore } from './workspace.store';

export type ViewMode = 'kanban' | 'list';

export interface TaskFilter {
  priority?: Priority;
  assignee?: string;
}

interface TasksStore {
  items: WorkItem[];
  filter: TaskFilter;
  viewMode: ViewMode;
  selectedTaskId: string | null;

  hydrate: (items: WorkItem[]) => void;
  refresh: () => Promise<void>;
  setFilter: (f: TaskFilter) => void;
  setViewMode: (m: ViewMode) => void;
  selectTask: (id: string | null) => void;
  upsert: (item: WorkItem) => void;
  getById: (id: string) => WorkItem | undefined;

  /** 看板拖动：乐观更新；版本冲突/状态机错误时回滚并刷新（协议 §5.1 并发语义）。 */
  moveOptimistic: (id: string, to: WorkItemStatus) => Promise<void>;
}

const refreshGuard = createRequestGuard();

export const useTasksStore = create<TasksStore>()((set, get) => ({
  items: [],
  filter: {},
  viewMode: 'kanban',
  selectedTaskId: null,

  hydrate: (items) => set({ items }),

  refresh: async () => {
    const wsId = useWorkspaceStore.getState().workspace?.id;
    if (!wsId) return;
    const { filter } = get();
    const isStale = refreshGuard.begin();
    const { items } = await listWorkItems(wsId, { ...filter });
    if (isStale()) return; // 期间已发出更新的 refresh（如筛选已变）：丢弃旧响应
    set({ items });
  },

  setFilter: (filter) => {
    set({ filter });
    void get().refresh();
  },

  setViewMode: (viewMode) => set({ viewMode }),
  selectTask: (selectedTaskId) => set({ selectedTaskId }),

  upsert: (item) =>
    set((s) => ({
      items: s.items.some((t) => t.id === item.id)
        ? s.items.map((t) => (t.id === item.id ? item : t))
        : [...s.items, item],
    })),

  getById: (id) => get().items.find((t) => t.id === id),

  moveOptimistic: async (id, to) => {
    const before = get().items;
    const item = before.find((t) => t.id === id);
    if (!item) return;
    set({
      items: before.map((t) => (t.id === id ? { ...t, status: to } : t)),
    });
    try {
      const updated = await moveWorkItem(id, to, item.version);
      get().upsert(updated);
    } catch (err) {
      set({ items: before });
      if (err instanceof ApiError && err.isVersionConflict) {
        await get().refresh();
      }
      throw err;
    }
  },
}));
