import type { ChatMessage, RunStreamParts } from '../stores/chat.store';
import { ACTIVE } from '../stores/chat.store';
import type { ApprovalRequest } from '../api/types';
import type { TimelineEntry } from '../stores/runs.store';
import { aggregateTurnDiff } from './turn-diff';
import type { ActivitySegment } from '../components/chat/tool-card';
import { groupActivity } from '../components/chat/tool-card';

export type TranscriptSegment =
  | { kind: 'user'; msg: ChatMessage }
  | { kind: 'assistant'; msg: ChatMessage; streaming?: boolean }
  | { kind: 'thinking'; msg: ChatMessage; streaming?: boolean }
  | { kind: 'meta'; msg: ChatMessage }
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
}

function isTranscriptVisible(m: ChatMessage): boolean {
  if (m.sessionMeta) return false;
  if (m.kind === 'plan') return false;
  if (m.kind === 'assistant' && m.itemType === 'plan') return false;
  return true;
}

function hasRunningTools(items: readonly ChatMessage[]): boolean {
  return items.some((m) => m.toolStatus === 'running');
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
    case 'system':
    case 'error':
      return { kind: 'meta', msg };
    default:
      return undefined;
  }
}

function appendTurnDiffs(segments: TranscriptSegment[], messages: readonly ChatMessage[]): TranscriptSegment[] {
  const toolsByRun = new Map<string, ChatMessage[]>();
  for (const m of messages) {
    if (m.toolStatus === undefined) continue;
    const list = toolsByRun.get(m.runId) ?? [];
    list.push(m);
    toolsByRun.set(m.runId, list);
  }

  const out: TranscriptSegment[] = [];
  const inserted = new Set<string>();

  for (const seg of segments) {
    out.push(seg);
    const runId =
      seg.kind === 'user' ||
      seg.kind === 'assistant' ||
      seg.kind === 'thinking' ||
      seg.kind === 'meta'
        ? seg.msg.runId
        : seg.kind === 'activity' ||
            seg.kind === 'thinking-placeholder' ||
            seg.kind === 'turn-diff'
          ? seg.runId
          : undefined;
    if (!runId || inserted.has(runId)) continue;

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
  const reasoning = liveStream?.reasoning?.trim() ?? '';
  const answerDraft = liveStream?.answerDraft?.trim() || answerTailDraft(rawMessages, liveRunId);

  const runTools = segments.flatMap((s) =>
    s.kind === 'activity' && s.runId === liveRunId ? s.items : [],
  );
  const running = hasRunningTools(runTools);

  if (reasoning) {
    const last = lastThinkingForRun(out, liveRunId);
    const duplicateInline =
      last !== undefined &&
      last.text === reasoning &&
      !last.key.endsWith('-live-reasoning');
    if (!duplicateInline) {
      out.push({
        kind: 'thinking',
        msg: {
          key: `${liveRunId}-live-reasoning`,
          runId: liveRunId,
          kind: 'thinking',
          text: reasoning,
          at: new Date().toISOString(),
        },
        streaming: true,
      });
    }
  }

  if (answerDraft) {
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
    });
  }

  if (runActive && !answerDraft && !running && !hasPendingApproval && !reasoning) {
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
  const visible = messages.filter(isTranscriptVisible);
  const segments: TranscriptSegment[] = [];

  for (const seg of groupActivity(visible) as ActivitySegment[]) {
    if (seg.kind === 'activity') {
      segments.push({ kind: 'activity', runId: seg.runId, items: seg.items });
      continue;
    }
    const mapped = segmentFromSingle(seg.item);
    if (mapped) segments.push(mapped);
  }

  return appendLiveTail(appendTurnDiffs(segments, messages), opts);
}
