import type { ChatMessage } from '../stores/chat.store';
import type { ReasoningSlot } from './transcript-layout';
import { formatWorkDurationFromRange } from './work-duration';

/** 活动组 worked-for：工具 started/completed 区间；无工具时用 assistant 落定时间作上界。 */
export function activityWorkedFor(
  items: readonly ChatMessage[],
  reasoning?: ReasoningSlot,
  assistantAt?: string,
): string | null {
  const starts = items.flatMap((m) => (m.startedAt ? [m.startedAt] : []));
  const ends = items.flatMap((m) => (m.completedAt ? [m.completedAt] : []));

  if (assistantAt && !reasoning?.streaming) {
    ends.push(assistantAt);
  }

  const fromTools = formatWorkDurationFromRange(starts, ends);
  if (fromTools) return fromTools;

  if (reasoning?.at && assistantAt && !reasoning.streaming) {
    return formatWorkDurationFromRange([reasoning.at], [assistantAt]);
  }

  return null;
}
