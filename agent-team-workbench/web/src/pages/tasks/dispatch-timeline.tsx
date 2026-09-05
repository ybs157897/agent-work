import { Bot } from 'lucide-react';
import { useEffect } from 'react';
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

/**
 * 新批次优先，每个派生任务只保留最新 Worker 尝试；Coordinator/evaluation
 * 永不进入用户详情。旧响应缺少 role 时，只信任挂在子任务上的 run。
 */
export function taskAgentRuns(dispatches: readonly DispatchCard[], rootTaskId: string): TaskAgentRun[] {
  const seenWorkItems = new Set<string>();
  const result: TaskAgentRun[] = [];
  for (const dispatch of dispatches) {
    const order: string[] = [];
    const latestByWorkItem = new Map<string, DispatchRun>();
    for (const run of dispatch.runs) {
      const worker = run.role === 'worker' || (!run.role && run.work_item_id !== rootTaskId);
      if (!worker) continue;
      if (!latestByWorkItem.has(run.work_item_id)) order.push(run.work_item_id);
      latestByWorkItem.set(run.work_item_id, run);
    }
    for (const workItemId of order) {
      if (seenWorkItems.has(workItemId)) continue;
      const run = latestByWorkItem.get(workItemId);
      if (!run) continue;
      seenWorkItems.add(workItemId);
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

  const runs = taskAgentRuns(dispatches ?? [], taskId);

  return (
    <section className="plane-task-section space-y-snug" aria-labelledby="task-agent-runs-title">
      <div>
        <h2 id="task-agent-runs-title" className="text-body-lg font-semibold text-text-primary">Agent 输入与输出</h2>
        <p className="mt-micro text-caption text-text-tertiary">按 Agent 直接展示本次输入与最终结果，无需逐层进入或展开</p>
      </div>
      {error && <InlineRequestError message={error} onRetry={() => {
        if (captureScope().workspaceId === workspaceId) void refreshFor(taskId, workspaceId);
      }} />}
      {dispatches === undefined && !error ? (
        <p className="text-body text-text-tertiary">执行记录加载中…</p>
      ) : runs.length === 0 && !error ? (
        <EmptyState
          icon={<Bot className="h-5 w-5" aria-hidden />}
          title="等待执行 Agent 输出"
          description="Coordinator 完成派发后，这里会直接出现各 Agent 的输入与最终结果。"
        />
      ) : runs.length > 0 ? (
        <ol className="space-y-snug">
          {runs.map(({ run, createdAt }) => (
            <li key={run.work_item_id}>
              <TaskAgentOutputCard run={run} createdAt={createdAt} />
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

/** Agent 结果直接展开，详情中不再制造第二层交互。 */
export function TaskAgentOutputCard({
  run,
  createdAt,
}: {
  run: DispatchRun;
  createdAt?: string;
}) {
  const runSnapshot = useRunsStore((state) => state.runs[run.id]);
  const name = run.agent_name ?? run.agent_profile_id ?? '未指派';
  const status = runSnapshot?.status ?? run.status;

  return (
    <article className="plane-task-agent" aria-label={`${name} 的输入与最终输出`}>
      <header className="plane-task-agent-header">
        <Avatar name={name === '未指派' ? run.id : name} size={24} />
        <span className="min-w-0 flex-1 truncate text-body font-medium text-text-primary">{name}</span>
        <StatusPill><span className={runStatusColor(status)}>{runStatusText(status)}</span></StatusPill>
        {(runSnapshot?.created_at || createdAt) && (
          <span className="hidden shrink-0 text-caption tabular-nums text-text-tertiary sm:inline">
            {formatDateTime(runSnapshot?.created_at ?? createdAt ?? '')}
          </span>
        )}
      </header>
      <TaskRunOutput run={run} agentName={name} />
    </article>
  );
}
