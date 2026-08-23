import { ChevronDown, KanbanSquare, List, Plus } from 'lucide-react';
import { useEffect, useState } from 'react';
import { useSearchParams } from 'react-router-dom';
import { ApiError } from '../api/client';
import { acceptWorkItem, unblockWorkItem } from '../api/endpoints';
import type { Priority, WorkItem, WorkItemStatus } from '../api/types';
import { Avatar } from '../components/avatar';
import { PriorityBadge } from '../components/priority-badge';
import { useAgentsStore } from '../stores/agents.store';
import { useTasksStore, type ViewMode } from '../stores/tasks.store';
import { toast } from '../stores/toast.store';
import { formatDueDate } from '../utils/format';
import { BlockTaskModal } from './tasks/block-modal';
import { CreateTaskModal } from './tasks/create-task-modal';
import { TaskDetail } from './tasks/task-detail';

const COLUMNS: { id: WorkItemStatus; title: string }[] = [
  { id: 'todo', title: '待办' },
  { id: 'in_progress', title: '进行中' },
  { id: 'completed', title: '完成' },
  { id: 'blocked', title: '阻塞' },
];

/** 任务看板：四列 + 看板/列表切换 + 筛选 + 拖拽移动（协议 §4.2 状态机）。 */
export default function TasksPage() {
  const items = useTasksStore((s) => s.items);
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

  return (
    <div className="layout-safe flex-1 min-h-0 flex flex-col py-comfortable">
      {/* Header */}
      <div className="flex items-center justify-between mb-stack-md shrink-0">
        <div className="flex items-center gap-comfortable">
          <h2 className="text-h2 text-text-primary tracking-tight">任务看板</h2>
          <div className="flex bg-surface-base rounded-button p-1 border border-border-subtle">
            <button
              onClick={() => switchView('kanban')}
              className={`flex items-center gap-1.5 px-3 py-1.5 rounded-sm text-body font-medium transition-colors ${
                viewMode === 'kanban'
                  ? 'bg-white shadow-sm text-text-primary'
                  : 'text-text-secondary hover:text-text-primary'
              }`}
            >
              <KanbanSquare className="w-4 h-4" />
              看板
            </button>
            <button
              onClick={() => switchView('list')}
              className={`flex items-center gap-1.5 px-3 py-1.5 rounded-sm text-body font-medium transition-colors ${
                viewMode === 'list'
                  ? 'bg-white shadow-sm text-text-primary'
                  : 'text-text-secondary hover:text-text-primary'
              }`}
            >
              <List className="w-4 h-4" />
              列表
            </button>
          </div>
        </div>

        <div className="flex items-center gap-snug">
          <span className="text-body text-text-secondary">筛选</span>
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
      </div>

      {/* 看板 / 列表 */}
      <div className="flex-1 min-h-0 overflow-x-auto pb-6">
        {viewMode === 'kanban' ? (
          <div className="flex gap-4 h-full">
            {columns.map((col) => (
              <KanbanColumn
                key={col.id}
                title={col.title}
                tasks={col.tasks}
                agents={agents}
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
          <div className="bg-surface-raised rounded-xl shadow-level-1 border border-border-subtle p-comfortable h-full overflow-y-auto">
            <div className="space-y-stack-md">
              {columns.map((col) => (
                <div key={col.id}>
                  <h3 className="font-semibold text-body-lg text-text-primary mb-4 flex items-center gap-2">
                    {col.title}
                    <span className="text-text-tertiary text-body font-normal tabular-nums">
                      {col.tasks.length}
                    </span>
                  </h3>
                  <div className="space-y-2">
                    {col.tasks.map((task) => (
                      <div
                        key={task.id}
                        onClick={() => selectTask(task.id)}
                        className="flex items-center justify-between p-3 border border-border-subtle rounded-lg hover:bg-surface-base transition-colors cursor-pointer"
                      >
                        <div className="flex items-center gap-4">
                          <PriorityBadge priority={task.priority} />
                          <span
                            className={`font-medium text-sm ${
                              task.status === 'completed'
                                ? 'line-through opacity-60'
                                : 'text-text-primary'
                            }`}
                          >
                            {task.title}
                          </span>
                        </div>
                        <div className="flex items-center gap-6">
                          <AssigneeTag agentId={task.agent_profile_id} agents={agents} size={20} />
                          <span className="text-xs text-text-tertiary tabular-nums w-12 text-right">
                            {formatDueDate(task.due_date)}
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
      </div>

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
    </div>
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
    <label className="relative flex items-center gap-1 px-3 py-1.5 bg-surface-raised border border-border-subtle rounded-button text-body text-text-primary hover:bg-surface-base transition-colors cursor-pointer">
      <span className={value ? '' : 'text-text-tertiary'}>
        {value ? options.find((o) => o.value === value)?.label ?? label : label}
      </span>
      <ChevronDown className="w-4 h-4 text-text-tertiary" />
      <select
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
  onDropTask,
  onCreate,
  onOpen,
}: {
  title: string;
  tasks: WorkItem[];
  agents: ReturnType<typeof useAgentsStore.getState>['agents'];
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
      className={`flex-1 min-w-[240px] flex flex-col rounded-xl transition-colors ${
        dragOver ? 'bg-brand-primary/5 ring-2 ring-brand-primary/40' : 'bg-surface-sunken'
      }`}
    >
      <div className="p-4 flex items-center justify-between shrink-0">
        <div className="flex items-center gap-2">
          <h3 className="font-semibold text-body-lg text-text-primary">{title}</h3>
          <span className="inline-flex items-center justify-center px-2 py-0.5 rounded-full bg-surface-raised border border-border-subtle text-caption font-medium text-text-secondary tabular-nums">
            {tasks.length}
          </span>
        </div>
        <button
          onClick={onCreate}
          title="创建任务"
          className="p-1 hover:bg-surface-base rounded text-text-tertiary hover:text-text-primary transition-colors"
        >
          <Plus className="w-5 h-5" />
        </button>
      </div>

      <div className="flex-1 overflow-y-auto px-4 pb-4 space-y-3">
        {tasks.map((task) => (
          <TaskCard key={task.id} task={task} agents={agents} onOpen={() => onOpen(task.id)} />
        ))}
      </div>
    </div>
  );
}

function TaskCard({
  task,
  agents,
  onOpen,
}: {
  task: WorkItem;
  agents: ReturnType<typeof useAgentsStore.getState>['agents'];
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
      className={`rounded-lg p-3 shadow-card border border-border-subtle cursor-pointer transition-all duration-200 hover:-translate-y-[2px] hover:shadow-level-2 ${bgClass}`}
    >
      <h4
        className={`font-medium text-sm text-text-primary mb-3 leading-snug ${
          isCompleted ? 'line-through opacity-60' : ''
        }`}
      >
        {task.title}
      </h4>

      <div className="mb-3">
        <AssigneeTag agentId={task.agent_profile_id} agents={agents} size={20} />
      </div>

      {isBlocked && task.blocker && (
        <div className="text-xs font-medium text-status-error mb-3">{task.blocker.message}</div>
      )}

      {task.status === 'in_progress' && task.phase === 'review' && (
        <div className="text-xs font-medium text-brand-accent mb-3">待验收</div>
      )}

      <div className="flex items-center justify-between mt-auto">
        <PriorityBadge priority={task.priority} />
        <span className="text-xs text-text-tertiary tabular-nums">{formatDueDate(task.due_date)}</span>
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
          <span className="text-[10px] text-text-tertiary">?</span>
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
