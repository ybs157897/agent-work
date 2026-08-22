import { useEffect, useState } from 'react';
import { ApiError } from '../../api/client';
import { assignWorkItem, getWorkItem } from '../../api/endpoints';
import type { WorkItem, WorkItemStatus } from '../../api/types';
import { Drawer } from '../../components/drawer';
import { PriorityBadge } from '../../components/priority-badge';
import { useAgentsStore } from '../../stores/agents.store';
import { useRunsStore } from '../../stores/runs.store';
import { useTasksStore } from '../../stores/tasks.store';
import { toast } from '../../stores/toast.store';
import { formatDateTime, formatDueDate } from '../../utils/format';
import { RunPanel } from './run-panel';

const STATUS_TEXT: Record<string, string> = {
  todo: '待办',
  in_progress: '进行中',
  blocked: '阻塞',
  completed: '已完成',
  cancelled: '已取消',
};

const PHASE_TEXT: Record<string, string> = {
  execution: '执行阶段',
  review: '评审中 · 待验收',
  acceptance: '待验收',
};

/** 任务详情抽屉：字段、指派、阻塞/验收操作、Run 面板（协议 §5.2 任务详情/启动）。 */
export function TaskDetail({
  taskId,
  onClose,
  onTransition,
}: {
  taskId: string | null;
  onClose: () => void;
  onTransition: (item: WorkItem, to: WorkItemStatus) => Promise<void>;
}) {
  const task = useTasksStore((s) => (taskId ? s.items.find((t) => t.id === taskId) : undefined));
  const upsert = useTasksStore((s) => s.upsert);
  const agents = useAgentsStore((s) => s.agents);
  const watchRun = useRunsStore((s) => s.watchRun);
  const unwatchRun = useRunsStore((s) => s.unwatchRun);
  const [assigning, setAssigning] = useState(false);

  // 打开时拉取权威快照（含 blocker/runs_count/version），防止列表数据陈旧。
  useEffect(() => {
    if (!taskId) return;
    let cancelled = false;
    getWorkItem(taskId)
      .then((wi) => {
        if (!cancelled) upsert(wi);
      })
      .catch(() => undefined);
    return () => {
      cancelled = true;
    };
  }, [taskId, upsert]);

  // 订阅最新 Run 的实时事件。
  const latestRunId = task?.latest_run_id;
  useEffect(() => {
    if (!latestRunId) return;
    watchRun(latestRunId);
    return () => unwatchRun(latestRunId);
  }, [latestRunId, watchRun, unwatchRun]);

  const onAssign = async (agentProfileId: string) => {
    if (!task) return;
    setAssigning(true);
    try {
      upsert(await assignWorkItem(task.id, agentProfileId, task.version));
      toast.success('已更新指派人');
    } catch (err) {
      if (err instanceof ApiError && err.isVersionConflict) {
        toast.error('任务已被他人修改，请重试');
        upsert(await getWorkItem(task.id));
      } else {
        toast.error(err instanceof ApiError ? err.message : '指派失败');
      }
    } finally {
      setAssigning(false);
    }
  };

  return (
    <Drawer open={taskId !== null} onClose={onClose} width={380}>
      {task && (
        <div className="flex flex-col h-full overflow-y-auto">
          <div className="p-comfortable space-y-comfortable">
            {/* 头部 */}
            <div className="pr-8">
              <div className="flex items-center gap-2 mb-2">
                <span className="inline-flex items-center px-2 py-0.5 rounded-sm text-caption font-medium bg-surface-base text-text-secondary border border-border-subtle">
                  {STATUS_TEXT[task.status] ?? task.status}
                </span>
                {task.phase && task.status === 'in_progress' && (
                  <span className="inline-flex items-center px-2 py-0.5 rounded-sm text-caption font-medium bg-brand-primary/10 text-brand-accent border border-brand-primary/20">
                    {PHASE_TEXT[task.phase] ?? task.phase}
                  </span>
                )}
                <PriorityBadge priority={task.priority} />
              </div>
              <h2 className="text-h3 text-text-primary leading-snug">{task.title}</h2>
              <p className="text-caption text-text-tertiary mt-1">
                {task.id} · 更新于 {formatDateTime(task.updated_at)}
              </p>
            </div>

            {/* 字段 */}
            <section className="space-y-snug">
              <label className="block">
                <span className="text-caption text-text-tertiary">指派人</span>
                <select
                  value={task.agent_profile_id ?? ''}
                  disabled={assigning || task.status === 'completed' || task.status === 'cancelled'}
                  onChange={(e) => void onAssign(e.target.value)}
                  className="mt-1 w-full rounded-input border border-border-strong bg-surface-raised px-snug py-tight text-body outline-none focus:ring-2 focus:ring-brand-primary/30 disabled:opacity-60"
                >
                  <option value="">未指派</option>
                  {agents.map((a) => (
                    <option key={a.id} value={a.id}>
                      {a.name}（{a.role}）
                    </option>
                  ))}
                </select>
              </label>
              <div className="flex gap-comfortable text-body">
                <div>
                  <span className="text-caption text-text-tertiary block">截止日</span>
                  <span className="text-text-primary tabular-nums">
                    {task.due_date ? formatDueDate(task.due_date) : '—'}
                  </span>
                </div>
                <div>
                  <span className="text-caption text-text-tertiary block">运行次数</span>
                  <span className="text-text-primary tabular-nums">{task.runs_count}</span>
                </div>
              </div>
              {task.description && (
                <div>
                  <span className="text-caption text-text-tertiary block mb-1">描述</span>
                  <p className="text-body text-text-secondary whitespace-pre-wrap">{task.description}</p>
                </div>
              )}
            </section>

            {/* 阻塞信息 */}
            {task.status === 'blocked' && task.blocker && (
              <section className="rounded-lg border border-status-error/20 bg-status-error/5 p-snug">
                <div className="text-caption font-medium text-status-error mb-1">
                  阻塞 · {task.blocker.code}
                </div>
                <p className="text-body text-text-primary">{task.blocker.message}</p>
                <p className="text-caption text-text-tertiary mt-1">
                  来源 {task.blocker.source} · {formatDateTime(task.blocker.created_at)}
                </p>
              </section>
            )}

            {/* 状态操作 */}
            {(task.status === 'in_progress' || task.status === 'blocked') && (
              <section className="flex flex-wrap gap-snug">
                {task.status === 'in_progress' && (
                  <>
                    <button
                      onClick={() => void onTransition(task, 'blocked')}
                      className="bg-transparent border border-status-warning text-status-warning rounded-button px-base py-tight text-body font-medium transition-colors hover:bg-status-warning/5 active:scale-[0.98]"
                    >
                      标记阻塞
                    </button>
                    {(task.phase === 'review' || task.phase === 'acceptance') && (
                      <button
                        onClick={() => void onTransition(task, 'completed')}
                        className="bg-status-success text-white rounded-button px-base py-tight text-body font-medium transition-all hover:opacity-90 active:scale-[0.98]"
                      >
                        验收通过
                      </button>
                    )}
                  </>
                )}
                {task.status === 'blocked' && (
                  <button
                    onClick={() => void onTransition(task, 'in_progress')}
                    className="bg-brand-primary text-white rounded-button px-base py-tight text-body font-medium transition-all hover:bg-brand-accent active:scale-[0.98]"
                  >
                    解除阻塞
                  </button>
                )}
              </section>
            )}

            {/* Run 面板 */}
            <RunPanel task={task} />
          </div>
        </div>
      )}
    </Drawer>
  );
}
