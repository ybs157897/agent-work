import { create } from 'zustand';
import { listWorkItems } from '../api/endpoints';
import type { Priority, WorkItem } from '../api/types';
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
  /** 任务集合是否正在补齐/重拉；首屏已有内容时仍可为 true。 */
  loading: boolean;
  /** 最近一次完整集合拉取失败；存在时看板不得伪装成已完成的空态。 */
  error: string | null;
  filter: TaskFilter;
  viewMode: ViewMode;

  hydrate: (items: WorkItem[]) => void;
  refresh: () => Promise<void>;
  /** 切换 Workspace 时清空看板数据（viewMode 是本地 UI 偏好，不随 Workspace 重置）。 */
  reset: () => void;
  setFilter: (f: TaskFilter) => void;
  setViewMode: (m: ViewMode) => void;
  upsert: (item: WorkItem) => void;
  getById: (id: string) => WorkItem | undefined;
}

const refreshGuard = createRequestGuard();

export const useTasksStore = create<TasksStore>()((set, get) => ({
  items: [],
  loaded: false,
  loading: false,
  error: null,
  filter: {},
  viewMode: 'kanban',

  hydrate: (items) => {
    const scope = captureScope();
    set({
      items: items.filter((item) => item.record_kind === 'task' && isCurrentWorkspaceEntity(scope, item)),
      loaded: true,
      loading: false,
      error: null,
    });
    // Bootstrap 与 SSE cursor resync 都从这里进入；首屏快照可能只是第一页，
    // 因此统一补齐未筛选的全部分页，避免 resync 后丢失后续子任务/参与者。
    if (typeof window !== 'undefined') void get().refresh();
  },

  reset: () => set({ items: [], loaded: false, loading: false, error: null, filter: {} }),

  refresh: async () => {
    const scope = captureScope();
    if (!scope.workspaceId) return;
    const isStale = refreshGuard.begin();
    set({ loading: true, error: null });

    try {
      const pages: WorkItem[] = [];
      const cursors = new Set<string>();
      let cursor: string | undefined;

      do {
        const response = await listWorkItems(scope.workspaceId, {
          // UI filter 只作用于根任务投影；这里必须拉完整 task 集合，
          // 否则分页后的子任务无法参与头像与子任务数归集。
          record_kind: 'task',
          ...(cursor ? { cursor } : {}),
        });
        pages.push(...response.items);
        cursor = response.next_cursor ?? undefined;
        if (cursor && cursors.has(cursor)) {
          throw new Error('任务列表分页游标重复，无法继续加载');
        }
        if (cursor) cursors.add(cursor);
        // 期间已切换 Workspace 或发出更新的 refresh：丢弃旧响应。
        if (isStale() || !isCurrent(scope)) return;
      } while (cursor);

      const uniqueItems = new Map<string, WorkItem>();
      for (const item of pages) {
        if (item.record_kind === 'task' && isCurrentWorkspaceEntity(scope, item)) uniqueItems.set(item.id, item);
      }

      set({ items: [...uniqueItems.values()], loaded: true, loading: false, error: null });
    } catch (error) {
      if (isStale() || !isCurrent(scope)) return;
      // 保留首屏/上一次成功数据；明确报错，避免把未知失败误渲染为「暂无任务」。
      set({ loading: false, error: error instanceof Error && error.message ? error.message : '任务加载失败，请重试' });
    }
  },

  setFilter: (filter) => {
    set({ filter });
  },

  setViewMode: (viewMode) => set({ viewMode }),

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
}));

registerWorkspaceScopedReset(() => useTasksStore.getState().reset());
