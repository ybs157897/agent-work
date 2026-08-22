import { create } from 'zustand';
import type { SseStatus } from '../api/sse';
import type { Health, Me, Workspace } from '../api/types';

export type BootPhase = 'booting' | 'ready' | 'error';

interface WorkspaceStore {
  phase: BootPhase;
  error: string | null;
  me: Me | null;
  workspace: Workspace | null;
  health: Health | null;
  eventCursor: number;
  sseStatus: SseStatus;

  setBooting: () => void;
  setReady: (me: Me | null, workspace: Workspace, health: Health, eventCursor: number) => void;
  setError: (message: string) => void;
  setSseStatus: (status: SseStatus) => void;
}

export const useWorkspaceStore = create<WorkspaceStore>()((set) => ({
  phase: 'booting',
  error: null,
  me: null,
  workspace: null,
  health: null,
  eventCursor: 0,
  sseStatus: 'connecting',

  setBooting: () => set({ phase: 'booting', error: null }),
  setReady: (me, workspace, health, eventCursor) =>
    set({ phase: 'ready', error: null, me, workspace, health, eventCursor, sseStatus: 'connecting' }),
  setError: (message) => set({ phase: 'error', error: message }),
  setSseStatus: (sseStatus) => set({ sseStatus }),
}));
