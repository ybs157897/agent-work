import { Bot, ChevronDown, ChevronRight } from 'lucide-react';
import { useEffect, useState } from 'react';
import type { DispatchCard, DispatchRun } from '../../api/types';
import { Avatar } from '../../components/avatar';
import { runStatusColor, runStatusText } from '../../components/status';
import { Button, EmptyState, StatusPill } from '../../components/ui';
import { useDispatchesStore } from '../../stores/dispatches.store';
import { useRunsStore } from '../../stores/runs.store';
import { captureScope } from '../../stores/scope';
import { formatDateTime } from '../../utils/format';
import { TaskRunOutput } from './task-run-output';

export interface DispatchTimelineProps {
  taskId: string;
  workspaceId: string;
}

export interface TaskAgentRun {
  run: DispatchRun;
  createdAt: string;
}

/** 新批次优先，按 run id 去重；批次本身不进入 Task 详情的视觉层级。 */
export function taskAgentRuns(dispatches: readonly DispatchCard[]): TaskAgentRun[] {
  const seen = new Set<string>();
  const result: TaskAgentRun[] = [];
  for (const dispatch of dispatches) {
    for (const run of dispatch.runs) {
      if (seen.has(run.id)) continue;
      seen.add(run.id);
      result.push({ run, createdAt: dispatch.created_at });
    }
  }
  return result;
}

/** Task 详情按 Agent 执行平铺；派发批次与技术时间线不再作为一级对象。 */
export function DispatchTimeline({ taskId, workspaceId }: DispatchTimelineProps) {
  const dispatches = useDispatchesStore((s) => s.byWorkItem[taskId]);
  const error = useDispatchesStore((s) => s.errorByWorkItem[taskId]);
  const refreshFor = useDispatchesStore((s) => s.refreshFor);

  useEffect(() => {
    if (captureScope().workspaceId === workspaceId) void refreshFor(taskId, workspaceId);
  }, [taskId, workspaceId, refreshFor]);

  const runs = taskAgentRuns(dispatches ?? []);

  return (
    <section className="plane-task-section space-y-snug" aria-labelledby="task-agent-runs-title">
      <div>
        <h2 id="task-agent-runs-title" className="text-body-lg font-semibold text-text-primary">执行 Agent</h2>
        <p className="mt-micro text-caption text-text-tertiary">每次执行只展示收到的输入与完成后的最终输出</p>
      </div>
      {error && <InlineRequestError message={error} onRetry={() => {
        if (captureScope().workspaceId === workspaceId) void refreshFor(taskId, workspaceId);
      }} />}
      {dispatches === undefined && !error ? (
        <p className="text-body text-text-tertiary">执行记录加载中…</p>
      ) : runs.length === 0 && !error ? (
        <EmptyState
          icon={<Bot className="h-5 w-5" aria-hidden />}
          title="尚无 Agent 执行"
          description="任务开始执行后，这里会显示每个 Agent 的输入与最终结果。"
        />
      ) : runs.length > 0 ? (
        <ol className="space-y-tight">
          {runs.map(({ run, createdAt }, index) => (
            <li key={run.id}>
              <DispatchRunRow run={run} createdAt={createdAt} defaultExpanded={index === 0} />
            </li>
          ))}
        </ol>
      ) : null}
    </section>
  );
}

function InlineRequestError({ message, onRetry }: { message: string; onRetry: () => void }) {
  return (
    <div
      className="flex items-center justify-between gap-snug rounded-button border border-status-error/30 bg-status-error/5 px-snug py-tight"
      role="alert"
      aria-live="assertive"
    >
      <span className="text-caption text-status-error">{message}</span>
      <Button type="button" size="sm" variant="secondary" onClick={onRetry}>重试</Button>
    </div>
  );
}

/** Agent 执行行：元信息保持单行，展开区域严格只有输入与最终输出。 */
export function DispatchRunRow({
  run,
  createdAt,
  defaultExpanded = false,
}: {
  run: DispatchRun;
  createdAt?: string;
  defaultExpanded?: boolean;
}) {
  const [expanded, setExpanded] = useState(defaultExpanded);
  const runSnapshot = useRunsStore((state) => state.runs[run.id]);
  const fetchRun = useRunsStore((state) => state.fetchRun);
  const name = run.agent_name ?? run.agent_profile_id ?? '未指派';
  const status = runSnapshot?.status ?? run.status;
  const outputId = `task-run-output-${run.id}`;

  // 折叠行也缓存轻量 Run 快照；runs.store 会就地应用后续 SSE 状态。
  // 完整事件历史仍只在展开 TaskRunOutput 后加载。
  useEffect(() => {
    void fetchRun(run.id);
  }, [fetchRun, run.id]);

  return (
    <div className="plane-task-agent">
      <button
        type="button"
        onClick={() => setExpanded((value) => !value)}
        aria-expanded={expanded}
        aria-controls={expanded ? outputId : undefined}
        title={expanded ? `收起 ${name} 的执行结果` : `查看 ${name} 的执行结果`}
        aria-label={`${expanded ? '收起' : '查看'} ${name} 的输入与最终输出`}
        className="plane-task-agent-trigger"
      >
        <Avatar name={name === '未指派' ? run.id : name} size={24} />
        <span className="min-w-0 flex-1 truncate text-body font-medium text-text-primary">{name}</span>
        <StatusPill><span className={runStatusColor(status)}>{runStatusText(status)}</span></StatusPill>
        {(runSnapshot?.created_at || createdAt) && (
          <span className="hidden shrink-0 text-caption tabular-nums text-text-tertiary sm:inline">
            {formatDateTime(runSnapshot?.created_at ?? createdAt ?? '')}
          </span>
        )}
        {expanded ? <ChevronDown className="h-3.5 w-3.5 shrink-0 text-text-tertiary" aria-hidden /> : <ChevronRight className="h-3.5 w-3.5 shrink-0 text-text-tertiary" aria-hidden />}
      </button>
      {expanded && (
        <div id={outputId}>
          <TaskRunOutput run={run} agentName={name} />
        </div>
      )}
    </div>
  );
}
