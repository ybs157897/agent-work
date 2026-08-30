import type { AgentProfile } from '../api/types';

/** 系统 Task Coordinator 不属于普通 Agent 列表、Chat 选人或 @ 候选。 */
export function isTaskCoordinatorAgent(agent: Pick<AgentProfile, 'is_system' | 'kind'>): boolean {
  return agent.is_system === true || agent.kind === 'task_coordinator';
}

export function isUserManagedAgent(agent: Pick<AgentProfile, 'is_system' | 'kind'>): boolean {
  return !isTaskCoordinatorAgent(agent);
}
