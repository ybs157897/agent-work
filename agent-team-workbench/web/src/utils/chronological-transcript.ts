import type { ChatMessage, RunStreamParts } from '../stores/chat.store';
import { ACTIVE } from '../stores/chat.store';
import type { ApprovalRequest } from '../api/types';
import type { TimelineEntry } from '../stores/runs.store';
import { aggregateTurnDiff } from './turn-diff';
import type { ActivitySegment, GroupActivityOptions } from '../components/chat/tool-card';
import { groupActivity } from '../components/chat/tool-card';

export type TranscriptSegment =
  | { kind: 'user'; msg: ChatMessage }
  | { kind: 'assistant'; msg: ChatMessage; streaming?: boolean; renderKey?: string }
  | { kind: 'thinking'; msg: ChatMessage; streaming?: boolean; renderKey?: string }
  | { kind: 'meta'; msg: ChatMessage }
  | { kind: 'swarm'; runId: string; msg: ChatMessage }
  | { kind: 'subagent'; runId: string; msg: ChatMessage }
  | { kind: 'activity'; runId: string; items: ChatMessage[] }
  | { kind: 'thinking-placeholder'; runId: string }
  | { kind: 'turn-diff'; runId: string; text: string }
  | { kind: 'approval'; approval: ApprovalRequest };

export interface BuildTranscriptSegmentsOptions {
  liveRunId?: string;
  liveStream?: RunStreamParts;
  /** 最新 run 是否仍在执行（含 waiting_approval） */
  liveRunActive?: boolean;
  runStatuses?: Readonly<Record<string, string>>;
  hasPendingApproval?: boolean;
  /** runId → 已发送但 timeline 尚未出现 run.created 的用户文案 */
  pendingUsers?: Readonly<Record<string, string>>;
  rawMessages?: readonly ChatMessage[];
  /** ZCode messageStreamShowReasoning: false 时每个 run 只保留第一段 reasoning。 */
  showReasoning?: boolean;
  /** ZCode 工具展示层分组开关；不改变底层 tool row。 */
  toolGrouping?: GroupActivityOptions;
}

export interface OutputLoadingState {
  runActive: boolean;
  hasPendingApproval?: boolean;
  hasRunningTool?: boolean;
  streamingReasoning?: boolean;
  /** Reasoning is still the latest phase, but no delta arrived recently. */
  reasoningIdle?: boolean;
  streamingAnswer?: boolean;
}

/** Whether the small post-output loader belongs at the end of the live run. */
export function shouldShowOutputLoading(state: OutputLoadingState): boolean {
  if (!state.runActive || state.hasPendingApproval || state.hasRunningTool) {
    return false;
  }
  if (state.streamingReasoning && !state.reasoningIdle) return false;
  return Boolean(state.streamingAnswer || state.reasoningIdle || (!state.streamingReasoning && !state.streamingAnswer));
}

function isTranscriptVisible(m: ChatMessage): boolean {
  if (m.sessionMeta) return false;
  if (m.kind === 'plan') return false;
  if (m.kind === 'assistant' && m.itemType === 'plan') return false;
  return true;
}

function filterVisibleMessages(
  messages: readonly ChatMessage[],
  showReasoning: boolean | undefined,
): ChatMessage[] {
  const firstThinkingRuns = new Set<string>();
  return messages.filter((message) => {
    if (!isTranscriptVisible(message)) return false;
    if (showReasoning !== false || message.kind !== 'thinking') return true;
    if (firstThinkingRuns.has(message.runId)) return false;
    firstThinkingRuns.add(message.runId);
    return true;
  });
}

function hasRunningTools(items: readonly ChatMessage[]): boolean {
  return items.some((m) => m.toolStatus === 'running' || m.swarm?.status === 'running');
}

/** timeline 已有 run.created 但 buildMessages 尚未产出 user 行时的兜底（竞态窗口）。 */
export function supplementUserFromTimeline(
  messages: readonly ChatMessage[],
  runIds: readonly string[],
  timelines: Readonly<Record<string, TimelineEntry[]>>,
): ChatMessage[] {
  const out = [...messages];
  for (const runId of runIds) {
    if (out.some((m) => m.runId === runId && m.kind === 'user')) continue;
    for (const e of timelines[runId] ?? []) {
      if (e.type !== 'run.created') continue;
      const instruction = typeof e.data?.instruction === 'string' ? e.data.instruction.trim() : '';
      if (!instruction) break;
      const msg: ChatMessage = {
        key: `${runId}-user-created`,
        runId,
        kind: 'user',
        text: instruction,
        at: e.occurred_at,
      };
      const firstIdx = out.findIndex((m) => m.runId === runId);
      if (firstIdx >= 0) out.splice(firstIdx, 0, msg);
      else out.push(msg);
      break;
    }
  }
  return out;
}

export function injectPendingUsers(
  messages: readonly ChatMessage[],
  pendingUsers?: Readonly<Record<string, string>>,
): ChatMessage[] {
  if (!pendingUsers || !Object.keys(pendingUsers).length) return [...messages];
  const out = [...messages];
  for (const [runId, text] of Object.entries(pendingUsers)) {
    const trimmed = text.trim();
    if (!trimmed) continue;
    if (out.some((m) => m.runId === runId && m.kind === 'user')) continue;
    const firstIdx = out.findIndex((m) => m.runId === runId);
    const at =
      firstIdx >= 0
        ? out[firstIdx].at
        : new Date().toISOString();
    const msg: ChatMessage = {
      key: `${runId}-user-pending`,
      runId,
      kind: 'user',
      text: trimmed,
      at,
    };
    if (firstIdx >= 0) out.splice(firstIdx, 0, msg);
    else out.push(msg);
  }
  return out;
}

function segmentFromSingle(msg: ChatMessage): TranscriptSegment | undefined {
  switch (msg.kind) {
    case 'user':
      return { kind: 'user', msg };
    case 'assistant':
      return { kind: 'assistant', msg };
    case 'thinking':
      return { kind: 'thinking', msg };
    case 'swarm':
      return msg.swarm ? { kind: 'swarm', runId: msg.runId, msg } : undefined;
    case 'subagent':
      return msg.childAgent ? { kind: 'subagent', runId: msg.runId, msg } : undefined;
    case 'system':
    case 'error':
      return { kind: 'meta', msg };
    default:
      return undefined;
  }
}

function segmentRunId(segment: TranscriptSegment): string | undefined {
  if (
    segment.kind === 'user'
    || segment.kind === 'assistant'
    || segment.kind === 'thinking'
    || segment.kind === 'meta'
  ) return segment.msg.runId;
  if (
    segment.kind === 'activity'
    || segment.kind === 'swarm'
    || segment.kind === 'subagent'
    || segment.kind === 'thinking-placeholder'
    || segment.kind === 'turn-diff'
  ) return segment.runId;
  return undefined;
}

function appendTurnDiffs(
  segments: TranscriptSegment[],
  messages: readonly ChatMessage[],
  opts: BuildTranscriptSegmentsOptions,
): TranscriptSegment[] {
  const toolsByRun = new Map<string, ChatMessage[]>();
  for (const m of messages) {
    if (m.toolStatus === undefined) continue;
    const list = toolsByRun.get(m.runId) ?? [];
    list.push(m);
    toolsByRun.set(m.runId, list);
  }

  const out: TranscriptSegment[] = [];
  const inserted = new Set<string>();
  const lastSegmentByRun = new Map<string, number>();
  segments.forEach((segment, index) => {
    const runId = segmentRunId(segment);
    if (runId) lastSegmentByRun.set(runId, index);
  });

  for (let index = 0; index < segments.length; index += 1) {
    const seg = segments[index];
    out.push(seg);
    const runId = segmentRunId(seg);
    if (!runId || inserted.has(runId)) continue;
    if (lastSegmentByRun.get(runId) !== index) continue;
    if (opts.liveRunActive && opts.liveRunId === runId) continue;

    const isRunEnd =
      seg.kind === 'assistant' ||
      seg.kind === 'meta' ||
      (seg.kind === 'activity' && !hasRunningTools(seg.items));
    if (!isRunEnd) continue;

    const diff = aggregateTurnDiff(toolsByRun.get(runId) ?? []);
    if (diff) {
      out.push({ kind: 'turn-diff', runId, text: diff });
      inserted.add(runId);
    }
  }
  return out;
}

function lastThinkingForRun(
  segments: readonly TranscriptSegment[],
  runId: string,
): ChatMessage | undefined {
  for (let i = segments.length - 1; i >= 0; i--) {
    const s = segments[i];
    if (s.kind === 'thinking' && s.msg.runId === runId) return s.msg;
  }
  return undefined;
}

function answerTailDraft(messages: readonly ChatMessage[] | undefined, runId: string): string {
  if (!messages) return '';
  for (let i = messages.length - 1; i >= 0; i--) {
    const m = messages[i];
    if (m.runId === runId && m.kind === 'assistant' && m.key.endsWith('-answer-tail')) {
      return m.text;
    }
  }
  return '';
}

function appendLiveTail(
  segments: TranscriptSegment[],
  opts: BuildTranscriptSegmentsOptions,
): TranscriptSegment[] {
  const { liveRunId, liveStream, liveRunActive, runStatuses, hasPendingApproval, rawMessages } = opts;
  if (!liveRunId || !liveRunActive) return segments;

  const out = [...segments];
  const status = runStatuses?.[liveRunId];
  const runActive = status !== undefined && ACTIVE.has(status);
  // 仅用 aggregateRunStream（message.completed 后重置）；不回退 thinking-tail，避免双源字数跳变。
  const reasoningSource = liveStream?.reasoning ?? '';
  const reasoning = reasoningSource.trim() ? reasoningSource : '';
  const answerSource = liveStream?.answerDraft ?? '';
  const answerFallback = answerTailDraft(rawMessages, liveRunId);
  const answerDraft = answerSource.trim()
    ? answerSource
    : answerFallback.trim()
      ? answerFallback
      : '';
  // 正文一旦开始，前置 reasoning 阶段已经落定：仍保留同一 stable key，
  // 但不再显示为「正在思考」，避免过程正文被视觉上误归入思考段。
  const reasoningStreaming = !answerDraft;

  const runTools = segments.flatMap((s) =>
    s.kind === 'activity' && s.runId === liveRunId ? s.items : [],
  );
  const running = hasRunningTools(runTools) || segments.some((segment) =>
    ((segment.kind === 'swarm' && segment.msg.swarm?.status === 'running')
      || (segment.kind === 'subagent' && segment.msg.childAgent?.status === 'running'))
    && segment.runId === liveRunId,
  );

  const reasoningVisible = opts.showReasoning !== false
    || !out.some((segment) => segment.kind === 'thinking' && segment.msg.runId === liveRunId);
  if (reasoning && reasoningVisible) {
    const last = lastThinkingForRun(out, liveRunId);
    const duplicateInline =
      last !== undefined &&
      last.text === reasoning &&
      !last.key.endsWith('-live-reasoning');
    if (!duplicateInline) {
      const ordinal = out.filter((segment) =>
        segment.kind === 'thinking' && segment.msg.runId === liveRunId,
      ).length;
      out.push({
        kind: 'thinking',
        msg: {
          key: `${liveRunId}-live-reasoning`,
          runId: liveRunId,
          kind: 'thinking',
          text: reasoning,
          at: new Date().toISOString(),
          ...(liveStream?.phaseStartedAt ? { startedAt: liveStream.phaseStartedAt } : {}),
          ...(liveStream?.phaseId ? { phaseId: liveStream.phaseId } : {}),
        },
        streaming: reasoningStreaming,
        renderKey: liveStream?.phaseId
          ? `thinking:${liveRunId}:${liveStream.phaseId}`
          : `thinking:${liveRunId}:${ordinal}`,
      });
    }
  }

  if (answerDraft) {
    const ordinal = out.filter((segment) =>
      segment.kind === 'assistant' && segment.msg.runId === liveRunId,
    ).length;
    out.push({
      kind: 'assistant',
      msg: {
        key: `${liveRunId}-live-answer`,
        runId: liveRunId,
        kind: 'assistant',
        text: answerDraft,
        at: new Date().toISOString(),
      },
      streaming: true,
      renderKey: `assistant:${liveRunId}:${ordinal}`,
    });
  }

  if (shouldShowOutputLoading({
    runActive,
    hasPendingApproval,
    hasRunningTool: running,
    streamingReasoning: reasoningStreaming && Boolean(reasoning),
    streamingAnswer: Boolean(answerDraft),
  })) {
    out.push({ kind: 'thinking-placeholder', runId: liveRunId });
  }

  return out;
}

/**
 * 按时间线顺序构建 transcript 段（用户/工具/思考/正文保持 SSE 到达顺序；
 * 仅连续同 run 工具行折叠为 activity 组）。
 */
export function buildTranscriptSegments(
  messages: readonly ChatMessage[],
  opts: BuildTranscriptSegmentsOptions = {},
): TranscriptSegment[] {
  const visible = filterVisibleMessages(messages, opts.showReasoning);
  const segments: TranscriptSegment[] = [];
  const assistantOrdinals = new Map<string, number>();
  const thinkingOrdinals = new Map<string, number>();

  for (const seg of groupActivity(visible, opts.toolGrouping) as ActivitySegment[]) {
    if (seg.kind === 'activity') {
      segments.push({ kind: 'activity', runId: seg.runId, items: seg.items });
      continue;
    }
    const mapped = segmentFromSingle(seg.item);
    if (mapped) {
      if (mapped.kind === 'assistant') {
        const ordinal = assistantOrdinals.get(mapped.msg.runId) ?? 0;
        mapped.renderKey = `assistant:${mapped.msg.runId}:${ordinal}`;
        assistantOrdinals.set(mapped.msg.runId, ordinal + 1);
      }
      if (mapped.kind === 'thinking') {
        const ordinal = thinkingOrdinals.get(mapped.msg.runId) ?? 0;
        mapped.renderKey = mapped.msg.phaseId
          ? `thinking:${mapped.msg.runId}:${mapped.msg.phaseId}`
          : `thinking:${mapped.msg.runId}:${ordinal}`;
        thinkingOrdinals.set(mapped.msg.runId, ordinal + 1);
      }
      segments.push(mapped);
    }
  }

  return appendLiveTail(appendTurnDiffs(segments, messages, opts), opts);
}
