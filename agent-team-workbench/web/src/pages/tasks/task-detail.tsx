import { Lock, Pencil } from 'lucide-react';
import { useEffect, useState } from 'react';
import { ApiError } from '../../api/client';
import { assignWorkItem, getWorkItem, getWorkItemTree, patchWorkItem } from '../../api/endpoints';
import type { Plan, PlanStep, Priority, WorkItem, WorkItemStatus } from '../../api/types';
import { Drawer } from '../../components/drawer';
import { PriorityBadge } from '../../components/priority-badge';
import { useAgentsStore } from '../../stores/agents.store';
import { usePlansStore } from '../../stores/plans.store';
import { useRunsStore } from '../../stores/runs.store';
import { useTasksStore } from '../../stores/tasks.store';
import { toast } from '../../stores/toast.store';
import { formatDateTime, formatDueDate } from '../../utils/format';
import { evaluationPassed, isAwaitingAcceptance, stepTriggeredEvaluation } from '../../utils/task-phase';
import { sortTasksTree } from '../../utils/task-tree';
import { DispatchTimeline } from './dispatch-timeline';
import { ReturnTaskModal } from './return-modal';
import { RunPanel } from './run-panel';
import { TaskLedger } from './task-ledger';

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

const PLAN_STATUS_TEXT: Record<string, string> = {
  active: '执行中',
  waiting: '等待唤醒',
  finished: '已完成',
  cancelled: '已取消',
  failed: '失败',
};

const PLAN_STATUS_CLS: Record<string, string> = {
  active: 'bg-brand-primary/10 text-brand-accent border-brand-primary/20',
  waiting: 'bg-status-warning/10 text-status-warning border-status-warning/20',
  finished: 'bg-status-success/10 text-status-success border-status-success/20',
  cancelled: 'bg-surface-base text-text-tertiary border-border-subtle',
  failed: 'bg-status-error/10 text-status-error border-status-error/20',
};

const VERB_TEXT: Record<string, string> = {
  dispatch: '派工',
  defer: '挂起',
  finish: '收尾',
};

const STEP_STATUS_TEXT: Record<string, string> = {
  pending: '待执行',
  executed: '已执行',
  skipped: '已跳过',
  failed: '失败',
};

/** 任务详情抽屉：字段、指派、子任务、编排计划、阻塞/验收操作、Run 面板。 */
export function TaskDetail({
  taskId,
  onClose,
  onTransition,
}: {
  taskId: string | null;
  onClose: () => void;
  onTransition: (item: WorkItem, to: WorkItemStatus) => Promise<void>;
}) {
  const storeTask = useTasksStore((s) => (taskId ? s.items.find((t) => t.id === taskId) : undefined));
  const upsert = useTasksStore((s) => s.upsert);
  const selectTask = useTasksStore((s) => s.selectTask);
  const storeItems = useTasksStore((s) => s.items);
  const agents = useAgentsStore((s) => s.agents);
  const plan = usePlansStore((s) => (taskId ? s.byWorkItem[taskId] : undefined));
  const refreshPlan = usePlansStore((s) => s.refreshFor);
  const watchRun = useRunsStore((s) => s.watchRun);
  const unwatchRun = useRunsStore((s) => s.unwatchRun);
  const [assigning, setAssigning] = useState(false);
  const [editOpen, setEditOpen] = useState(false);
  const [returning, setReturning] = useState(false);
  // 子任务树快照：null = 未加载/加载失败（回退列表本地推导）。
  const [treeChildren, setTreeChildren] = useState<WorkItem[] | null>(null);
  // 树接口带回的任务缓存：点子任务即时渲染，权威快照仍由 getWorkItem 补齐。
  const [known, setKnown] = useState<Record<string, WorkItem>>({});

  const task = storeTask ?? (taskId ? known[taskId] : undefined);

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

  // 子任务树：直接子任务以树返回项自身的 parent_id 判定（不依赖「树是否含根」的顺序假设）。
  useEffect(() => {
    if (!taskId) return;
    let cancelled = false;
    setTreeChildren(null);
    getWorkItemTree(taskId)
      .then(({ items }) => {
        if (cancelled) return;
        setTreeChildren(items.filter((t) => t.parent_id === taskId));
        setKnown((prev) => {
          const next = { ...prev };
          for (const t of items) next[t.id] = t;
          return next;
        });
      })
      // 只吞树接口失败：子任务区回退列表推导（可能受看板筛选影响），不影响其余区块。
      .catch(() => undefined);
    return () => {
      cancelled = true;
    };
  }, [taskId]);

  // 编排计划：按主任务刷新最新 plan（冷启动无索引时空操作，等 SSE plan.* 事件）。
  useEffect(() => {
    if (taskId) void refreshPlan(taskId);
  }, [taskId, refreshPlan]);

  // 订阅最新 Run 的实时事件。
  const latestRunId = task?.latest_run_id;
  useEffect(() => {
    if (!latestRunId) return;
    watchRun(latestRunId);
    return () => unwatchRun(latestRunId);
  }, [latestRunId, watchRun, unwatchRun]);

  // 子任务展示序：同一父下按 created_at（sortTasksTree 对同源兄弟退化为排序）。
  const children = sortTasksTree(
    treeChildren ?? (taskId ? storeItems.filter((t) => t.parent_id === taskId) : []),
  ).map((e) => e.item);

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
                {task.locked_by_run_id && (
                  <span
                    title={`执行锁：run ${task.locked_by_run_id}（防止同任务双跑）`}
                    className="inline-flex items-center px-1.5 py-0.5 rounded-sm text-caption font-medium bg-surface-base text-text-secondary border border-border-subtle"
                  >
                    <Lock className="w-3 h-3" />
                  </span>
                )}
                {task.status !== 'completed' && task.status !== 'cancelled' && (
                  <button
                    onClick={() => setEditOpen(true)}
                    title="编辑任务字段"
                    className="ml-auto flex items-center gap-1 text-caption border border-border-strong text-text-secondary rounded-button px-2 py-1 hover:bg-surface-base transition-colors"
                  >
                    <Pencil className="w-3.5 h-3.5" />
                    编辑
                  </button>
                )}
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
                    {task.due_date ? formatDueDate(task.due_date) : '未设置'}
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

            {/* 子任务（编排 dispatch 派生） */}
            <section className="space-y-snug">
              <h3 className="text-caption font-medium text-text-tertiary">
                子任务{children.length > 0 ? `（${children.length}）` : ''}
              </h3>
              {children.length === 0 ? (
                <p className="text-body text-text-tertiary">暂无子任务</p>
              ) : (
                <ul className="space-y-1.5">
                  {children.map((child) => {
                    const assignee = child.agent_profile_id
                      ? agents.find((a) => a.id === child.agent_profile_id)?.name ?? child.agent_profile_id
                      : '未指派';
                    return (
                      <li
                        key={child.id}
                        className="flex items-center gap-2 p-2 rounded-lg border border-border-subtle bg-surface-base"
                      >
                        <button
                          onClick={() => selectTask(child.id)}
                          className="flex-1 min-w-0 text-left"
                          title="打开子任务详情"
                        >
                          <div className="flex items-center gap-2">
                            <span className="shrink-0 inline-flex items-center px-1.5 py-0.5 rounded-sm text-caption text-text-secondary bg-surface-raised border border-border-subtle">
                              {STATUS_TEXT[child.status] ?? child.status}
                            </span>
                            <span
                              className={`text-body truncate ${
                                child.status === 'completed' ? 'line-through opacity-60 text-text-primary' : 'text-text-primary'
                              }`}
                            >
                              {child.title}
                            </span>
                          </div>
                          <span className="text-caption text-text-tertiary">{assignee}</span>
                        </button>
                      </li>
                    );
                  })}
                </ul>
              )}
            </section>

            {/* 编排计划（该任务最新 plan） */}
            <section className="space-y-snug">
              <h3 className="text-caption font-medium text-text-tertiary">编排计划</h3>
              {plan ? (
                <PlanPanel plan={plan} task={task} agents={agents} onOpenWorkItem={selectTask} />
              ) : (
                <p className="text-body text-text-tertiary">暂无编排计划</p>
              )}
            </section>

            {/* 派发时间线（会话元模型：一次发送 = 一个执行批次） */}
            <DispatchTimeline taskId={task.id} />

            {/* 任务台账（S2：滚动摘要 + 决策原话，任务级共享记忆） */}
            <TaskLedger taskId={task.id} agentProfileId={task.agent_profile_id} />

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
                      <>
                        <button
                          onClick={() => void onTransition(task, 'completed')}
                          className="bg-status-success text-text-inverse rounded-button px-base py-tight text-body font-medium transition-all hover:opacity-90 active:scale-[0.98]"
                        >
                          验收通过
                        </button>
                        <button
                          onClick={() => setReturning(true)}
                          className="bg-transparent border border-status-warning text-status-warning rounded-button px-base py-tight text-body font-medium transition-colors hover:bg-status-warning/5 active:scale-[0.98]"
                        >
                          打回重做
                        </button>
                      </>
                    )}
                  </>
                )}
                {task.status === 'blocked' && (
                  <button
                    onClick={() => void onTransition(task, 'in_progress')}
                    className="bg-brand-primary text-text-inverse rounded-button px-base py-tight text-body font-medium transition-all hover:bg-brand-accent active:scale-[0.98]"
                  >
                    解除阻塞
                  </button>
                )}
              </section>
            )}

            {/* Run 面板 */}
            <RunPanel task={task} />
          </div>

          {editOpen && (
            <TaskEditModal
              task={task}
              onClose={() => setEditOpen(false)}
              onSaved={(wi) => {
                upsert(wi);
                setEditOpen(false);
              }}
            />
          )}
          <ReturnTaskModal
            task={returning && isAwaitingAcceptance(task) ? task : null}
            onClose={() => setReturning(false)}
          />
        </div>
      )}
    </Drawer>
  );
}

/** plan 摘要面板：状态徽标 + 评估提示 + steps 执行明细。 */
function PlanPanel({
  plan,
  task,
  agents,
  onOpenWorkItem,
}: {
  plan: Plan;
  task: WorkItem;
  agents: { id: string; name: string }[];
  onOpenWorkItem: (workItemId: string) => void;
}) {
  const ownerName = agents.find((a) => a.id === plan.agent_profile_id)?.name ?? plan.agent_profile_id;
  return (
    <div className="rounded-lg border border-border-subtle bg-surface-base p-snug space-y-snug">
      <div className="flex items-center gap-2 flex-wrap">
        <span
          className={`inline-flex items-center px-2 py-0.5 rounded-sm text-caption font-medium border ${
            PLAN_STATUS_CLS[plan.status] ?? 'bg-surface-base text-text-secondary border-border-subtle'
          }`}
        >
          {PLAN_STATUS_TEXT[plan.status] ?? plan.status}
        </span>
        <span className="text-caption text-text-tertiary">
          {ownerName} · {formatDateTime(plan.updated_at)}
        </span>
      </div>
      {evaluationPassed(task, plan) && (
        <div className="rounded-lg border border-status-success/20 bg-status-success/10 px-snug py-tight text-body text-status-success">
          评估通过，等待人工验收
        </div>
      )}
      {plan.superseded_by && (
        <p className="text-caption text-text-tertiary">已被后续计划 {plan.superseded_by} 取代</p>
      )}
      <ol className="space-y-1.5">
        {plan.steps.map((step) => (
          <PlanStepRow key={step.seq} step={step} agents={agents} onOpenWorkItem={onOpenWorkItem} />
        ))}
      </ol>
    </div>
  );
}

/** 单个 plan step：verb/状态/摘要/dispatch 结果链接。 */
function PlanStepRow({
  step,
  agents,
  onOpenWorkItem,
}: {
  step: PlanStep;
  agents: { id: string; name: string }[];
  onOpenWorkItem: (workItemId: string) => void;
}) {
  const text = (v: unknown): string => (typeof v === 'string' ? v : '');
  const summary =
    step.verb === 'dispatch'
      ? text(step.payload?.title) || text(step.payload?.instruction)
      : step.verb === 'defer'
        ? text(step.payload?.reason)
        : text(step.payload?.summary);
  const targetAgent = step.verb === 'dispatch' ? text(step.payload?.agent_id) : '';
  const targetName = targetAgent ? agents.find((a) => a.id === targetAgent)?.name ?? targetAgent : '';
  const resultWorkItemId = step.result_work_item_id ?? undefined;

  return (
    <li className="flex items-start gap-2 p-2 rounded-lg border border-border-subtle bg-surface-raised">
      <span className="text-caption text-text-tertiary tabular-nums mt-0.5">{step.seq}</span>
      <div className="flex-1 min-w-0">
        <div className="flex items-center gap-1.5 flex-wrap">
          <span className="inline-flex items-center px-1.5 py-0.5 rounded-sm text-caption font-medium bg-surface-base text-text-secondary border border-border-subtle">
            {VERB_TEXT[step.verb] ?? step.verb}
          </span>
          <span
            className={`text-caption font-medium ${
              step.status === 'failed'
                ? 'text-status-error'
                : step.status === 'executed'
                  ? 'text-status-success'
                  : 'text-text-tertiary'
            }`}
          >
            {STEP_STATUS_TEXT[step.status] ?? step.status}
          </span>
          {stepTriggeredEvaluation(step) && (
            <span
              title="plan 落 finished 后自动创建评估 run，verdict 通过后进入待验收"
              className="inline-flex items-center px-1.5 py-0.5 rounded-sm text-caption font-medium bg-brand-primary/10 text-brand-accent border border-brand-primary/20"
            >
              已触发评估
            </span>
          )}
          {targetName && <span className="text-caption text-text-tertiary">→ {targetName}</span>}
        </div>
        {summary && (
          <p className="text-body text-text-secondary truncate mt-0.5" title={summary}>
            {summary}
          </p>
        )}
        {step.error && <p className="text-caption text-status-error mt-0.5">{step.error}</p>}
        <div className="flex items-center gap-comfortable mt-0.5">
          {resultWorkItemId && (
            <button
              onClick={() => onOpenWorkItem(resultWorkItemId)}
              className="text-caption text-brand-accent hover:underline"
              title="打开派生的子任务详情"
            >
              子任务 {resultWorkItemId} →
            </button>
          )}
          {step.result_run_id && (
            <span className="text-caption text-text-tertiary">run {step.result_run_id}</span>
          )}
        </div>
      </div>
    </li>
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
  onSaved: (wi: WorkItem) => void;
}) {
  const [title, setTitle] = useState(task.title);
  const [description, setDescription] = useState(task.description ?? '');
  const [priority, setPriority] = useState<Priority>(task.priority);
  const [dueDate, setDueDate] = useState(task.due_date ?? '');
  const [saving, setSaving] = useState(false);

  const inputCls =
    'mt-1 w-full rounded-input border border-border-strong bg-surface-raised px-snug py-tight text-body outline-none focus:ring-2 focus:ring-brand-primary/30';

  const save = async () => {
    setSaving(true);
    try {
      const updated = await patchWorkItem(task.id, {
        title: title.trim(),
        description,
        priority,
        // 后端 PATCH 不支持清空截止日（null 视为未设置字段）：仅在选择新日期时提交。
        ...(dueDate ? { due_date: dueDate } : {}),
        expected_version: task.version,
      });
      toast.success('任务已更新');
      onSaved(updated);
    } catch (err) {
      if (err instanceof ApiError && err.isVersionConflict) {
        toast.error('任务已被他人修改，已为你刷新最新数据');
        onSaved(await getWorkItem(task.id));
      } else {
        toast.error(err instanceof ApiError ? err.message : '保存失败');
      }
    } finally {
      setSaving(false);
    }
  };

  return (
    <Drawer open onClose={onClose} title="编辑任务" width={480}>
      <div className="p-comfortable">
        <div className="space-y-base">
        <label className="block">
          <span className="text-body text-text-secondary">标题</span>
          <input value={title} onChange={(e) => setTitle(e.target.value)} className={inputCls} />
        </label>
        <label className="block">
          <span className="text-body text-text-secondary">描述</span>
          <textarea value={description} onChange={(e) => setDescription(e.target.value)} rows={4} className={`${inputCls} resize-y`} />
        </label>
        <div className="grid grid-cols-2 gap-snug">
          <label className="block">
            <span className="text-body text-text-secondary">优先级</span>
            <select value={priority} onChange={(e) => setPriority(e.target.value as Priority)} className={inputCls}>
              <option value="low">低</option>
              <option value="medium">中</option>
              <option value="high">高</option>
              <option value="urgent">紧急</option>
            </select>
          </label>
          <label className="block">
            <span className="text-body text-text-secondary">截止日</span>
            <input type="date" value={dueDate} onChange={(e) => setDueDate(e.target.value)} className={inputCls} />
          </label>
        </div>
        <div className="flex justify-end gap-snug pt-tight">
          <button
            onClick={onClose}
            className="bg-transparent border border-border-strong text-text-secondary rounded-button px-base py-tight font-medium hover:bg-surface-base transition-colors"
          >
            取消
          </button>
          <button
            onClick={() => void save()}
            disabled={!title.trim() || saving}
            className="bg-brand-primary text-text-inverse rounded-button px-base py-tight font-medium transition-all hover:bg-brand-accent active:scale-[0.98] disabled:opacity-50"
          >
            {saving ? '保存中…' : '保存'}
          </button>
        </div>
      </div>
      </div>
    </Drawer>
  );
}
