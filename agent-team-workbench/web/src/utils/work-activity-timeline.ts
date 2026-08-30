import type { TranscriptSegment } from './chronological-transcript';

type ThinkingSegment = Extract<TranscriptSegment, { kind: 'thinking' }>;
type ActivitySegment = Extract<TranscriptSegment, { kind: 'activity' }>;
type SwarmSegment = Extract<TranscriptSegment, { kind: 'swarm' }>;
type SubagentSegment = Extract<TranscriptSegment, { kind: 'subagent' }>;
type PlaceholderSegment = Extract<TranscriptSegment, { kind: 'thinking-placeholder' }>;
type AssistantSegment = Extract<TranscriptSegment, { kind: 'assistant' }>;
type MetaSegment = Extract<TranscriptSegment, { kind: 'meta' }>;
type ApprovalSegment = Extract<TranscriptSegment, { kind: 'approval' }>;

/** One ordered item inside a run's work log. */
export type WorkActivityItem =
  | ThinkingSegment
  | ActivitySegment
  | SwarmSegment
  | SubagentSegment
  | PlaceholderSegment
  | AssistantSegment
  | MetaSegment
  | ApprovalSegment;

export type PresentedTranscriptSegment =
  | Extract<TranscriptSegment, { kind: 'user' }>
  | {
      kind: 'work-timeline';
      runId: string;
      status?: string;
      createdAt: string;
      updatedAt: string;
      items: WorkActivityItem[];
      metadata?: unknown;
    }
  | AssistantSegment
  | PlaceholderSegment
  | Extract<TranscriptSegment, { kind: 'turn-diff' }>;

export type WorkTimelineSegment = Extract<PresentedTranscriptSegment, { kind: 'work-timeline' }>;

export interface WorkActivityTimelineOptions {
  /** Snapshot status by run. Unknown runs intentionally remain status-less. */
  runStatuses?: Readonly<Record<string, string | undefined>>;
  /** Canonical run timestamps. Event timestamps are only a defensive fallback. */
  timingByRun?: Readonly<Record<string, { createdAt?: string; updatedAt?: string } | undefined>>;
  /** Caller-owned metadata (agent, model, title, etc.) copied to each run timeline. */
  metadataByRun?: Readonly<Record<string, unknown>>;
}

function segmentRunId(segment: TranscriptSegment): string | undefined {
  if (segment.kind === 'user' || segment.kind === 'assistant' || segment.kind === 'thinking' || segment.kind === 'meta') {
    return segment.msg.runId;
  }
  if (segment.kind === 'activity' || segment.kind === 'swarm' || segment.kind === 'subagent' || segment.kind === 'thinking-placeholder' || segment.kind === 'turn-diff') {
    return segment.runId;
  }
  if (segment.kind === 'approval') return segment.approval.run_id;
  return undefined;
}

function segmentAt(segment: TranscriptSegment): string | undefined {
  if (segment.kind === 'user' || segment.kind === 'assistant' || segment.kind === 'thinking' || segment.kind === 'meta') {
    return segment.msg.at;
  }
  if (segment.kind === 'turn-diff') return undefined;
  if (segment.kind === 'swarm') return segment.msg.at;
  if (segment.kind === 'subagent') return segment.msg.at;
  if (segment.kind === 'approval') return segment.approval.resolved_at ?? undefined;
  if (segment.kind === 'activity') {
    return segment.items.reduce<string | undefined>((value, item) => value ?? item.at, undefined);
  }
  return undefined;
}

function isTimelineItem(segment: TranscriptSegment): segment is Exclude<TranscriptSegment, { kind: 'user' | 'turn-diff' }> {
  return segment.kind !== 'user' && segment.kind !== 'turn-diff';
}

function asWorkItem(segment: Exclude<TranscriptSegment, { kind: 'user' | 'turn-diff' }>): WorkActivityItem {
  return segment;
}

function minTimestamp(values: string[]): string {
  return values.sort()[0] ?? '';
}

function maxTimestamp(values: string[]): string {
  return values.sort().at(-1) ?? '';
}

/**
 * Group the transient work of each run into one ordered timeline.
 *
 * User messages, the live/final answer, its output loader and turn diffs stay
 * at the outer transcript level. Settled interim output remains in the run
 * timeline, so later reasoning/tool phases preserve the source event order.
 */
export function projectWorkActivityTimeline(
  segments: readonly TranscriptSegment[],
  options: WorkActivityTimelineOptions = {},
): PresentedTranscriptSegment[] {
  const outerAssistantIndex = new Map<string, number>();
  for (let index = 0; index < segments.length; index += 1) {
    const segment = segments[index];
    if (segment.kind !== 'assistant') continue;
    const runId = segment.msg.runId;
    // The live final draft belongs in the normal answer column immediately.
    // If a later tool starts, that draft becomes a settled interim item on the
    // next projection and naturally returns to the ordered work timeline.
    if (segment.streaming || options.runStatuses?.[runId] === 'succeeded') {
      outerAssistantIndex.set(runId, index);
    }
  }

  const timelineItems = new Map<string, { item: WorkActivityItem; at?: string }[]>();
  const timelinePlacement = new Map<string, number>();
  const timelineTimes = new Map<string, string[]>();

  const ensureTimeline = (runId: string, index: number) => {
    if (!timelinePlacement.has(runId)) timelinePlacement.set(runId, index);
  };

  for (let index = 0; index < segments.length; index += 1) {
    const segment = segments[index];
    if (segment.kind === 'user' || segment.kind === 'turn-diff' || segment.kind === 'thinking-placeholder') {
      continue;
    }
    const runId = segmentRunId(segment);
    if (!runId || !isTimelineItem(segment)) continue;
    if (segment.kind === 'assistant' && outerAssistantIndex.get(runId) === index) {
      if (options.runStatuses?.[runId] !== undefined) {
        ensureTimeline(runId, index);
        if (!timelineTimes.has(runId)) timelineTimes.set(runId, [segment.msg.at]);
      }
      continue;
    }
    ensureTimeline(runId, index);
    const list = timelineItems.get(runId) ?? [];
    list.push({ item: asWorkItem(segment), at: segmentAt(segment) });
    timelineItems.set(runId, list);
    const at = segmentAt(segment);
    if (at) timelineTimes.set(runId, [...(timelineTimes.get(runId) ?? []), at]);
  }

  // Emit each run's timeline at its first work item. A run with only an
  // optimistic user has no status or work item and therefore produces no phantom row.
  const emitted = new Set<string>();
  const result: PresentedTranscriptSegment[] = [];
  for (let index = 0; index < segments.length; index += 1) {
    const segment = segments[index];
    const runId = segmentRunId(segment);
    if (runId && !emitted.has(runId) && timelinePlacement.get(runId) === index) {
      const items = timelineItems.get(runId) ?? [];
      const timestamps = timelineTimes.get(runId) ?? [];
      const timing = options.timingByRun?.[runId];
      result.push({
        kind: 'work-timeline',
        runId,
        status: options.runStatuses?.[runId],
        createdAt: timing?.createdAt ?? minTimestamp([...timestamps]),
        updatedAt: timing?.updatedAt ?? maxTimestamp([...timestamps]),
        items: items.map((entry) => entry.item),
        ...(options.metadataByRun?.[runId] !== undefined ? { metadata: options.metadataByRun[runId] } : {}),
      });
      emitted.add(runId);
    }
    if (segment.kind === 'user' || segment.kind === 'turn-diff' || segment.kind === 'thinking-placeholder') {
      result.push(segment);
    } else if (segment.kind === 'assistant' && outerAssistantIndex.get(runId ?? '') === index) {
      result.push(segment);
    }
  }
  return result;
}

export function presentedTranscriptSegmentKey(segment: PresentedTranscriptSegment): string {
  if (segment.kind === 'work-timeline') return `work-timeline:${segment.runId}`;
  if (segment.kind === 'thinking-placeholder') return `output-loading:${segment.runId}`;
  if (segment.kind === 'user' || segment.kind === 'assistant') return segment.kind === 'user' ? segment.msg.key : segment.renderKey ?? segment.msg.key;
  return `turn-diff:${segment.runId}`;
}
