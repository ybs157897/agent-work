import { create } from 'zustand';
import { listActivities } from '../api/endpoints';
import type { Activity, CanonicalEvent } from '../api/types';
import { useWorkspaceStore } from './workspace.store';

const LOGS_CAP = 200;

interface LogsStore {
  items: Activity[];
  loaded: boolean;
  refresh: () => Promise<void>;
  /** activity.appended：实时前插（协议 §5.2 日志页事件）。 */
  prependFromEvent: (ev: CanonicalEvent) => void;
}

export const useLogsStore = create<LogsStore>()((set) => ({
  items: [],
  loaded: false,

  refresh: async () => {
    const wsId = useWorkspaceStore.getState().workspace?.id;
    if (!wsId) return;
    const { items } = await listActivities(wsId);
    set({ items, loaded: true });
  },

  prependFromEvent: (ev) => {
    const kind = typeof ev.data?.kind === 'string' ? ev.data.kind : 'event';
    const message = typeof ev.data?.message === 'string' ? ev.data.message : ev.type;
    set((s) => ({
      items: [
        { id: ev.event_id, kind, message, occurred_at: ev.occurred_at },
        ...s.items,
      ].slice(0, LOGS_CAP),
    }));
  },
}));
