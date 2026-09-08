import type { AgentProfile } from '../api/types';

/** Coordinator 没有直接聊天入口；未知旧系统身份也保持受保护。 */
export function isTaskCoordinatorAgent(agent: Pick<AgentProfile, 'is_system' | 'kind'>): boolean {
  return agent.kind === 'task_coordinator' || (agent.is_system === true && !agent.kind);
}

export function isKnowledgeLibrarianAgent(agent: Pick<AgentProfile, 'kind'>): boolean {
  return agent.kind === 'knowledge_librarian';
}

export function isUserManagedAgent(agent: Pick<AgentProfile, 'is_system' | 'kind'>): boolean {
  return !agent.is_system && !isTaskCoordinatorAgent(agent) && !isKnowledgeLibrarianAgent(agent);
}

export function isChatAgent(agent: Pick<AgentProfile, 'is_system' | 'kind'>): boolean {
  return isUserManagedAgent(agent) || isKnowledgeLibrarianAgent(agent);
}
