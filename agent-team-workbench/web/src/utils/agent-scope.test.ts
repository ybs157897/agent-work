import { describe, expect, it } from 'vitest';
import { isChatAgent, isKnowledgeLibrarianAgent, isTaskCoordinatorAgent, isUserManagedAgent } from './agent-scope';

describe('内置智能体的产品入口', () => {
  it('内置知识管理员参加普通聊天，但不成为可任意编辑的普通成员', () => {
    const librarian = { kind: 'knowledge_librarian', is_system: true };
    expect(isKnowledgeLibrarianAgent(librarian)).toBe(true);
    expect(isChatAgent(librarian)).toBe(true);
    expect(isUserManagedAgent(librarian)).toBe(false);
    expect(isTaskCoordinatorAgent(librarian)).toBe(false);
  });

  it('Coordinator 和未知系统身份保持不进入聊天', () => {
    expect(isChatAgent({ kind: 'task_coordinator', is_system: true })).toBe(false);
    expect(isChatAgent({ kind: 'future_system_agent', is_system: true })).toBe(false);
    expect(isChatAgent({ is_system: true })).toBe(false);
  });

  it('普通成员继续可聊天，不能仅靠角色名称冒充内置身份', () => {
    expect(isChatAgent({ kind: 'user', is_system: false })).toBe(true);
    expect(isKnowledgeLibrarianAgent({ kind: 'user' })).toBe(false);
  });
});
