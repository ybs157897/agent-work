import { create } from 'zustand';
import { listActivities } from '../api/endpoints';
import type { Activity, CanonicalEvent } from '../api/types';
import { captureScope, isCurrent, registerWorkspaceScopedReset } from './scope';

const LOGS_CAP = 200;

interface LogsStore {
  items: Activity[];
  loaded: boolean;
  refresh: () => Promise<void>;
  /** activity.appended：实时前插（协议 §5.2 日志页事件）。 */
  prependFromEvent: (ev: CanonicalEvent) => void;
  /** 切换 Workspace 时清空活动日志。 */
  reset: () => void;
}

export const useLogsStore = create<LogsStore>()((set) => ({
  items: [],
  loaded: false,

  reset: () => set({ items: [], loaded: false }),

  refresh: async () => {
    const scope = captureScope();
    if (!scope.workspaceId) return;
    const { items } = await listActivities(scope.workspaceId);
    if (!isCurrent(scope)) return; // 切换后旧 Workspace 的日志不回写
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

registerWorkspaceScopedReset(() => useLogsStore.getState().reset());
