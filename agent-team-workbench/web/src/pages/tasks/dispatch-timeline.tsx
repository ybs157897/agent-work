import { ChevronDown, ChevronRight, MessagesSquare } from 'lucide-react';
import { useEffect, useState } from 'react';
import type { DispatchCard, DispatchRun } from '../../api/types';
import { Avatar } from '../../components/avatar';
import { runStatusColor, runStatusText } from '../../components/status';
import { Button, Card, EmptyState, StatusPill } from '../../components/ui';
import { useDispatchesStore } from '../../stores/dispatches.store';
import { useRunsStore } from '../../stores/runs.store';
import { captureScope } from '../../stores/scope';
import { formatDateTime } from '../../utils/format';
import { TaskRunOutput } from './task-run-output';

const TRIGGER_TEXT: Record<string, string> = {
  user_message: '用户消息',
  lead_plan: '编排派发',
  wakeup: '唤醒',
};

const DISPATCH_STATUS_TEXT: Record<string, string> = {
  running: '运行中',
  collecting: '汇总中',
  completed: '已完成',
  degraded: '已降级',
  cancelled: '已取消',
};

/** 派发状态点（session-dot 色板，DESIGN.md components.session-dot）：执行中脉冲、汇总待唤醒、终态落成功/失败/待机。 */
const DISPATCH_STATUS_DOT: Record<string, string> = {
  running: 'bg-brand-accent status-pulse',
  collecting: 'bg-status-warning',
  completed: 'bg-status-success',
  degraded: 'bg-status-error',
  cancelled: 'bg-status-standby',
};

export interface DispatchTimelineProps {
  taskId: string;
  workspaceId: string;
}

/**
 * 派发时间线（会话元模型 S1）：一次发送 = 一个执行批次。
 * 卡片 = 触发消息摘录 + 状态 + 成员会话组（可展开）；数据来自
 * useDispatchesStore（抽屉打开时拉快照，dispatch.* 事件失效重取）。
 */
export function DispatchTimeline({ taskId, workspaceId }: DispatchTimelineProps) {
  const dispatches = useDispatchesStore((s) => s.byWorkItem[taskId]);
  const error = useDispatchesStore((s) => s.errorByWorkItem[taskId]);
  const refreshFor = useDispatchesStore((s) => s.refreshFor);

  useEffect(() => {
    if (captureScope().workspaceId === workspaceId) void refreshFor(taskId, workspaceId);
  }, [taskId, workspaceId, refreshFor]);

  return (
    <section className="space-y-snug">
      <h3 className="text-caption font-medium text-text-tertiary">派发时间线</h3>
      {error && <InlineRequestError message={error} onRetry={() => {
        if (captureScope().workspaceId === workspaceId) void refreshFor(taskId, workspaceId);
      }} />}
      {dispatches === undefined && !error ? (
        <p className="text-body text-text-tertiary">派发加载中…</p>
      ) : dispatches && dispatches.length === 0 && !error ? (
        <EmptyState
          icon={<MessagesSquare className="h-5 w-5" aria-hidden />}
          title="尚无派发记录"
          description="向该任务发送第一条消息后，这里会按批次展示每次派发的执行情况。"
        />
      ) : dispatches && dispatches.length > 0 ? (
        <ol className="space-y-1.5">
          {dispatches.map((card) => (
            <DispatchCardItem key={card.id} card={card} />
          ))}
        </ol>
      ) : null}
    </section>
  );
}

function InlineRequestError({ message, onRetry }: { message: string; onRetry: () => void }) {
  return (
    <div
      className="flex items-center justify-between gap-snug rounded-button border border-status-error/30 bg-status-error/5 px-snug py-tight"
      role="alert"
      aria-live="assertive"
    >
      <span className="text-caption text-status-error">{message}</span>
      <Button type="button" size="sm" variant="secondary" onClick={onRetry}>重试</Button>
    </div>
  );
}

/** 单张派发卡片：头行摘录 + 状态；展开列出成员会话与 Task 内正文。 */
export function DispatchCardItem({ card }: { card: DispatchCard }) {
  const [expanded, setExpanded] = useState(false);
  const excerpt = card.trigger_message?.excerpt ?? card.runs[0]?.summary ?? '';
  const membersId = `${card.id}-members`;

  return (
    <Card className="p-snug space-y-snug">
      <button
        type="button"
        onClick={() => setExpanded((v) => !v)}
        aria-expanded={expanded}
        aria-controls={membersId}
        className="w-full text-left"
        title={expanded ? '收起成员会话' : '展开成员会话'}
      >
        <div className="flex items-center gap-2">
          {expanded ? (
            <ChevronDown className="w-3.5 h-3.5 shrink-0 text-text-tertiary" aria-hidden />
          ) : (
            <ChevronRight className="w-3.5 h-3.5 shrink-0 text-text-tertiary" aria-hidden />
          )}
          <span className="shrink-0 inline-flex items-center px-1.5 py-0.5 rounded-sm text-caption font-medium bg-surface-base text-text-secondary border border-border-subtle">
            {TRIGGER_TEXT[card.trigger] ?? card.trigger}
          </span>
          {excerpt && (
            <span className="min-w-0 flex-1 truncate text-body text-text-secondary" title={excerpt}>
              {excerpt}
            </span>
          )}
          <span className="ml-auto shrink-0 flex items-center gap-2">
            <span className="text-caption text-text-tertiary tabular-nums">{card.runs.length} 个会话</span>
            <StatusPill>
              <span
                aria-hidden
                className={`w-1.5 h-1.5 rounded-full shrink-0 ${DISPATCH_STATUS_DOT[card.status] ?? 'bg-status-standby'}`}
              />
              {DISPATCH_STATUS_TEXT[card.status] ?? card.status}
            </StatusPill>
          </span>
        </div>
        <div className="flex items-center gap-comfortable mt-1 pl-6">
          <span className="text-caption text-text-tertiary">{formatDateTime(card.created_at)}</span>
          {card.closed_at && (
            <span className="text-caption text-text-tertiary">收口于 {formatDateTime(card.closed_at)}</span>
          )}
        </div>
      </button>
      {expanded && (
        <ul id={membersId} className="space-y-1 pl-6" aria-label="派发成员会话">
          {card.runs.map((run) => (
            <li key={run.id}>
              <DispatchRunRow run={run} />
            </li>
          ))}
        </ul>
      )}
    </Card>
  );
}

/** 成员会话行：Avatar + run 状态/时间 + 摘要；展开后在 Task 内展示该 Agent 正文。 */
export function DispatchRunRow({ run }: { run: DispatchRun }) {
  const [expanded, setExpanded] = useState(false);
  const runSnapshot = useRunsStore((state) => state.runs[run.id]);
  const name = run.agent_name ?? run.agent_profile_id ?? '未指派';
  const status = runSnapshot?.status ?? run.status;
  const outputId = `task-run-output-${run.id}`;
  return (
    <div className="space-y-1.5">
      <button
        type="button"
        onClick={() => setExpanded((value) => !value)}
        aria-expanded={expanded}
        aria-controls={expanded ? outputId : undefined}
        disabled={!run.agent_profile_id}
        title={run.agent_profile_id ? (expanded ? `收起 ${name} 的任务正文` : `查看 ${name} 的任务正文`) : '成员未绑定 agent，无法查看正文'}
        aria-label={run.agent_profile_id ? `${expanded ? '收起' : '查看'} ${name} 的任务正文` : '成员未绑定 agent，无法查看正文'}
        className="w-full flex items-center gap-2 p-1.5 rounded-lg border border-border-subtle bg-surface-base text-left transition-colors hover:bg-surface-raised disabled:opacity-50 disabled:cursor-not-allowed"
      >
        <Avatar name={name === '未指派' ? run.id : name} size={20} />
        <span className="shrink-0 text-caption font-medium text-text-primary">{name}</span>
        <span className={`shrink-0 text-caption ${runStatusColor(status)}`}>{runStatusText(status)}</span>
        {run.summary && (
          <span className="min-w-0 flex-1 truncate text-caption text-text-tertiary" title={run.summary}>
            {run.summary}
          </span>
        )}
        {runSnapshot?.created_at && (
          <span className="shrink-0 text-caption text-text-tertiary tabular-nums" title={`开始于 ${formatDateTime(runSnapshot.created_at)}`}>
            {formatDateTime(runSnapshot.created_at)}
          </span>
        )}
        {expanded ? <ChevronDown className="h-3.5 w-3.5 shrink-0 text-text-tertiary" aria-hidden /> : <ChevronRight className="h-3.5 w-3.5 shrink-0 text-text-tertiary" aria-hidden />}
      </button>
      {expanded && run.agent_profile_id && (
        <div id={outputId} className="pl-6">
          <TaskRunOutput run={run} agentName={name} />
        </div>
      )}
    </div>
  );
}
