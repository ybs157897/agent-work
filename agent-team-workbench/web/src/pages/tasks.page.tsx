import {
  Calendar,
  CircleAlert,
  ChevronDown,
  GitBranch,
  KanbanSquare,
  LayoutList,
  Plus,
  RefreshCw,
  Search,
  SignalHigh,
  X,
} from 'lucide-react';
import { useEffect, useMemo, useState, type CSSProperties } from 'react';
import { useLocation, useNavigate, useSearchParams } from 'react-router-dom';
import type { AgentProfile, Priority, WorkItem } from '../api/types';
import { Avatar } from '../components/avatar';
import { KanbanSkeleton, ListSkeleton } from '../components/ui';
import { useAgentsStore } from '../stores/agents.store';
import { useTasksStore, type ViewMode } from '../stores/tasks.store';
import { isUserManagedAgent } from '../utils/agent-scope';
import { childCountByParent, sortTasksTree, type TaskTreeEntry } from '../utils/task-tree';
import { isAwaitingAcceptance, taskBoardLane, taskBoardLaneExplanation, type TaskBoardLane } from '../utils/task-phase';
import { formatDueDate } from '../utils/format';
import { taskPeekTarget } from '../utils/task-peek';
import { CreateTaskModal } from './tasks/create-task-modal';

// 树工具实现归 utils/task-tree（task-detail/创建弹窗共用）；此处转出供测试与页面使用。
export { sortTasksTree, childCountByParent } from '../utils/task-tree';

export const TASK_STATUS_COLUMNS: { id: TaskBoardLane; title: string; dot: string; ring: string }[] = [
  { id: 'todo', title: '待办', dot: 'bg-status-standby', ring: 'ring-status-standby/30' },
  { id: 'in_progress', title: '进行中', dot: 'bg-brand-primary', ring: 'ring-brand-primary/25' },
  { id: 'awaiting_acceptance', title: '待验收', dot: 'bg-status-warning', ring: 'ring-status-warning/25' },
  { id: 'completed', title: '完成', dot: 'bg-status-success', ring: 'ring-status-success/25' },
  { id: 'blocked', title: '阻塞', dot: 'bg-status-error', ring: 'ring-status-error/25' },
  { id: 'cancelled', title: '已取消', dot: 'bg-status-standby', ring: 'ring-status-standby/30' },
];

/** 看板只追踪用户发布的总任务；带 parent_id 的派生任务只进入总任务详情。 */
export function rootTasks(items: readonly WorkItem[]): WorkItem[] {
  return items.filter((item) => !item.parent_id);
}

/**
 * 总任务卡片的参与者摘要。Plan 会把 Worker 固化到派生任务的
 * agent_profile_id；兼容直接绑定 Worker 的历史根任务，排除系统 Agent 后去重。
 */
export function taskParticipantsByRoot(
  items: readonly WorkItem[],
  agents: readonly AgentProfile[],
): Map<string, AgentProfile[]> {
  const itemsById = new Map(items.map((item) => [item.id, item]));
  const agentsById = new Map(agents.filter(isUserManagedAgent).map((agent) => [agent.id, agent]));
  const participants = new Map<string, AgentProfile[]>();
  const seenByRoot = new Map<string, Set<string>>();
  const orderedItems = [...items].sort((left, right) =>
    left.created_at.localeCompare(right.created_at) || left.id.localeCompare(right.id));

  for (const item of orderedItems) {
    if (!item.agent_profile_id) continue;
    const agent = agentsById.get(item.agent_profile_id);
    if (!agent) continue;

    const rootId = item.parent_id ? taskRootId(item, itemsById) : item.id;
    if (!rootId) continue;
    const seen = seenByRoot.get(rootId) ?? new Set<string>();
    if (seen.has(agent.id)) continue;
    seen.add(agent.id);
    seenByRoot.set(rootId, seen);
    participants.set(rootId, [...(participants.get(rootId) ?? []), agent]);
  }
  return participants;
}

function taskRootId(item: WorkItem, itemsById: ReadonlyMap<string, WorkItem>): string | undefined {
  const seen = new Set([item.id]);
  let current = item;
  for (let depth = 0; depth < 100 && current.parent_id; depth += 1) {
    if (seen.has(current.parent_id)) return undefined;
    seen.add(current.parent_id);
    const parent = itemsById.get(current.parent_id);
    if (!parent || parent.workspace_id !== item.workspace_id || parent.record_kind !== 'task') return undefined;
    if (!parent.parent_id) return parent.id;
    current = parent;
  }
  return undefined;
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
export function workItemKey(id: string): string {
  const tail = id.replace(/^wi_?/i, '').slice(-6).toUpperCase();
  return `WI-${tail || '------'}`;
}

/** 搜索同时覆盖后端 id、Plane 短编号和任务文本。 */
export function matchesTaskQuery(task: Pick<WorkItem, 'id' | 'title' | 'description'>, query: string): boolean {
  const normalized = query.trim().toLocaleLowerCase();
  if (!normalized) return true;
  return [task.id, workItemKey(task.id), task.title, task.description]
    .some((value) => value.toLocaleLowerCase().includes(normalized));
}

/** 任务入口：看板只做观察与导航；Coordinator 管理状态，不允许看板拖拽改状态。 */
export default function TasksPage() {
  const items = useTasksStore((s) => s.items);
  const agents = useAgentsStore((s) => s.agents);
  const loaded = useTasksStore((s) => s.loaded);
  const loading = useTasksStore((s) => s.loading);
  const taskError = useTasksStore((s) => s.error);
  const refreshTasks = useTasksStore((s) => s.refresh);
  const filter = useTasksStore((s) => s.filter);
  const setFilter = useTasksStore((s) => s.setFilter);
  const viewMode = useTasksStore((s) => s.viewMode);
  const setViewMode = useTasksStore((s) => s.setViewMode);
  const location = useLocation();
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
  const childCounts = useMemo(() => childCountByParent(items), [items]);
  const participantsByRoot = useMemo(() => taskParticipantsByRoot(items, agents), [agents, items]);
  const visibleItems = useMemo(() => rootItems.filter((item) => {
    if (filter.priority && item.priority !== filter.priority) return false;
    if (filter.assignee) {
      const participantIds = participantsByRoot.get(item.id)?.map((agent) => agent.id) ?? [];
      if (item.agent_profile_id !== filter.assignee && !participantIds.includes(filter.assignee)) return false;
    }
    return matchesTaskQuery(item, query);
  }), [filter.assignee, filter.priority, participantsByRoot, query, rootItems]);

  const columns = TASK_STATUS_COLUMNS.map((c) => ({
    ...c,
    tasks: visibleItems.filter((task) => taskBoardLane(task) === c.id),
  }));
  const emptyColumns = columns.filter((column) => column.tasks.length === 0);
  const [showEmptyColumns, setShowEmptyColumns] = useState(false);
  const displayedColumns = showEmptyColumns ? columns : columns.filter((column) => column.tasks.length > 0);
  const hasActiveFilter = Boolean(query.trim() || filter.priority || filter.assignee);
  const hasSearchNoResults = loaded && !taskError && hasActiveFilter && visibleItems.length === 0;
  const hasNoTasks = loaded && !taskError && !hasActiveFilter && rootItems.length === 0;
  const clearSearchAndFilters = () => {
    setQuery('');
    setFilter({});
  };
  const openTask = (taskId: string) => {
    navigate(taskPeekTarget(taskId, location.search), {
      state: { backgroundLocation: location },
    });
  };

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
            <label className="plane-board-search relative inline-flex h-8 w-48 items-center sm:w-56">
              <Search className="pointer-events-none absolute left-snug h-3.5 w-3.5 text-text-tertiary" aria-hidden />
              <input
                value={query}
                onChange={(event) => setQuery(event.target.value)}
                placeholder="搜索任务或 WI 编号"
                aria-label="搜索任务"
                className="h-8 w-full rounded-button border border-border-subtle bg-surface-raised py-tight pl-8 pr-8 text-caption text-text-primary outline-none transition-colors placeholder:text-text-secondary focus:border-brand-primary/40 focus:ring-2 focus:ring-brand-primary/20"
              />
              {query && (
                <button
                  type="button"
                  onClick={() => setQuery('')}
                  aria-label="清空搜索"
                  title="清空搜索"
                  className="absolute right-1 inline-flex h-6 w-6 items-center justify-center rounded-button text-text-tertiary transition-colors hover:bg-surface-sunken hover:text-text-primary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-brand-primary/40"
                >
                  <X className="h-3.5 w-3.5" aria-hidden />
                </button>
              )}
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
            {hasActiveFilter && (
              <button
                type="button"
                onClick={clearSearchAndFilters}
                className="plane-board-clear focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-brand-primary/40"
              >
                <X className="h-3.5 w-3.5" aria-hidden />
                清空筛选
              </button>
            )}
            {emptyColumns.length > 0 && (
              <button
                type="button"
                onClick={() => setShowEmptyColumns((shown) => !shown)}
                aria-pressed={showEmptyColumns}
                className="plane-board-empty-toggle focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-brand-primary/40"
              >
                {showEmptyColumns ? '收起空列' : `显示空列（${emptyColumns.length}）`}
              </button>
            )}
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
        {taskError && (
          <TaskBoardFeedback
            tone="error"
            message={rootItems.length > 0 ? `任务列表未完全加载：${taskError}` : taskError}
            actionLabel="重试加载"
            onAction={() => void refreshTasks()}
            disabled={loading}
          />
        )}
        {!taskError && loading && loaded && (
          <TaskBoardFeedback
            tone="info"
            message="正在同步全部任务…"
            actionLabel="刷新"
            onAction={() => void refreshTasks()}
            disabled
          />
        )}
        {!loaded && !taskError ? (
          <div className="h-full p-snug">{viewMode === 'kanban' ? <KanbanSkeleton /> : <ListSkeleton />}</div>
        ) : hasSearchNoResults ? (
          <div className="px-base py-snug">
            <EmptyLaneIndex
              columns={columns}
              showEmptyColumns={showEmptyColumns}
              onShowEmpty={() => setShowEmptyColumns(true)}
            />
            <EmptyTaskSearchState onClear={clearSearchAndFilters} />
          </div>
        ) : hasNoTasks ? (
          <div className="px-base py-snug">
            <EmptyLaneIndex
              columns={columns}
              showEmptyColumns={showEmptyColumns}
              onShowEmpty={() => setShowEmptyColumns(true)}
            />
            <EmptyTaskState onCreate={() => setCreateOpen(true)} />
          </div>
        ) : viewMode === 'kanban' ? (
          <div className="px-base py-snug">
            <EmptyLaneIndex
              columns={columns}
              showEmptyColumns={showEmptyColumns}
              onShowEmpty={() => setShowEmptyColumns(true)}
            />
            <div
              className="plane-board-columns min-h-[32rem]"
              data-column-count={displayedColumns.length}
              style={{ '--plane-board-column-count': Math.max(1, displayedColumns.length) } as CSSProperties}
            >
              {displayedColumns.map((col) => (
                <KanbanColumn
                  key={col.id}
                  title={col.title}
                  dotClass={col.dot}
                  ringClass={col.ring}
                  tasks={col.tasks}
                  childCounts={childCounts}
                  participantsByRoot={participantsByRoot}
                  onCreate={col.id === 'todo' ? () => setCreateOpen(true) : undefined}
                  onOpen={openTask}
                />
              ))}
            </div>
          </div>
        ) : (
          <div className="px-base py-snug">
            <EmptyLaneIndex
              columns={columns}
              showEmptyColumns={showEmptyColumns}
              onShowEmpty={() => setShowEmptyColumns(true)}
            />
            <div className="plane-board-list-wrap">
              <div className="plane-board-list-head">
                <span>任务</span>
                <span>截止时间</span>
                <span>Agent</span>
                <span className="text-right">优先级</span>
              </div>
              {displayedColumns.map((col) => (
                <div key={col.id}>
                  <div className="plane-board-list-group-head">
                    <span className={`h-2.5 w-2.5 rounded-full ring-4 ${col.dot} ${col.ring}`} aria-hidden />
                    <h3 className="text-caption font-semibold text-text-primary">{col.title}</h3>
                    <span className="plane-board-count">{col.tasks.length}</span>
                  </div>
                  {col.tasks.length === 0 ? (
                    <div className="px-base py-snug text-caption text-text-secondary">暂无任务</div>
                  ) : (
                    <ul>
                      {sortTasksTree(col.tasks).map((entry) => (
                        <li key={entry.item.id}>
                          <TaskListRow
                            entry={entry}
                            laneTitle={col.title}
                            participants={participantsByRoot.get(entry.item.id) ?? []}
                            onOpen={() => openTask(entry.item.id)}
                          />
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

function TaskBoardFeedback({
  tone,
  message,
  actionLabel,
  onAction,
  disabled = false,
}: {
  tone: 'error' | 'info';
  message: string;
  actionLabel: string;
  onAction: () => void;
  disabled?: boolean;
}) {
  return (
    <div className={`plane-board-feedback plane-board-feedback-${tone}`} role={tone === 'error' ? 'alert' : 'status'} aria-live="polite">
      <div className="flex min-w-0 items-start gap-tight">
        {tone === 'error' ? <CircleAlert className="mt-px h-4 w-4 shrink-0" aria-hidden /> : <RefreshCw className="mt-px h-4 w-4 shrink-0" aria-hidden />}
        <p className="min-w-0 text-caption">{message}</p>
      </div>
      <button
        type="button"
        onClick={onAction}
        disabled={disabled}
        className="plane-board-feedback-action focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-brand-primary/40"
      >
        {actionLabel}
      </button>
    </div>
  );
}

function EmptyTaskSearchState({ onClear }: { onClear: () => void }) {
  return (
    <div className="plane-board-search-empty" role="status">
      <Search className="h-5 w-5 text-text-tertiary" aria-hidden />
      <h3 className="text-body-lg font-semibold text-text-primary">没有匹配的任务</h3>
      <p className="max-w-md text-caption text-text-secondary">试试任务标题、描述或 WI 短编号，也可以清空搜索和筛选条件。</p>
      <button
        type="button"
        onClick={onClear}
        className="plane-board-clear plane-board-clear-prominent focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-brand-primary/40"
      >
        <X className="h-3.5 w-3.5" aria-hidden />
        清空搜索和筛选
      </button>
    </div>
  );
}

function EmptyTaskState({ onCreate }: { onCreate: () => void }) {
  return (
    <div className="plane-board-search-empty" role="status">
      <KanbanSquare className="h-5 w-5 text-text-tertiary" aria-hidden />
      <h3 className="text-body-lg font-semibold text-text-primary">还没有总任务</h3>
      <p className="max-w-md text-caption text-text-secondary">创建一个总任务后，执行 Agent 的输入和最终输出会集中在任务详情里。</p>
      <button
        type="button"
        onClick={onCreate}
        className="plane-board-add focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-brand-primary/40"
      >
        <Plus className="h-3.5 w-3.5" aria-hidden />
        新建总任务
      </button>
    </div>
  );
}

function EmptyLaneIndex({
  columns,
  showEmptyColumns,
  onShowEmpty,
}: {
  columns: readonly { id: TaskBoardLane; title: string; dot: string; ring: string; tasks: WorkItem[] }[];
  showEmptyColumns: boolean;
  onShowEmpty: () => void;
}) {
  const emptyColumns = columns.filter((column) => column.tasks.length === 0);
  if (emptyColumns.length === 0 || showEmptyColumns) return null;
  return (
    <div className="plane-board-lane-index" aria-label="空状态列">
      <span className="plane-board-lane-index-label">空状态</span>
      {emptyColumns.map((column) => (
        <button
          key={column.id}
          type="button"
          onClick={onShowEmpty}
          aria-label={`显示${column.title}空列`}
          className="plane-board-lane-chip focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-brand-primary/40"
        >
          <span className={`h-2 w-2 rounded-full ${column.dot}`} aria-hidden />
          <span>{column.title}</span>
          <span className="tabular-nums text-text-secondary">0</span>
        </button>
      ))}
      <span className="plane-board-lane-index-hint">点击状态可显示空列</span>
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
  participantsByRoot,
  onCreate,
  onOpen,
}: {
  title: string;
  dotClass: string;
  ringClass: string;
  tasks: WorkItem[];
  childCounts: Map<string, number>;
  participantsByRoot: Map<string, AgentProfile[]>;
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
            participants={participantsByRoot.get(task.id) ?? []}
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

function TaskListRow({
  entry,
  laneTitle,
  participants,
  onOpen,
}: {
  entry: TaskTreeEntry;
  laneTitle: string;
  participants: AgentProfile[];
  onOpen: () => void;
}) {
  const due = formatDueDate(entry.item.due_date);
  const participantNames = participants.map((agent) => agent.name);
  const laneExplanation = taskBoardLaneExplanation(entry.item);
  return (
    <button
      type="button"
      onClick={onOpen}
      aria-label={`打开任务 ${entry.item.title}，状态 ${laneTitle}${laneExplanation ? `，${laneExplanation}` : ''}，截止时间 ${due || '未设置截止日'}，优先级 ${PRIORITY_LABEL[entry.item.priority]}${participantNames.length > 0 ? `，参与 Agent ${participantNames.join('、')}` : ''}`}
      className="plane-board-list-row"
    >
      <div className="plane-board-list-task-cell min-w-0">
        <div className="plane-board-list-task-copy flex min-w-0 gap-tight" style={{ paddingLeft: entry.depth * 20 }}>
          {entry.depth > 0 && <GitBranch className="h-3.5 w-3.5 shrink-0 text-text-tertiary" aria-hidden />}
          <span className="plane-board-card-key shrink-0">{workItemKey(entry.item.id)}</span>
          <span
            className={`plane-board-list-task-title truncate text-body font-medium ${entry.item.status === 'completed' ? 'text-text-secondary' : 'text-text-primary'}`}
          >
            {entry.item.title}
          </span>
        </div>
        {participants.length > 0 && (
          <div className="plane-board-list-mobile-agents">
            <span className="plane-board-list-mobile-agents-label">Agent</span>
            <TaskAgentAvatars participants={participants} />
          </div>
        )}
      </div>
      <span className="text-caption tabular-nums text-text-secondary">{due || '未设置'}</span>
      <TaskAgentAvatars participants={participants} />
      <div className="flex items-center justify-end gap-1">
        <span className={`h-2 w-2 rounded-full ${PRIORITY_DOT[entry.item.priority]}`} aria-hidden />
        <span className="text-caption text-text-secondary">{PRIORITY_LABEL[entry.item.priority]}</span>
      </div>
    </button>
  );
}

export function TaskCard({
  task,
  childCount,
  participants,
  onOpen,
}: {
  task: WorkItem;
  childCount: number;
  participants: AgentProfile[];
  onOpen: () => void;
}) {
  const isCompleted = task.status === 'completed';
  const isBlocked = task.status === 'blocked';
  const due = formatDueDate(task.due_date);
  const dueLabel = due || '未设置截止日';
  const lane = taskBoardLane(task);
  const laneTitle = TASK_STATUS_COLUMNS.find((column) => column.id === lane)?.title ?? task.status;
  const participantNames = participants.map((agent) => agent.name);
  const laneExplanation = taskBoardLaneExplanation(task);

  return (
    <button
      type="button"
      onClick={onOpen}
      aria-label={`打开任务 ${task.title}，状态 ${laneTitle}${laneExplanation ? `，${laneExplanation}` : ''}，截止时间 ${dueLabel}，优先级 ${PRIORITY_LABEL[task.priority]}${participantNames.length > 0 ? `，参与 Agent ${participantNames.join('、')}` : ''}`}
      className={`plane-board-card${isBlocked ? ' plane-board-card-blocked' : ''}`}
    >
      <div className="flex items-center justify-between gap-tight">
        <span className="plane-board-card-key">{workItemKey(task.id)}</span>
        {isAwaitingAcceptance(task) && (
          <span
            className="rounded-button bg-status-warning/10 px-1.5 py-px text-caption font-medium text-status-warning"
          >
            待验收
          </span>
        )}
      </div>

      <span className={`plane-board-card-title${isCompleted ? ' is-done' : ''}`}>{task.title}</span>

      {laneExplanation && <p className="plane-board-card-review-hint">{laneExplanation}</p>}

      {isBlocked && task.blocker && (
        <p className="mb-snug line-clamp-2 text-caption text-status-error">{task.blocker.message}</p>
      )}

      <div className="plane-board-card-props">
        <span className="inline-flex items-center gap-1" title={PRIORITY_LABEL[task.priority]}>
          <span className={`h-2 w-2 rounded-full ${PRIORITY_DOT[task.priority]}`} aria-hidden />
          {PRIORITY_LABEL[task.priority]}
        </span>
        {due && (
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
        {participants.length > 0 && <TaskAgentAvatars participants={participants} />}
      </div>
    </button>
  );
}

const MAX_TASK_CARD_AGENTS = 4;

function TaskAgentAvatars({ participants }: { participants: AgentProfile[] }) {
  const shown = participants.slice(0, MAX_TASK_CARD_AGENTS);
  const overflow = participants.length - shown.length;
  return (
    <span
      className="ml-auto inline-flex shrink-0 -space-x-1.5"
      aria-hidden="true"
      title={`参与 Agent：${participants.map((agent) => agent.name).join('、')}`}
    >
      {shown.map((agent) => (
        <Avatar key={agent.id} name={agent.name} url={agent.avatar} size={20} ring />
      ))}
      {overflow > 0 && (
        <span className="relative flex h-5 min-w-5 items-center justify-center rounded-button border-2 border-surface-raised bg-surface-sunken px-1 text-caption font-semibold tabular-nums text-text-secondary">
          +{overflow}
        </span>
      )}
    </span>
  );
}
