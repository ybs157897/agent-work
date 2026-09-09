import { create } from 'zustand';
import type { SseStatus } from '../api/sse';
import type { Health, Me, Workspace } from '../api/types';

export type BootPhase = 'booting' | 'ready' | 'error';

const LAST_ROUTE_KEY = 'workbench.last-route-by-workspace';

function readLastRoutes(): Record<string, string> {
  if (typeof window === 'undefined') return {};
  try {
    const parsed: unknown = JSON.parse(window.localStorage.getItem(LAST_ROUTE_KEY) ?? '{}');
    if (typeof parsed !== 'object' || parsed === null) return {};
    return Object.fromEntries(Object.entries(parsed).filter(([key, value]) => typeof key === 'string' && typeof value === 'string'));
  } catch {
    return {};
  }
}

function writeLastRoutes(routes: Record<string, string>): void {
  if (typeof window === 'undefined') return;
  try { window.localStorage.setItem(LAST_ROUTE_KEY, JSON.stringify(routes)); } catch { /* optional recovery */ }
}

/**
 * Workspace 级状态（任务控制面 RFC §12.1）：
 * - generation 单调递增，是所有异步闭包的 fencing 凭证（stores/scope.ts）；
 * - workspaces/selectedWorkspaceId 支撑全局切换；switching 期间选择器禁用；
 * - eventCursor 只由合法 SSE 事件原子推进（max 语义），是 Brief/Queue 判 stale 的基准。
 */
interface WorkspaceStore {
  phase: BootPhase;
  error: string | null;
  /** 面向用户的切换/回退播报（role=status 播报；与 error 互不吞并）。 */
  notice: string | null;
  me: Me | null;
  workspace: Workspace | null;
  workspaces: Workspace[];
  selectedWorkspaceId: string | null;
  lastRoutes: Record<string, string>;
  generation: number;
  /** switchWorkspace 进行中（WorkspaceSelector disabled 依据）。 */
  switching: boolean;
  health: Health | null;
  eventCursor: number;
  sseStatus: SseStatus;

  setBooting: () => void;
  setWorkspaces: (items: Workspace[]) => void;
  setReady: (me: Me | null, workspace: Workspace, health: Health, eventCursor: number) => void;
  setWorkspace: (workspace: Workspace) => void;
  setError: (message: string) => void;
  setSseStatus: (status: SseStatus) => void;
  setNotice: (notice: string | null) => void;
  /** Preload target data while keeping the current Workspace usable. */
  setSwitching: (switching: boolean) => void;
  rememberRoute: (workspaceId: string, route: string) => void;
  lastRouteFor: (workspaceId: string) => string | null;
  /** 切换第 1 步：generation+1、指向目标并进入 booting（旧 workspace 数据立即失效）。 */
  beginSwitch: (workspaceId: string) => void;
  /** 原子推进事件游标：eventCursor = max(current, seq)。 */
  advanceEventCursor: (seq: number) => void;
}

export const useWorkspaceStore = create<WorkspaceStore>()((set, get) => ({
  phase: 'booting',
  error: null,
  notice: null,
  me: null,
  workspace: null,
  workspaces: [],
  selectedWorkspaceId: null,
  lastRoutes: readLastRoutes(),
  generation: 0,
  switching: false,
  health: null,
  eventCursor: 0,
  sseStatus: 'connecting',

  setBooting: () => set({ phase: 'booting', error: null }),
  setWorkspaces: (workspaces) => set({ workspaces }),
  setReady: (me, workspace, health, eventCursor) =>
    set({
      phase: 'ready',
      error: null,
      me,
      workspace,
      health,
      eventCursor,
      sseStatus: 'connecting',
      selectedWorkspaceId: workspace.id,
      switching: false,
    }),
  setWorkspace: (workspace) => set({ workspace }),
  setError: (message) => set({ phase: 'error', error: message, switching: false }),
  setSseStatus: (sseStatus) => set({ sseStatus }),
  setNotice: (notice) => set({ notice }),
  setSwitching: (switching) => set({ switching }),
  rememberRoute: (workspaceId, route) => set((state) => {
    if (!workspaceId || !route || state.lastRoutes[workspaceId] === route) return state;
    const lastRoutes = { ...state.lastRoutes, [workspaceId]: route };
    writeLastRoutes(lastRoutes);
    return { lastRoutes };
  }),
  lastRouteFor: (workspaceId): string | null => get().lastRoutes[workspaceId] ?? null,
  beginSwitch: (workspaceId) =>
    set((s) => ({
      phase: 'booting',
      error: null,
      switching: true,
      selectedWorkspaceId: workspaceId,
      generation: s.generation + 1,
      workspace: null,
      health: null,
      eventCursor: 0,
      sseStatus: 'connecting',
    })),
  advanceEventCursor: (seq) => set((s) => (seq > s.eventCursor ? { eventCursor: seq } : s)),
}));
