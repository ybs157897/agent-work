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

/**
 * 可进入对话的 Agent：身份允许，且未被停用。停用成员发起的 CreateRun 会被
 * 拒（agent 已停用），所以它不该出现在对话 chips、@ 提及或对话深链里。
 */
export function isChatAgent(agent: Pick<AgentProfile, 'is_system' | 'kind' | 'availability'>): boolean {
  if (agent.availability === 'disabled') return false;
  return isUserManagedAgent(agent) || isKnowledgeLibrarianAgent(agent);
}
