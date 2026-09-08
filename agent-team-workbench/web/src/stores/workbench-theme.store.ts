import { create } from 'zustand';

/** The one persisted appearance preference shared by the workbench and Chat. */
export const WORKBENCH_THEME_KEY = 'chat:theme';
export type WorkbenchTheme = 'light' | 'dark';

export const DEFAULT_WORKBENCH_THEME: WorkbenchTheme = 'light';

export function normalizeWorkbenchTheme(value: unknown): WorkbenchTheme {
  return value === 'dark' ? 'dark' : DEFAULT_WORKBENCH_THEME;
}

function readStoredTheme(): WorkbenchTheme {
  if (typeof window === 'undefined') return DEFAULT_WORKBENCH_THEME;
  try {
    return normalizeWorkbenchTheme(window.localStorage.getItem(WORKBENCH_THEME_KEY));
  } catch {
    // Storage is optional; the in-memory default keeps the shell renderable.
    return DEFAULT_WORKBENCH_THEME;
  }
}

function persistTheme(theme: WorkbenchTheme): void {
  if (typeof window === 'undefined') return;
  try {
    window.localStorage.setItem(WORKBENCH_THEME_KEY, theme);
  } catch {
    // Private browsing and restricted embeds can reject storage writes.
  }
}

/** Keep portaled dialogs on the same mode as the mounted workbench shell. */
export function applyWorkbenchTheme(theme: WorkbenchTheme): void {
  if (typeof document === 'undefined') return;
  document.documentElement.dataset.workbenchTheme = theme;
}

interface WorkbenchThemeStore {
  theme: WorkbenchTheme;
  setTheme: (theme: WorkbenchTheme) => void;
  toggleTheme: () => void;
}

export const useWorkbenchThemeStore = create<WorkbenchThemeStore>()((set, get) => ({
  theme: readStoredTheme(),
  setTheme: (theme) => {
    const next = normalizeWorkbenchTheme(theme);
    persistTheme(next);
    set({ theme: next });
  },
  toggleTheme: () => {
    const next = get().theme === 'dark' ? 'light' : 'dark';
    persistTheme(next);
    set({ theme: next });
  },
}));
