import { AlertCircle, ArrowDown, CircleCheck, CircleDot, GitBranch, ReceiptText, ShieldAlert } from 'lucide-react';
import { useEffect } from 'react';
import type { GovernanceGoalProjection, GovernanceHandoff, GovernanceEvidenceItem, GovernanceTodo, GovernanceTurnReceipt, WorkItem } from '../../api/types';
import { Button, Card, EmptyState, StatusPill } from '../../components/ui';
import { useGovernanceStore } from '../../stores/governance.store';

const GOAL_STATUS_TEXT: Record<string, string> = {
  draft: '草稿', active: '进行中', waiting: '等待中', blocked: '已阻塞', completed: '已完成', cancelled: '已取消',
};

const TODO_STATUS_TEXT: Record<string, string> = {
  pending: '待处理', claimed: '已认领', running: '执行中', waiting: '等待中', completed: '已完成', blocked: '已阻塞', cancelled: '已取消',
};

const QUOTA_KIND_TEXT: Record<string, string> = {
  turn_count: 'Turn 次数', active_worker: '活动 Worker', input_tokens_total: '总输入 Token',
  input_uncached_tokens: '未缓存输入', cache_read_tokens: '缓存读取', cache_write_tokens: '缓存写入',
  output_tokens: '输出 Token', cost_microusd: '成本（微美元）',
};

function statusClass(status: string): string {
  if (status === 'completed') return 'border-status-success/30 bg-status-success/5 text-status-success';
  if (status === 'blocked' || status === 'cancelled') return 'border-status-error/25 bg-status-error/5 text-status-error';
  if (status === 'waiting' || status === 'claimed') return 'border-status-warning/30 bg-status-warning/5 text-status-warning';
  return 'border-border-subtle bg-surface-base text-text-secondary';
}

/** Goal/Todo/Receipt/Quota 的服务端只读投影；不在浏览器推导治理结论。 */
export function GovernancePanel({ task }: { task: WorkItem }) {
  const view = useGovernanceStore((state) => state.byWorkItem[task.id]);
  const status = useGovernanceStore((state) => state.statusByWorkItem[task.id] ?? 'idle');
  const error = useGovernanceStore((state) => state.errorByWorkItem[task.id]);
  const refreshFor = useGovernanceStore((state) => state.refreshFor);

  useEffect(() => {
    if (task.record_kind !== 'task') return;
    void refreshFor(task.id, task.workspace_id);
  }, [refreshFor, task.id, task.record_kind, task.workspace_id]);

  if (task.record_kind !== 'task' || status === 'legacy') return null;
  if (status === 'error' && !view) {
    return (
      <section aria-labelledby="governance-heading" className="space-y-snug">
        <h3 id="governance-heading" className="text-caption font-medium text-text-tertiary">长期治理</h3>
        <Card padded className="flex items-start justify-between gap-snug">
          <p className="flex items-start gap-tight text-body text-status-error" role="alert">
            <AlertCircle className="mt-0.5 h-4 w-4 shrink-0" aria-hidden />
            {error ?? '治理状态加载失败，请重试'}
          </p>
          <Button size="sm" variant="secondary" onClick={() => void refreshFor(task.id, task.workspace_id)}>重试</Button>
        </Card>
      </section>
    );
  }
  if (!view) {
    return (
      <section aria-labelledby="governance-heading" className="space-y-snug">
        <h3 id="governance-heading" className="text-caption font-medium text-text-tertiary">长期治理</h3>
        <p className="text-body text-text-tertiary" role="status">治理状态加载中…</p>
      </section>
    );
  }

  const currentTodo = view.goal.current_todo_id
    ? view.todos.find((todo) => todo.id === view.goal.current_todo_id)
    : undefined;
  return (
    <section aria-labelledby="governance-heading" className="space-y-snug">
      <div className="flex items-start justify-between gap-snug">
        <div>
          <p className="text-caption uppercase tracking-widest text-text-tertiary">治理读模型</p>
          <h3 id="governance-heading" className="mt-1 text-h3 text-text-primary">长期目标</h3>
          <p className="mt-1 text-body text-text-secondary">Goal、Todo、Receipt 与配额均来自控制面快照</p>
        </div>
        <StatusPill className={statusClass(view.goal.status)} role="status">
          {GOAL_STATUS_TEXT[view.goal.status] ?? view.goal.status}
        </StatusPill>
      </div>

      <Card padded className="space-y-snug">
        <div>
          <p className="text-caption text-text-tertiary">目标</p>
          <p className="mt-1 whitespace-pre-wrap text-body text-text-primary">{view.goal.objective}</p>
        </div>
        <div className="flex flex-wrap gap-tight text-caption text-text-tertiary">
          <span>Goal {view.goal.id}</span>
          <span>·</span>
          <span>阶段 {view.goal.phase}</span>
          <span>·</span>
          <span>版本 {view.goal.version}</span>
        </div>
      </Card>

      <GovernanceTimeline
        goal={view.goal}
        todo={currentTodo}
        receipt={view.latest_receipt}
        evidence={view.projection?.evidence_summary ?? view.evidence}
        handoffs={view.handoffs}
        nextActionCheckpoint={view.projection?.next_action_checkpoint}
        evidenceHref={`#delivery-brief-${task.id}`}
      />

      <QuotaSummary quota={view.quota} />
    </section>
  );
}

export function GovernanceTimeline({
  goal,
  todo,
  receipt,
  evidence,
  handoffs,
  nextActionCheckpoint,
  evidenceHref,
}: {
  goal: ReturnType<typeof useGovernanceStore.getState>['byWorkItem'][string]['goal'];
  todo?: GovernanceTodo;
  receipt?: GovernanceTurnReceipt;
  evidence: GovernanceEvidenceItem[];
  handoffs: GovernanceHandoff[];
  nextActionCheckpoint?: GovernanceGoalProjection['next_action_checkpoint'];
  evidenceHref: string;
}) {
  // Plan/Run/Evidence references come only from this Turn's immutable Receipt;
  // never mix them with the task's latest Plan/Run, which may belong to a later turn.
  const planIDs = [...new Set((receipt?.phases ?? []).flatMap((phase) => phase.plan_id ? [phase.plan_id] : []))];
  const runIDs = [...new Set((receipt?.phases ?? []).flatMap((phase) => phase.run_ids ?? []))];
  // Service contract orders Handoffs by created_at,id ascending; the tail is
  // therefore the latest record without guessing from status.
  const latestHandoff = handoffs.length > 0 ? handoffs[handoffs.length - 1] : undefined;
  const nodes = [
    { key: 'goal', icon: <CircleDot className="h-4 w-4" aria-hidden />, label: 'Goal', value: goal.objective, detail: goal.id, rawStatus: goal.status, displayStatus: GOAL_STATUS_TEXT[goal.status] ?? goal.status },
    { key: 'todo', icon: <ArrowDown className="h-4 w-4" aria-hidden />, label: 'Todo', value: todo?.instruction ?? '尚未生成当前 Todo', detail: todo?.id, rawStatus: todo?.status ?? 'waiting', displayStatus: todo ? TODO_STATUS_TEXT[todo.status] ?? todo.status : '等待中' },
    { key: 'plan', icon: <GitBranch className="h-4 w-4" aria-hidden />, label: 'Plan', value: planIDs.length > 0 ? `${planIDs.length} 个本 Turn Plan 引用` : '本 Turn 尚未关联 Plan', detail: planIDs.join('、'), rawStatus: planIDs.length > 0 ? 'linked' : 'waiting', displayStatus: planIDs.length > 0 ? '已关联' : '等待中' },
    { key: 'run', icon: <ReceiptText className="h-4 w-4" aria-hidden />, label: 'Run', value: runIDs.length > 0 ? `${runIDs.length} 个本 Turn Run 引用` : '本 Turn 尚未关联 Run', detail: runIDs.join('、'), rawStatus: runIDs.length > 0 ? 'linked' : 'waiting', displayStatus: runIDs.length > 0 ? '已关联' : '等待中' },
    { key: 'evidence', icon: <CircleCheck className="h-4 w-4" aria-hidden />, label: 'Evidence', value: evidence.length > 0 ? `${evidence.length} 条服务端证据引用` : '尚未记录治理证据', detail: receipt ? `receipt ${receipt.header.canonical_digest}` : undefined, rawStatus: evidence.length > 0 ? 'recorded' : 'waiting', displayStatus: evidence.length > 0 ? '已记录' : '等待中' },
    { key: 'handoff', icon: <ArrowDown className="h-4 w-4" aria-hidden />, label: 'Handoff', value: handoffs.length > 0 ? `${handoffs.length} 条交接记录` : '尚无治理交接', detail: latestHandoff?.id, rawStatus: latestHandoff?.status ?? 'none', displayStatus: latestHandoff?.status ?? '无' },
  ];
  return (
    <Card padded className="space-y-snug">
      <div className="flex items-center justify-between gap-snug">
        <div>
          <h4 className="text-body-lg font-medium text-text-primary">治理链路</h4>
          <p className="mt-0.5 text-caption text-text-tertiary">同一条 Task 时间线串起意图、执行与证据</p>
        </div>
        {receipt && <span className="text-caption text-text-tertiary">Turn {receipt.header.turn_key.turn_seq}</span>}
      </div>
      <ol className="space-y-1.5" aria-label="Goal 到 Evidence 的治理链路">
        {nodes.map((node, index) => (
          <li key={node.key} className="flex items-start gap-tight">
            <div className="flex w-5 shrink-0 flex-col items-center text-brand-primary">
              {node.icon}
              {index < nodes.length - 1 && <span className="mt-1 h-5 w-px bg-border-subtle" aria-hidden />}
            </div>
            <div className="min-w-0 flex-1 rounded-button border border-border-subtle bg-surface-base px-snug py-tight">
              <div className="flex flex-wrap items-center gap-tight">
                <span className="text-caption font-medium text-text-primary">{node.label}</span>
                <StatusPill className={statusClass(node.rawStatus)}>{node.displayStatus}</StatusPill>
                {node.detail && <span className="truncate font-mono text-caption text-text-tertiary">{node.detail}</span>}
              </div>
              {node.key === 'evidence' ? (
                <div className="mt-1 space-y-1">
                  <a href={evidenceHref} className="inline-flex text-body text-brand-primary underline-offset-2 hover:underline">{node.value}</a>
                  {evidence.length > 0 && (
                    <ul className="space-y-0.5 text-caption text-text-tertiary" aria-label="Receipt 证据引用">
                      {evidence.slice(0, 8).map((item) => (
                        <li key={`${item.source_kind}:${item.source_id}:${item.verification}:${item.recorded_at}`} className="truncate" title={item.summary}>
                          {item.source_kind} · {item.source_id} · {item.verification}
                        </li>
                      ))}
                    </ul>
                  )}
                </div>
              ) : (
                <p className="mt-1 whitespace-pre-wrap break-words text-body text-text-secondary">{node.value}</p>
              )}
            </div>
          </li>
        ))}
      </ol>
      {nextActionCheckpoint && typeof nextActionCheckpoint === 'object' && (
        <div className="rounded-button border border-border-subtle bg-surface-base px-snug py-tight">
          <p className="text-caption text-text-tertiary">下一动作</p>
          {Object.entries(nextActionCheckpoint).map(([key, value]) => (
            <p key={key} className="mt-0.5 text-body text-text-secondary">
              {key}: {typeof value === 'string' || typeof value === 'number' ? String(value) : JSON.stringify(value)}
            </p>
          ))}
        </div>
      )}
      {!receipt && <EmptyState icon={<ReceiptText className="h-4 w-4" aria-hidden />} title="等待首个 Receipt" description="控制面提交治理 Turn 后，这里会显示不可变回执。" />}
    </Card>
  );
}

function QuotaSummary({ quota }: { quota: ReturnType<typeof useGovernanceStore.getState>['byWorkItem'][string]['quota'] }) {
  return (
    <Card padded className="space-y-snug" aria-label="Goal 配额">
      <div className="flex items-center justify-between gap-snug">
        <div>
          <h4 className="text-body-lg font-medium text-text-primary">配额</h4>
          <p className="mt-0.5 text-caption text-text-tertiary">已用、冻结与不可证明项均由服务端计算</p>
        </div>
        <span className="font-mono text-caption text-text-tertiary">{quota.goal_id}</span>
      </div>
      {quota.kinds.length === 0 ? (
        <p className="text-body text-text-tertiary">该 Goal 尚未启用配额策略。</p>
      ) : (
        <ul className="space-y-1.5">
          {quota.kinds.map((item) => (
            <li key={item.kind} className="flex flex-wrap items-center gap-tight rounded-button border border-border-subtle bg-surface-base px-snug py-tight text-body">
              <span className="min-w-32 text-text-secondary">{QUOTA_KIND_TEXT[item.kind] ?? item.kind}</span>
              {item.active_worker !== undefined ? (
                <span className="tabular-nums text-text-primary">活动 {item.active_worker}</span>
              ) : (
                <>
                  <span className="tabular-nums text-text-primary">已用 {item.committed}</span>
                  <span className="tabular-nums text-text-tertiary">冻结 {item.active_reserved}</span>
                </>
              )}
              {item.unresolved.length > 0 && (
                <span className="ml-auto inline-flex items-center gap-1 text-caption text-status-warning">
                  <ShieldAlert className="h-3.5 w-3.5" aria-hidden />
                  {item.unresolved.length} 项待对账
                </span>
              )}
            </li>
          ))}
        </ul>
      )}
    </Card>
  );
}
