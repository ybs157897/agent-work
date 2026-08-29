import { ChevronDown, ChevronRight, MessagesSquare } from 'lucide-react';
import { useEffect, useState } from 'react';
import type { DispatchCard, DispatchRun } from '../../api/types';
import { Avatar } from '../../components/avatar';
import { runStatusColor, runStatusText } from '../../components/status';
import { Card, EmptyState, StatusPill } from '../../components/ui';
import { useDispatchesStore } from '../../stores/dispatches.store';
import { formatDateTime } from '../../utils/format';

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
  /** 成员行点击 → 打开对应 chat（TaskDetail 的 openChildChat 同款导航）。 */
  onOpenRunChat: (run: DispatchRun) => void;
}

type OpenRunChat = DispatchTimelineProps['onOpenRunChat'];

/**
 * 派发时间线（会话元模型 S1）：一次发送 = 一个执行批次。
 * 卡片 = 触发消息摘录 + 状态 + 成员会话组（可展开）；数据来自
 * useDispatchesStore（抽屉打开时拉快照，dispatch.* 事件失效重取）。
 */
export function DispatchTimeline({ taskId, onOpenRunChat }: DispatchTimelineProps) {
  const dispatches = useDispatchesStore((s) => s.byWorkItem[taskId]);
  const refreshFor = useDispatchesStore((s) => s.refreshFor);

  useEffect(() => {
    void refreshFor(taskId);
  }, [taskId, refreshFor]);

  return (
    <section className="space-y-snug">
      <h3 className="text-caption font-medium text-text-tertiary">派发时间线</h3>
      {dispatches === undefined ? (
        <p className="text-body text-text-tertiary">派发加载中…</p>
      ) : dispatches.length === 0 ? (
        <EmptyState
          icon={<MessagesSquare className="h-5 w-5" aria-hidden />}
          title="尚无派发记录"
          description="向该任务发送第一条消息后，这里会按批次展示每次派发的执行情况。"
        />
      ) : (
        <ol className="space-y-1.5">
          {dispatches.map((card) => (
            <DispatchCardItem key={card.id} card={card} onOpenRunChat={onOpenRunChat} />
          ))}
        </ol>
      )}
    </section>
  );
}

/** 单张派发卡片：头行摘录 + 状态；展开列出成员会话，点击跳对应 chat。 */
export function DispatchCardItem({ card, onOpenRunChat }: { card: DispatchCard; onOpenRunChat: OpenRunChat }) {
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
              <DispatchRunRow run={run} onOpenRunChat={onOpenRunChat} />
            </li>
          ))}
        </ul>
      )}
    </Card>
  );
}

/** 成员会话行：Avatar + 名字 + run 状态 + 一行摘要；点击进对应会话。 */
export function DispatchRunRow({ run, onOpenRunChat }: { run: DispatchRun; onOpenRunChat: OpenRunChat }) {
  const name = run.agent_name ?? run.agent_profile_id ?? '未指派';
  return (
    <button
      type="button"
      onClick={() => onOpenRunChat(run)}
      disabled={!run.agent_profile_id}
      title={run.agent_profile_id ? `与 ${name} 的会话` : '成员未绑定 agent，无法打开会话'}
      className="w-full flex items-center gap-2 p-1.5 rounded-lg border border-border-subtle bg-surface-base text-left transition-colors hover:bg-surface-raised disabled:opacity-50 disabled:cursor-not-allowed"
    >
      <Avatar name={name === '未指派' ? run.id : name} size={20} />
      <span className="shrink-0 text-caption font-medium text-text-primary">{name}</span>
      <span className={`shrink-0 text-caption ${runStatusColor(run.status)}`}>{runStatusText(run.status)}</span>
      {run.summary && (
        <span className="min-w-0 flex-1 truncate text-caption text-text-tertiary" title={run.summary}>
          {run.summary}
        </span>
      )}
    </button>
  );
}
