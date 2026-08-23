import { create } from 'zustand';
import { getPlan, getWorkItemPlan } from '../api/endpoints';
import type { CanonicalEvent, Plan } from '../api/types';

/**
 * Plan 投影（M1 编排）：
 * - 权威来源是控制平面的 plan 聚合事件流 + GET /plans/{id}，本 store 只缓存
 *   「每个主任务的最新 plan」（同主任务新 plan 提交 = 整体覆盖，对应 supersede）。
 * - 冷启动（无 SSE 索引）经 GET /work-items/{id}/plan 按主任务直取最新一份；
 *   无 plan（404）保持空缓存，等 plan.* 事件。
 */
interface PlansStore {
  /** work_item_id → 最新 plan。 */
  byWorkItem: Record<string, Plan>;
  /** plan_id → work_item_id：事件信封只带 plan 聚合 id 时的反查索引。 */
  workItemOf: Record<string, string>;
  /** plan 域 SSE 事件（plan.*）；返回是否已消费。 */
  applyEvent: (ev: CanonicalEvent) => boolean;
  /** 详情页打开时按主任务拉最新快照；缓存已建走 plan id 重拉，冷启动走按主任务端点。 */
  refreshFor: (workItemId: string) => Promise<void>;
}

/** 同一 plan 的事件突发（逐 step 执行）合并为一次在途请求 + 一次补拉，保证末次事件状态最终落缓存。 */
const inflight: Record<string, boolean> = {};
const dirty: Record<string, boolean> = {};

const strField = (v: unknown): string | undefined => (typeof v === 'string' ? v : undefined);

export const usePlansStore = create<PlansStore>()((set, get) => {
  /** 落缓存并学习 plan_id → work_item_id 反查索引。 */
  const cachePlan = (plan: Plan) =>
    set((s) => ({
      byWorkItem: { ...s.byWorkItem, [plan.work_item_id]: plan },
      workItemOf: { ...s.workItemOf, [plan.id]: plan.work_item_id },
    }));

  const fetchPlan = async (planId: string): Promise<void> => {
    if (inflight[planId]) {
      dirty[planId] = true;
      return;
    }
    inflight[planId] = true;
    try {
      cachePlan(await getPlan(planId));
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
      if (cached) {
        await fetchPlan(cached.id);
        return;
      }
      // 冷启动（刷新页面直开详情，SSE 索引未建）：按主任务直取最新 plan。
      try {
        cachePlan(await getWorkItemPlan(workItemId));
      } catch {
        // 同 fetchPlan 口径：只吞本次 GET 失败（404 = 该任务无 plan）。
      }
    },
  };
});
