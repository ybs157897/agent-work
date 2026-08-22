import { create } from 'zustand';
import { disableAgent, enableAgent, listAgents } from '../api/endpoints';
import type { AgentProfile } from '../api/types';
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

export const useAgentsStore = create<AgentsStore>()((set, get) => ({
  agents: [],
  selectedAgentId: null,

  hydrate: (items) => set({ agents: items }),

  refresh: async () => {
    const wsId = useWorkspaceStore.getState().workspace?.id;
    if (!wsId) return;
    const { items } = await listAgents(wsId);
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
