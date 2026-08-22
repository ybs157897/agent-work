import { create } from 'zustand';
import { getDashboard } from '../api/endpoints';
import type { Dashboard } from '../api/types';
import { useWorkspaceStore } from './workspace.store';

interface DashboardStore {
  dashboard: Dashboard | null;
  hydrate: (d: Dashboard) => void;
  refresh: () => Promise<void>;
}

export const useDashboardStore = create<DashboardStore>()((set) => ({
  dashboard: null,
  hydrate: (dashboard) => set({ dashboard }),
  refresh: async () => {
    const wsId = useWorkspaceStore.getState().workspace?.id;
    if (!wsId) return;
    set({ dashboard: await getDashboard(wsId) });
  },
}));
