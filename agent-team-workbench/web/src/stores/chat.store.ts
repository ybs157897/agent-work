import { create } from 'zustand';
import {
  createRun,
  createWorkItem,
  listWorkItemRuns,
  listWorkItems,
  sendRunInput,
} from '../api/endpoints';
import type { ExecutionRun, WorkItem } from '../api/types';
import { useRunsStore, type TimelineEntry } from './runs.store';
import { useTasksStore } from './tasks.store';
import { toast } from './toast.store';
import { useWorkspaceStore } from './workspace.store';

/** 对话消息气泡（由 run 事件时间线推导）。 */
export interface ChatMessage {
  key: string;
  runId: string;
  kind: 'user' | 'assistant' | 'tool' | 'error' | 'system';
  text: string;
  at: string;
}

const TERMINAL: ReadonlySet<string> = new Set(['succeeded', 'interrupted', 'cancelled', 'lost', 'failed']);

/** 从 run 时间线推导对话气泡（user 右 / assistant 左 / tool·error·system 居中细行）。 */
export function buildMessages(runIds: string[], timelines: Record<string, TimelineEntry[]>): ChatMessage[] {
  const out: ChatMessage[] = [];
  for (const runId of runIds) {
    const entries = timelines[runId] ?? [];
    for (const e of entries) {
      const instruction = typeof e.data?.instruction === 'string' ? e.data.instruction : '';
      switch (e.type) {
        case 'run.created':
          if (instruction) {
            out.push({ key: e.event_id, runId, kind: 'user', text: instruction, at: e.occurred_at });
          }
          break;
        case 'message.delta':
          if (e.role === 'user' && e.text) {
            out.push({ key: e.event_id, runId, kind: 'user', text: e.text, at: e.occurred_at });
          }
          break;
        case 'message.completed':
          if (e.text) {
            out.push({ key: e.event_id, runId, kind: 'assistant', text: e.text, at: e.occurred_at });
          }
          break;
        case 'tool.started': {
          const tool = typeof e.data?.tool === 'string' ? e.data.tool : 'tool';
          out.push({ key: e.event_id, runId, kind: 'tool', text: `调用工具 ${tool}`, at: e.occurred_at });
          break;
        }
        case 'tool.failed':
          out.push({ key: e.event_id, runId, kind: 'error', text: '工具调用失败', at: e.occurred_at });
          break;
        case 'run.failed': {
          const msg = typeof e.data?.message === 'string' ? e.data.message : '运行失败';
          const code = typeof e.data?.code === 'string' ? e.data.code : '';
          out.push({ key: e.event_id, runId, kind: 'error', text: `${code ? code + ': ' : ''}${msg}`, at: e.occurred_at });
          break;
        }
      }
    }
  }
  return out;
}

interface ChatStore {
  agentId: string | null;
  conversationId: string | null;
  /** 会话列表（该 agent 名下的 work items，最新在前）。 */
  conversations: WorkItem[];
  /** 当前会话的 run 列表（创建时间正序）。 */
  runs: ExecutionRun[];
  sending: boolean;

  selectAgent: (id: string | null) => void;
  openConversation: (workItemId: string | null) => void;
  refreshConversations: () => Promise<void>;
  refreshRuns: () => Promise<void>;
  send: (text: string) => Promise<void>;
}

export const useChatStore = create<ChatStore>()((set, get) => ({
  agentId: null,
  conversationId: null,
  conversations: [],
  runs: [],
  sending: false,

  selectAgent: (id) => {
    set({ agentId: id, conversationId: null, runs: [] });
    void get().refreshConversations();
  },

  openConversation: (workItemId) => {
    set({ conversationId: workItemId, runs: [] });
    void get().refreshRuns();
  },

  refreshConversations: async () => {
    const wsId = useWorkspaceStore.getState().workspace?.id;
    const agentId = get().agentId;
    if (!wsId || !agentId) {
      set({ conversations: [] });
      return;
    }
    const { items } = await listWorkItems(wsId, { assignee: agentId });
    items.sort((a, b) => b.updated_at.localeCompare(a.updated_at));
    set({ conversations: items });
  },

  refreshRuns: async () => {
    const conversationId = get().conversationId;
    if (!conversationId) {
      set({ runs: [] });
      return;
    }
    const { items } = await listWorkItemRuns(conversationId);
    items.sort((a, b) => a.created_at.localeCompare(b.created_at));
    set({ runs: items });
  },

  send: async (text) => {
    const wsId = useWorkspaceStore.getState().workspace?.id;
    const agentId = get().agentId;
    if (!wsId || !agentId || !text.trim() || get().sending) return;
    set({ sending: true });
    try {
      let conversationId = get().conversationId;
      if (!conversationId) {
        // 首发消息：建会话（work item）+ 建 run；任务状态由控制平面推进。
        const title = text.trim().slice(0, 24) + (text.trim().length > 24 ? '…' : '');
        const wi = await createWorkItem(wsId, {
          title,
          status: 'todo',
          priority: 'medium',
          agent_profile_id: agentId,
        });
        conversationId = wi.id;
        set({ conversationId });
      }
      const runsStore = useRunsStore.getState();
      const latest = get().runs[get().runs.length - 1];

      if (latest && !TERMINAL.has(latest.status)) {
        // 活动 Run：steering 追加（协议 §5.3 commands/input）。
        await sendRunInput(latest.id, text.trim());
      } else {
        const resp = await createRun(conversationId, {
          agent_profile_id: agentId,
          input: { instruction: text.trim() },
        });
        // 立即订阅新 run；SSE run.created 会刷新任务与 run 列表。
        runsStore.watchRun(resp.run_id);
      }
      await get().refreshRuns();
      await useTasksStore.getState().refresh();
    } catch (err) {
      toast.error(err instanceof Error ? err.message : '发送失败');
    } finally {
      set({ sending: false });
    }
  },
}));
