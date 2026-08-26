import { ChevronDown, GitBranch, KanbanSquare, List, Lock, Plus } from 'lucide-react';
import { useEffect, useMemo, useState } from 'react';
import { useSearchParams } from 'react-router-dom';
import { ApiError } from '../api/client';
import { acceptWorkItem, unblockWorkItem } from '../api/endpoints';
import type { Priority, WorkItem, WorkItemStatus } from '../api/types';
import { Avatar } from '../components/avatar';
import { PriorityBadge } from '../components/priority-badge';
import { KanbanSkeleton, ListSkeleton } from '../components/ui';
import { useAgentsStore } from '../stores/agents.store';
import { useTasksStore, type ViewMode } from '../stores/tasks.store';
import { toast } from '../stores/toast.store';
import { childCountByParent, sortTasksTree } from '../utils/task-tree';
import { isAwaitingAcceptance } from '../utils/task-phase';
import { formatDueDate } from '../utils/format';
import { BlockTaskModal } from './tasks/block-modal';
import { CreateTaskModal } from './tasks/create-task-modal';
import { TaskDetail } from './tasks/task-detail';

// 树工具实现归 utils/task-tree（task-detail/创建弹窗共用）；此处转出供测试与页面使用。
export { sortTasksTree, childCountByParent } from '../utils/task-tree';

const COLUMNS: { id: WorkItemStatus; title: string }[] = [
  { id: 'todo', title: '待办' },
  { id: 'in_progress', title: '进行中' },
  { id: 'completed', title: '完成' },
  { id: 'blocked', title: '阻塞' },
];

/** 任务看板：四列 + 看板/列表切换 + 筛选 + 拖拽移动（协议 §4.2 状态机）。 */
export default function TasksPage() {
  const items = useTasksStore((s) => s.items);
  const loaded = useTasksStore((s) => s.loaded);
  const filter = useTasksStore((s) => s.filter);
  const setFilter = useTasksStore((s) => s.setFilter);
  const viewMode = useTasksStore((s) => s.viewMode);
  const setViewMode = useTasksStore((s) => s.setViewMode);
  const selectedTaskId = useTasksStore((s) => s.selectedTaskId);
  const selectTask = useTasksStore((s) => s.selectTask);
  const moveOptimistic = useTasksStore((s) => s.moveOptimistic);
  const upsert = useTasksStore((s) => s.upsert);
  const refresh = useTasksStore((s) => s.refresh);
  const agents = useAgentsStore((s) => s.agents);

  const [searchParams, setSearchParams] = useSearchParams();
  const [createFor, setCreateFor] = useState<WorkItemStatus | null>(null);
  const [blocking, setBlocking] = useState<WorkItem | null>(null);

  // viewMode 是本地 UI 状态：放 URL，不进后端（协议 §4.1）。
  useEffect(() => {
    const v = searchParams.get('view');
    if (v === 'kanban' || v === 'list') setViewMode(v);
  }, [searchParams, setViewMode]);

  const switchView = (m: ViewMode) => {
    setViewMode(m);
    setSearchParams(m === 'kanban' ? {} : { view: m }, { replace: true });
  };

  /** 拖拽/按钮触发的状态迁移：按协议选择命令（move / block / unblock / accept）。 */
  const transitionTask = async (item: WorkItem, to: WorkItemStatus) => {
    if (item.status === to) return;
    try {
      if (to === 'blocked') {
        if (item.status !== 'in_progress') {
          toast.error('只有进行中的任务才能标记阻塞');
          return;
        }
        setBlocking(item);
        return;
      }
      if (item.status === 'blocked') {
        if (to !== 'in_progress') {
          toast.error('阻塞任务只能解除阻塞回到进行中');
          return;
        }
        upsert(await unblockWorkItem(item.id, item.version));
        toast.success(`已解除阻塞「${item.title}」`);
        return;
      }
      if (to === 'completed') {
        if (item.phase === 'review' || item.phase === 'acceptance') {
          upsert(await acceptWorkItem(item.id, item.version));
          toast.success(`任务「${item.title}」验收通过`);
        } else {
          toast.error('任务需先运行成功并进入评审，才能验收完成');
        }
        return;
      }
      if (to === 'in_progress' && item.status === 'todo') {
        await moveOptimistic(item.id, to);
        return;
      }
      if (to === 'todo') {
        toast.error('状态机不支持回退到待办');
        return;
      }
      toast.error('不支持的状态迁移');
    } catch (err) {
      if (err instanceof ApiError) {
        if (err.isVersionConflict) {
          toast.error('任务已被他人修改，已为你刷新最新状态');
          void refresh();
        } else {
          toast.error(err.message);
        }
      } else {
        toast.error('操作失败，请重试');
      }
    }
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
          <FilterSelect
            label="指派人"
            value={filter.assignee ?? ''}
            options={agents.map((a) => ({ value: a.id, label: a.name }))}
            onChange={(v) => setFilter({ ...filter, assignee: v || undefined })}
          />
        </div>
      </header>

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
                agents={agents}
                childCounts={childCounts}
                onDropTask={(taskId) => {
                  const item = items.find((t) => t.id === taskId);
                  if (item) void transitionTask(item, col.id);
                }}
                onCreate={() => setCreateFor(col.id)}
                onOpen={(id) => selectTask(id)}
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
                        onClick={() => selectTask(entry.item.id)}
                        onKeyDown={(event) => {
                          if (event.key === 'Enter' || event.key === ' ') {
                            event.preventDefault();
                            selectTask(entry.item.id);
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
                          <AssigneeTag agentId={entry.item.agent_profile_id} agents={agents} size={20} />
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

      <TaskDetail
        taskId={selectedTaskId}
        onClose={() => selectTask(null)}
        onTransition={transitionTask}
      />
      <CreateTaskModal
        open={createFor !== null}
        // blocked 列的 "+" 创建为 todo（blocked 只能经 block 命令进入）。
        initialStatus={createFor === 'blocked' ? 'todo' : createFor ?? 'todo'}
        onClose={() => setCreateFor(null)}
      />
      <BlockTaskModal task={blocking} onClose={() => setBlocking(null)} />
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
  agents,
  childCounts,
  onDropTask,
  onCreate,
  onOpen,
}: {
  title: string;
  tasks: WorkItem[];
  agents: ReturnType<typeof useAgentsStore.getState>['agents'];
  childCounts: Map<string, number>;
  onDropTask: (taskId: string) => void;
  onCreate: () => void;
  onOpen: (taskId: string) => void;
}) {
  const [dragOver, setDragOver] = useState(false);

  return (
    <div
      onDragOver={(e) => {
        e.preventDefault();
        setDragOver(true);
      }}
      onDragLeave={() => setDragOver(false)}
      onDrop={(e) => {
        e.preventDefault();
        setDragOver(false);
        const taskId = e.dataTransfer.getData('text/plain');
        if (taskId) onDropTask(taskId);
      }}
      className={`flex-1 min-w-0 flex flex-col rounded-card transition-colors ${
        dragOver ? 'bg-brand-primary/5 ring-2 ring-brand-primary/40' : 'bg-surface-sunken'
      }`}
    >
      <div className="flex items-center justify-between px-base py-snug shrink-0">
        <div className="flex items-center gap-2">
          <h3 className="font-semibold text-body-lg text-text-primary">{title}</h3>
          <span className="inline-flex items-center justify-center px-2 py-0.5 rounded-full bg-surface-raised border border-border-subtle text-caption font-medium text-text-secondary tabular-nums">
            {tasks.length}
          </span>
        </div>
        <button
          type="button"
          onClick={onCreate}
          aria-label={`在${title}列创建任务`}
          title="创建任务"
          className="inline-flex h-8 w-8 items-center justify-center rounded-button text-text-tertiary transition-colors hover:bg-surface-base hover:text-text-primary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-brand-primary/40"
        >
          <Plus className="w-5 h-5" />
        </button>
      </div>

      <div className="flex-1 overflow-y-auto px-snug pb-snug space-y-snug">
        {tasks.map((task) => (
          <TaskCard
            key={task.id}
            task={task}
            agents={agents}
            childCount={childCounts.get(task.id) ?? 0}
            onOpen={() => onOpen(task.id)}
          />
        ))}
        {tasks.length === 0 && (
          <div className="rounded-card border border-dashed border-border-strong/60 px-snug py-comfortable text-center text-caption text-text-tertiary">
            <p>此列暂无案牍</p>
            <p className="mt-1">拖入任务，或使用右上角加号创建</p>
          </div>
        )}
      </div>
    </div>
  );
}

function TaskCard({
  task,
  agents,
  childCount,
  onOpen,
}: {
  task: WorkItem;
  agents: ReturnType<typeof useAgentsStore.getState>['agents'];
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
      draggable
      onDragStart={(e) => e.dataTransfer.setData('text/plain', task.id)}
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
        <AssigneeTag agentId={task.agent_profile_id} agents={agents} size={20} />
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

function AssigneeTag({
  agentId,
  agents,
  size,
}: {
  agentId: string | undefined;
  agents: { id: string; name: string; avatar?: string }[];
  size: number;
}) {
  const agent = agentId ? agents.find((a) => a.id === agentId) : undefined;
  if (!agent) {
    return (
      <div className="flex items-center gap-1.5">
        <div
          className="rounded-full border border-dashed border-border-strong flex items-center justify-center bg-surface-base"
          style={{ width: size, height: size }}
        >
          <span className="text-[11px] text-text-tertiary">?</span>
        </div>
        <span className="text-xs text-text-tertiary">无 Agent</span>
      </div>
    );
  }
  return (
    <div className="flex items-center gap-1.5">
      <Avatar name={agent.name} url={agent.avatar} size={size} />
      <span className="text-xs font-medium text-text-secondary">{agent.name}</span>
    </div>
  );
}
