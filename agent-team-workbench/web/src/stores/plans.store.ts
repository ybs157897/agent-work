import { create } from 'zustand';
import { getPlan } from '../api/endpoints';
import type { CanonicalEvent, Plan } from '../api/types';

/**
 * Plan 投影（M1 编排）：
 * - 权威来源是控制平面的 plan 聚合事件流 + GET /plans/{id}，本 store 只缓存
 *   「每个主任务的最新 plan」（同主任务新 plan 提交 = 整体覆盖，对应 supersede）。
 * - M1 没有「按主任务查最新 plan」的列表端点：冷启动缓存为空，须等 SSE
 *   plan.* 事件建立 plan_id → work_item_id 索引后才能按主任务刷新。
 */
interface PlansStore {
  /** work_item_id → 最新 plan。 */
  byWorkItem: Record<string, Plan>;
  /** plan_id → work_item_id：事件信封只带 plan 聚合 id 时的反查索引。 */
  workItemOf: Record<string, string>;
  /** plan 域 SSE 事件（plan.*）；返回是否已消费。 */
  applyEvent: (ev: CanonicalEvent) => boolean;
  /** 详情页打开时按主任务拉最新快照；plan id 未知（冷启动）时为空操作。 */
  refreshFor: (workItemId: string) => Promise<void>;
}

/** 同一 plan 的事件突发（逐 step 执行）合并为一次在途请求 + 一次补拉，保证末次事件状态最终落缓存。 */
const inflight: Record<string, boolean> = {};
const dirty: Record<string, boolean> = {};

const strField = (v: unknown): string | undefined => (typeof v === 'string' ? v : undefined);

export const usePlansStore = create<PlansStore>()((set, get) => {
  const fetchPlan = async (planId: string): Promise<void> => {
    if (inflight[planId]) {
      dirty[planId] = true;
      return;
    }
    inflight[planId] = true;
    try {
      const plan = await getPlan(planId);
      set((s) => ({
        byWorkItem: { ...s.byWorkItem, [plan.work_item_id]: plan },
        workItemOf: { ...s.workItemOf, [plan.id]: plan.work_item_id },
      }));
    } catch {
      // 只吞本次 GET 失败（网络/5xx）：SSE 仍在流上，下一个 plan.* 事件或
      // 详情页重开会重试；除此之外没有别的消费者需要这个错误。
    } finally {
      inflight[planId] = false;
      if (dirty[planId]) {
        delete dirty[planId];
        void fetchPlan(planId);
      }
    }
  };

  return {
    byWorkItem: {},
    workItemOf: {},

    applyEvent: (ev) => {
      if (!ev.type.startsWith('plan.')) return false;
      const planId =
        ev.aggregate.type === 'plan' ? ev.aggregate.id : (strField(ev.data?.plan_id) ?? '');
      if (!planId) return true; // 已消费但无可定位信息，丢弃
      // 事件 data 的 work_item_id 只是定位线索；最终以 PlanDTO 自带的为准。
      const hintedWi = strField(ev.data?.work_item_id);
      if (hintedWi) {
        set((s) => ({ workItemOf: { ...s.workItemOf, [planId]: hintedWi } }));
      }
      void fetchPlan(planId);
      return true;
    },

    refreshFor: async (workItemId) => {
      const cached = get().byWorkItem[workItemId];
      if (!cached) return; // 冷启动无索引：等 plan.* 事件建立缓存
      await fetchPlan(cached.id);
    },
  };
});
