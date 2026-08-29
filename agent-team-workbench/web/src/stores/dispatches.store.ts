import { create } from 'zustand';
import { listWorkItemDispatches } from '../api/endpoints';
import type { CanonicalEvent, DispatchCard } from '../api/types';

/**
 * 派发卡片投影（会话元模型 S1）：
 * - 权威来源 GET /work-items/{id}/dispatches（新→旧），本 store 按 work item 缓存列表；
 * - dispatch.* SSE 事件信封只带 dispatch 聚合 id，data.work_item_id 作定位线索：
 *   按 work item 失效重取，窗口内突发（批次逐个建出）合并为一次请求，
 *   节奏对齐 events.ts 的 work_item.* 刷新（SSE_REFRESH_DEBOUNCE_MS）。
 */
interface DispatchesStore {
  /** work_item_id → 派发卡片列表（新→旧）；未拉取过 = undefined。 */
  byWorkItem: Record<string, DispatchCard[]>;
  /** 详情抽屉打开时按主任务拉快照；GET 失败只吞本次（下一个 dispatch.* 事件或重开重试）。 */
  refreshFor: (workItemId: string) => Promise<void>;
  /** dispatch 域 SSE 事件（dispatch.*）；返回是否已消费。 */
  applyEvent: (ev: CanonicalEvent) => boolean;
}

/** 与 events.ts SSE_REFRESH_DEBOUNCE_MS 同值；本地定义避免 store→events 循环引用。 */
const REFETCH_DEBOUNCE_MS = 200;

const timers = new Map<string, ReturnType<typeof setTimeout>>();

export const useDispatchesStore = create<DispatchesStore>()((set) => ({
  byWorkItem: {},

  refreshFor: async (workItemId) => {
    try {
      const { items } = await listWorkItemDispatches(workItemId);
      set((s) => ({ byWorkItem: { ...s.byWorkItem, [workItemId]: items } }));
    } catch {
      // 只吞本次 GET 失败（网络/5xx）：SSE 仍在流上，重开详情或下个事件会重试。
    }
  },

  applyEvent: (ev) => {
    if (!ev.type.startsWith('dispatch.')) return false;
    const wi = typeof ev.data?.work_item_id === 'string' ? ev.data.work_item_id : '';
    if (!wi) return true; // 已消费但定位不到 work item，丢弃
    const pending = timers.get(wi);
    if (pending) clearTimeout(pending);
    timers.set(
      wi,
      setTimeout(() => {
        timers.delete(wi);
        void useDispatchesStore.getState().refreshFor(wi);
      }, REFETCH_DEBOUNCE_MS),
    );
    return true;
  },
}));
