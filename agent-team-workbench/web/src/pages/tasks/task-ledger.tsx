import { ScrollText } from 'lucide-react';
import { useCallback, useEffect, useRef, useState } from 'react';
import { getWorkItem, listTaskSessions } from '../../api/endpoints';
import type { DecisionEntry, TaskSession } from '../../api/types';
import { Button, EmptyState } from '../../components/ui';
import { useDecisionsStore } from '../../stores/decisions.store';
import { formatDateTime } from '../../utils/format';

export interface TaskLedgerProps {
  taskId: string;
  /** 有指派时拉会话锚点展示「第 N 段」（轮换代际）；缺省不展示。 */
  agentProfileId?: string;
}

/**
 * 任务台账（会话元模型 S2）：rolling_digest 滚动摘要 + 决策原话引文列表。
 * - digest 只随详情响应携带且无 SSE 推送：台账区自取详情快照，不读看板列表
 *   投影（列表 omitempty，SSE 刷新会把它剥掉）；
 * - 决策列表走 decisions store（冷启动 refreshFor + decision.created 失效重取）。
 */
export function TaskLedger({ taskId, agentProfileId }: TaskLedgerProps) {
  const decisions = useDecisionsStore((s) => s.byWorkItem[taskId]);
  const decisionsError = useDecisionsStore((s) => s.errorByWorkItem[taskId]);
  const refreshFor = useDecisionsStore((s) => s.refreshFor);
  // null = 加载中；'' = 尚无摘要（首轮会话收尾后由终态钩子生成）。
  const [digest, setDigest] = useState<string | null>(null);
  const [digestError, setDigestError] = useState<string | undefined>();
  const [segmentSeq, setSegmentSeq] = useState<number | null>(null);
  const [sessionError, setSessionError] = useState<string | undefined>();
  const digestRequest = useRef(0);
  const sessionRequest = useRef(0);

  const loadDigest = useCallback(async () => {
    const requestId = ++digestRequest.current;
    setDigest(null);
    setDigestError(undefined);
    try {
      const wi = await getWorkItem(taskId);
      if (requestId !== digestRequest.current) return;
      setDigest(wi.rolling_digest ?? '');
    } catch {
      if (requestId !== digestRequest.current) return;
      setDigestError('台账摘要加载失败，请重试');
    }
  }, [taskId]);

  useEffect(() => {
    void loadDigest();
  }, [loadDigest]);

  const loadSessions = useCallback(async () => {
    const requestId = ++sessionRequest.current;
    if (!agentProfileId) {
      setSegmentSeq(null);
      setSessionError(undefined);
      return;
    }
    setSessionError(undefined);
    try {
      const { items } = await listTaskSessions(agentProfileId);
      if (requestId !== sessionRequest.current) return;
      setSegmentSeq(latestSegmentSeq(items, taskId));
    } catch {
      if (requestId !== sessionRequest.current) return;
      setSessionError('会话参与线加载失败，请重试');
    }
  }, [agentProfileId, taskId]);

  useEffect(() => {
    void loadSessions();
  }, [loadSessions]);

  useEffect(() => {
    void refreshFor(taskId);
  }, [refreshFor, taskId]);

  const retryDecisions = useCallback(() => {
    void refreshFor(taskId);
  }, [refreshFor, taskId]);

  return (
    <TaskLedgerView
      digest={digest}
      digestError={digestError}
      onRetryDigest={() => void loadDigest()}
      decisions={decisions}
      decisionsError={decisionsError}
      onRetryDecisions={retryDecisions}
      segmentSeq={segmentSeq}
      sessionError={sessionError}
      onRetrySession={() => void loadSessions()}
    />
  );
}

/** task_key === 任务 id 的会话锚点里的当前段序（轮换代际取最大）；无锚点返回 null。 */
export function latestSegmentSeq(sessions: readonly TaskSession[], taskKey: string): number | null {
  const seq = sessions
    .filter((t) => t.task_key === taskKey)
    .reduce((max, t) => Math.max(max, t.segment_seq), 0);
  return seq > 0 ? seq : null;
}

export interface TaskLedgerViewProps {
  /** null = 加载中；'' = 尚无摘要。 */
  digest: string | null;
  digestError?: string;
  onRetryDigest?: () => void;
  decisions?: readonly DecisionEntry[];
  decisionsError?: string;
  onRetryDecisions?: () => void;
  segmentSeq?: number | null;
  sessionError?: string;
  onRetrySession?: () => void;
}

/** 台账展示面：摘要多行预格式化（空则 EmptyState）+ 决策原话引文（左线引文样式，区别于普通文本）。 */
export function TaskLedgerView({
  digest,
  digestError,
  onRetryDigest,
  decisions,
  decisionsError,
  onRetryDecisions,
  segmentSeq,
  sessionError,
  onRetrySession,
}: TaskLedgerViewProps) {
  return (
    <section className="space-y-snug">
      <h3 className="text-caption font-medium text-text-tertiary">
        任务台账{segmentSeq ? ` · 第 ${segmentSeq} 段` : ''}
      </h3>
      {sessionError && <InlineRequestError message={sessionError} onRetry={onRetrySession} />}
      {digestError ? (
        <InlineRequestError message={digestError} onRetry={onRetryDigest} />
      ) : digest === null ? (
        <p className="text-body text-text-tertiary">台账加载中…</p>
      ) : digest ? (
        <pre className="whitespace-pre-wrap break-words rounded-lg border border-border-subtle bg-surface-sunken px-snug py-tight font-zh text-caption text-text-secondary">
          {digest}
        </pre>
      ) : (
        <EmptyState
          icon={<ScrollText className="h-5 w-5" aria-hidden />}
          title="尚无台账摘要"
          description="会话轮次收尾后自动生成滚动摘要，无需手动维护。"
        />
      )}
      {decisionsError && <InlineRequestError message={decisionsError} onRetry={onRetryDecisions} />}
      <div className="space-y-1.5">
        {(decisions ?? []).map((entry) => (
          <figure key={entry.id} className="border-l-2 border-border-strong pl-snug">
            <blockquote className="whitespace-pre-wrap break-words text-body text-text-primary">
              {entry.quote}
            </blockquote>
            <figcaption className="mt-0.5 text-caption text-text-tertiary">
              <span title={entry.source_run_id ?? undefined}>
                {formatDateTime(entry.created_at)}
                {entry.source_run_id ? ` · 来自 ${entry.source_run_id}` : ''}
              </span>
            </figcaption>
          </figure>
        ))}
        {decisions !== undefined && decisions.length === 0 && !decisionsError && (
          <p className="text-caption text-text-tertiary">
            暂无决策原话；任务决策操作将在任务详情中完成。
          </p>
        )}
      </div>
    </section>
  );
}

function InlineRequestError({ message, onRetry }: { message: string; onRetry?: () => void }) {
  return (
    <div
      className="flex items-center justify-between gap-snug rounded-button border border-status-error/30 bg-status-error/5 px-snug py-tight"
      role="alert"
      aria-live="assertive"
    >
      <span className="text-caption text-status-error">{message}</span>
      {onRetry && <Button type="button" size="sm" variant="secondary" onClick={onRetry}>重试</Button>}
    </div>
  );
}
