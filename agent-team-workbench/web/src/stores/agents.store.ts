import { create } from 'zustand';
import { disableAgent, enableAgent, listAgents } from '../api/endpoints';
import type { AgentProfile } from '../api/types';
import { captureScope, isCurrent, registerWorkspaceScopedReset } from './scope';
import { createRequestGuard } from './request-guard';

interface AgentsStore {
  agents: AgentProfile[];
  selectedAgentId: string | null;
  hydrate: (items: AgentProfile[]) => void;
  refresh: () => Promise<void>;
  select: (id: string | null) => void;
  upsert: (agent: AgentProfile) => void;
  /** enable/disable 命令；失败抛出由调用方 toast。 */
  setAvailability: (agent: AgentProfile, enabled: boolean) => Promise<void>;
  /** 切换 Workspace 时清空 Agent 列表。 */
  reset: () => void;
}

const refreshGuard = createRequestGuard();

export const useAgentsStore = create<AgentsStore>()((set, get) => ({
  agents: [],
  selectedAgentId: null,

  hydrate: (items) => set({ agents: items }),

  reset: () => set({ agents: [], selectedAgentId: null }),

  refresh: async () => {
    const scope = captureScope();
    if (!scope.workspaceId) return;
    const isStale = refreshGuard.begin();
    const { items } = await listAgents(scope.workspaceId);
    // 期间已发出更新的 refresh 或已切换 Workspace：丢弃旧响应
    if (isStale() || !isCurrent(scope)) return;
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
    const scope = captureScope();
    const updated = enabled ? await enableAgent(agent.id) : await disableAgent(agent.id);
    if (!isCurrent(scope)) return; // 切换后旧 Agent 的回写不得进入新列表
    get().upsert(updated);
  },
}));

registerWorkspaceScopedReset(() => useAgentsStore.getState().reset());
