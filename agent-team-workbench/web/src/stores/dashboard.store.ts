import { create } from 'zustand';
import { getDashboard } from '../api/endpoints';
import type { Dashboard } from '../api/types';
import { createRequestGuard } from './request-guard';
import { useWorkspaceStore } from './workspace.store';

interface DashboardStore {
  dashboard: Dashboard | null;
  hydrate: (d: Dashboard) => void;
  refresh: () => Promise<void>;
}

const refreshGuard = createRequestGuard();

export const useDashboardStore = create<DashboardStore>()((set) => ({
  dashboard: null,
  hydrate: (dashboard) => set({ dashboard }),
  refresh: async () => {
    const wsId = useWorkspaceStore.getState().workspace?.id;
    if (!wsId) return;
    const isStale = refreshGuard.begin();
    const dashboard = await getDashboard(wsId);
    if (isStale()) return; // 期间已发出更新的 refresh：丢弃旧响应
    set({ dashboard });
  },
}));
