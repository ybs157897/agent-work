import {
  Calendar,
  ChevronDown,
  GitBranch,
  KanbanSquare,
  LayoutList,
  Plus,
  Search,
  SignalHigh,
} from 'lucide-react';
import { useEffect, useMemo, useState } from 'react';
import { useNavigate, useSearchParams } from 'react-router-dom';
import type { Priority, WorkItem, WorkItemStatus } from '../api/types';
import { KanbanSkeleton, ListSkeleton } from '../components/ui';
import { useTasksStore, type ViewMode } from '../stores/tasks.store';
import { childCountByParent, sortTasksTree } from '../utils/task-tree';
import { isAwaitingAcceptance } from '../utils/task-phase';
import { formatDueDate } from '../utils/format';
import { CreateTaskModal } from './tasks/create-task-modal';

// 树工具实现归 utils/task-tree（task-detail/创建弹窗共用）；此处转出供测试与页面使用。
export { sortTasksTree, childCountByParent } from '../utils/task-tree';

export const TASK_STATUS_COLUMNS: { id: WorkItemStatus; title: string; dot: string; ring: string }[] = [
  { id: 'todo', title: '待办', dot: 'bg-status-standby', ring: 'ring-status-standby/30' },
  { id: 'in_progress', title: '进行中', dot: 'bg-brand-primary', ring: 'ring-brand-primary/25' },
  { id: 'completed', title: '完成', dot: 'bg-status-success', ring: 'ring-status-success/25' },
  { id: 'blocked', title: '阻塞', dot: 'bg-status-error', ring: 'ring-status-error/25' },
  { id: 'cancelled', title: '已取消', dot: 'bg-status-standby', ring: 'ring-status-standby/30' },
];

/** 看板只追踪用户发布的总任务；带 parent_id 的派生任务只进入总任务详情。 */
export function rootTasks(items: readonly WorkItem[]): WorkItem[] {
  return items.filter((item) => !item.parent_id);
}

const PRIORITY_DOT: Record<Priority, string> = {
  low: 'bg-status-standby',
  medium: 'bg-status-warning',
  high: 'bg-status-error',
  urgent: 'bg-status-error ring-2 ring-status-error/35',
};

const PRIORITY_LABEL: Record<Priority, string> = {
  low: '低优',
  medium: '中优',
  high: '高优',
  urgent: '紧急',
};

/** 仅展示用短键（Plane 风 WI-XXXXXX）；不改后端 id，不参与协议。 */
function workItemKey(id: string): string {
  const tail = id.replace(/^wi_?/i, '').slice(-6).toUpperCase();
  return `WI-${tail || '------'}`;
}

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
  const [query, setQuery] = useState('');

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

  const rootItems = useMemo(() => rootTasks(items), [items]);
  const visibleItems = useMemo(() => {
    const normalized = query.trim().toLocaleLowerCase();
    if (!normalized) return rootItems;
    return rootItems.filter((item) =>
      item.id.toLocaleLowerCase().includes(normalized)
      || item.title.toLocaleLowerCase().includes(normalized)
      || item.description.toLocaleLowerCase().includes(normalized));
  }, [query, rootItems]);

  const columns = TASK_STATUS_COLUMNS.map((c) => ({
    ...c,
    tasks: visibleItems.filter((t) => t.status === c.id),
  }));
  const childCounts = useMemo(() => childCountByParent(items), [items]);

  return (
    <main className="plane-board flex min-h-0 flex-1 flex-col">
      <div className="plane-board-toolbar shrink-0">
        <div className="flex flex-wrap items-center justify-between gap-snug px-base py-snug">
          <div className="flex min-w-0 flex-wrap items-center gap-snug">
            <h2 className="font-zh text-body-lg font-semibold tracking-tight text-text-primary">任务</h2>
            <span className="plane-board-count">{visibleItems.length}</span>

            <div className="plane-board-view-toggle ml-tight" role="group" aria-label="任务视图">
              <button
                type="button"
                onClick={() => switchView('kanban')}
                aria-pressed={viewMode === 'kanban'}
                title="看板"
                className="plane-board-view-btn focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-brand-primary/40"
              >
                <KanbanSquare className="h-3.5 w-3.5" aria-hidden />
                看板
              </button>
              <button
                type="button"
                onClick={() => switchView('list')}
                aria-pressed={viewMode === 'list'}
                title="列表"
                className="plane-board-view-btn focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-brand-primary/40"
              >
                <LayoutList className="h-3.5 w-3.5" aria-hidden />
                列表
              </button>
            </div>
          </div>

          <div className="flex flex-wrap items-center gap-tight">
            <label className="relative inline-flex h-8 w-48 items-center sm:w-56">
              <Search className="pointer-events-none absolute left-snug h-3.5 w-3.5 text-text-tertiary" aria-hidden />
              <input
                value={query}
                onChange={(event) => setQuery(event.target.value)}
                placeholder="搜索任务"
                aria-label="搜索任务"
                className="h-8 w-full rounded-button border border-border-subtle bg-surface-raised py-tight pl-8 pr-snug text-caption text-text-primary outline-none transition-colors placeholder:text-text-tertiary focus:border-brand-primary/40 focus:ring-2 focus:ring-brand-primary/20"
              />
            </label>
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
            <button
              type="button"
              onClick={() => setCreateOpen(true)}
              className="plane-board-add focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-brand-primary/40"
            >
              <Plus className="h-3.5 w-3.5" aria-hidden />
              新建任务
            </button>
          </div>
        </div>
      </div>

      <section aria-label="任务看板" className="plane-board-canvas min-h-0 flex-1 overflow-auto">
        {!loaded ? (
          <div className="h-full p-snug">{viewMode === 'kanban' ? <KanbanSkeleton /> : <ListSkeleton />}</div>
        ) : viewMode === 'kanban' ? (
          <div className="flex h-full min-h-[32rem] gap-base overflow-x-auto px-base py-snug">
            {columns.map((col) => (
              <KanbanColumn
                key={col.id}
                title={col.title}
                dotClass={col.dot}
                ringClass={col.ring}
                tasks={col.tasks}
                childCounts={childCounts}
                onCreate={col.id === 'todo' ? () => setCreateOpen(true) : undefined}
                onOpen={(id) => navigate(`/tasks/${id}`)}
              />
            ))}
          </div>
        ) : (
          <div className="px-base py-snug">
            <div className="plane-board-list-wrap">
              <div className="plane-board-list-head">
                <span>任务</span>
                <span>截止时间</span>
                <span className="text-right">优先级</span>
              </div>
              {columns.map((col) => (
                <div key={col.id}>
                  <div className="plane-board-list-group-head">
                    <span className={`h-2.5 w-2.5 rounded-full ring-4 ${col.dot} ${col.ring}`} aria-hidden />
                    <h3 className="text-caption font-semibold text-text-primary">{col.title}</h3>
                    <span className="plane-board-count">{col.tasks.length}</span>
                  </div>
                  {col.tasks.length === 0 ? (
                    <div className="px-base py-snug text-caption text-text-tertiary">暂无任务</div>
                  ) : (
                    <ul>
                      {sortTasksTree(col.tasks).map((entry) => (
                        <li key={entry.item.id}>
                          <button
                            type="button"
                            onClick={() => navigate(`/tasks/${entry.item.id}`)}
                            aria-label={`打开任务 ${entry.item.title}，状态 ${col.title}，截止时间 ${formatDueDate(entry.item.due_date)}，优先级 ${PRIORITY_LABEL[entry.item.priority]}`}
                            className="plane-board-list-row"
                          >
                            <div className="flex min-w-0 items-center gap-tight" style={{ paddingLeft: entry.depth * 20 }}>
                              {entry.depth > 0 && <GitBranch className="h-3.5 w-3.5 shrink-0 text-text-tertiary" aria-hidden />}
                              <span className="plane-board-card-key shrink-0">{workItemKey(entry.item.id)}</span>
                              <span
                                className={`truncate text-[13px] font-medium ${
                                  entry.item.status === 'completed' ? 'text-text-tertiary line-through' : 'text-text-primary'
                                }`}
                              >
                                {entry.item.title}
                              </span>
                            </div>
                            <span className="text-caption tabular-nums text-text-tertiary">{formatDueDate(entry.item.due_date)}</span>
                            <div className="flex items-center justify-end gap-1">
                              <span className={`h-2 w-2 rounded-full ${PRIORITY_DOT[entry.item.priority]}`} aria-hidden />
                              <span className="text-caption text-text-secondary">{PRIORITY_LABEL[entry.item.priority]}</span>
                            </div>
                          </button>
                        </li>
                      ))}
                    </ul>
                  )}
                </div>
              ))}
            </div>
          </div>
        )}
      </section>

      <CreateTaskModal open={createOpen} onClose={() => setCreateOpen(false)} />
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
    <label className="plane-board-chip relative cursor-pointer transition-colors hover:bg-surface-sunken focus-within:ring-2 focus-within:ring-brand-primary/40">
      <SignalHigh className="h-3.5 w-3.5 text-text-tertiary" aria-hidden />
      <span className={value ? 'font-medium text-text-primary' : 'plane-board-chip-muted'}>
        {value ? options.find((o) => o.value === value)?.label ?? label : label}
      </span>
      <ChevronDown className="h-3.5 w-3.5 text-text-tertiary" aria-hidden />
      <select
        aria-label={label}
        value={value}
        onChange={(e) => onChange(e.target.value)}
        className="absolute inset-0 cursor-pointer opacity-0"
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
  dotClass,
  ringClass,
  tasks,
  childCounts,
  onCreate,
  onOpen,
}: {
  title: string;
  dotClass: string;
  ringClass: string;
  tasks: WorkItem[];
  childCounts: Map<string, number>;
  onCreate?: () => void;
  onOpen: (taskId: string) => void;
}) {
  return (
    <div className="plane-board-column">
      <div className="plane-board-column-header">
        <div className="flex min-w-0 items-center gap-tight">
          <span className={`h-2.5 w-2.5 shrink-0 rounded-full ring-4 ${dotClass} ${ringClass}`} aria-hidden />
          <h3 className="truncate text-[13px] font-semibold text-text-primary">{title}</h3>
          <span className="plane-board-count">{tasks.length}</span>
        </div>
        {onCreate && (
          <button
            type="button"
            onClick={onCreate}
            aria-label="发布任务"
            title="发布任务"
            className="inline-flex h-7 w-7 items-center justify-center rounded-button text-text-tertiary transition-colors hover:bg-surface-raised hover:text-text-primary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-brand-primary/40"
          >
            <Plus className="h-4 w-4" aria-hidden />
          </button>
        )}
      </div>

      <div className="flex flex-1 flex-col gap-tight overflow-y-auto pb-base">
        {tasks.map((task) => (
          <TaskCard
            key={task.id}
            task={task}
            childCount={childCounts.get(task.id) ?? 0}
            onOpen={() => onOpen(task.id)}
          />
        ))}
        {tasks.length === 0 && (
          <div className="plane-board-empty">
            <p className="font-medium text-text-secondary">暂无任务</p>
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
  const due = formatDueDate(task.due_date);

  return (
    <button
      type="button"
      onClick={onOpen}
      aria-label={`打开任务 ${task.title}，状态 ${TASK_STATUS_COLUMNS.find((column) => column.id === task.status)?.title ?? task.status}，截止时间 ${due}，优先级 ${PRIORITY_LABEL[task.priority]}`}
      className={`plane-board-card${isBlocked ? ' plane-board-card-blocked' : ''}`}
    >
      <div className="flex items-center justify-between gap-tight">
        <span className="plane-board-card-key">{workItemKey(task.id)}</span>
        {isAwaitingAcceptance(task) && (
          <span
            title="run 成功待评审或评估通过，等待人工验收"
            className="rounded-button bg-brand-primary/10 px-1.5 py-px text-[11px] font-medium text-brand-accent"
          >
            待验收
          </span>
        )}
      </div>

      <span className={`plane-board-card-title${isCompleted ? ' is-done' : ''}`}>{task.title}</span>

      {isBlocked && task.blocker && (
        <p className="mb-snug line-clamp-2 text-[11px] text-status-error">{task.blocker.message}</p>
      )}

      <div className="plane-board-card-props">
        <span className="inline-flex items-center gap-1" title={PRIORITY_LABEL[task.priority]}>
          <span className={`h-2 w-2 rounded-full ${PRIORITY_DOT[task.priority]}`} aria-hidden />
          {PRIORITY_LABEL[task.priority]}
        </span>
        {due !== '—' && (
          <span className="inline-flex items-center gap-1 tabular-nums">
            <Calendar className="h-3 w-3 text-text-tertiary" aria-hidden />
            {due}
          </span>
        )}
        {childCount > 0 && (
          <span title={`${childCount} 个直接子任务`} className="inline-flex items-center gap-0.5 tabular-nums">
            <GitBranch className="h-3 w-3 text-text-tertiary" aria-hidden />
            {childCount}
          </span>
        )}
      </div>
    </button>
  );
}
