import { create } from 'zustand';
import { ApiError } from '../api/client';
import { listWorkItems, moveWorkItem } from '../api/endpoints';
import type { Priority, WorkItem, WorkItemStatus } from '../api/types';
import { captureScope, isCurrent, isCurrentWorkspaceEntity, registerWorkspaceScopedReset } from './scope';
import { createRequestGuard } from './request-guard';

export type ViewMode = 'kanban' | 'list';

export interface TaskFilter {
  priority?: Priority;
  assignee?: string;
}

interface TasksStore {
  items: WorkItem[];
  /** 首屏是否已加载（看板骨架屏依据）。 */
  loaded: boolean;
  filter: TaskFilter;
  viewMode: ViewMode;
  selectedTaskId: string | null;

  hydrate: (items: WorkItem[]) => void;
  refresh: () => Promise<void>;
  /** 切换 Workspace 时清空看板数据（viewMode 是本地 UI 偏好，不随 Workspace 重置）。 */
  reset: () => void;
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
  loaded: false,
  filter: {},
  viewMode: 'kanban',
  selectedTaskId: null,

  hydrate: (items) => {
    const scope = captureScope();
    set({
      items: items.filter((item) => item.record_kind === 'task' && isCurrentWorkspaceEntity(scope, item)),
      loaded: true,
    });
  },

  reset: () => set({ items: [], loaded: false, filter: {}, selectedTaskId: null }),

  refresh: async () => {
    const scope = captureScope();
    if (!scope.workspaceId) return;
    const { filter } = get();
    const isStale = refreshGuard.begin();
    const { items } = await listWorkItems(scope.workspaceId, { ...filter, record_kind: 'task' });
    // 期间已发出更新的 refresh（如筛选已变）或已切换 Workspace：丢弃旧响应
    if (isStale() || !isCurrent(scope)) return;
    // Fail closed if a server or proxy returns records outside the requested kind.
    set({
      items: items.filter((item) => item.record_kind === 'task' && isCurrentWorkspaceEntity(scope, item)),
      loaded: true,
    });
  },

  setFilter: (filter) => {
    set({ filter });
    void get().refresh();
  },

  setViewMode: (viewMode) => set({ viewMode }),
  selectTask: (selectedTaskId) => set({ selectedTaskId }),

  upsert: (item) => {
    const scope = captureScope();
    if (item.record_kind !== 'task' || !isCurrentWorkspaceEntity(scope, item)) return;
    set((s) => ({
      items: s.items.some((t) => t.id === item.id)
        ? s.items.map((t) => (t.id === item.id ? item : t))
        : [...s.items, item],
    }));
  },

  getById: (id) => get().items.find((t) => t.id === id),

  moveOptimistic: async (id, to) => {
    const scope = captureScope();
    const before = get().items;
    const item = before.find((t) => t.id === id);
    if (!item || !isCurrentWorkspaceEntity(scope, item)) return;
    set({
      items: before.map((t) => (t.id === id ? { ...t, status: to } : t)),
    });
    try {
      const updated = await moveWorkItem(id, to, item.version);
      if (!isCurrentWorkspaceEntity(scope, updated)) return; // 切换后旧任务的回写不得进入新看板
      get().upsert(updated);
    } catch (err) {
      if (!isCurrent(scope)) throw err; // 回滚数据已随 reset 清空，不回写旧列表
      set({ items: before });
      if (err instanceof ApiError && err.isVersionConflict) {
        await get().refresh();
      }
      throw err;
    }
  },
}));

registerWorkspaceScopedReset(() => useTasksStore.getState().reset());
