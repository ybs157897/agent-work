import { renderToStaticMarkup } from 'react-dom/server';
import { beforeEach, describe, expect, it } from 'vitest';
import { DEFAULT_CHAT_DISPLAY_PREFERENCES, useChatPreferencesStore } from '../stores/chat-preferences.store';
import SettingsPage from './settings.page';

describe('SettingsPage · ZCode chat preferences', () => {
  beforeEach(() => {
    useChatPreferencesStore.setState({
      ...DEFAULT_CHAT_DISPLAY_PREFERENCES,
      setPreference: useChatPreferencesStore.getState().setPreference,
    });
  });

  it('exposes the four display-only switches with ZCode defaults', () => {
    const html = renderToStaticMarkup(<SettingsPage />);
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
  });
});
