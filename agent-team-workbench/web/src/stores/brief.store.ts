import { create } from 'zustand';
import { getDeliveryBrief } from '../api/endpoints';
import type { CanonicalEvent, DeliveryBrief } from '../api/types';
import { captureScope, isCurrent, registerWorkspaceScopedReset } from './scope';
import { useWorkspaceStore } from './workspace.store';

/**
 * Delivery Brief 投影（任务控制面 RFC §4.11/§12.5）：
 * - 确定性服务端聚合，前端不重算证据，只补 freshness；
 * - stale 规则：当前 eventCursor > brief.freshness.as_of_event_seq → 先标 stale
 *   → 后台补拉 → 补拉失败保留旧内容并提示「可能有更新」（禁止把错误显示为空态）；
 * - work_item / coordinator / run terminal / artifact / task_comment 事件（RFC §12.4
 *   同一组触发条件）按缓存命中的 work item 失效，debounce 合并突发。
 */
type BriefStatus = 'idle' | 'loading' | 'ready';

interface BriefStore {
  byWorkItem: Record<string, DeliveryBrief>;
  statusByWorkItem: Record<string, BriefStatus>;
  errorByWorkItem: Record<string, string | undefined>;
  /** 客户端判定 stale：eventCursor > freshness.as_of_event_seq（展示层显示水印提示）。 */
  staleByWorkItem: Record<string, boolean>;
  refreshFor: (workItemId: string, expectedWorkspaceId?: string) => Promise<void>;
  /** SSE 事件命中缓存中的 brief 时标 stale 并后台补拉；返回是否已消费。 */
  applyEvent: (ev: CanonicalEvent) => boolean;
  /** 切换 Workspace 时清空 Brief 投影。 */
  reset: () => void;
}

const REFETCH_DEBOUNCE_MS = 200;

const timers = new Map<string, ReturnType<typeof setTimeout>>();
const requestVersions = new Map<string, number>();

/** 事件负载里能定位到 work item 的全部线索（aggregate id 兜底）。 */
function eventWorkItemIds(ev: CanonicalEvent): string[] {
  const ids = new Set<string>();
  if (typeof ev.data?.work_item_id === 'string' && ev.data.work_item_id) ids.add(ev.data.work_item_id);
  if (typeof ev.data?.root_work_item_id === 'string' && ev.data.root_work_item_id) ids.add(ev.data.root_work_item_id);
  if (ev.aggregate.type === 'work_item' && ev.aggregate.id) ids.add(ev.aggregate.id);
  return [...ids];
}

/** 当前事件游标大于 Brief 聚合水位：聚合后提交的事件使 Brief 落后。 */
function computeStale(brief: DeliveryBrief): boolean {
  return useWorkspaceStore.getState().eventCursor > brief.freshness.as_of_event_seq;
}

export const useBriefStore = create<BriefStore>()((set, get) => {
  /** 后台补拉：成功则按新水位重算 stale；失败保留旧内容（stale 提示仍在）。 */
  const backfill = async (
    workItemId: string,
    scope: { workspaceId: string; generation: number },
    expectedWorkspaceId: string,
  ) => {
    if (!scope.workspaceId || expectedWorkspaceId !== scope.workspaceId) return;
    const requestVersion = (requestVersions.get(workItemId) ?? 0) + 1;
    requestVersions.set(workItemId, requestVersion);
    try {
      const brief = await getDeliveryBrief(workItemId);
      if (requestVersions.get(workItemId) !== requestVersion || !isCurrent(scope)) return;
      if (brief.work_item.workspace_id !== scope.workspaceId) return;
      set((s) => {
        const errorByWorkItem = { ...s.errorByWorkItem };
        delete errorByWorkItem[workItemId];
        return {
          byWorkItem: { ...s.byWorkItem, [workItemId]: brief },
          staleByWorkItem: { ...s.staleByWorkItem, [workItemId]: computeStale(brief) },
          errorByWorkItem,
        };
      });
    } catch {
      if (requestVersions.get(workItemId) !== requestVersion || !isCurrent(scope)) return;
      // 保留旧内容 + stale 标记；错误提示为「可能有更新」，不显示为空态。
      set((s) => ({
        errorByWorkItem: {
          ...s.errorByWorkItem,
          [workItemId]: '简报补拉失败，当前内容可能不是最新',
        },
      }));
    }
  };

  return {
    byWorkItem: {},
    statusByWorkItem: {},
    errorByWorkItem: {},
    staleByWorkItem: {},

    reset: () => {
      for (const timer of timers.values()) clearTimeout(timer);
      timers.clear();
      requestVersions.clear();
      set({ byWorkItem: {}, statusByWorkItem: {}, errorByWorkItem: {}, staleByWorkItem: {} });
    },

    refreshFor: async (workItemId, expectedWorkspaceId) => {
      const scope = captureScope();
      if (!scope.workspaceId || (expectedWorkspaceId !== undefined && expectedWorkspaceId !== scope.workspaceId)) return;
      const requestVersion = (requestVersions.get(workItemId) ?? 0) + 1;
      requestVersions.set(workItemId, requestVersion);
      set((s) => ({ statusByWorkItem: { ...s.statusByWorkItem, [workItemId]: 'loading' } }));
      try {
        const brief = await getDeliveryBrief(workItemId);
        if (requestVersions.get(workItemId) !== requestVersion || !isCurrent(scope)) return;
        if (brief.work_item.workspace_id !== scope.workspaceId) return;
        set((s) => ({
          byWorkItem: { ...s.byWorkItem, [workItemId]: brief },
          statusByWorkItem: { ...s.statusByWorkItem, [workItemId]: 'ready' },
          staleByWorkItem: { ...s.staleByWorkItem, [workItemId]: computeStale(brief) },
          errorByWorkItem: (() => {
            const next = { ...s.errorByWorkItem };
            delete next[workItemId];
            return next;
          })(),
        }));
      } catch (err) {
        if (requestVersions.get(workItemId) !== requestVersion || !isCurrent(scope)) return;
        const hadContent = Boolean(get().byWorkItem[workItemId]);
        set((s) => ({
          statusByWorkItem: { ...s.statusByWorkItem, [workItemId]: hadContent ? 'ready' : 'idle' },
          errorByWorkItem: {
            ...s.errorByWorkItem,
            [workItemId]: err instanceof Error && err.message ? err.message : '交付简报加载失败，请重试',
          },
        }));
      }
    },

    applyEvent: (ev) => {
      // 与 Review Queue 同一组触发条件（§12.4）；其余事件不消费。
      const relevant = ev.type === 'task_comment.created'
        || ev.type.startsWith('artifact.')
        || ev.type.startsWith('coordinator.')
        || ev.type.startsWith('work_item.')
        || ev.type === 'run.completed'
        || ev.type === 'run.failed';
      if (!relevant) return false;

      const cached = get().byWorkItem;
      const hintedIDs = eventWorkItemIds(ev);
      // artifact/run 信封可能只带 aggregate id，无法反查所属 WorkItem。此时宁可让
      // 所有已打开 Brief 进入 stale 并合并补拉，也不能错误宣称旧聚合仍是 current。
      const hits = hintedIDs.length > 0
        ? hintedIDs.filter((id) => cached[id])
        : Object.keys(cached);
      if (hits.length === 0) return true; // 已消费：无缓存的 brief 无需失效

      for (const id of hits) {
        if (cached[id].work_item.workspace_id !== captureScope().workspaceId) continue;
        if (computeStale(cached[id])) {
          set((s) => ({ staleByWorkItem: { ...s.staleByWorkItem, [id]: true } }));
          const pending = timers.get(id);
          if (pending) clearTimeout(pending);
          timers.set(
            id,
            setTimeout(() => {
              timers.delete(id);
              void backfill(id, captureScope(), cached[id].work_item.workspace_id);
            }, REFETCH_DEBOUNCE_MS),
          );
        }
      }
      return true;
    },
  };
});

registerWorkspaceScopedReset(() => useBriefStore.getState().reset());
