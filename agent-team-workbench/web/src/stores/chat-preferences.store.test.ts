import { beforeEach, describe, expect, it, vi } from 'vitest';
import {
  CHAT_DISPLAY_PREFERENCES_KEY,
  DEFAULT_CHAT_DISPLAY_PREFERENCES,
  useChatPreferencesStore,
} from './chat-preferences.store';

describe('chat display preferences', () => {
  beforeEach(() => {
    useChatPreferencesStore.setState({
      ...DEFAULT_CHAT_DISPLAY_PREFERENCES,
      setPreference: useChatPreferencesStore.getState().setPreference,
    });
    vi.unstubAllGlobals();
  });

  it('ZCode defaults show all reasoning and group Explore/Terminal but not Changes', () => {
    expect(useChatPreferencesStore.getState()).toMatchObject(DEFAULT_CHAT_DISPLAY_PREFERENCES);
  });

  it('updates one preference and persists the complete snapshot', () => {
    const setItem = vi.fn();
    vi.stubGlobal('window', { localStorage: { setItem } });

    useChatPreferencesStore.getState().setPreference('showReasoning', false);

    expect(useChatPreferencesStore.getState().showReasoning).toBe(false);
    expect(setItem).toHaveBeenCalledWith(
      CHAT_DISPLAY_PREFERENCES_KEY,
      JSON.stringify({ ...DEFAULT_CHAT_DISPLAY_PREFERENCES, showReasoning: false }),
    );
  });
});
