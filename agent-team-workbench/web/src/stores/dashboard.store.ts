import { create } from 'zustand';
import { getDashboard } from '../api/endpoints';
import type { Dashboard } from '../api/types';
import { captureScope, isCurrent, registerWorkspaceScopedReset } from './scope';
import { createRequestGuard } from './request-guard';

interface DashboardStore {
  dashboard: Dashboard | null;
  hydrate: (d: Dashboard) => void;
  refresh: () => Promise<void>;
  /** 切换 Workspace 时清空总览投影。 */
  reset: () => void;
}

const refreshGuard = createRequestGuard();

export const useDashboardStore = create<DashboardStore>()((set) => ({
  dashboard: null,
  hydrate: (dashboard) => set({ dashboard }),
  reset: () => set({ dashboard: null }),
  refresh: async () => {
    const scope = captureScope();
    if (!scope.workspaceId) return;
    const isStale = refreshGuard.begin();
    const dashboard = await getDashboard(scope.workspaceId);
    // 期间已发出更新的 refresh 或已切换 Workspace：丢弃旧响应
    if (isStale() || !isCurrent(scope)) return;
    set({ dashboard });
  },
}));

registerWorkspaceScopedReset(() => useDashboardStore.getState().reset());
