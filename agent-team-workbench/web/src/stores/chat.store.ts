import { create } from 'zustand';
import { ApiError } from '../api/client';
import {
  createRun,
  createWorkItem,
  interruptRun,
  listWorkItemRuns,
  listWorkItems,
} from '../api/endpoints';
import type { ExecutionRun, WorkItem } from '../api/types';
import { chatErrorMessage, logChatError, type ChatErrorCode } from '../utils/chat-errors';
import { formatRunFailureMessage } from '../utils/format-run-failure';
import { useRunsStore, type TimelineEntry } from './runs.store';
import { createRequestGuard } from './request-guard';
import { useTasksStore } from './tasks.store';
import { toast } from './toast.store';
import { useWorkspaceStore } from './workspace.store';

/** 工具行卡片状态：started 时 running，completed/failed 后落定。 */
export type ToolStatus = 'running' | 'success' | 'failed';

/** run.plan_updated 的步骤视图（契约：data.steps=[{step,status}]，同 run 新帧替换旧帧）。 */
export interface PlanStepView {
  step: string;
  status: 'pending' | 'in_progress' | 'completed';
}

/** 对话消息气泡（由 run 事件时间线推导）。 */
export interface ChatMessage {
  key: string;
  runId: string;
  kind: 'user' | 'assistant' | 'thinking' | 'tool' | 'error' | 'system' | 'plan';
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
  /** 计划复选清单步骤（kind=plan）。 */
  steps?: PlanStepView[];
  /** tool.started 的 args_summary 原值（未经「调用工具 X：」前缀包装）。 */
  argsSummary?: string;
  /** tool.started 的 args 截断 JSON（后端 canonical 新增；可能不存在）。 */
  args?: string;
  /** tool.completed/failed 的 exit_code。 */
  exitCode?: number;
  /** tool.progress 流式输出的累计文本（运行中实时展示；completed 后被 detail 取代）。 */
  liveOutput?: string;
  /** message.completed 的 item_type（Codex：plan | agentMessage 等）。 */
  itemType?: string;
  /** 会话运维元信息（session.decision / compacted）：不在对话正文展示。 */
  sessionMeta?: boolean;
}

export interface RunStreamParts {
  reasoning: string;
  answerDraft: string;
}

/** 队列消息：text + 入队时生成的实体级幂等键（drain 重试安全）。 */
export interface QueuedMessage {
  text: string;
  clientKey: string;
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

const PLAN_STEP_STATUSES: ReadonlySet<string> = new Set(['pending', 'in_progress', 'completed']);

/** session.decision 的历史档位（EffectiveInstruction 三档对外投影）。 */
export type HistoryTier = 'full' | 'digest' | 'handoff';

/** 历史回放统计：turns 轮数 / est_tokens 估算 token 总量。 */
export interface HistoryStats {
  turns: number;
  est_tokens: number;
}

/** sessionLine 输入：session.decision 的 tier/reason；'compacted' 为 session.compacted 的 UI 本地判别。 */
export interface SessionEventData {
  tier: string;
  reason?: string;
  /** 可选：模型侧实际注入的那一档历史（resume 契约上省略；rotation 恒为 handoff；inline 按实际档位）。 */
  history_tier?: HistoryTier;
  /** 可选：随 history_tier 出现的量化统计；字段无效按缺失处理（降级渲染不带数字）。 */
  history_stats?: HistoryStats;
}

const HISTORY_TIERS: ReadonlySet<string> = new Set(['full', 'digest', 'handoff']);

/**
 * 计数人话缩写：<1k 原样，<1M 一位小数 k（整数千 k 无小数点），其余 M。
 * est_tokens 是粗估口径，不承诺精确值。
 */
function formatCount(n: number): string {
  if (n < 1000) return String(Math.round(n));
  if (n < 1_000_000) {
    const k = n < 100_000 ? Math.round(n / 100) / 10 : Math.round(n / 1000);
    return `${k}k`;
  }
  return `${Math.round(n / 100_000) / 10}M`;
}

/** stats 两字段均为有限非负数才算有效，否则视为缺失（降级裸档位）。 */
function validStats(stats?: HistoryStats): boolean {
  return (
    !!stats &&
    Number.isFinite(stats.turns) && stats.turns >= 0 &&
    Number.isFinite(stats.est_tokens) && stats.est_tokens >= 0
  );
}

/** 历史档位片段：full/digest 有有效 stats 时缀轮数与估算 tokens，缺则降级裸档位；handoff 恒定措辞、不消费 stats。 */
function historyFragment(tier: HistoryTier, stats?: HistoryStats): string {
  if (tier === 'handoff') return '交接摘要延续';
  const label = tier === 'full' ? '历史回放' : '压缩回放';
  if (!validStats(stats)) return label;
  return `${label}：${Math.round(stats.turns)} 轮（约 ${formatCount(stats.est_tokens)} tokens）`;
}

/**
 * 会话状态元信息行文案映射（tier×reason → 人话；未知组合返回 null 不渲染）。
 * 会话管理可视化：续接/轮换/自愈/压缩从隐形变可见。
 *
 * 合并策略（防同一事件渲染两行）：带 history_tier 的 decision 不另起行，
 * 以「基线文案 · 档位片段」并为单行——轮换/自愈的原因语义（budget/threshold/
 * session_unknown/config_drift…）是既有观测面，不能被档位行顶掉；契约上
 * resume 不带 history_tier，即便带上也照常拼接进「已续接会话」同一行。
 * 未知 history_tier 按 absence 处理（只出基线行）；fail-safe 保持事件级：
 * tier×reason 组合不被识别时整行不渲染（history_tier 不改变此判定）。
 */
export function sessionLine(data?: SessionEventData): string | null {
  if (!data) return null;
  let base: string | null;
  switch (data.tier) {
    case 'resume':
      base = '已续接会话';
      break;
    case 'rotation':
      if (data.reason === 'budget') base = '已轮换新会话（预算）';
      else if (data.reason === 'threshold') base = '已轮换新会话（阈值）';
      else base = '已轮换新会话';
      break;
    case 'inline':
      if (data.reason === 'session_unknown') base = '已重建会话（自愈）';
      else if (data.reason === 'config_drift') base = '已重建会话（配置漂移）';
      else if (data.reason === 'fresh') base = '已开新会话';
      else return null;
      break;
    case 'compacted':
      base = '会话已压缩';
      break;
    default:
      return null;
  }
  const fragment = data.history_tier ? historyFragment(data.history_tier, data.history_stats) : '';
  return fragment ? `${base} · ${fragment}` : base;
}

/** wire 层收窄 history_tier：仅认三档枚举，缺席/未知返回 undefined（降级纯基线行）。 */
function parseHistoryTier(data?: Record<string, unknown>): HistoryTier | undefined {
  const v = data?.history_tier;
  return typeof v === 'string' && HISTORY_TIERS.has(v) ? (v as HistoryTier) : undefined;
}

/** wire 层收窄 history_stats 容器：数值有效性由 historyFragment 统一判定（单一校验点）。 */
function parseHistoryStats(data?: Record<string, unknown>): HistoryStats | undefined {
  const v = data?.history_stats;
  return v && typeof v === 'object' ? (v as HistoryStats) : undefined;
}

/**
 * 防御式解析 run.plan_updated 载荷：steps 非数组或全为无效条目返回 null
 * （事件契约未落地/载荷异常时卡片不出现，不报错）。
 */
export function parsePlanSteps(data?: Record<string, unknown>): PlanStepView[] | null {
  const raw = data?.steps;
  if (!Array.isArray(raw)) return null;
  const steps: PlanStepView[] = [];
  for (const s of raw) {
    if (!s || typeof s !== 'object') continue;
    const rec = s as Record<string, unknown>;
    if (typeof rec.step !== 'string' || !rec.step.trim()) continue;
    if (typeof rec.status !== 'string' || !PLAN_STEP_STATUSES.has(rec.status)) continue;
    steps.push({ step: rec.step, status: rec.status as PlanStepView['status'] });
  }
  return steps.length ? steps : null;
}

/**
 * 活跃 run 的流式 tail 由 appendLiveTail 渲染；仅隐藏 buildMessages 尾缓冲，避免与 live tail 双份。
 * 已定稿 thinking / assistant 保留时间线位置（多阶段 run：工具段之间的正文不再消失）。
 */
export function hideLiveRunDrafts(
  messages: ChatMessage[],
  runId: string | undefined,
  live: boolean,
): ChatMessage[] {
  if (!live || !runId) return messages;
  return messages.filter((m) => {
    if (m.runId !== runId) return true;
    if (m.key.endsWith('-thinking-tail') || m.key.endsWith('-answer-tail')) return false;
    return true;
  });
}

/** run 是否仍在执行（含 waiting_approval / reconnecting）。 */
export function isRunLive(status: string | undefined): boolean {
  return status !== undefined && ACTIVE.has(status);
}

/** 聚合单个 run 时间线中的推理与回复草稿（message.completed 之后重置，仅保留当前段流式内容）。 */
export function aggregateRunStream(entries: TimelineEntry[]): RunStreamParts {
  let reasoning = '';
  let answerDraft = '';
  for (const e of entries) {
    if (e.type === 'message.completed') {
      reasoning = '';
      answerDraft = '';
      continue;
    }
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

/** liveOutput 累计上限：超出保留尾部（流式输出最新内容最有价值，头部可弃）。 */
const LIVE_OUTPUT_LIMIT = 4_000;

function capLiveOutput(text: string): string {
  return text.length > LIVE_OUTPUT_LIMIT ? text.slice(text.length - LIVE_OUTPUT_LIMIT) : text;
}

/** 从 run 时间线推导对话气泡（user 右 / assistant 左 / tool·error·system 居中细行）。 */
export function buildMessages(runIds: string[], timelines: Record<string, TimelineEntry[]>): ChatMessage[] {
  const out: ChatMessage[] = [];
  for (const runId of runIds) {
    const entries = timelines[runId] ?? [];
    let reasoningBuf = '';
    let answerBuf = '';
    let planIdx = -1; // 同 run 的 plan 快照在 out 中的位置：新帧原地替换
    for (const e of entries) {
      const instruction = typeof e.data?.instruction === 'string' ? e.data.instruction : '';
      switch (e.type) {
        case 'run.plan_updated': {
          const steps = parsePlanSteps(e.data);
          if (!steps) break; // 契约未落地/载荷异常：不产生卡片
          if (planIdx >= 0) {
            out[planIdx] = { ...out[planIdx], steps, at: e.occurred_at };
          } else {
            out.push({ key: `${runId}-plan`, runId, kind: 'plan', text: '', steps, at: e.occurred_at });
            planIdx = out.length - 1;
          }
          break;
        }
        case 'run.created':
          if (instruction) {
            out.push({ key: e.event_id, runId, kind: 'user', text: instruction, at: e.occurred_at });
          }
          break;
        case 'session.decision': {
          // CreateRun 会话决议（纯观测面）：映射失败（未知 tier/reason）不渲染；
          // history_tier/history_stats 为可选增强，wire 层收窄后由 sessionLine 并为单行。
          const tier = typeof e.data?.tier === 'string' ? e.data.tier : '';
          const reason = typeof e.data?.reason === 'string' ? e.data.reason : '';
          const historyTier = parseHistoryTier(e.data);
          const historyStats = parseHistoryStats(e.data);
          const text = sessionLine({
            tier,
            reason,
            ...(historyTier ? { history_tier: historyTier } : {}),
            ...(historyTier && historyStats ? { history_stats: historyStats } : {}),
          });
          if (text) out.push({ key: e.event_id, runId, kind: 'system', text, at: e.occurred_at, sessionMeta: true });
          break;
        }
        case 'session.compacted': {
          const text = sessionLine({ tier: 'compacted' });
          if (text) out.push({ key: e.event_id, runId, kind: 'system', text, at: e.occurred_at, sessionMeta: true });
          break;
        }
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
            const itemType = typeof e.data?.item_type === 'string' ? e.data.item_type : undefined;
            out.push({ key: e.event_id, runId, kind: 'assistant', text: e.text, at: e.occurred_at, itemType });
          }
          break;
        case 'tool.started': {
          const tool = typeof e.data?.tool === 'string' ? e.data.tool : 'tool';
          const argsSummary = typeof e.data?.args_summary === 'string' ? e.data.args_summary : '';
          const args = typeof e.data?.args === 'string' ? e.data.args : undefined;
          const callId = typeof e.data?.call_id === 'string' ? e.data.call_id : '';
          // 与 completed/failed 同键折叠：结果输出挂回同一行。
          const key = callId ? `${runId}-tool-${callId}` : e.event_id;
          const started: ChatMessage = {
            key,
            runId,
            kind: 'tool',
            text: argsSummary ? `调用工具 ${tool}：${argsSummary}` : `调用工具 ${tool}`,
            at: e.occurred_at,
            tool,
            toolStatus: 'running',
            startedAt: e.occurred_at,
            ...(argsSummary ? { argsSummary } : {}),
            ...(args !== undefined ? { args } : {}),
          };
          // progress 先于 started（回放乱序）已建同键占位行：原地补全摘要/args，不另起一行。
          const idx = callId ? out.findIndex((m) => m.kind === 'tool' && m.key === key) : -1;
          if (idx >= 0) out[idx] = { ...started, liveOutput: out[idx].liveOutput };
          else out.push(started);
          break;
        }
        case 'tool.progress': {
          // 流式工具输出：无 call_id 或空文本的帧无法挂行，忽略。
          const callId = typeof e.data?.call_id === 'string' ? e.data.call_id : '';
          const text = typeof e.data?.text === 'string' ? e.data.text : '';
          if (!callId || !text) break;
          const key = `${runId}-tool-${callId}`;
          const idx = out.findIndex((m) => m.kind === 'tool' && m.key === key);
          if (idx >= 0) {
            out[idx] = { ...out[idx], liveOutput: capLiveOutput((out[idx].liveOutput ?? '') + text) };
          } else {
            // 回放乱序下 progress 可能先于 started：先建 running 占位行，started 到达后同键补全。
            const tool = typeof e.data?.tool === 'string' ? e.data.tool : 'tool';
            out.push({
              key,
              runId,
              kind: 'tool',
              text: `调用工具 ${tool}`,
              at: e.occurred_at,
              tool,
              toolStatus: 'running',
              startedAt: e.occurred_at,
              liveOutput: capLiveOutput(text),
            });
          }
          break;
        }
        case 'tool.completed':
        case 'tool.failed': {
          const output = typeof e.data?.output === 'string' ? e.data.output.trim() : '';
          const exitCode = extractExitCode(e.data);
          // 非零 exit 即使事件是 completed 也按 error 行渲染（终端语义：命令失败）；退出码本身由卡片层用 exitCode 字段渲染。
          const failedExit = exitCode !== undefined && exitCode !== 0;
          const callId = typeof e.data?.call_id === 'string' ? e.data.call_id : '';
          const key = callId ? `${runId}-tool-${callId}` : '';
          const idx = key ? out.findIndex((m) => m.kind === 'tool' && m.key === key) : -1;
          const liveOutput = idx >= 0 ? out[idx].liveOutput : undefined;
          if (e.type === 'tool.failed') {
            const failed: ChatMessage = {
              key: key || e.event_id,
              runId,
              kind: 'error',
              text: idx >= 0 ? out[idx].text.replace(/^调用工具/, '工具失败') : '工具调用失败',
              detail: output || liveOutput || undefined,
              at: e.occurred_at,
              tool: idx >= 0 ? out[idx].tool : undefined,
              toolStatus: 'failed',
              startedAt: idx >= 0 ? out[idx].startedAt : undefined,
              completedAt: e.occurred_at,
              ...(exitCode !== undefined ? { exitCode } : {}),
            };
            if (idx >= 0) out[idx] = failed;
            else out.push(failed);
            break;
          }
          // 无输出、无退出码、也无流式输出的完成不刷屏。
          if (!output && exitCode === undefined && !liveOutput) break;
          if (idx >= 0) {
            out[idx] = {
              ...out[idx],
              kind: failedExit ? 'error' : out[idx].kind,
              detail: output || liveOutput || undefined,
              at: e.occurred_at,
              toolStatus: failedExit ? 'failed' : 'success',
              completedAt: e.occurred_at,
              // 落定：流式输出并入 detail（output 优先），liveOutput 删除避免双份渲染。
              liveOutput: undefined,
              ...(exitCode !== undefined ? { exitCode } : {}),
            };
          } else {
            out.push({
              key: e.event_id,
              runId,
              kind: failedExit ? 'error' : 'tool',
              text: '工具输出',
              detail: output || undefined,
              at: e.occurred_at,
              toolStatus: failedExit ? 'failed' : 'success',
              completedAt: e.occurred_at,
              ...(exitCode !== undefined ? { exitCode } : {}),
            });
          }
          break;
        }
        case 'run.failed': {
          const msg = typeof e.data?.message === 'string' ? e.data.message : undefined;
          const code = typeof e.data?.code === 'string' ? e.data.code : undefined;
          out.push({
            key: e.event_id,
            runId,
            kind: 'error',
            text: formatRunFailureMessage(code, msg),
            at: e.occurred_at,
          });
          break;
        }
        case 'run.recovery_started':
          out.push({
            key: e.event_id,
            runId,
            kind: 'system',
            text: '正在重连…',
            at: e.occurred_at,
          });
          break;
        case 'run.recovery_failed':
          out.push({
            key: e.event_id,
            runId,
            kind: 'error',
            text: '重连失败',
            at: e.occurred_at,
          });
          break;
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

/** 分叉上下文包的标记前缀：work item 的 description 以此开头即识别为分叉会话。 */
export const FORK_CONTEXT_MARKER = '【分叉上下文】';

/** 分叉上下文包上限：整体（含标记行）超限保头截断——头部指令密度最高，尾部可弃。 */
const FORK_CONTEXT_LIMIT = 4_000;

/**
 * 分叉上下文包：截取 [0..atKey] 的对话投影为纯文本（用户/助手交替）。
 * tool/error 折叠为一行 `[工具] {tool}`（正文/detail 不进包）；thinking/plan/system 跳过。
 * atKey 不在 messages 中返回 null（调用方不分叉）。
 */
export function buildForkContext(messages: ChatMessage[], atKey: string): string | null {
  const idx = messages.findIndex((m) => m.key === atKey);
  if (idx < 0) return null;
  const lines: string[] = [];
  for (const m of messages.slice(0, idx + 1)) {
    if (m.kind === 'user') lines.push(`用户：${m.text}`);
    else if (m.kind === 'assistant') lines.push(`助手：${m.text}`);
    else if (m.kind === 'tool' || m.kind === 'error') lines.push(`[工具] ${m.tool ?? ''}`.trimEnd());
  }
  const body = `${FORK_CONTEXT_MARKER}以下是此前对话的记录（用户/助手交替）：\n\n${lines.join('\n')}`;
  return body.length > FORK_CONTEXT_LIMIT ? body.slice(0, FORK_CONTEXT_LIMIT) + '\n…（已截断）' : body;
}

/** 会话侧栏展示：优先反映最新 Run 状态，避免任务 in_progress 与 Run 已成功不一致。 */
export function conversationLabel(
  item: WorkItem,
  runs: Record<string, { status: string }>,
): string {
  const run = item.latest_run_id ? runs[item.latest_run_id] : undefined;
  if (run) {
    // waiting_approval 先于 ACTIVE：它在 ACTIVE 集合里，但"需要你"必须盖过"在跑"
    if (run.status === 'waiting_approval') return '待审批';
    if (ACTIVE.has(run.status)) return '思考中…';
    if (run.status === 'succeeded') return '已回复';
    if (run.status === 'failed') return '失败';
  }
  if (item.phase === 'review') return '已回复';
  if (item.status === 'completed') return '已完成';
  if (item.status === 'todo') return '待开始';
  return item.status;
}

export { ACTIVE, TERMINAL };

/** 队列消息的幂等键：入队时刻唯一（时间戳+随机），drain 重试沿用同一键。 */
function newQueueKey(): string {
  return `q:${Date.now().toString(36)}:${crypto.randomUUID()}`;
}

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

/** 对话页 run 级提示（超时/手动停止等），key 为 runId。 */
export interface RunAlert {
  code: ChatErrorCode;
  message: string;
  at: string;
}

interface ChatStore {
  agentId: string | null;
  conversationId: string | null;
  /** 会话列表（该 agent 名下的 work items，最新在前）。 */
  conversations: WorkItem[];
  /** 当前会话的 run 列表（创建时间正序）。 */
  runs: ExecutionRun[];
  sending: boolean;
  /**
   * 当前会话的待发送队列（Codex 式：运行中入队，本轮成功后自动续发）。
   * 内存级不落盘：刷新即弃、切换会话即清——持久化需要后端队列契约，暂以简单优先。
   * 每条携 clientKey（入队时生成）：drain 重试时作 run 的实体级幂等键，防重复建轮。
   */
  queue: QueuedMessage[];
  /** 活动 run 停止请求进行中（防连点）。 */
  stoppingRunId: string | null;
  /** 前端触发的 run 级错误提示（超时/停止），按 runId 索引。 */
  runAlerts: Record<string, RunAlert>;
  /** createRun 已返回但 timeline 尚无 run.created 时的用户文案（runId → instruction）。 */
  pendingUsers: Record<string, string>;

  selectAgent: (id: string | null) => void;
  openConversation: (workItemId: string | null) => void;
  refreshConversations: () => Promise<void>;
  refreshRuns: () => Promise<void>;
  send: (text: string) => Promise<void>;
  enqueue: (text: string) => void;
  removeQueued: (index: number) => void;
  /** 出队首条开新轮（自动续发与手动「继续发送」共用；复用 sending 闸防重）。 */
  drainQueue: () => Promise<void>;
  /** 中断活动 run（对话页「停止」/首响超时）。 */
  stopActiveRun: (runId: string, code: Exclude<ChatErrorCode, 'stop_failed'>) => Promise<void>;
  /** 从指定消息分叉新会话：上下文包写入新 work item 的 description，首发时注入。 */
  forkConversation: (atMessageKey: string) => Promise<void>;
}

export const useChatStore = create<ChatStore>()((set, get) => {
  // 出队首发公共路径（send 的终态分支与 drainQueue 共用）：createRun + 订阅 + 各列表刷新。
  // 分叉会话首发：尚无 run 且 description 以分叉标记开头时，把上下文包拼进首条
  // instruction（description 原样保留在 work item 上不消费，仅本轮注入一次）。
  // clientKey 透传为 run 的实体级幂等键：同键重试由服务端查回既有 run（200 重放）。
  const startRun = async (conversationId: string, agentId: string, text: string, clientKey?: string): Promise<void> => {
    let instruction = text;
    if (get().runs.length === 0) {
      const conversation = get().conversations.find((c) => c.id === conversationId);
      if (conversation?.description.startsWith(FORK_CONTEXT_MARKER)) {
        instruction = `${conversation.description}\n\n【用户新指令】\n${text}`;
      }
    }
    const resp = await createRun(conversationId, {
      agent_profile_id: agentId,
      input: { instruction },
      ...(clientKey ? { client_key: clientKey } : {}),
    });
    set((s) => ({
      pendingUsers: { ...s.pendingUsers, [resp.run_id]: text.trim() },
    }));
    // 先刷新 run 列表再订阅，避免时间线已加载但 runIds 仍为空导致消息不渲染。
    await get().refreshRuns();
    useRunsStore.getState().watchRun(resp.run_id);
    await get().refreshConversations();
    await get().refreshRuns();
    await useTasksStore.getState().refresh();
  };

  return {
    agentId: null,
    conversationId: null,
    conversations: [],
    runs: [],
    sending: false,
    queue: [],
    stoppingRunId: null,
    runAlerts: {},
    pendingUsers: {},

    selectAgent: (id) => {
      set({ agentId: id, conversationId: null, runs: [], queue: [], runAlerts: {}, pendingUsers: {} });
      void get().refreshConversations();
    },

    openConversation: (workItemId) => {
      set({ conversationId: workItemId, runs: [], queue: [], runAlerts: {}, pendingUsers: {} });
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
      const trimmed = text.trim();
      const wsId = useWorkspaceStore.getState().workspace?.id;
      const agentId = get().agentId;
      if (!wsId || !agentId || !trimmed) return;

      const runsStore = useRunsStore.getState();
      const latest = get().runs[get().runs.length - 1];
      const latestSnapshot = currentRunSnapshot(latest, runsStore.runs);
      // 活动 Run 或有首发在途（sending，含 drain）：入队等本轮完成后自动续发，
      // 不再走 sendRunInput 的 steering 追加。
      if ((latestSnapshot && !TERMINAL.has(latestSnapshot.status)) || get().sending) {
        get().enqueue(trimmed);
        return;
      }
      set({ sending: true });
      let conversationId = get().conversationId;
      try {
        if (!conversationId) {
          // 首发消息：建会话（work item）+ 建 run；任务状态由控制平面推进。
          const title = trimmed.slice(0, 24) + (trimmed.length > 24 ? '…' : '');
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
        // 队列非空：先把本条入队再出队首条 createRun（FIFO——队头先发，本条排队尾）。
        const pending = [...get().queue, { text: trimmed, clientKey: newQueueKey() }];
        const first = pending.shift();
        if (!first) return; // 不可达（trimmed 非空）：类型收窄
        set({ queue: pending });
        await startRun(conversationId, agentId, first.text, first.clientKey);
      } catch (err) {
        toast.error(err instanceof Error ? err.message : '发送失败');
      } finally {
        set({ sending: false });
      }
    },

    enqueue: (text) => set((s) => ({ queue: [...s.queue, { text, clientKey: newQueueKey() }] })),

    removeQueued: (index) => set((s) => ({ queue: s.queue.filter((_, i) => i !== index) })),

    drainQueue: async () => {
      const wsId = useWorkspaceStore.getState().workspace?.id;
      const agentId = get().agentId;
      const conversationId = get().conversationId;
      const first = get().queue[0];
      if (!wsId || !agentId || !conversationId || !first || get().sending) return;
      set({ sending: true, queue: get().queue.slice(1) });
      try {
        await startRun(conversationId, agentId, first.text, first.clientKey);
      } catch (err) {
        toast.error(err instanceof Error ? err.message : '发送失败');
      } finally {
        set({ sending: false });
      }
    },

    stopActiveRun: async (runId, code) => {
      if (get().stoppingRunId === runId) return;
      logChatError(code, { runId, conversationId: get().conversationId });
      set({ stoppingRunId: runId });
      const message = chatErrorMessage(code);
      try {
        await interruptRun(runId);
        set((s) => ({
          runAlerts: {
            ...s.runAlerts,
            [runId]: { code, message, at: new Date().toISOString() },
          },
        }));
        if (code === 'user_stopped') toast.info(message);
        else toast.error(message);
        await useRunsStore.getState().fetchRun(runId);
        await get().refreshConversations();
      } catch (err) {
        logChatError('stop_failed', {
          runId,
          conversationId: get().conversationId,
          cause: err instanceof Error ? err.message : String(err),
          apiCode: err instanceof ApiError ? err.code : undefined,
        });
        toast.error(chatErrorMessage('stop_failed'));
      } finally {
        set((s) => (s.stoppingRunId === runId ? { stoppingRunId: null } : {}));
      }
    },

    forkConversation: async (atMessageKey) => {
      const wsId = useWorkspaceStore.getState().workspace?.id;
      const agentId = get().agentId;
      const source = get().conversations.find((c) => c.id === get().conversationId);
      if (!wsId || !agentId || !source) return;
      const context = buildForkContext(
        buildMessages(get().runs.map((r) => r.id), useRunsStore.getState().timelines),
        atMessageKey,
      );
      if (!context) return; // 锚点消息不在当前投影里（时间线未加载等）：不分叉
      try {
        const wi = await createWorkItem(wsId, {
          title: `${source.title}（分叉）`,
          parent_id: source.id,
          agent_profile_id: agentId,
          description: context,
          // 同一锚点重复分叉（双击/重试）查回既有分叉会话而非重复建卡。
          client_key: `fork:${source.id}:${atMessageKey}`,
        });
        get().openConversation(wi.id);
        await get().refreshConversations();
      } catch (err) {
        toast.error(err instanceof Error ? err.message : '分叉失败');
      }
    },
  };
});
