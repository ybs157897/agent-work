import type { Plan, PlanStep, WorkItem, WorkItemReview, WorkItemStatus } from '../api/types';

/**
 * WorkItem phase 的展示判定（M2 编排链路）。phase 只在 status=in_progress 期间
 * 有意义；领域状态不变，任务看板可将 review/acceptance 投影为独立展示泳道。
 */

type ReviewAwareTask = Pick<WorkItem, 'status' | 'phase' | 'parent_id'> & {
  review?: WorkItemReview;
};

const isReviewPhase = (task: Pick<WorkItem, 'status' | 'phase'>): boolean =>
  task.status === 'in_progress' && (task.phase === 'review' || task.phase === 'acceptance');

/**
 * 待验收：review/acceptance 阶段且服务端 review 快照已准备好。
 * pending review（ready=false/缺省）保持进行中，不能误显示验收入口。
 */
export const isAwaitingAcceptance = (task: ReviewAwareTask): boolean =>
  isReviewPhase(task) && task.review?.ready === true;

export type TaskBoardLane = WorkItemStatus | 'awaiting_acceptance';

/** 展示层泳道：待验收从进行中拆出，但不写回新的领域状态。 */
export const taskBoardLane = (task: ReviewAwareTask): TaskBoardLane =>
  isAwaitingAcceptance(task) ? 'awaiting_acceptance' : task.status;

/** review 尚未准备好时给看板的明确解释，避免“为什么没进待验收”变成无提示的状态跳变。 */
export const taskBoardLaneExplanation = (task: ReviewAwareTask): string | undefined => {
  if (!isReviewPhase(task) || task.review?.ready === true) return undefined;
  return task.review?.accept_reason?.trim() || '验收依据尚未就绪，任务仍在进行中';
};

export type CoordinatorResolution = 'loading' | 'coordinated' | 'legacy';

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
export const evaluationPassed = (task: ReviewAwareTask, plan: Plan | undefined): boolean =>
  isAwaitingAcceptance(task) && task.phase === 'acceptance' && planTriggeredEvaluation(plan);
