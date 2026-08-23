import { describe, expect, it } from 'vitest';
import type { Plan, PlanStep, WorkItem } from '../api/types';
import { evaluationPassed, isAwaitingAcceptance, planTriggeredEvaluation, stepTriggeredEvaluation } from './task-phase';

const task = (status: WorkItem['status'], phase?: WorkItem['phase']): Pick<WorkItem, 'status' | 'phase'> => ({
  status,
  ...(phase ? { phase } : {}),
});

const step = (verb: PlanStep['verb'], payload: Record<string, unknown>): PlanStep => ({
  seq: 1,
  verb,
  status: 'executed',
  payload,
});

const planOf = (steps: PlanStep[]): Plan => ({
  id: 'plan_1',
  workspace_id: 'ws_1',
  work_item_id: 'wi_1',
  agent_profile_id: 'agent_lead',
  source_run_id: null,
  status: 'finished',
  superseded_by: null,
  steps,
  version: 2,
  created_at: '2026-08-23T10:00:00Z',
  updated_at: '2026-08-23T10:01:00Z',
});

describe('isAwaitingAcceptance', () => {
  it('review 与 acceptance 阶段（in_progress）都提示待验收', () => {
    expect(isAwaitingAcceptance(task('in_progress', 'review'))).toBe(true);
    expect(isAwaitingAcceptance(task('in_progress', 'acceptance'))).toBe(true);
  });

  it('execution、缺省 phase 不提示', () => {
    expect(isAwaitingAcceptance(task('in_progress', 'execution'))).toBe(false);
    expect(isAwaitingAcceptance(task('in_progress'))).toBe(false);
  });

  it('phase 只在 in_progress 期间有意义：其余看板列一律不提示', () => {
    expect(isAwaitingAcceptance(task('todo', 'acceptance'))).toBe(false);
    expect(isAwaitingAcceptance(task('completed', 'acceptance'))).toBe(false);
    expect(isAwaitingAcceptance(task('blocked', 'review'))).toBe(false);
    expect(isAwaitingAcceptance(task('cancelled', 'review'))).toBe(false);
  });
});

describe('planTriggeredEvaluation', () => {
  it('finish 步 payload.evaluation=true 触发评估', () => {
    expect(planTriggeredEvaluation(planOf([step('finish', { summary: '完成', evaluation: true })]))).toBe(true);
  });

  it('evaluation 缺省或非严格 true 不触发（后端同款布尔断言口径）', () => {
    expect(planTriggeredEvaluation(planOf([step('finish', { summary: '完成' })]))).toBe(false);
    expect(planTriggeredEvaluation(planOf([step('finish', { summary: '完成', evaluation: 'true' })]))).toBe(false);
    expect(planTriggeredEvaluation(planOf([step('finish', { summary: '完成', evaluation: 1 })]))).toBe(false);
  });

  it('非 finish 步的 evaluation 字段不算（dispatch/defer 无评估语义）', () => {
    expect(planTriggeredEvaluation(planOf([step('dispatch', { agent_id: 'a', evaluation: true })]))).toBe(false);
  });

  it('plan 缺省（冷启动无 plan）不触发', () => {
    expect(planTriggeredEvaluation(undefined)).toBe(false);
  });

  it('stepTriggeredEvaluation 只看单个 step：多 finish 任一触发即真', () => {
    expect(stepTriggeredEvaluation(step('finish', { evaluation: true }))).toBe(true);
    expect(
      planTriggeredEvaluation(planOf([step('finish', { summary: '中途' }), step('finish', { evaluation: true })])),
    ).toBe(true);
  });
});

describe('evaluationPassed', () => {
  const evaluatedPlan = planOf([step('finish', { summary: '完成', evaluation: true })]);
  const plainPlan = planOf([step('finish', { summary: '完成' })]);

  it('acceptance + 最新 plan 触发过评估 → 评估通过待人工验收', () => {
    expect(evaluationPassed(task('in_progress', 'acceptance'), evaluatedPlan)).toBe(true);
  });

  it('acceptance 但 plan 未触发评估（人工直评路径）不显示评估提示', () => {
    expect(evaluationPassed(task('in_progress', 'acceptance'), plainPlan)).toBe(false);
    expect(evaluationPassed(task('in_progress', 'acceptance'), undefined)).toBe(false);
  });

  it('非 acceptance 阶段一律不显示：评估失败回 execution 后不做历史推测', () => {
    expect(evaluationPassed(task('in_progress', 'execution'), evaluatedPlan)).toBe(false);
    expect(evaluationPassed(task('in_progress', 'review'), evaluatedPlan)).toBe(false);
    expect(evaluationPassed(task('completed', 'acceptance'), evaluatedPlan)).toBe(false);
  });
});
