import { create } from 'zustand';
import { listReviewQueue } from '../api/endpoints';
import type { CanonicalEvent, ReviewQueueItem } from '../api/types';
import { captureScope, isCurrent, registerWorkspaceScopedReset } from './scope';

/**
 * Review Queue 投影（任务控制面 RFC §4.10/§12.4）：
 * - 服务端权威 read model：record_kind=task、status=in_progress、
 *   phase IN (review, acceptance)，排序 pending_since ASC；
 * - total_count 是 badge 唯一权威值——store 原样保存服务端字段，
 *   禁止用当前页长度或已加载 Task 数推导；
 * - work_item / coordinator / run terminal / artifact / task_comment 事件
 *   触发失效重取（路由层 debounce 合并），refresh 内部带 workspace/generation fencing。
 */
export type ReviewQueuePhaseFilter = 'review' | 'acceptance' | '';

interface ReviewQueueStore {
  items: ReviewQueueItem[];
  /** 服务端 total_count：badge 唯一权威，与当前页长度无关。 */
  totalCount: number;
  nextCursor: string | null;
  generatedAt: string | null;
  phaseFilter: ReviewQueuePhaseFilter;
  loaded: boolean;
  loading: boolean;
  /** 拉取失败保留旧内容，错误不得伪装成空队列。 */
  error: string | null;

  /** 拉首页（事件失效重取与首次加载共用）；phase 过滤变化时也走这里。 */
  refresh: () => Promise<void>;
  /** cursor 分页追加下一页。 */
  loadMore: () => Promise<void>;
  setPhaseFilter: (phase: ReviewQueuePhaseFilter) => void;
  /** SSE 事件是否命中本 store 的刷新条件（路由层已 debounce）。 */
  shouldRefreshForEvent: (ev: CanonicalEvent) => boolean;
  /** 切换 Workspace 时清空队列投影。 */
  reset: () => void;
}

const PAGE_LIMIT = 50;

// 与 workspace generation 独立的本地查询栅栏：同一 Workspace 内 phase/filter
// 变化也会产生并发请求，旧查询绝不能覆盖新查询的页和 total_count。
let queryEpoch = 0;

export const useReviewQueueStore = create<ReviewQueueStore>()((set, get) => ({
  items: [],
  totalCount: 0,
  nextCursor: null,
  generatedAt: null,
  phaseFilter: '',
  loaded: false,
  loading: false,
  error: null,

  reset: () =>
    {
      queryEpoch += 1;
      set({
        items: [],
        totalCount: 0,
        nextCursor: null,
        generatedAt: null,
        phaseFilter: '',
        loaded: false,
        loading: false,
        error: null,
      });
    },

  refresh: async () => {
    const scope = captureScope();
    if (!scope.workspaceId) return;
    const epoch = ++queryEpoch;
    const phaseFilter = get().phaseFilter;
    set({ loading: true });
    try {
      const page = await listReviewQueue(scope.workspaceId, {
        limit: PAGE_LIMIT,
        ...(phaseFilter ? { phase: phaseFilter as 'review' | 'acceptance' } : {}),
      });
      if (!isCurrent(scope) || queryEpoch !== epoch) return;
      set({
        items: page.items,
        totalCount: page.total_count,
        nextCursor: page.next_cursor,
        generatedAt: page.generated_at,
        loaded: true,
        loading: false,
        error: null,
      });
    } catch {
      if (!isCurrent(scope) || queryEpoch !== epoch) return;
      // 保留旧投影与 badge；错误显示为错误，不清空成空队列。
      set({ loaded: true, loading: false, error: '复审队列加载失败，请重试' });
    }
  },

  loadMore: async () => {
    const scope = captureScope();
    const cursor = get().nextCursor;
    if (!scope.workspaceId || !cursor || get().loading) return;
    const epoch = ++queryEpoch;
    const phaseFilter = get().phaseFilter;
    set({ loading: true });
    try {
      const page = await listReviewQueue(scope.workspaceId, {
        limit: PAGE_LIMIT,
        cursor,
        ...(phaseFilter ? { phase: phaseFilter as 'review' | 'acceptance' } : {}),
      });
      if (!isCurrent(scope) || queryEpoch !== epoch) return;
      set((s) => ({
        items: [...s.items, ...page.items],
        // total_count 以最新一页响应为准（全量匹配数，与页大小无关）。
        totalCount: page.total_count,
        nextCursor: page.next_cursor,
        generatedAt: page.generated_at,
        loading: false,
        error: null,
      }));
    } catch {
      if (!isCurrent(scope) || queryEpoch !== epoch) return;
      set({ loading: false, error: '复审队列加载失败，请重试' });
    }
  },

  setPhaseFilter: (phaseFilter) => {
    if (phaseFilter === get().phaseFilter) return;
    // 过滤条件变了就废弃当前页；保留旧行会把「acceptance」标签和「review」数据
    // 混在一起，也会让慢响应有机会覆盖新查询。
    set({
      phaseFilter,
      items: [],
      totalCount: 0,
      nextCursor: null,
      generatedAt: null,
      loaded: false,
      error: null,
    });
    void get().refresh();
  },

  shouldRefreshForEvent: (ev) => {
    if (ev.type === 'task_comment.created') return true;
    if (ev.type.startsWith('artifact.')) return true;
    if (ev.type === 'run.completed' || ev.type === 'run.failed') {
      return ev.data?.record_kind === 'task';
    }
    if (ev.type.startsWith('work_item.') || ev.type.startsWith('coordinator.')) {
      return ev.data?.record_kind === 'task';
    }
    return false;
  },
}));

registerWorkspaceScopedReset(() => useReviewQueueStore.getState().reset());
