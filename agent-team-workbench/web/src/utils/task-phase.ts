import type { WorkItem } from '../api/types';

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
