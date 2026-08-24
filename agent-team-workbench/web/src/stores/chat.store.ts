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

/** 工具行卡片状态：started 时 running，completed/failed 后落定。 */
export type ToolStatus = 'running' | 'success' | 'failed';

/** 对话消息气泡（由 run 事件时间线推导）。 */
export interface ChatMessage {
  key: string;
  runId: string;
  kind: 'user' | 'assistant' | 'thinking' | 'tool' | 'error' | 'system';
  text: string;
  /** 工具结果输出等附属正文（适配器已截断），渲染为等宽块。 */
  detail?: string;
  at: string;
  /** 工具行卡片数据（kind=tool 与工具失败的 error 行）：无则不按工具行渲染。 */
  tool?: string;
  toolStatus?: ToolStatus;
  /** 耗时数据源：时间线事件的 occurred_at（同 call_id 的 started→completed 差值）。 */
  startedAt?: string;
  completedAt?: string;
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

/** 从 tool.completed/failed 载荷提取 exit_code（仅有限数值，其余形态忽略）。 */
export function extractExitCode(data?: Record<string, unknown>): number | undefined {
  const v = data?.exit_code;
  if (typeof v === 'number' && Number.isFinite(v)) return v;
  return undefined;
}

/**
 * 工具耗时人话格式：<1s 显示 ms，<60s 显示 s（1 位小数，整数不带小数点），≥60s 显示 m+s。
 * 任一时间缺失/不可解析/差值为负返回 null（调用方不渲染徽章）。
 */
export function toolDuration(startedAt?: string, completedAt?: string): string | null {
  if (!startedAt || !completedAt) return null;
  const ms = Date.parse(completedAt) - Date.parse(startedAt);
  if (!Number.isFinite(ms) || ms < 0) return null;
  if (ms < 1000) return `${Math.round(ms)}ms`;
  if (ms < 60_000) {
    return `${Math.round(ms / 100) / 10}s`;
  }
  return `${Math.floor(ms / 60_000)}m ${Math.round((ms % 60_000) / 1000)}s`;
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

/**
 * Composer 用量提示文案：`{used}k tokens`，窗口可得时 `{used}k / {window}k tokens`。
 * used 缺失/非有限/非正返回 null（调用方不渲染）；k 取整，不足 1k 记 1k 避免「0k」噪音。
 */
export function formatTokenUsage(used?: number, contextWindow?: number): string | null {
  if (typeof used !== 'number' || !Number.isFinite(used) || used <= 0) return null;
  const usedK = Math.max(1, Math.round(used / 1000));
  if (typeof contextWindow !== 'number' || !Number.isFinite(contextWindow) || contextWindow <= 0) {
    return `${usedK}k tokens`;
  }
  return `${usedK}k / ${Math.round(contextWindow / 1000)}k tokens`;
}

/** 从 run 时间线推导对话气泡（user 右 / assistant 左 / tool·error·system 居中细行）。 */
export function buildMessages(runIds: string[], timelines: Record<string, TimelineEntry[]>): ChatMessage[] {
  const out: ChatMessage[] = [];
  for (const runId of runIds) {
    const entries = timelines[runId] ?? [];
    let reasoningBuf = '';
    let answerBuf = '';
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
          if (chunk?.type === 'text-delta' && chunk.text) {
            answerBuf += chunk.text;
          }
          if (e.role === 'user' && e.text) {
            out.push({ key: e.event_id, runId, kind: 'user', text: e.text, at: e.occurred_at });
          }
          break;
        }
        case 'message.completed':
          if (reasoningBuf) {
            out.push({
              key: `${runId}-thinking-${e.event_id}`,
              runId,
              kind: 'thinking',
              text: reasoningBuf,
              at: e.occurred_at,
            });
            reasoningBuf = '';
          }
          answerBuf = '';
          if (e.text) {
            out.push({ key: e.event_id, runId, kind: 'assistant', text: e.text, at: e.occurred_at });
          }
          break;
        case 'tool.started': {
          const tool = typeof e.data?.tool === 'string' ? e.data.tool : 'tool';
          const argsSummary = typeof e.data?.args_summary === 'string' ? e.data.args_summary : '';
          const callId = typeof e.data?.call_id === 'string' ? e.data.call_id : '';
          out.push({
            // 与 completed/failed 同键折叠：结果输出挂回同一行。
            key: callId ? `${runId}-tool-${callId}` : e.event_id,
            runId,
            kind: 'tool',
            text: argsSummary ? `调用工具 ${tool}：${argsSummary}` : `调用工具 ${tool}`,
            at: e.occurred_at,
            tool,
            toolStatus: 'running',
            startedAt: e.occurred_at,
          });
          break;
        }
        case 'tool.completed':
        case 'tool.failed': {
          const output = typeof e.data?.output === 'string' ? e.data.output.trim() : '';
          const exitCode = extractExitCode(e.data);
          const exitSuffix = exitCode === undefined ? '' : ` · exit ${exitCode}`;
          // 非零 exit 即使事件是 completed 也按 error 行渲染（终端语义：命令失败）。
          const failedExit = exitCode !== undefined && exitCode !== 0;
          const callId = typeof e.data?.call_id === 'string' ? e.data.call_id : '';
          const key = callId ? `${runId}-tool-${callId}` : '';
          const idx = key ? out.findIndex((m) => m.kind === 'tool' && m.key === key) : -1;
          if (e.type === 'tool.failed') {
            const failed: ChatMessage = {
              key: key || e.event_id,
              runId,
              kind: 'error',
              text: (idx >= 0 ? out[idx].text.replace(/^调用工具/, '工具失败') : '工具调用失败') + exitSuffix,
              detail: output || undefined,
              at: e.occurred_at,
              tool: idx >= 0 ? out[idx].tool : undefined,
              toolStatus: 'failed',
              startedAt: idx >= 0 ? out[idx].startedAt : undefined,
              completedAt: e.occurred_at,
            };
            if (idx >= 0) out[idx] = failed;
            else out.push(failed);
            break;
          }
          if (!output && exitCode === undefined) break; // 无输出且无退出码的完成不刷屏
          if (idx >= 0) {
            out[idx] = {
              ...out[idx],
              kind: failedExit ? 'error' : out[idx].kind,
              text: out[idx].text + exitSuffix,
              detail: output || undefined,
              at: e.occurred_at,
              toolStatus: failedExit ? 'failed' : 'success',
              completedAt: e.occurred_at,
            };
          } else {
            out.push({
              key: e.event_id,
              runId,
              kind: failedExit ? 'error' : 'tool',
              text: '工具输出' + exitSuffix,
              detail: output || undefined,
              at: e.occurred_at,
              toolStatus: failedExit ? 'failed' : 'success',
              completedAt: e.occurred_at,
            });
          }
          break;
        }
        case 'run.failed': {
          const msg = typeof e.data?.message === 'string' ? e.data.message : '运行失败';
          const code = typeof e.data?.code === 'string' ? e.data.code : '';
          out.push({ key: e.event_id, runId, kind: 'error', text: `${code ? code + ': ' : ''}${msg}`, at: e.occurred_at });
          break;
        }
      }
    }
    if (reasoningBuf) {
      out.push({
        key: `${runId}-thinking-tail`,
        runId,
        kind: 'thinking',
        text: reasoningBuf,
        at: entries[entries.length - 1]?.occurred_at ?? '',
      });
    }
    if (answerBuf) {
      out.push({
        key: `${runId}-answer-tail`,
        runId,
        kind: 'assistant',
        text: answerBuf,
        at: entries[entries.length - 1]?.occurred_at ?? '',
      });
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
