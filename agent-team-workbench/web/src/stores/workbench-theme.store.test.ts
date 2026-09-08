import { beforeEach, describe, expect, it, vi } from 'vitest';
import {
  DEFAULT_WORKBENCH_THEME,
  WORKBENCH_THEME_KEY,
  applyWorkbenchTheme,
  normalizeWorkbenchTheme,
  useWorkbenchThemeStore,
} from './workbench-theme.store';

describe('workbench theme preference', () => {
  beforeEach(() => {
    useWorkbenchThemeStore.setState({
      theme: DEFAULT_WORKBENCH_THEME,
      setTheme: useWorkbenchThemeStore.getState().setTheme,
      toggleTheme: useWorkbenchThemeStore.getState().toggleTheme,
    });
    vi.unstubAllGlobals();
  });

  it('normalizes unknown persisted values to the light default', () => {
    expect(normalizeWorkbenchTheme('dark')).toBe('dark');
    expect(normalizeWorkbenchTheme('light')).toBe('light');
    expect(normalizeWorkbenchTheme('sepia')).toBe('light');
    expect(normalizeWorkbenchTheme(null)).toBe('light');
  });

  it('toggles and persists the shared chat theme key', () => {
    const setItem = vi.fn();
    vi.stubGlobal('window', { localStorage: { setItem } });

    useWorkbenchThemeStore.getState().toggleTheme();

    expect(useWorkbenchThemeStore.getState().theme).toBe('dark');
    expect(setItem).toHaveBeenCalledWith(WORKBENCH_THEME_KEY, 'dark');
  });

  it('keeps the in-memory setting when storage is unavailable', () => {
    vi.stubGlobal('window', {
      localStorage: {
        setItem: () => { throw new Error('storage blocked'); },
      },
    });

    expect(() => useWorkbenchThemeStore.getState().setTheme('dark')).not.toThrow();
    expect(useWorkbenchThemeStore.getState().theme).toBe('dark');
  });

  it('syncs the document mode for portaled surfaces when a document exists', () => {
    const documentElement = { dataset: {} as Record<string, string> };
    vi.stubGlobal('document', { documentElement });

    applyWorkbenchTheme('dark');

    expect(documentElement.dataset.workbenchTheme).toBe('dark');
  });
});
