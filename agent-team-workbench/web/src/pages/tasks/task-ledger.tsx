import { ScrollText } from 'lucide-react';
import { useEffect, useState } from 'react';
import { getWorkItem, listTaskSessions } from '../../api/endpoints';
import type { DecisionEntry, TaskSession } from '../../api/types';
import { EmptyState } from '../../components/ui';
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
  const refreshFor = useDecisionsStore((s) => s.refreshFor);
  // null = 加载中；'' = 尚无摘要（首轮会话收尾后由终态钩子生成）。
  const [digest, setDigest] = useState<string | null>(null);
  const [segmentSeq, setSegmentSeq] = useState<number | null>(null);

  useEffect(() => {
    let cancelled = false;
    setDigest(null);
    getWorkItem(taskId)
      .then((wi) => {
        if (!cancelled) setDigest(wi.rolling_digest ?? '');
      })
      // 只吞本次 GET 失败：按无摘要呈现，重开抽屉重试。
      .catch(() => {
        if (!cancelled) setDigest('');
      });
    return () => {
      cancelled = true;
    };
  }, [taskId]);

  useEffect(() => {
    void refreshFor(taskId);
  }, [taskId, refreshFor]);

  useEffect(() => {
    if (!agentProfileId) return;
    let cancelled = false;
    listTaskSessions(agentProfileId)
      .then(({ items }) => {
        if (!cancelled) setSegmentSeq(latestSegmentSeq(items, taskId));
      })
      .catch(() => undefined);
    return () => {
      cancelled = true;
    };
  }, [agentProfileId, taskId]);

  return (
    <TaskLedgerView
      digest={digest}
      decisions={decisions}
      segmentSeq={segmentSeq}
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
  decisions?: readonly DecisionEntry[];
  segmentSeq?: number | null;
}

/** 台账展示面：摘要多行预格式化（空则 EmptyState）+ 决策原话引文（左线引文样式，区别于普通文本）。 */
export function TaskLedgerView({ digest, decisions, segmentSeq }: TaskLedgerViewProps) {
  return (
    <section className="space-y-snug">
      <h3 className="text-caption font-medium text-text-tertiary">
        任务台账{segmentSeq ? ` · 第 ${segmentSeq} 段` : ''}
      </h3>
      {digest === null ? (
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
        {decisions !== undefined && decisions.length === 0 && (
          <p className="text-caption text-text-tertiary">
            暂无决策原话；在对话里用「钉为决策」把关键结论记到这里。
          </p>
        )}
      </div>
    </section>
  );
}
