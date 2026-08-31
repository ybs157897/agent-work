import { ChevronDown, GitBranch, KanbanSquare, List, Lock, Plus } from 'lucide-react';
import { useEffect, useMemo, useState } from 'react';
import { useNavigate, useSearchParams } from 'react-router-dom';
import type { Priority, WorkItem, WorkItemStatus } from '../api/types';
import { PriorityBadge } from '../components/priority-badge';
import { KanbanSkeleton, ListSkeleton } from '../components/ui';
import { useTasksStore, type ViewMode } from '../stores/tasks.store';
import { childCountByParent, sortTasksTree } from '../utils/task-tree';
import { isAwaitingAcceptance } from '../utils/task-phase';
import { formatDueDate } from '../utils/format';
import { CreateTaskModal } from './tasks/create-task-modal';
import { ReviewQueueSummary } from './tasks/review-queue';
import { TaskSearch } from './tasks/search-panel';

// 树工具实现归 utils/task-tree（task-detail/创建弹窗共用）；此处转出供测试与页面使用。
export { sortTasksTree, childCountByParent } from '../utils/task-tree';

const COLUMNS: { id: WorkItemStatus; title: string }[] = [
  { id: 'todo', title: '待办' },
  { id: 'in_progress', title: '进行中' },
  { id: 'completed', title: '完成' },
  { id: 'blocked', title: '阻塞' },
];

/** 任务入口：看板只做观察与导航；Coordinator 管理状态，不允许看板拖拽改状态。 */
export default function TasksPage() {
  const items = useTasksStore((s) => s.items);
  const loaded = useTasksStore((s) => s.loaded);
  const filter = useTasksStore((s) => s.filter);
  const setFilter = useTasksStore((s) => s.setFilter);
  const viewMode = useTasksStore((s) => s.viewMode);
  const setViewMode = useTasksStore((s) => s.setViewMode);
  const navigate = useNavigate();

  const [searchParams, setSearchParams] = useSearchParams();
  const [createOpen, setCreateOpen] = useState(false);

  // viewMode 是本地 UI 状态：放 URL，不进后端（协议 §4.1）；queue=review 与 view 独立。
  useEffect(() => {
    const v = searchParams.get('view');
    if (v === 'kanban' || v === 'list') setViewMode(v);
  }, [searchParams, setViewMode]);

  const switchView = (m: ViewMode) => {
    setViewMode(m);
    // view 语义不变；切换视图时保留既有的 queue=review 参数。
    const next = new URLSearchParams(searchParams);
    if (m === 'kanban') next.delete('view');
    else next.set('view', m);
    setSearchParams(next, { replace: true });
  };

  const columns = COLUMNS.map((c) => ({
    ...c,
    tasks: items.filter((t) => t.status === c.id),
  }));
  const childCounts = useMemo(() => childCountByParent(items), [items]);

  return (
    <main className="layout-safe flex-1 min-h-0 flex flex-col py-comfortable">
      {/* Header */}
      <header className="mb-snug flex items-center justify-between gap-comfortable shrink-0 border-b border-border-subtle pb-snug">
        <div className="flex items-center gap-snug">
          <div>
            <p className="text-caption uppercase tracking-widest text-text-tertiary">案牍 · Work Items</p>
            <h2 className="mt-1 font-display text-h2 text-text-primary tracking-tight">任务看板</h2>
          </div>
          <span className="hidden rounded-full border border-border-subtle bg-surface-raised px-2 py-0.5 text-caption text-text-secondary tabular-nums md:inline-flex">
            {items.length} 项
          </span>
          <div className="flex bg-surface-base rounded-button p-1 border border-border-subtle" role="group" aria-label="任务视图">
            <button
              type="button"
              onClick={() => switchView('kanban')}
              aria-pressed={viewMode === 'kanban'}
              className={`flex items-center gap-1.5 rounded-sm px-snug py-tight text-body font-medium transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-brand-primary/40 ${
                viewMode === 'kanban'
                  ? 'bg-surface-raised shadow-sm text-text-primary'
                  : 'text-text-secondary hover:text-text-primary'
              }`}
            >
              <KanbanSquare className="w-4 h-4" />
              看板
            </button>
            <button
              type="button"
              onClick={() => switchView('list')}
              aria-pressed={viewMode === 'list'}
              className={`flex items-center gap-1.5 rounded-sm px-snug py-tight text-body font-medium transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-brand-primary/40 ${
                viewMode === 'list'
                  ? 'bg-surface-raised shadow-sm text-text-primary'
                  : 'text-text-secondary hover:text-text-primary'
              }`}
            >
              <List className="w-4 h-4" />
              列表
            </button>
          </div>
        </div>

        <div className="flex items-center gap-snug">
          <button
            type="button"
            onClick={() => setCreateOpen(true)}
            className="inline-flex items-center gap-tight rounded-button bg-brand-primary px-base py-tight text-body font-medium text-text-inverse transition-all hover:bg-brand-accent active:scale-[0.98] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-brand-primary/40"
          >
            <Plus className="h-4 w-4" aria-hidden />
            发布任务
          </button>
          <TaskSearch onOpenTask={(id) => navigate(`/tasks/${id}`)} />
          <span className="text-caption uppercase tracking-widest text-text-tertiary">筛选</span>
          <FilterSelect
            label="优先级"
            value={filter.priority ?? ''}
            options={[
              { value: 'low', label: '低优' },
              { value: 'medium', label: '中优' },
              { value: 'high', label: '高优' },
              { value: 'urgent', label: '紧急' },
            ]}
            onChange={(v) => setFilter({ ...filter, priority: (v || undefined) as Priority | undefined })}
          />
          <span className="rounded-button border border-brand-primary/20 bg-brand-primary/5 px-snug py-tight text-caption text-brand-accent">
            Coordinator 自动调度
          </span>
        </div>
      </header>

      {/* 复审队列摘要（RFC §12.4：服务端 total_count badge；展开走 ?queue=review） */}
      <div className="mb-snug shrink-0">
        <ReviewQueueSummary />
      </div>

      {/* 看板 / 列表 */}
      <section aria-label="任务看板" className="flex-1 min-h-0 overflow-x-auto pb-6">
        {!loaded ? (
          viewMode === 'kanban' ? <KanbanSkeleton /> : <ListSkeleton />
        ) : viewMode === 'kanban' ? (
          <div className="flex h-full min-w-[1040px] gap-snug">
            {columns.map((col) => (
              <KanbanColumn
                key={col.id}
                title={col.title}
                tasks={col.tasks}
                childCounts={childCounts}
                onCreate={col.id === 'todo' ? () => setCreateOpen(true) : undefined}
                onOpen={(id) => navigate(`/tasks/${id}`)}
              />
            ))}
          </div>
        ) : (
          <div className="bg-surface-raised rounded-card shadow-level-1 border border-border-subtle p-base h-full overflow-y-auto">
            <div className="space-y-comfortable">
              {columns.map((col) => (
                <div key={col.id}>
                  <h3 className="font-display text-h3 text-text-primary mb-snug flex items-center gap-2">
                    {col.title}
                    <span className="text-text-tertiary text-body font-normal tabular-nums">
                      {col.tasks.length}
                    </span>
                  </h3>
                  <div className="space-y-tight">
                    {sortTasksTree(col.tasks).map((entry) => (
                      <div
                        key={entry.item.id}
                        onClick={() => navigate(`/tasks/${entry.item.id}`)}
                        onKeyDown={(event) => {
                          if (event.key === 'Enter' || event.key === ' ') {
                            event.preventDefault();
                            navigate(`/tasks/${entry.item.id}`);
                          }
                        }}
                        role="button"
                        tabIndex={0}
                        aria-label={`打开任务 ${entry.item.title}`}
                        className="flex cursor-pointer items-center justify-between rounded-card border border-border-subtle p-snug transition-colors hover:bg-surface-base focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-brand-primary/40"
                      >
                        <div className="flex items-center gap-4 min-w-0" style={{ paddingLeft: entry.depth * 24 }}>
                          {entry.depth > 0 && <GitBranch className="w-3.5 h-3.5 text-text-tertiary shrink-0" />}
                          <PriorityBadge priority={entry.item.priority} />
                          <span
                            className={`font-medium text-sm truncate ${
                              entry.item.status === 'completed'
                                ? 'line-through opacity-60'
                                : 'text-text-primary'
                            }`}
                          >
                            {entry.item.title}
                          </span>
                        </div>
                        <div className="flex items-center gap-6">
                          <span className="text-caption text-text-tertiary">Coordinator</span>
                          <span className="text-caption text-text-tertiary tabular-nums w-12 text-right">
                            {formatDueDate(entry.item.due_date)}
                          </span>
                        </div>
                      </div>
                    ))}
                    {col.tasks.length === 0 && (
                      <div className="text-caption text-text-tertiary py-2">暂无任务</div>
                    )}
                  </div>
                </div>
              ))}
            </div>
          </div>
        )}
      </section>

      <CreateTaskModal
        open={createOpen}
        onClose={() => setCreateOpen(false)}
      />
    </main>
  );
}

function FilterSelect({
  label,
  value,
  options,
  onChange,
}: {
  label: string;
  value: string;
  options: { value: string; label: string }[];
  onChange: (value: string) => void;
}) {
  return (
    <label className="relative flex items-center gap-1 rounded-button border border-border-subtle bg-surface-raised px-snug py-tight text-body text-text-primary transition-colors hover:bg-surface-base cursor-pointer focus-within:ring-2 focus-within:ring-brand-primary/40">
      <span className={value ? '' : 'text-text-tertiary'}>
        {value ? options.find((o) => o.value === value)?.label ?? label : label}
      </span>
      <ChevronDown className="w-4 h-4 text-text-tertiary" />
      <select
        aria-label={label}
        value={value}
        onChange={(e) => onChange(e.target.value)}
        className="absolute inset-0 opacity-0 cursor-pointer"
      >
        <option value="">全部{label}</option>
        {options.map((o) => (
          <option key={o.value} value={o.value}>
            {o.label}
          </option>
        ))}
      </select>
    </label>
  );
}

function KanbanColumn({
  title,
  tasks,
  childCounts,
  onCreate,
  onOpen,
}: {
  title: string;
  tasks: WorkItem[];
  childCounts: Map<string, number>;
  onCreate?: () => void;
  onOpen: (taskId: string) => void;
}) {
  return (
    <div className="flex-1 min-w-0 flex flex-col rounded-card bg-surface-sunken">
      <div className="flex items-center justify-between px-base py-snug shrink-0">
        <div className="flex items-center gap-2">
          <h3 className="font-semibold text-body-lg text-text-primary">{title}</h3>
          <span className="inline-flex items-center justify-center px-2 py-0.5 rounded-full bg-surface-raised border border-border-subtle text-caption font-medium text-text-secondary tabular-nums">
            {tasks.length}
          </span>
        </div>
        {onCreate && (
          <button
            type="button"
            onClick={onCreate}
            aria-label="发布任务"
            title="发布任务"
            className="inline-flex h-8 w-8 items-center justify-center rounded-button text-text-tertiary transition-colors hover:bg-surface-base hover:text-text-primary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-brand-primary/40"
          >
            <Plus className="w-5 h-5" />
          </button>
        )}
      </div>

      <div className="flex-1 overflow-y-auto px-snug pb-snug space-y-snug">
        {tasks.map((task) => (
          <TaskCard
            key={task.id}
            task={task}
            childCount={childCounts.get(task.id) ?? 0}
            onOpen={() => onOpen(task.id)}
          />
        ))}
        {tasks.length === 0 && (
          <div className="rounded-card border border-dashed border-border-strong/60 px-snug py-comfortable text-center text-caption text-text-tertiary">
            <p>此列暂无任务</p>
            <p className="mt-1">由 Coordinator 自动推进；可从待办列发布新任务</p>
          </div>
        )}
      </div>
    </div>
  );
}

function TaskCard({
  task,
  childCount,
  onOpen,
}: {
  task: WorkItem;
  childCount: number;
  onOpen: () => void;
}) {
  const isCompleted = task.status === 'completed';
  const isBlocked = task.status === 'blocked';

  let bgClass = 'bg-surface-raised';
  if (isBlocked) {
    bgClass = task.blocker?.code === 'permission'
      ? 'bg-status-error/10 border-status-error/20'
      : 'bg-status-warning/10 border-status-warning/20';
  }

  return (
    <div
      onClick={onOpen}
      role="button"
      tabIndex={0}
      aria-label={`打开任务 ${task.title}`}
      onKeyDown={(event) => {
        if (event.key === 'Enter' || event.key === ' ') {
          event.preventDefault();
          onOpen();
        }
      }}
      className={`rounded-card p-snug shadow-card border border-border-subtle cursor-pointer transition-all duration-200 hover:-translate-y-0.5 hover:shadow-level-2 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-brand-primary/40 ${bgClass}`}
    >
      <h4
        className={`font-medium text-body text-text-primary mb-snug leading-snug ${
          isCompleted ? 'line-through opacity-60' : ''
        }`}
      >
        {task.title}
      </h4>

      <div className="mb-snug">
        <span className={`inline-flex items-center rounded-full border px-2 py-0.5 text-caption font-medium ${coordinatorCardClass(task)}`}>
          <span className="mr-1 h-1.5 w-1.5 rounded-full bg-current" aria-hidden />
          {coordinatorCardText(task)}
        </span>
      </div>

      {isBlocked && task.blocker && (
        <div className="text-caption font-medium text-status-error mb-snug">{task.blocker.message}</div>
      )}

      {isAwaitingAcceptance(task) && (
        <div className="mb-snug">
          <span
            title="run 成功待评审或评估通过，等待人工验收"
            className="inline-flex items-center px-1.5 py-0.5 rounded-sm text-[11px] font-medium bg-brand-primary/10 text-brand-accent border border-brand-primary/20"
          >
            待验收
          </span>
        </div>
      )}

      <div className="flex items-center justify-between mt-auto">
        <div className="flex items-center gap-1.5">
          <PriorityBadge priority={task.priority} />
          {task.locked_by_run_id && (
            <span
              title={`执行锁：run ${task.locked_by_run_id}（防止同任务双跑）`}
              className="inline-flex items-center px-1.5 py-0.5 rounded-sm text-[11px] font-medium bg-surface-base text-text-secondary border border-border-subtle"
            >
              <Lock className="w-3 h-3" />
            </span>
          )}
          {task.parent_id && (
            <span
              title="编排派生的子任务"
              className="inline-flex items-center px-1.5 py-0.5 rounded-sm text-[11px] font-medium bg-brand-primary/10 text-brand-accent border border-brand-primary/20"
            >
              子任务
            </span>
          )}
          {childCount > 0 && (
            <span
              title={`${childCount} 个直接子任务`}
              className="inline-flex items-center gap-0.5 px-1.5 py-0.5 rounded-sm text-[11px] font-medium bg-surface-base text-text-secondary border border-border-subtle tabular-nums"
            >
              <GitBranch className="w-3 h-3" />
              {childCount}
            </span>
          )}
        </div>
        <span className="text-caption text-text-tertiary tabular-nums">{formatDueDate(task.due_date)}</span>
      </div>
    </div>
  );
}

function coordinatorCardText(task: WorkItem): string {
  if (task.status === 'completed') return 'Coordinator 已交付';
  if (task.status === 'blocked') return 'Coordinator 等待介入';
  if (task.status === 'in_progress') return 'Coordinator 执行中';
  return 'Coordinator 接取中';
}

function coordinatorCardClass(task: WorkItem): string {
  if (task.status === 'completed') return 'bg-status-success/10 text-status-success border-status-success/20';
  if (task.status === 'blocked') return 'bg-status-error/10 text-status-error border-status-error/20';
  if (task.status === 'in_progress') return 'bg-brand-primary/10 text-brand-accent border-brand-primary/20';
  return 'bg-status-warning/10 text-status-warning border-status-warning/20';
}
