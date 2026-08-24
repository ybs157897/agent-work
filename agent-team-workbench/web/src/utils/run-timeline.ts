import type { TimelineEntry } from '../stores/runs.store';

/** Run 时间线上视为「已有可见进展」的事件（非纯 run/usage 心跳）。 */
const VISIBLE_PROGRESS_TYPES = new Set([
  'message.delta',
  'message.completed',
  'tool.started',
  'tool.progress',
  'tool.completed',
  'tool.failed',
  'run.plan_updated',
]);

export function runHasVisibleOutput(entries: TimelineEntry[] | undefined): boolean {
  if (!entries?.length) return false;
  return entries.some((e) => VISIBLE_PROGRESS_TYPES.has(e.type));
}
