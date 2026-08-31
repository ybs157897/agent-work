import { Ban, CirclePause, ShieldAlert } from 'lucide-react';
import { useState } from 'react';
import { ApiError } from '../../api/client';
import { cancelRun, interruptRun, resolveApproval } from '../../api/endpoints';
import type { WorkItem } from '../../api/types';
import { runStatusColor, runStatusText } from '../../components/status';
import { useRunsStore, type TimelineEntry } from '../../stores/runs.store';
import { captureScope, isCurrent, isCurrentWorkspaceEntity, type Scope } from '../../stores/scope';
import { toast } from '../../stores/toast.store';
import { formatTime } from '../../utils/format';
import { promptRejectionReason } from '../../utils/prompt';
import type { CoordinatorResolution } from '../../utils/task-phase';

const ACTIVE: ReadonlySet<string> = new Set(['running', 'waiting_approval', 'starting', 'succeeding', 'reconnecting']);

export function runPanelOwnershipText(resolution: CoordinatorResolution): string {
  return resolution === 'legacy' ? '历史任务 · 未启用 Coordinator 控制线' : '由 Coordinator 托管';
}

/** Run 面板：Coordinator 任务显示托管归属；历史任务保留自己的执行记录语义。 */
export function RunPanel({
  task,
  coordinatorResolution,
}: {
  task: WorkItem;
  coordinatorResolution: CoordinatorResolution;
}) {
  const run = useRunsStore((s) => (task.latest_run_id ? s.runs[task.latest_run_id] : undefined));
  const timeline = useRunsStore((s) => (task.latest_run_id ? s.timelines[task.latest_run_id] : undefined));
  const approvals = useRunsStore((s) => (task.latest_run_id ? s.approvals[task.latest_run_id] : undefined));
  const artifacts = useRunsStore((s) => (task.latest_run_id ? s.artifacts[task.latest_run_id] : undefined));
  const fetchApprovals = useRunsStore((s) => s.fetchApprovals);

  const [busy, setBusy] = useState<string | null>(null);

  const runActive = run ? ACTIVE.has(run.status) : false;

  const guard = async (action: string, fn: (scope: Scope) => Promise<unknown>) => {
    const scope = captureScope();
    if (!isCurrentWorkspaceEntity(scope, task)) {
      toast.error('该任务不属于当前工作区，无法操作。');
      return;
    }
    setBusy(action);
    try {
      await fn(scope);
      if (!isCurrent(scope)) return;
    } catch (err) {
      if (!isCurrent(scope)) return;
      toast.error(err instanceof ApiError ? err.message : '操作失败');
    } finally {
      if (isCurrent(scope)) setBusy(null);
    }
  };

  const onResolve = (approvalId: string, runId: string, decision: 'approved' | 'rejected') =>
    guard(`resolve-${approvalId}`, async (scope) => {
      let reason = '';
      if (decision === 'rejected') {
        const input = promptRejectionReason();
        if (input === null) return; // 用户取消弹窗：中止提交，不视为空理由拒绝
        reason = input;
      }
      await resolveApproval(approvalId, runId, decision, reason);
      if (!isCurrent(scope)) return;
      await fetchApprovals(runId);
      if (!isCurrent(scope)) return;
      toast.success(decision === 'approved' ? '已批准' : '已拒绝');
    });

  return (
    <section className="space-y-base border-t border-border-subtle pt-comfortable">
      <div className="flex items-center justify-between gap-snug">
        <h3 className="text-body font-semibold text-text-primary">执行记录</h3>
        <span className="text-caption text-text-tertiary">{runPanelOwnershipText(coordinatorResolution)}</span>
      </div>

      {/* 最新 Run 状态 */}
      {run ? (
        <div className="rounded-lg border border-border-subtle bg-surface-base p-snug space-y-2">
          <div className="flex items-center justify-between">
            <span className={`text-body font-medium ${runStatusColor(run.status)}`}>
              {runStatusText(run.status)}
            </span>
            <span className="text-caption text-text-tertiary">{run.runtime_label || 'runtime'} · 当前尝试</span>
          </div>
          {run.progress != null && runActive && (
            <div className="h-1 bg-surface-raised rounded-full overflow-hidden">
              <div
                className="h-full bg-brand-primary transition-all duration-500"
                style={{ width: `${Math.round(run.progress * 100)}%` }}
              />
            </div>
          )}
          {run.failure && (
            <div className="rounded-button border border-status-error/20 bg-status-error/5 px-snug py-tight" role="alert">
              <p className="text-caption font-medium text-status-error">
                {run.failure.code ? `${run.failure.code}：` : ''}{run.failure.message}
              </p>
              <p className="mt-0.5 text-caption text-text-secondary">
                {run.failure.retryable === false ? 'Coordinator 将等待你补充信息或解除阻塞。' : 'Coordinator 已记录失败，正在安排恢复或重新分配。'}
              </p>
            </div>
          )}
          <div className="flex flex-wrap gap-2">
            {runActive && (
              <>
                <button
                  disabled={busy !== null}
                  onClick={() => guard('interrupt', () => interruptRun(run.id))}
                  className="flex items-center gap-1 text-caption border border-border-strong text-text-secondary rounded-button px-2 py-1 hover:bg-surface-raised transition-colors disabled:opacity-50"
                >
                  <CirclePause className="w-3.5 h-3.5" /> 暂停当前尝试
                </button>
                <button
                  disabled={busy !== null}
                  onClick={() => guard('cancel', () => cancelRun(run.id))}
                  className="flex items-center gap-1 text-caption border border-status-error/40 text-status-error rounded-button px-2 py-1 hover:bg-status-error/5 transition-colors disabled:opacity-50"
                >
                  <Ban className="w-3.5 h-3.5" /> 取消当前尝试
                </button>
              </>
            )}
          </div>
        </div>
      ) : (
        <p className="text-caption text-text-tertiary">
          {task.runs_count > 0 ? '运行数据加载中…' : '尚未运行过'}
        </p>
      )}

      {/* 待审批 */}
      {(approvals ?? []).filter((a) => a.status === 'pending').map((a) => (
        <div key={a.id} className="rounded-lg border border-status-warning/30 bg-status-warning/5 p-snug space-y-2">
          <div className="flex items-center gap-1.5 text-body font-medium text-text-primary">
            <ShieldAlert className="w-4 h-4 text-status-warning" />
            审批请求 · {a.kind} · {a.risk}
          </div>
          <p className="text-caption text-text-secondary">{a.summary}</p>
          <div className="flex gap-2">
            <button
              disabled={busy !== null}
              onClick={() => void onResolve(a.id, a.run_id, 'approved')}
              className="text-caption bg-status-success text-text-inverse rounded-button px-2 py-1 disabled:opacity-50"
            >
              批准
            </button>
            <button
              disabled={busy !== null}
              onClick={() => void onResolve(a.id, a.run_id, 'rejected')}
              className="text-caption border border-status-error/40 text-status-error rounded-button px-2 py-1 disabled:opacity-50"
            >
              拒绝
            </button>
          </div>
        </div>
      ))}

      {/* 时间线（SSE 实时；M1 无历史回放端点） */}
      {timeline && timeline.filter((e) => e.type !== 'run.progress_updated').length > 0 && (
        <details className="rounded-card border border-border-subtle bg-surface-base">
          <summary className="cursor-pointer list-none px-snug py-tight text-caption font-medium text-text-secondary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-brand-primary/40">
            技术日志 · {timeline.filter((e) => e.type !== 'run.progress_updated').length} 条
          </summary>
          <div className="max-h-48 space-y-1.5 overflow-y-auto border-t border-border-subtle px-snug py-tight pr-1">
            {timeline
              .filter((e) => e.type !== 'run.progress_updated')
              .map((e) => (
                <TimelineRow key={e.event_id} entry={e} />
              ))}
          </div>
        </details>
      )}

      {/* 产物 */}
      {(artifacts ?? []).length > 0 && (
        <div className="space-y-1">
          <h4 className="text-caption font-medium text-text-tertiary">产物</h4>
          {artifacts!.map((a) => (
            <div key={a.id} className="flex items-center justify-between text-caption text-text-secondary">
              <span className="truncate">{a.logical_path}</span>
              <span className="text-text-tertiary tabular-nums shrink-0 ml-2">
                {(a.size / 1024).toFixed(1)} KB · {a.status}
              </span>
            </div>
          ))}
        </div>
      )}
    </section>
  );
}

function TimelineRow({ entry }: { entry: TimelineEntry }) {
  const status = typeof entry.data?.status === 'string' ? entry.data.status : undefined;
  let text: string;
  if (entry.text) {
    text = entry.text;
  } else if (status) {
    text = `状态 → ${runStatusText(status)}`;
  } else {
    text = entry.type;
  }
  return (
    <div className="flex items-start gap-2 text-caption">
      <span className="text-text-tertiary tabular-nums shrink-0">{formatTime(entry.occurred_at)}</span>
      <span className={`${status ? runStatusColor(status) : 'text-text-secondary'} break-words`}>
        {text}
      </span>
    </div>
  );
}
