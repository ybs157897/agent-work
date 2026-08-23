import { Ban, CirclePause, Play, RotateCcw, ShieldAlert } from 'lucide-react';
import { useState } from 'react';
import { ApiError } from '../../api/client';
import {
  cancelRun,
  createRun,
  interruptRun,
  resolveApproval,
  resumeRun,
  retryRun,
} from '../../api/endpoints';
import type { WorkItem } from '../../api/types';
import { runStatusColor, runStatusText } from '../../components/status';
import { useAgentsStore } from '../../stores/agents.store';
import { useRunsStore, type TimelineEntry } from '../../stores/runs.store';
import { toast } from '../../stores/toast.store';
import { formatTime } from '../../utils/format';
import { promptRejectionReason } from '../../utils/prompt';

const TERMINAL: ReadonlySet<string> = new Set(['succeeded', 'interrupted', 'cancelled', 'lost', 'failed']);
const ACTIVE: ReadonlySet<string> = new Set(['running', 'waiting_approval', 'starting', 'succeeding', 'reconnecting']);

/** Run 面板：启动 / 进度 / 时间线 / 审批 / 产物 / 运行控制（协议 §5.2、§5.4）。 */
export function RunPanel({ task }: { task: WorkItem }) {
  const agents = useAgentsStore((s) => s.agents);
  const run = useRunsStore((s) => (task.latest_run_id ? s.runs[task.latest_run_id] : undefined));
  const timeline = useRunsStore((s) => (task.latest_run_id ? s.timelines[task.latest_run_id] : undefined));
  const approvals = useRunsStore((s) => (task.latest_run_id ? s.approvals[task.latest_run_id] : undefined));
  const artifacts = useRunsStore((s) => (task.latest_run_id ? s.artifacts[task.latest_run_id] : undefined));
  const fetchApprovals = useRunsStore((s) => s.fetchApprovals);

  const [instruction, setInstruction] = useState('');
  const [agentId, setAgentId] = useState('');
  const [busy, setBusy] = useState<string | null>(null);

  const effectiveAgentId = agentId || task.agent_profile_id || '';
  const runActive = run ? ACTIVE.has(run.status) : false;

  const guard = async (action: string, fn: () => Promise<unknown>) => {
    setBusy(action);
    try {
      await fn();
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : '操作失败');
    } finally {
      setBusy(null);
    }
  };

  const startRun = () =>
    guard('start', async () => {
      if (!instruction.trim()) return;
      // 202 Accepted：真正开始由 SSE 确认；run.created 事件会刷新任务，
      // 详情抽屉随 latest_run_id 自动订阅新 Run。
      await createRun(task.id, {
        agent_profile_id: effectiveAgentId || undefined,
        input: { instruction: instruction.trim() },
      });
      setInstruction('');
      toast.success('运行已受理，等待调度确认');
    });

  const onRetry = (runId: string) =>
    guard('retry', async () => {
      await retryRun(runId);
      toast.success('已创建重试运行');
    });

  const onResume = (runId: string) =>
    guard('resume', async () => {
      try {
        await resumeRun(runId);
        toast.success('已恢复会话');
      } catch (err) {
        // 能力协商失败（adapter 未声明 resume=supported）：显式提示，不静默降级。
        if (err instanceof ApiError && err.code === 'capability_missing') {
          toast.error('该 Runtime 不支持会话恢复，请改用重试（新 Run）');
        } else {
          throw err;
        }
      }
    });

  const onResolve = (approvalId: string, runId: string, decision: 'approved' | 'rejected') =>
    guard(`resolve-${approvalId}`, async () => {
      let reason = '';
      if (decision === 'rejected') {
        const input = promptRejectionReason();
        if (input === null) return; // 用户取消弹窗：中止提交，不视为空理由拒绝
        reason = input;
      }
      await resolveApproval(approvalId, runId, decision, reason);
      await fetchApprovals(runId);
      toast.success(decision === 'approved' ? '已批准' : '已拒绝');
    });

  return (
    <section className="space-y-base border-t border-border-subtle pt-comfortable">
      <h3 className="text-body font-semibold text-text-primary">运行</h3>

      {/* 最新 Run 状态 */}
      {run ? (
        <div className="rounded-lg border border-border-subtle bg-surface-base p-snug space-y-2">
          <div className="flex items-center justify-between">
            <span className={`text-body font-medium ${runStatusColor(run.status)}`}>
              {runStatusText(run.status)}
            </span>
            <span className="text-caption text-text-tertiary">{run.runtime_label || 'mock'}</span>
          </div>
          {run.progress != null && !TERMINAL.has(run.status) && (
            <div className="h-1 bg-surface-raised rounded-full overflow-hidden">
              <div
                className="h-full bg-brand-primary transition-all duration-500"
                style={{ width: `${Math.round(run.progress * 100)}%` }}
              />
            </div>
          )}
          {run.failure && (
            <p className="text-caption text-status-error">
              {run.failure.code}: {run.failure.message}
            </p>
          )}
          <div className="flex flex-wrap gap-2">
            {(run.status === 'reconnecting' || run.status === 'lost') && (
              <button
                disabled={busy !== null}
                onClick={() => void onResume(run.id)}
                title="恢复失联会话（需 Runtime 声明 resume 能力）"
                className="flex items-center gap-1 text-caption border border-status-warning text-status-warning rounded-button px-2 py-1 hover:bg-status-warning/5 transition-colors disabled:opacity-50"
              >
                <Play className="w-3.5 h-3.5" /> 恢复会话
              </button>
            )}
            {runActive && (
              <>
                <button
                  disabled={busy !== null}
                  onClick={() => guard('interrupt', () => interruptRun(run.id))}
                  className="flex items-center gap-1 text-caption border border-border-strong text-text-secondary rounded-button px-2 py-1 hover:bg-surface-raised transition-colors disabled:opacity-50"
                >
                  <CirclePause className="w-3.5 h-3.5" /> 中断
                </button>
                <button
                  disabled={busy !== null}
                  onClick={() => guard('cancel', () => cancelRun(run.id))}
                  className="flex items-center gap-1 text-caption border border-status-error/40 text-status-error rounded-button px-2 py-1 hover:bg-status-error/5 transition-colors disabled:opacity-50"
                >
                  <Ban className="w-3.5 h-3.5" /> 取消
                </button>
              </>
            )}
            {TERMINAL.has(run.status) && (
              <button
                disabled={busy !== null}
                onClick={() => onRetry(run.id)}
                className="flex items-center gap-1 text-caption border border-brand-primary text-brand-primary rounded-button px-2 py-1 hover:bg-brand-primary/5 transition-colors disabled:opacity-50"
              >
                <RotateCcw className="w-3.5 h-3.5" /> 重试（新 Run）
              </button>
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
              className="text-caption bg-status-success text-white rounded-button px-2 py-1 disabled:opacity-50"
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
        <div className="space-y-1.5">
          <h4 className="text-caption font-medium text-text-tertiary">时间线（本次连接实时事件）</h4>
          <div className="max-h-48 overflow-y-auto space-y-1.5 pr-1">
            {timeline
              .filter((e) => e.type !== 'run.progress_updated')
              .map((e) => (
                <TimelineRow key={e.event_id} entry={e} />
              ))}
          </div>
        </div>
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

      {/* 启动新 Run */}
      {task.status !== 'completed' && task.status !== 'cancelled' && (
        <div className="space-y-2">
          <textarea
            value={instruction}
            onChange={(e) => setInstruction(e.target.value)}
            rows={2}
            placeholder="给 Runtime 的指令（含「审批」可演示审批流）"
            className="w-full rounded-input border border-border-strong bg-surface-raised px-snug py-tight text-body outline-none focus:ring-2 focus:ring-brand-primary/30 resize-none"
          />
          <div className="flex items-center gap-2">
            <select
              value={effectiveAgentId}
              onChange={(e) => setAgentId(e.target.value)}
              className="flex-1 rounded-input border border-border-strong bg-surface-raised px-snug py-tight text-body outline-none focus:ring-2 focus:ring-brand-primary/30"
            >
              <option value="">选择 Agent</option>
              {agents
                .filter((a) => a.availability === 'enabled')
                .map((a) => (
                  <option key={a.id} value={a.id}>
                    {a.name}（{a.role}）
                  </option>
                ))}
            </select>
            <button
              onClick={() => void startRun()}
              disabled={!instruction.trim() || busy !== null}
              className="bg-brand-primary text-white rounded-button px-base py-tight text-body font-medium transition-all hover:bg-brand-accent active:scale-[0.98] disabled:opacity-50 disabled:cursor-not-allowed"
            >
              {busy === 'start' ? '受理中…' : '启动运行'}
            </button>
          </div>
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
