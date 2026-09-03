import { ArrowLeft, CalendarDays, GitBranch, Lock, Pencil } from 'lucide-react';
import { useEffect, useState, type ReactNode } from 'react';
import { useNavigate } from 'react-router-dom';
import { ApiError } from '../../api/client';
import { getWorkItem, getWorkItemTree, patchWorkItem } from '../../api/endpoints';
import type { Priority, WorkItem, WorkItemStatus } from '../../api/types';
import { Drawer } from '../../components/drawer';
import { PriorityBadge } from '../../components/priority-badge';
import { useTasksStore } from '../../stores/tasks.store';
import { captureScope, isCurrent, isCurrentWorkspaceEntity } from '../../stores/scope';
import { toast } from '../../stores/toast.store';
import { formatDateTime, formatDueDate } from '../../utils/format';
import { isAwaitingAcceptance } from '../../utils/task-phase';
import { sortTasksTree } from '../../utils/task-tree';
import { DispatchTimeline } from './dispatch-timeline';
import { ReturnTaskModal } from './return-modal';

const STATUS_TEXT: Record<string, string> = {
  todo: '待办',
  in_progress: '进行中',
  blocked: '阻塞',
  completed: '已完成',
  cancelled: '已取消',
};

const PHASE_TEXT: Record<string, string> = {
  execution: '执行中',
  review: '待验收',
  acceptance: '待验收',
};

function workItemKey(id: string): string {
  const tail = id.replace(/^wi_?/i, '').slice(-6).toUpperCase();
  return `WI-${tail || '------'}`;
}

/** Task 详情以任务为唯一主语，执行记录只下钻到 Agent 输入与最终输出。 */
export function TaskDetail({
  taskId,
  onClose,
  onTransition,
  fullPage = false,
}: {
  taskId: string | null;
  onClose: () => void;
  onTransition: (item: WorkItem, to: WorkItemStatus) => Promise<void>;
  fullPage?: boolean;
}) {
  const storeTask = useTasksStore((state) => (taskId ? state.items.find((item) => item.id === taskId) : undefined));
  const upsert = useTasksStore((state) => state.upsert);
  const selectTask = useTasksStore((state) => state.selectTask);
  const storeItems = useTasksStore((state) => state.items);
  const navigate = useNavigate();
  const [editOpen, setEditOpen] = useState(false);
  const [returning, setReturning] = useState(false);
  const [treeChildren, setTreeChildren] = useState<WorkItem[] | null>(null);
  const [known, setKnown] = useState<Record<string, WorkItem>>({});

  const task = storeTask ?? (taskId ? known[taskId] : undefined);
  const taskWorkspaceId = task?.workspace_id;

  useEffect(() => {
    if (!taskId || !taskWorkspaceId) return;
    let cancelled = false;
    const scope = captureScope();
    if (!isCurrentWorkspaceEntity(scope, { workspace_id: taskWorkspaceId })) return;
    setTreeChildren(null);
    getWorkItemTree(taskId)
      .then(({ items }) => {
        if (cancelled || !isCurrent(scope)) return;
        const currentItems = items.filter((item) => isCurrentWorkspaceEntity(scope, item));
        setTreeChildren(currentItems.filter((item) => item.parent_id === taskId));
        setKnown((previous) => {
          const next = { ...previous };
          for (const item of currentItems) next[item.id] = item;
          return next;
        });
      })
      .catch(() => undefined);
    return () => {
      cancelled = true;
    };
  }, [taskId, taskWorkspaceId]);

  const children = sortTasksTree(
    treeChildren ?? (taskId ? storeItems.filter((item) => item.parent_id === taskId) : []),
  ).map((entry) => entry.item);

  const openWorkItem = (workItemId: string) => {
    if (fullPage) navigate(`/tasks/${workItemId}`);
    else selectTask(workItemId);
  };

  const content = task ? (
    <div className="plane-board plane-task-detail h-full min-h-0 overflow-y-auto">
      <div className="plane-task-detail-body">
        <article className="plane-task-detail-main">
          <button
            type="button"
            onClick={onClose}
            className="mb-base inline-flex min-h-8 items-center gap-tight rounded-button px-tight text-caption font-medium text-text-secondary transition-colors hover:bg-surface-sunken hover:text-text-primary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-brand-primary/40"
          >
            <ArrowLeft className="h-4 w-4" aria-hidden />
            返回任务列表
          </button>

          <nav aria-label="任务路径" className="mb-snug flex items-center gap-tight text-caption text-text-tertiary">
            <span>任务</span>
            <span aria-hidden>/</span>
            <span className="font-mono">{workItemKey(task.id)}</span>
          </nav>

          <header>
            <div className="mb-snug flex flex-wrap items-center gap-tight">
              <span className="inline-flex rounded-button border border-border-subtle bg-surface-sunken px-tight py-micro text-caption font-medium text-text-secondary">
                {STATUS_TEXT[task.status] ?? task.status}
              </span>
              {task.phase && task.status === 'in_progress' && (
                <span className="inline-flex rounded-button border border-brand-primary/20 bg-brand-primary/10 px-tight py-micro text-caption font-medium text-brand-accent">
                  {PHASE_TEXT[task.phase] ?? task.phase}
                </span>
              )}
              <PriorityBadge priority={task.priority} />
            </div>
            <h1 className="max-w-4xl text-h2 font-semibold leading-tight text-text-primary">{task.title}</h1>
            <p className="mt-base max-w-4xl whitespace-pre-wrap text-body leading-relaxed text-text-secondary">
              {task.description || '暂无任务描述'}
            </p>
          </header>

          {task.status === 'blocked' && task.blocker && (
            <section className="mt-comfortable rounded-card border border-status-error/25 bg-status-error/5 p-snug" aria-label="任务阻塞原因">
              <h2 className="text-caption font-semibold text-status-error">阻塞 · {task.blocker.code}</h2>
              <p className="mt-micro text-body text-text-primary">{task.blocker.message}</p>
            </section>
          )}

          {children.length > 0 && (
            <section className="plane-task-section mt-comfortable" aria-labelledby="task-children-title">
              <div className="mb-snug flex items-center gap-tight">
                <h2 id="task-children-title" className="text-body-lg font-semibold text-text-primary">子任务</h2>
                <span className="plane-board-count">{children.length}</span>
              </div>
              <ul className="overflow-hidden rounded-card border border-border-subtle bg-surface-raised">
                {children.map((child) => (
                  <li key={child.id} className="border-b border-border-subtle last:border-b-0">
                    <button
                      type="button"
                      onClick={() => openWorkItem(child.id)}
                      className="flex min-h-11 w-full items-center gap-snug px-snug py-tight text-left transition-colors hover:bg-surface-sunken focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-brand-primary/40"
                    >
                      <GitBranch className="h-4 w-4 shrink-0 text-text-tertiary" aria-hidden />
                      <span className="shrink-0 font-mono text-caption text-text-tertiary">{workItemKey(child.id)}</span>
                      <span className={`min-w-0 flex-1 truncate text-body font-medium ${child.status === 'completed' ? 'text-text-tertiary line-through' : 'text-text-primary'}`}>
                        {child.title}
                      </span>
                      <span className="shrink-0 text-caption text-text-secondary">{STATUS_TEXT[child.status] ?? child.status}</span>
                    </button>
                  </li>
                ))}
              </ul>
            </section>
          )}

          <div className="mt-comfortable">
            <DispatchTimeline taskId={task.id} workspaceId={task.workspace_id} />
          </div>
        </article>

        <aside className="plane-task-detail-aside" aria-label="任务属性与操作">
          {task.status !== 'completed' && task.status !== 'cancelled' && (
            <button
              type="button"
              onClick={() => setEditOpen(true)}
              className="mb-comfortable inline-flex min-h-9 w-full items-center justify-center gap-tight rounded-button border border-border-strong bg-surface-raised px-snug text-body font-medium text-text-secondary transition-colors hover:bg-surface-base hover:text-text-primary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-brand-primary/40"
            >
              <Pencil className="h-3.5 w-3.5" aria-hidden />
              编辑任务
            </button>
          )}

          <h2 className="text-caption font-semibold uppercase tracking-wide text-text-tertiary">任务属性</h2>
          <dl className="mt-snug space-y-base text-body">
            <TaskProperty label="状态" value={STATUS_TEXT[task.status] ?? task.status} />
            <TaskProperty label="优先级" value={<PriorityBadge priority={task.priority} />} />
            <TaskProperty
              label="截止时间"
              value={
                <span className="inline-flex items-center gap-tight tabular-nums">
                  <CalendarDays className="h-3.5 w-3.5 text-text-tertiary" aria-hidden />
                  {task.due_date ? formatDueDate(task.due_date) : '未设置'}
                </span>
              }
            />
            <TaskProperty label="执行次数" value={<span className="tabular-nums">{task.runs_count}</span>} />
            <TaskProperty label="更新时间" value={<span className="tabular-nums">{formatDateTime(task.updated_at)}</span>} />
            {task.locked_by_run_id && (
              <TaskProperty
                label="执行锁"
                value={
                  <span className="inline-flex items-center gap-tight" title={task.locked_by_run_id}>
                    <Lock className="h-3.5 w-3.5 text-text-tertiary" aria-hidden />
                    已锁定
                  </span>
                }
              />
            )}
          </dl>

          {(task.status === 'in_progress' || task.status === 'blocked') && (
            <section className="mt-comfortable border-t border-border-subtle pt-comfortable" aria-labelledby="task-actions-title">
              <h2 id="task-actions-title" className="mb-snug text-caption font-semibold uppercase tracking-wide text-text-tertiary">任务操作</h2>
              <div className="space-y-tight">
                {task.status === 'in_progress' && (
                  <>
                    {isAwaitingAcceptance(task) && (
                      <button
                        type="button"
                        onClick={() => void onTransition(task, 'completed')}
                        className="min-h-9 w-full rounded-button bg-status-success px-snug text-body font-medium text-text-inverse transition-opacity hover:opacity-90 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-status-success/40"
                      >
                        验收通过
                      </button>
                    )}
                    {isAwaitingAcceptance(task) && !task.parent_id && (
                      <button
                        type="button"
                        onClick={() => setReturning(true)}
                        className="min-h-9 w-full rounded-button border border-status-warning/50 bg-surface-raised px-snug text-body font-medium text-status-warning transition-colors hover:bg-status-warning/5 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-status-warning/30"
                      >
                        打回重做
                      </button>
                    )}
                    <button
                      type="button"
                      onClick={() => void onTransition(task, 'blocked')}
                      className="min-h-9 w-full rounded-button border border-border-strong bg-surface-raised px-snug text-body font-medium text-text-secondary transition-colors hover:bg-surface-base hover:text-text-primary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-brand-primary/30"
                    >
                      标记阻塞
                    </button>
                  </>
                )}
                {task.status === 'blocked' && (
                  <button
                    type="button"
                    onClick={() => void onTransition(task, 'in_progress')}
                    className="min-h-9 w-full rounded-button bg-brand-primary px-snug text-body font-medium text-text-inverse transition-colors hover:bg-brand-accent focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-brand-primary/40"
                  >
                    解除阻塞
                  </button>
                )}
              </div>
            </section>
          )}
        </aside>
      </div>

      {editOpen && (
        <TaskEditModal
          task={task}
          onClose={() => setEditOpen(false)}
          onSaved={(item) => {
            upsert(item);
            setEditOpen(false);
          }}
        />
      )}
      <ReturnTaskModal
        task={returning && isAwaitingAcceptance(task) ? task : null}
        onClose={() => setReturning(false)}
      />
    </div>
  ) : null;

  if (fullPage) return content;
  return <Drawer open={taskId !== null} onClose={onClose} width={760}>{content}</Drawer>;
}

function TaskProperty({ label, value }: { label: string; value: ReactNode }) {
  return (
    <div className="grid grid-cols-[5rem_minmax(0,1fr)] items-start gap-snug">
      <dt className="text-caption text-text-tertiary">{label}</dt>
      <dd className="min-w-0 break-words text-caption text-text-primary">{value}</dd>
    </div>
  );
}

/** 任务字段编辑：标题/描述/优先级/截止日（乐观锁；状态走 commands 不在此改）。 */
function TaskEditModal({
  task,
  onClose,
  onSaved,
}: {
  task: WorkItem;
  onClose: () => void;
  onSaved: (item: WorkItem) => void;
}) {
  const [title, setTitle] = useState(task.title);
  const [description, setDescription] = useState(task.description ?? '');
  const [priority, setPriority] = useState<Priority>(task.priority);
  const [dueDate, setDueDate] = useState(task.due_date ?? '');
  const [saving, setSaving] = useState(false);
  const inputClassName = 'mt-1 w-full rounded-input border border-border-strong bg-surface-raised px-snug py-tight text-body outline-none focus:ring-2 focus:ring-brand-primary/30';

  const save = async () => {
    const scope = captureScope();
    if (!isCurrentWorkspaceEntity(scope, task)) {
      toast.error('该任务不属于当前工作区，无法保存。');
      onClose();
      return;
    }
    setSaving(true);
    try {
      const updated = await patchWorkItem(task.id, {
        title: title.trim(),
        description,
        priority,
        ...(dueDate ? { due_date: dueDate } : {}),
        expected_version: task.version,
      });
      if (!isCurrentWorkspaceEntity(scope, updated)) return;
      toast.success('任务已更新');
      onSaved(updated);
    } catch (error) {
      if (!isCurrent(scope)) return;
      if (error instanceof ApiError && error.isVersionConflict) {
        toast.error('任务已被他人修改，已为你刷新最新数据');
        const latest = await getWorkItem(task.id);
        if (isCurrentWorkspaceEntity(scope, latest)) onSaved(latest);
      } else {
        toast.error(error instanceof ApiError ? error.message : '保存失败');
      }
    } finally {
      if (isCurrent(scope)) setSaving(false);
    }
  };

  return (
    <Drawer open onClose={onClose} title="编辑任务" width={480}>
      <div className="p-comfortable">
        <div className="space-y-base">
          <label className="block">
            <span className="text-body text-text-secondary">标题</span>
            <input value={title} onChange={(event) => setTitle(event.target.value)} className={inputClassName} />
          </label>
          <label className="block">
            <span className="text-body text-text-secondary">描述</span>
            <textarea value={description} onChange={(event) => setDescription(event.target.value)} rows={5} className={`${inputClassName} resize-y`} />
          </label>
          <div className="grid grid-cols-1 gap-snug sm:grid-cols-2">
            <label className="block">
              <span className="text-body text-text-secondary">优先级</span>
              <select value={priority} onChange={(event) => setPriority(event.target.value as Priority)} className={inputClassName}>
                <option value="low">低</option>
                <option value="medium">中</option>
                <option value="high">高</option>
                <option value="urgent">紧急</option>
              </select>
            </label>
            <label className="block">
              <span className="text-body text-text-secondary">截止日</span>
              <input type="date" value={dueDate} onChange={(event) => setDueDate(event.target.value)} className={inputClassName} />
            </label>
          </div>
          <div className="flex justify-end gap-snug pt-tight">
            <button type="button" onClick={onClose} className="rounded-button border border-border-strong bg-transparent px-base py-tight font-medium text-text-secondary transition-colors hover:bg-surface-base">
              取消
            </button>
            <button type="button" onClick={() => void save()} disabled={!title.trim() || saving} className="rounded-button bg-brand-primary px-base py-tight font-medium text-text-inverse transition-all hover:bg-brand-accent disabled:opacity-50">
              {saving ? '保存中…' : '保存'}
            </button>
          </div>
        </div>
      </div>
    </Drawer>
  );
}
