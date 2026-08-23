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
import { createRequestGuard } from './request-guard';
import { useTasksStore } from './tasks.store';
import { toast } from './toast.store';
import { useWorkspaceStore } from './workspace.store';

/** 对话消息气泡（由 run 事件时间线推导）。 */
export interface ChatMessage {
  key: string;
  runId: string;
  kind: 'user' | 'assistant' | 'thinking' | 'tool' | 'error' | 'system';
  text: string;
  at: string;
}

export interface RunStreamParts {
  reasoning: string;
  answerDraft: string;
}

/** 从 DSH message.delta 的 raw.chunk 提取流式块（reasoning-delta / text-delta）。 */
export function extractDeltaChunk(data?: Record<string, unknown>): { type?: string; text?: string } | null {
  const raw = data?.raw;
  if (!raw || typeof raw !== 'object') return null;
  const chunk = (raw as Record<string, unknown>).chunk;
  if (!chunk || typeof chunk !== 'object') return null;
  const c = chunk as Record<string, unknown>;
  return {
    type: typeof c.type === 'string' ? c.type : undefined,
    text: typeof c.text === 'string' ? c.text : undefined,
  };
}

/** 聚合单个 run 时间线中的推理与回复草稿（用于实时展示）。 */
export function aggregateRunStream(entries: TimelineEntry[]): RunStreamParts {
  let reasoning = '';
  let answerDraft = '';
  for (const e of entries) {
    if (e.type !== 'message.delta') continue;
    const chunk = extractDeltaChunk(e.data);
    if (!chunk?.text) continue;
    if (chunk.type === 'reasoning-delta') reasoning += chunk.text;
    if (chunk.type === 'text-delta') answerDraft += chunk.text;
  }
  return { reasoning, answerDraft };
}

const TERMINAL: ReadonlySet<string> = new Set(['succeeded', 'interrupted', 'cancelled', 'lost', 'failed']);
const ACTIVE: ReadonlySet<string> = new Set(['queued', 'starting', 'running', 'waiting_approval', 'interrupting', 'cancelling', 'reconnecting', 'succeeding']);

/** 从 run 时间线推导对话气泡（user 右 / assistant 左 / tool·error·system 居中细行）。 */
export function buildMessages(runIds: string[], timelines: Record<string, TimelineEntry[]>): ChatMessage[] {
  const out: ChatMessage[] = [];
  for (const runId of runIds) {
    const entries = timelines[runId] ?? [];
    let reasoningBuf = '';
    for (const e of entries) {
      const instruction = typeof e.data?.instruction === 'string' ? e.data.instruction : '';
      switch (e.type) {
        case 'run.created':
          if (instruction) {
            out.push({ key: e.event_id, runId, kind: 'user', text: instruction, at: e.occurred_at });
          }
          break;
        case 'message.delta': {
          const chunk = extractDeltaChunk(e.data);
          if (chunk?.type === 'reasoning-delta' && chunk.text) {
            reasoningBuf += chunk.text;
          }
          if (e.role === 'user' && e.text) {
            out.push({ key: e.event_id, runId, kind: 'user', text: e.text, at: e.occurred_at });
          }
          break;
        }
        case 'message.completed':
          if (reasoningBuf) {
            out.push({
              key: `${runId}-thinking`,
              runId,
              kind: 'thinking',
              text: reasoningBuf,
              at: e.occurred_at,
            });
            reasoningBuf = '';
          }
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

/** 会话侧栏展示：优先反映最新 Run 状态，避免任务 in_progress 与 Run 已成功不一致。 */
export function conversationLabel(
  item: WorkItem,
  runs: Record<string, { status: string }>,
): string {
  const run = item.latest_run_id ? runs[item.latest_run_id] : undefined;
  if (run) {
    if (ACTIVE.has(run.status)) return '思考中…';
    if (run.status === 'succeeded') return '已回复';
    if (run.status === 'failed') return '失败';
    if (run.status === 'waiting_approval') return '待审批';
  }
  if (item.phase === 'review') return '已回复';
  if (item.status === 'completed') return '已完成';
  if (item.status === 'todo') return '待开始';
  return item.status;
}

export { ACTIVE, TERMINAL };

// 请求序号守卫：会话列表与 run 列表各自独立计数（互不干扰），只丢弃各自域的过期响应。
const conversationsGuard = createRequestGuard();
const runsGuard = createRequestGuard();

/** SSE 快照优先于会话列表加载时的旧状态，避免终态后仍误走 steering。 */
export function currentRunSnapshot(
  local: ExecutionRun | undefined,
  snapshots: Record<string, ExecutionRun>,
): ExecutionRun | undefined {
  return local ? (snapshots[local.id] ?? local) : undefined;
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
    const isStale = conversationsGuard.begin();
    const { items } = await listWorkItems(wsId, { assignee: agentId });
    if (isStale()) return; // 期间已切换 agent：丢弃旧响应
    items.sort((a, b) => b.updated_at.localeCompare(a.updated_at));
    set({ conversations: items });
    const runsStore = useRunsStore.getState();
    const runIds = [...new Set(items.map((i) => i.latest_run_id).filter((id): id is string => !!id))];
    await Promise.all(runIds.map((id) => runsStore.fetchRun(id)));
  },

  refreshRuns: async () => {
    const conversationId = get().conversationId;
    if (!conversationId) {
      set({ runs: [] });
      return;
    }
    const isStale = runsGuard.begin();
    const { items } = await listWorkItemRuns(conversationId);
    // 期间已切换会话（openConversation 会 bump 票号）或已切走 agent
    // （selectAgent 把 conversationId 置空但不 bump 票号）：两种都丢弃旧响应。
    if (isStale() || get().conversationId !== conversationId) return;
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
        await get().refreshConversations();
      }
      const runsStore = useRunsStore.getState();
      const latest = get().runs[get().runs.length - 1];
      const latestSnapshot = currentRunSnapshot(latest, runsStore.runs);

      if (latestSnapshot && !TERMINAL.has(latestSnapshot.status)) {
        // 活动 Run：steering 追加（协议 §5.3 commands/input）。
        await sendRunInput(latestSnapshot.id, text.trim());
      } else {
        const resp = await createRun(conversationId, {
          agent_profile_id: agentId,
          input: { instruction: text.trim() },
        });
        // 先刷新 run 列表再订阅，避免时间线已加载但 runIds 仍为空导致消息不渲染。
        await get().refreshRuns();
        runsStore.watchRun(resp.run_id);
      }
      await get().refreshConversations();
      await get().refreshRuns();
      await useTasksStore.getState().refresh();
    } catch (err) {
      toast.error(err instanceof Error ? err.message : '发送失败');
    } finally {
      set({ sending: false });
    }
  },
}));
