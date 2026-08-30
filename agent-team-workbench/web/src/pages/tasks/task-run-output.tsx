import { useEffect, useMemo } from 'react';
import type { DispatchRun } from '../../api/types';
import { AgentOutput } from '../../components/chat/agent-output';
import { MessageActions } from '../../components/chat/message-actions';
import { OutputLoadingIndicator, WorkActivityTimeline } from '../../components/chat/work-activity-timeline';
import { ACTIVE, aggregateRunStream, buildMessages } from '../../stores/chat.store';
import { useRunsStore, type TimelineEntry } from '../../stores/runs.store';
import { buildTranscriptSegments } from '../../utils/chronological-transcript';
import {
  presentedTranscriptSegmentKey,
  projectWorkActivityTimeline,
  type PresentedTranscriptSegment,
} from '../../utils/work-activity-timeline';

type TaskVisibleSegment = Extract<PresentedTranscriptSegment, { kind: 'assistant' | 'work-timeline' | 'thinking-placeholder' }>;

/**
 * Task 侧的单个 Agent/run 正文投影。
 *
 * 它只订阅指定 run 的事件历史，不写入 Chat store，也不提供 Chat 的
 * 分叉/钉决策操作；正文本体和 Chat 继续共享 AgentOutput。
 */
export function TaskRunOutput({ run, agentName }: { run: DispatchRun; agentName: string }) {
  const snapshot = useRunsStore((state) => state.runs[run.id]);
  const timeline = useRunsStore((state) => state.timelines[run.id]);
  const approvals = useRunsStore((state) => state.approvals[run.id] ?? []);
  const watchRun = useRunsStore((state) => state.watchRun);
  const unwatchRun = useRunsStore((state) => state.unwatchRun);

  useEffect(() => {
    watchRun(run.id);
    return () => unwatchRun(run.id);
  }, [run.id, unwatchRun, watchRun]);

  const status = snapshot?.status ?? run.status;
  const runStatuses = useMemo(() => ({ [run.id]: status }), [run.id, status]);
  const timelineByRun = useMemo<Record<string, TimelineEntry[]>>(
    () => ({ [run.id]: timeline ?? [] }),
    [run.id, timeline],
  );
  const runStream = useMemo(() => aggregateRunStream(timeline ?? []), [timeline]);
  const runActive = ACTIVE.has(status);
  const messages = useMemo(
    () => buildMessages([run.id], timelineByRun),
    [run.id, timelineByRun],
  );
  const transcript = useMemo(() => {
    const raw = buildTranscriptSegments(messages, {
      runStatuses,
      liveRunId: runActive ? run.id : undefined,
      liveStream: runStream,
      liveRunActive: runActive,
      hasPendingApproval: approvals.some((approval) => approval.status === 'pending'),
      showReasoning: true,
    });
    return projectWorkActivityTimeline(raw, {
      runStatuses,
      timingByRun: {
        [run.id]: {
          createdAt: snapshot?.created_at,
          updatedAt: snapshot?.updated_at,
        },
      },
    });
  }, [approvals, messages, run.id, runActive, runStatuses, runStream, snapshot?.created_at, snapshot?.updated_at]);

  const visible = transcript.filter((segment): segment is TaskVisibleSegment =>
    segment.kind === 'assistant' || segment.kind === 'work-timeline' || segment.kind === 'thinking-placeholder');

  return (
    <div className="mt-snug space-y-3 border-t border-border-subtle pt-snug" data-task-run-output={run.id}>
      {timeline === undefined ? (
        <p className="text-caption text-text-tertiary">正文加载中…</p>
      ) : visible.length === 0 ? (
        <p className="text-caption text-text-tertiary">该 Agent 尚未产生可展示正文</p>
      ) : (
        visible.map((segment) => <TaskTranscriptSegment key={presentedTranscriptSegmentKey(segment)} segment={segment} agentName={agentName} />)
      )}
    </div>
  );
}

function TaskTranscriptSegment({
  segment,
  agentName,
}: {
  segment: TaskVisibleSegment;
  agentName: string;
}) {
  if (segment.kind === 'assistant') {
    return (
      <article className="chat-assistant-turn" aria-label={`${agentName} 的任务正文`}>
        <AgentOutput
          text={segment.msg.text}
          streaming={segment.streaming}
          contentBlocks={segment.msg.contentBlocks}
          runId={segment.msg.runId}
          messageId={segment.renderKey ?? segment.msg.key}
        />
        {!segment.streaming && segment.msg.text && (
          <MessageActions text={segment.msg.text} className="mt-2" />
        )}
      </article>
    );
  }
  if (segment.kind === 'work-timeline') return <WorkActivityTimeline segment={segment} />;
  return <OutputLoadingIndicator />;
}
