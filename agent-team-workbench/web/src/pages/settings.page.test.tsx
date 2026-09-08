import { renderToStaticMarkup } from 'react-dom/server';
import { MemoryRouter } from 'react-router-dom';
import { beforeEach, describe, expect, it } from 'vitest';
import { DEFAULT_CHAT_DISPLAY_PREFERENCES, useChatPreferencesStore } from '../stores/chat-preferences.store';
import type { AgentProfile } from '../api/types';
import SettingsPage, { BuiltinKnowledgeSettings } from './settings.page';

describe('SettingsPage · ZCode chat preferences', () => {
  beforeEach(() => {
    useChatPreferencesStore.setState({
      ...DEFAULT_CHAT_DISPLAY_PREFERENCES,
      setPreference: useChatPreferencesStore.getState().setPreference,
    });
  });

  it('exposes the four display-only switches with ZCode defaults', () => {
    const html = renderToStaticMarkup(<MemoryRouter><SettingsPage /></MemoryRouter>);
    expect(html).toContain('对话 · ZCode');
    expect(html).toContain('显示全部思考');
    expect(html).toContain('合并探索工具');
    expect(html).toContain('合并执行工具');
    expect(html).toContain('合并文件变更');
    expect(html.match(/role="switch"/g)).toHaveLength(4);
    expect(html.match(/aria-checked="true"/g)).toHaveLength(3);
    expect(html.match(/aria-checked="false"/g)).toHaveLength(1);
    expect(html).toContain('aria-label="显示全部思考"');
    expect(html).toContain('aria-label="合并文件变更"');
    expect(html).toContain('系统内置的团队成员');
    expect(html).not.toContain('选择负责整理资料的智能体');
  });

  it('内置管理员入口进入通用聊天与模型配置，不回到知识库录入台', () => {
    const html = renderToStaticMarkup(<MemoryRouter><BuiltinKnowledgeSettings agent={{ id: 'builtin-library' } as AgentProfile} /></MemoryRouter>);
    expect(html).toContain('href="/chat?agent=builtin-library"');
    expect(html).toContain('href="/agents?agent=builtin-library"');
    expect(html).not.toContain('/knowledge?settings');
  });
});
