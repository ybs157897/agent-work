import { aggregateRunStream, buildMessages, isRunLive, type RunStreamParts, type ChatMessage } from '../stores/chat.store';
import { buildTranscriptSegments } from './chronological-transcript';
import { projectWorkActivityTimeline, type PresentedTranscriptSegment } from './work-activity-timeline';
import type { TimelineEntry } from '../stores/runs.store';
import type { GroupActivityOptions } from '../components/chat/tool-card';

export interface AgentTranscriptProjection {
  segments: PresentedTranscriptSegment[];
  messages: ChatMessage[];
  liveStream: RunStreamParts;
  liveActive: boolean;
}

export function buildAgentTranscriptProjection({
  runId, agentId = 'main', runStatus, timeline, showReasoning = true, toolGrouping, timing,
}: {
  runId: string;
  agentId?: string;
  runStatus?: string;
  timeline: TimelineEntry[];
  showReasoning?: boolean;
  toolGrouping?: GroupActivityOptions;
  timing?: { createdAt?: string; updatedAt?: string };
}): AgentTranscriptProjection {
  const timelines = { [runId]: timeline };
  const messages = buildMessages([runId], timelines, agentId);
  const liveStream = aggregateRunStream(timeline, agentId);
  const liveActive = runStatus ? isRunLive(runStatus) : false;
  const segments = buildTranscriptSegments(messages, {
    runStatuses: { [runId]: runStatus ?? 'running' },
    liveRunId: runId,
    liveStream,
    liveRunActive: liveActive,
    rawMessages: messages,
    showReasoning,
    toolGrouping,
  });
  return {
    messages,
    liveStream,
    liveActive,
    segments: projectWorkActivityTimeline(segments, {
      runStatuses: { [runId]: runStatus ?? 'running' },
      timingByRun: { [runId]: timing ?? {} },
    }),
  };
}
