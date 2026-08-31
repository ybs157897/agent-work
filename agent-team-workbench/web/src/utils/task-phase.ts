import type { Plan, PlanStep, WorkItem } from '../api/types';

/**
 * WorkItem phase 的展示判定（M2 编排链路）。phase 只在 status=in_progress 期间
 * 有意义（review/acceptance 均不改看板列），判定一律以两者同时为准。
 */

/**
 * 待验收：review（run 成功待评审）与 acceptance（评估 verdict 通过）都需要
 * 人工验收或打回——卡片徽标与详情操作共用同一显隐口径。
 */
export const isAwaitingAcceptance = (task: Pick<WorkItem, 'status' | 'phase'>): boolean =>
  task.status === 'in_progress' && (task.phase === 'review' || task.phase === 'acceptance');

export type CoordinatorResolution = 'loading' | 'coordinated' | 'legacy';

/**
 * Return 会创建根 Coordinator 消费的 review_feedback。派生子任务没有独立
 * Return 命令（服务端返回 child_review_not_supported），应改走 requirement。
 */
export const canReturnTask = (
  task: Pick<WorkItem, 'status' | 'phase' | 'parent_id'>,
  coordinatorResolution: CoordinatorResolution,
): boolean => {
  if (!isAwaitingAcceptance(task)) return false;
  // Root Tasks support Return both before and after Coordinator adoption. A child
  // is only forbidden once the authoritative Coordinator lookup confirms it.
  if (!task.parent_id) return true;
  return coordinatorResolution === 'legacy';
};

/**
 * finish 步触发评估：payload 为提交时 JSON 原文，evaluation 严格判 true
 * （后端同款 t.payload["evaluation"].(bool)，缺省/非布尔均视为未触发）。
 */
export const stepTriggeredEvaluation = (step: PlanStep): boolean =>
  step.verb === 'finish' && step.payload?.evaluation === true;

/** 最新 plan 的任一 finish 步带 evaluation（M2：plan 落 finished 后自动创建评估 run）。 */
export const planTriggeredEvaluation = (plan: Plan | undefined): boolean =>
  !!plan?.steps.some(stepTriggeredEvaluation);

/**
 * 评估通过、等待人工验收：acceptance 阶段且最新 plan 触发过评估——评估 run
 * verdict pass 的唯一可推导前端投影（verdict activity 无任务归属，不做历史推测）。
 */
export const evaluationPassed = (task: Pick<WorkItem, 'status' | 'phase'>, plan: Plan | undefined): boolean =>
  task.status === 'in_progress' && task.phase === 'acceptance' && planTriggeredEvaluation(plan);
