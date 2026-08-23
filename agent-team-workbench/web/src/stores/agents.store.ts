import { create } from 'zustand';
import { disableAgent, enableAgent, listAgents } from '../api/endpoints';
import type { AgentProfile } from '../api/types';
import { createRequestGuard } from './request-guard';
import { useWorkspaceStore } from './workspace.store';

interface AgentsStore {
  agents: AgentProfile[];
  selectedAgentId: string | null;
  hydrate: (items: AgentProfile[]) => void;
  refresh: () => Promise<void>;
  select: (id: string | null) => void;
  upsert: (agent: AgentProfile) => void;
  /** enable/disable 命令；失败抛出由调用方 toast。 */
  setAvailability: (agent: AgentProfile, enabled: boolean) => Promise<void>;
}

const refreshGuard = createRequestGuard();

export const useAgentsStore = create<AgentsStore>()((set, get) => ({
  agents: [],
  selectedAgentId: null,

  hydrate: (items) => set({ agents: items }),

  refresh: async () => {
    const wsId = useWorkspaceStore.getState().workspace?.id;
    if (!wsId) return;
    const isStale = refreshGuard.begin();
    const { items } = await listAgents(wsId);
    if (isStale()) return; // 期间已发出更新的 refresh：丢弃旧响应
    set({ agents: items });
  },

  select: (selectedAgentId) => set({ selectedAgentId }),

  upsert: (agent) =>
    set((s) => ({
      agents: s.agents.some((a) => a.id === agent.id)
        ? s.agents.map((a) => (a.id === agent.id ? agent : a))
        : [...s.agents, agent],
    })),

  setAvailability: async (agent, enabled) => {
    const updated = enabled ? await enableAgent(agent.id) : await disableAgent(agent.id);
    get().upsert(updated);
  },
}));
