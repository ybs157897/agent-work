import { create } from 'zustand';

export const CHAT_DISPLAY_PREFERENCES_KEY = 'chat:display-preferences';

export interface ChatDisplayPreferences {
  showReasoning: boolean;
  groupExploreTools: boolean;
  groupTerminalTools: boolean;
  groupChangesTools: boolean;
}

export const DEFAULT_CHAT_DISPLAY_PREFERENCES: ChatDisplayPreferences = {
  showReasoning: true,
  groupExploreTools: true,
  groupTerminalTools: true,
  groupChangesTools: false,
};

function readPreferences(): ChatDisplayPreferences {
  if (typeof window === 'undefined') return DEFAULT_CHAT_DISPLAY_PREFERENCES;
  try {
    const raw = window.localStorage.getItem(CHAT_DISPLAY_PREFERENCES_KEY);
    if (!raw) return DEFAULT_CHAT_DISPLAY_PREFERENCES;
    const parsed = JSON.parse(raw) as Partial<ChatDisplayPreferences>;
    return {
      showReasoning: typeof parsed.showReasoning === 'boolean'
        ? parsed.showReasoning
        : DEFAULT_CHAT_DISPLAY_PREFERENCES.showReasoning,
      groupExploreTools: typeof parsed.groupExploreTools === 'boolean'
        ? parsed.groupExploreTools
        : DEFAULT_CHAT_DISPLAY_PREFERENCES.groupExploreTools,
      groupTerminalTools: typeof parsed.groupTerminalTools === 'boolean'
        ? parsed.groupTerminalTools
        : DEFAULT_CHAT_DISPLAY_PREFERENCES.groupTerminalTools,
      groupChangesTools: typeof parsed.groupChangesTools === 'boolean'
        ? parsed.groupChangesTools
        : DEFAULT_CHAT_DISPLAY_PREFERENCES.groupChangesTools,
    };
  } catch {
    return DEFAULT_CHAT_DISPLAY_PREFERENCES;
  }
}

function persist(preferences: ChatDisplayPreferences): void {
  if (typeof window === 'undefined') return;
  try {
    window.localStorage.setItem(CHAT_DISPLAY_PREFERENCES_KEY, JSON.stringify(preferences));
  } catch {
    // Storage is an enhancement; the in-memory setting still applies.
  }
}

interface ChatPreferencesStore extends ChatDisplayPreferences {
  setPreference: <K extends keyof ChatDisplayPreferences>(key: K, value: ChatDisplayPreferences[K]) => void;
}

export const useChatPreferencesStore = create<ChatPreferencesStore>()((set) => ({
  ...readPreferences(),
  setPreference: (key, value) => set((state) => {
    const next: ChatDisplayPreferences = {
      showReasoning: state.showReasoning,
      groupExploreTools: state.groupExploreTools,
      groupTerminalTools: state.groupTerminalTools,
      groupChangesTools: state.groupChangesTools,
      [key]: value,
    };
    persist(next);
    return next;
  }),
}));
