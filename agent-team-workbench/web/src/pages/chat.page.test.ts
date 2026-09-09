import { describe, expect, it } from 'vitest';
import { formatLegacyRecoveryContent, shouldPreserveChatDeepLink, shouldRebindChatOwner } from './chat.page';

describe('Chat Workspace route owner fencing', () => {
  it('A/settings → B/chat → A/settings 不允许 outgoing Chat 覆盖全局恢复路由', () => {
    expect(shouldRebindChatOwner('ws_A', 'ws_B', '/settings', '/chat?ws=ws_B')).toBe(false);
    expect(shouldRebindChatOwner('ws_B', 'ws_A', '/chat', '/settings?ws=ws_A')).toBe(false);
    expect(shouldRebindChatOwner('ws_B', 'ws_A', '/settings', '/settings?ws=ws_A')).toBe(true);
  });
});

describe('Chat deep-link hydration fence', () => {
  it('keeps ?c= while the requested conversation is still opening', () => {
    expect(shouldPreserveChatDeepLink('wi_1', 'wi_1', null)).toBe(true);
    expect(shouldPreserveChatDeepLink('wi_1', null, 'wi_1')).toBe(true);
    expect(shouldPreserveChatDeepLink('wi_1', 'wi_2', null)).toBe(false);
    expect(shouldPreserveChatDeepLink('wi_1', null, null)).toBe(false);
  });
});

describe('legacy task-intake recovery boundary', () => {
  it('shows the historical project key and restores only text into the current Chat', () => {
    const content = formatLegacyRecoveryContent({
      projectKey: 'legacy-project-a',
      hasTerminalState: true,
      draft: { title: '旧登录草案', description: '历史描述', acceptance_criteria: ['可恢复'] },
      composerText: '继续补充',
      messages: [{ role: 'user', content: '旧需求' }],
    });
    expect(content).toContain('历史来源 projectKey：legacy-project-a');
    expect(content).toContain('旧登录草案');
    expect(content).toContain('仅恢复历史文字到当前 Chat');
    expect(content).toContain('不切换当前工作区目录');
    expect(content).toContain('不自动发布任务');
  });
});
