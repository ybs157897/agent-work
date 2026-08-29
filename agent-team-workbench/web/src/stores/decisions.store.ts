import { create } from 'zustand';
import { listWorkItemDecisions } from '../api/endpoints';
import type { CanonicalEvent, DecisionEntry } from '../api/types';

/**
 * 决策台账投影（会话元模型 S2）：
 * - 权威来源 GET /work-items/{id}/decisions（创建时间升序），本 store 按 work item 缓存；
 * - decision.created SSE 事件信封只带 decision 聚合 id，data.work_item_id 作定位线索：
 *   按 work item 失效重取，窗口内突发合并为一次请求，节奏对齐 dispatches.store。
 */
interface DecisionsStore {
  /** work_item_id → 决策台账（升序）；未拉取过 = undefined。 */
  byWorkItem: Record<string, DecisionEntry[]>;
  /** 详情抽屉打开时拉快照；GET 失败只吞本次（下一个 decision.* 事件或重开重试）。 */
  refreshFor: (workItemId: string) => Promise<void>;
  /** decision 域 SSE 事件（decision.*）；返回是否已消费。 */
  applyEvent: (ev: CanonicalEvent) => boolean;
}

/** 与 events.ts SSE_REFRESH_DEBOUNCE_MS 同值；本地定义避免 store→events 循环引用。 */
const REFETCH_DEBOUNCE_MS = 200;

const timers = new Map<string, ReturnType<typeof setTimeout>>();

export const useDecisionsStore = create<DecisionsStore>()((set) => ({
  byWorkItem: {},

  refreshFor: async (workItemId) => {
    try {
      const { items } = await listWorkItemDecisions(workItemId);
      set((s) => ({ byWorkItem: { ...s.byWorkItem, [workItemId]: items } }));
    } catch {
      // 只吞本次 GET 失败（网络/5xx）：SSE 仍在流上，重开详情或下个事件会重试。
    }
  },

  applyEvent: (ev) => {
    if (!ev.type.startsWith('decision.')) return false;
    const wi = typeof ev.data?.work_item_id === 'string' ? ev.data.work_item_id : '';
    if (!wi) return true; // 已消费但定位不到 work item，丢弃
    const pending = timers.get(wi);
    if (pending) clearTimeout(pending);
    timers.set(
      wi,
      setTimeout(() => {
        timers.delete(wi);
        void useDecisionsStore.getState().refreshFor(wi);
      }, REFETCH_DEBOUNCE_MS),
    );
    return true;
  },
}));
