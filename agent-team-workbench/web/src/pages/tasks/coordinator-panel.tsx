import {
  Bot,
  Check,
  CircleAlert,
  CircleDot,
  Clock3,
  LoaderCircle,
  RefreshCw,
  RotateCcw,
  ShieldAlert,
  UserRound,
} from 'lucide-react';
import type {
  CoordinatorAttempt,
  CoordinatorSnapshot,
  CoordinatorStage,
  CoordinatorStatus,
  CoordinatorTimelineEntry,
  WorkItem,
} from '../../api/types';
import { Button, Card, EmptyState, StatusPill } from '../../components/ui';
import type { CoordinatorResolution } from '../../utils/task-phase';
import { formatDateTime } from '../../utils/format';

const STATUS_TEXT: Record<string, string> = {
  queued: '接取中',
  running: '执行中',
  waiting_retry: '等待自动重试',
  waiting_user: '等待你处理',
  blocked: '需要介入',
  completed: '已完成',
};

const STAGE_TEXT: Record<string, string> = {
  plan: '规划',
  dispatch: '派发',
  attempt: '执行尝试',
  failure: '失败诊断',
  retry: '自动重试',
  reassign: '重新分配',
  settlement: '派发收口',
  result: '结果汇总',
  acceptance: '等待验收',
};

const STATUS_CLASS: Record<string, string> = {
  queued: 'bg-surface-base text-text-secondary border-border-subtle',
  running: 'bg-brand-primary/10 text-brand-accent border-brand-primary/20',
  waiting_retry: 'bg-status-warning/10 text-status-warning border-status-warning/20',
  waiting_user: 'bg-status-warning/10 text-status-warning border-status-warning/20',
  blocked: 'bg-status-error/10 text-status-error border-status-error/20',
  completed: 'bg-status-success/10 text-status-success border-status-success/20',
  cancelled: 'bg-surface-base text-text-tertiary border-border-subtle',
};

const ATTEMPT_TEXT: Record<string, string> = {
  queued: '排队中',
  running: '执行中',
  succeeded: '已完成',
  completed: '已完成',
  failed: '失败',
  retrying: '准备重试',
  waiting_retry: '等待重试',
  waiting_user: '等待处理',
  cancelled: '已取消',
};

const ATTEMPT_CLASS: Record<string, string> = {
  queued: 'text-text-secondary',
  running: 'text-brand-accent',
  succeeded: 'text-status-success',
  completed: 'text-status-success',
  failed: 'text-status-error',
  retrying: 'text-status-warning',
  waiting_retry: 'text-status-warning',
  waiting_user: 'text-status-warning',
  cancelled: 'text-text-tertiary',
};

export function coordinatorStatusText(status: CoordinatorStatus | string | undefined): string {
  return status ? STATUS_TEXT[status] ?? status : '接取中';
}

export function coordinatorStageText(stage: CoordinatorStage | string | undefined): string {
  return stage ? STAGE_TEXT[stage] ?? stage : '准备规划';
}

function progressFor(snapshot: CoordinatorSnapshot | undefined, task: WorkItem): number | undefined {
  if (typeof snapshot?.progress === 'number' && Number.isFinite(snapshot.progress)) {
    return Math.max(0, Math.min(1, snapshot.progress));
  }
  if (
    typeof snapshot?.completed_steps === 'number' &&
    typeof snapshot.total_steps === 'number' &&
    snapshot.total_steps > 0
  ) {
    return Math.max(0, Math.min(1, snapshot.completed_steps / snapshot.total_steps));
  }
  if (task.status === 'completed' || snapshot?.status === 'completed' ||
    snapshot?.status === 'waiting_user' && snapshot.stage === 'acceptance') return 1;
  return undefined;
}

function progressLabel(snapshot: CoordinatorSnapshot | undefined, progress: number | undefined): string {
  if (typeof snapshot?.completed_steps === 'number' && typeof snapshot.total_steps === 'number') {
    return `${snapshot.completed_steps}/${snapshot.total_steps} 步`;
  }
  if (progress !== undefined) return `${Math.round(progress * 100)}%`;
  return '等待首个计划';
}

export function CoordinatorPanel({
  task,
  snapshot,
  error,
  onRetry,
  resolution = 'loading',
}: {
  task: WorkItem;
  snapshot?: CoordinatorSnapshot;
  error?: string;
  onRetry: () => void;
  resolution?: CoordinatorResolution;
}) {
  if (resolution === 'legacy') {
    return (
      <section aria-labelledby="coordinator-heading" className="space-y-snug">
        <div>
          <p className="text-caption uppercase tracking-widest text-text-tertiary">历史任务</p>
          <h3 id="coordinator-heading" className="mt-1 text-h3 text-text-primary">任务统筹</h3>
          <p className="mt-1 text-body text-text-secondary">该任务未启用 Coordinator 控制线，保留历史任务执行与回流方式。</p>
        </div>
        <Card padded>
          <p className="text-body text-text-secondary" role="status">
            历史任务，未启用 Coordinator 控制线。
          </p>
        </Card>
      </section>
    );
  }
  const progress = progressFor(snapshot, task);
  const status = snapshot?.status ?? (task.status === 'completed' ? 'completed' : 'queued');
  const currentAgent = snapshot?.current_agent;
  const nextAction = snapshot?.next_action ?? (task.status === 'todo' ? 'Coordinator 正在接取任务并生成首个计划' : undefined);
  const blocker = snapshot?.blocker;

  return (
    <section aria-labelledby="coordinator-heading" className="space-y-snug">
      <div className="flex items-start justify-between gap-snug">
        <div>
          <p className="text-caption uppercase tracking-widest text-text-tertiary">控制线 · Task Coordinator</p>
          <h3 id="coordinator-heading" className="mt-1 text-h3 text-text-primary">任务统筹</h3>
          <p className="mt-1 text-body text-text-secondary">系统 Coordinator 自动规划、派发、恢复和收口</p>
        </div>
        {snapshot?.updated_at && <span className="text-caption text-text-tertiary">更新于 {formatDateTime(snapshot.updated_at)}</span>}
      </div>

      {error && (
        <div className="flex items-start justify-between gap-snug rounded-card border border-status-error/30 bg-status-error/5 px-snug py-tight" role="alert">
          <div className="flex min-w-0 items-start gap-2 text-body text-status-error">
            <CircleAlert className="mt-0.5 h-4 w-4 shrink-0" aria-hidden />
            <span>{error}</span>
          </div>
          <Button type="button" size="sm" onClick={onRetry}>
            <RefreshCw className="h-3.5 w-3.5" aria-hidden />
            重试读取
          </Button>
        </div>
      )}

      <div className="grid gap-snug md:grid-cols-2">
        <Card padded className="space-y-snug">
          <div className="flex items-center justify-between gap-snug">
            <span className="text-caption text-text-tertiary">当前状态</span>
            <StatusPill
              role="status"
              aria-live="polite"
              aria-atomic="true"
              className={STATUS_CLASS[status] ?? 'bg-surface-base text-text-secondary border-border-subtle'}
            >
              <span className={`h-1.5 w-1.5 rounded-full ${statusDotClass(status)}`} aria-hidden />
              {coordinatorStatusText(status)}
            </StatusPill>
          </div>
          <div className="flex items-center gap-snug">
            <div className="flex h-10 w-10 items-center justify-center rounded-full border border-brand-primary/20 bg-brand-primary/10 text-brand-accent">
              {status === 'waiting_retry' ? (
                <RotateCcw className="h-5 w-5 motion-safe:animate-pulse motion-reduce:animate-none" aria-hidden />
              ) : status === 'completed' ? (
                <Check className="h-5 w-5" aria-hidden />
              ) : (
                <Bot className="h-5 w-5" aria-hidden />
              )}
            </div>
            <div className="min-w-0">
              <p className="text-body-lg font-medium text-text-primary">系统 Coordinator</p>
              <p className="text-caption text-text-tertiary">阶段：{coordinatorStageText(snapshot?.stage)}</p>
              {(snapshot?.runtime_label || snapshot?.model_ref) && (
                <p className="mt-0.5 text-caption text-text-tertiary">
                  底座 · {coordinatorRuntimeText(snapshot.runtime_label)}{snapshot.model_ref ? ` · ${snapshot.model_ref}` : ''}
                </p>
              )}
            </div>
          </div>
          {currentAgent ? (
            <div className="flex items-center gap-2 border-t border-border-subtle pt-snug text-body">
              <UserRound className="h-4 w-4 text-text-tertiary" aria-hidden />
              <span className="text-text-secondary">当前 Agent</span>
              <span className="ml-auto font-medium text-text-primary">{currentAgent.name}</span>
            </div>
          ) : (
            <p className="border-t border-border-subtle pt-snug text-caption text-text-tertiary">当前尚未分配 Worker，Coordinator 会根据计划自动选择。</p>
          )}
        </Card>

        <Card padded className="space-y-snug">
          <div className="flex items-center justify-between gap-snug">
            <span className="text-caption text-text-tertiary">整体进度</span>
            <span className="text-body font-medium tabular-nums text-text-primary">{progressLabel(snapshot, progress)}</span>
          </div>
          <div
            className="h-2 overflow-hidden rounded-full bg-surface-sunken"
            role="progressbar"
            aria-label="任务整体进度"
            aria-valuemin={0}
            aria-valuemax={100}
            {...(progress === undefined ? { 'aria-valuetext': '等待首个计划' } : { 'aria-valuenow': Math.round(progress * 100) })}
          >
            <div
              className="h-full rounded-full bg-brand-primary transition-[width] duration-300 motion-reduce:transition-none"
              style={{ width: `${Math.round((progress ?? 0) * 100)}%` }}
            />
          </div>
          <div className="flex items-start gap-2 rounded-button bg-surface-base px-snug py-tight">
            <CircleDot className="mt-0.5 h-4 w-4 shrink-0 text-brand-accent" aria-hidden />
            <div className="min-w-0">
              <p className="text-caption text-text-tertiary">下一动作</p>
              <p className="mt-0.5 text-body text-text-primary">{nextAction ?? 'Coordinator 正在观察执行结果'}</p>
              {snapshot?.next_action_at && (
                <p className="mt-0.5 text-caption tabular-nums text-text-tertiary">计划于 {formatDateTime(snapshot.next_action_at)} 自动继续</p>
              )}
            </div>
          </div>
          {blocker && (
            <div className="flex items-start gap-2 rounded-button border border-status-error/20 bg-status-error/5 px-snug py-tight" role="alert">
              <ShieldAlert className="mt-0.5 h-4 w-4 shrink-0 text-status-error" aria-hidden />
              <div className="min-w-0">
                <p className="text-caption font-medium text-status-error">阻塞 · {blocker.code}</p>
                <p className="mt-0.5 text-body text-text-primary">{blocker.message}</p>
                <p className="mt-0.5 text-caption text-text-tertiary">下一步：解除阻塞或补充信息后，Coordinator 会继续推进。</p>
              </div>
            </div>
          )}
        </Card>
      </div>

      <div className="grid gap-snug lg:grid-cols-[minmax(0,1.25fr)_minmax(18rem,0.75fr)]">
        <CoordinatorTimeline entries={snapshot?.timeline ?? []} />
        <CoordinatorAttempts attempts={snapshot?.attempts ?? []} />
      </div>
    </section>
  );
}

function coordinatorRuntimeText(runtime: string | undefined): string {
  if (runtime === 'codex_local' || runtime === 'codex') return 'Codex';
  if (runtime === 'kimi_local' || runtime === 'kimi') return 'Kimi';
  return runtime || '系统默认';
}

function statusDotClass(status: string): string {
  if (status === 'completed') return 'bg-status-success';
  if (status === 'blocked') return 'bg-status-error';
  if (status === 'waiting_retry' || status === 'waiting_user') return 'bg-status-warning';
  if (status === 'cancelled') return 'bg-status-standby';
  return 'bg-brand-accent status-pulse';
}

export function CoordinatorTimeline({ entries }: { entries: CoordinatorTimelineEntry[] }) {
  const ordered = [...entries].sort((a, b) => a.occurred_at.localeCompare(b.occurred_at));
  return (
    <Card padded className="space-y-snug">
      <div className="flex items-center justify-between gap-snug">
        <div>
          <h4 className="text-body-lg font-medium text-text-primary">任务主时间线</h4>
          <p className="mt-0.5 text-caption text-text-tertiary">按规划 → 派发 → 尝试 → 恢复 → 结果记录</p>
        </div>
        <span className="text-caption tabular-nums text-text-tertiary">{ordered.length} 个节点</span>
      </div>
      {ordered.length === 0 ? (
        <EmptyState
          icon={<Clock3 className="h-5 w-5" aria-hidden />}
          title="等待 Coordinator 产生计划"
          description="任务创建后会自动接取；首个计划和执行节点会出现在这里。"
        />
      ) : (
        <ol className="relative space-y-0 border-l border-border-subtle pl-comfortable" aria-label="Coordinator 任务主时间线">
          {ordered.map((entry) => <CoordinatorTimelineRow key={entry.id} entry={entry} />)}
        </ol>
      )}
    </Card>
  );
}

function CoordinatorTimelineRow({ entry }: { entry: CoordinatorTimelineEntry }) {
  const stage = timelineStage(entry);
  const isFailure = stage === 'failure' || entry.status === 'failed' || Boolean(entry.failure);
  const isCompleted = stage === 'result' || stage === 'acceptance' || entry.status === 'completed' || entry.status === 'succeeded';
  return (
    <li className="relative pb-snug last:pb-0">
      <span
        className={`absolute -left-[calc(theme(spacing.comfortable)+0.3rem)] top-1 flex h-2.5 w-2.5 rounded-full border-2 border-surface-raised ${isFailure ? 'bg-status-error' : isCompleted ? 'bg-status-success' : 'bg-brand-primary'}`}
        aria-hidden
      />
      <div className="flex flex-wrap items-baseline gap-x-snug gap-y-1">
        <span className="text-caption font-medium text-text-primary">{entry.title}</span>
        <span className="text-caption text-text-tertiary">{coordinatorStageText(stage)}</span>
        <time className="ml-auto text-caption tabular-nums text-text-tertiary" dateTime={entry.occurred_at}>{formatDateTime(entry.occurred_at)}</time>
      </div>
      {entry.message && <p className={`mt-0.5 text-body ${isFailure ? 'text-status-error' : 'text-text-secondary'}`}>{entry.message}</p>}
      <div className="mt-1 flex flex-wrap items-center gap-snug text-caption text-text-tertiary">
        {entry.agent && <span>Agent · {entry.agent.name}</span>}
        {entry.attempt !== undefined && <span className="tabular-nums">第 {entry.attempt} 次尝试</span>}
        {entry.retry_of && <span className="font-mono">retry_of {entry.retry_of}</span>}
      </div>
      {entry.failure && (
        <p className="mt-1 rounded-button border border-status-error/20 bg-status-error/5 px-snug py-tight text-caption text-status-error">
          {entry.failure.code ? `${entry.failure.code}：` : ''}{entry.failure.message}
          {entry.failure.retryable === true ? ' · 将自动恢复' : entry.failure.retryable === false ? ' · 需要你处理' : ''}
        </p>
      )}
    </li>
  );
}

function timelineStage(entry: CoordinatorTimelineEntry): string {
  if (entry.failure || entry.status === 'failed' || entry.kind?.includes('failed')) return 'failure';
  if (entry.kind?.includes('retry')) return 'retry';
  if (entry.kind?.includes('reassign') || entry.kind?.includes('replace')) return 'reassign';
  return entry.stage;
}

export function CoordinatorAttempts({ attempts }: { attempts: CoordinatorAttempt[] }) {
  const ordered = uniqueAttempts(attempts);
  return (
    <Card padded className="space-y-snug">
      <div className="flex items-center justify-between gap-snug">
        <div>
          <h4 className="text-body-lg font-medium text-text-primary">尝试链</h4>
          <p className="mt-0.5 text-caption text-text-tertiary">失败原因与后续动作</p>
        </div>
        <span className="text-caption tabular-nums text-text-tertiary">{ordered.length} 次</span>
      </div>
      {ordered.length === 0 ? (
        <p className="rounded-button bg-surface-base px-snug py-tight text-body text-text-tertiary">首个 Worker 尚未开始。</p>
      ) : (
        <ol className="space-y-1.5" aria-label="Worker 尝试链">
          {ordered.map((attempt) => <AttemptRow key={attempt.id} attempt={attempt} />)}
        </ol>
      )}
    </Card>
  );
}

/** Backend timeline may emit start/failure/retry rows for one attempt; show one row per attempt. */
function uniqueAttempts(attempts: CoordinatorAttempt[]): CoordinatorAttempt[] {
  const ordered = [...attempts].sort((a, b) => b.attempt - a.attempt || b.started_at.localeCompare(a.started_at));
  const seen = new Set<number>();
  return ordered.filter((attempt) => {
    if (seen.has(attempt.attempt)) return false;
    seen.add(attempt.attempt);
    return true;
  });
}

function AttemptRow({ attempt }: { attempt: CoordinatorAttempt }) {
  const status = attempt.status;
  return (
    <li className="rounded-button border border-border-subtle bg-surface-base px-snug py-tight">
      <div className="flex items-center gap-2">
        {status === 'running' || status === 'retrying' || status === 'waiting_retry' ? (
          <LoaderCircle className="h-4 w-4 shrink-0 text-brand-accent motion-safe:animate-spin motion-reduce:animate-none" aria-hidden />
        ) : status === 'failed' || status === 'waiting_user' ? (
          <CircleAlert className="h-4 w-4 shrink-0 text-status-error" aria-hidden />
        ) : status === 'succeeded' || status === 'completed' ? (
          <Check className="h-4 w-4 shrink-0 text-status-success" aria-hidden />
        ) : (
          <Clock3 className="h-4 w-4 shrink-0 text-text-tertiary" aria-hidden />
        )}
        <span className="text-caption font-medium tabular-nums text-text-primary">尝试 {attempt.attempt}{attempt.max_attempts ? ` / ${attempt.max_attempts}` : ''}</span>
        <span className={`ml-auto text-caption ${ATTEMPT_CLASS[status] ?? 'text-text-secondary'}`}>{ATTEMPT_TEXT[status] ?? status}</span>
      </div>
      <div className="mt-1 flex flex-wrap gap-x-snug gap-y-1 text-caption text-text-tertiary">
        {attempt.agent && <span>{attempt.agent.name}</span>}
        <time dateTime={attempt.started_at}>{formatDateTime(attempt.started_at)}</time>
      </div>
      {attempt.failure && <p className="mt-1 text-caption text-status-error">{attempt.failure.message}</p>}
      {attempt.next_action && <p className="mt-1 text-caption text-text-secondary">下一步：{attempt.next_action}</p>}
    </li>
  );
}
