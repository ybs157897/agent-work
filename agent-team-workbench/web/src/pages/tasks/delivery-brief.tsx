import { AlertCircle, Clock3, FileDiff, Package, ScrollText, ShieldCheck, TriangleAlert } from 'lucide-react';
import { useEffect } from 'react';
import type { AttemptSummary, DeliveryBrief, RiskItem, TaskComment } from '../../api/types';
import { Button, StatusPill } from '../../components/ui';
import { useBriefStore } from '../../stores/brief.store';
import { captureScope } from '../../stores/scope';
import { formatDateTime } from '../../utils/format';
import { coordinatorStageText, coordinatorStatusText } from './coordinator-panel';

const ATTEMPT_ROLE_TEXT: Record<AttemptSummary['role'], string> = {
  coordinator: '协调',
  worker: '执行',
  evaluation: '评估',
};

const EVIDENCE_STATUS_TEXT: Record<string, string> = {
  passed: '通过',
  failed: '失败',
  warning: '警告',
  unknown: '未知',
};

const EVIDENCE_STATUS_CLASS: Record<string, string> = {
  passed: 'text-status-success',
  failed: 'text-status-error',
  warning: 'text-status-warning',
  unknown: 'text-text-tertiary',
};

const SEVERITY_CLASS: Record<RiskItem['severity'], string> = {
  high: 'border-status-error/30 bg-status-error/5 text-status-error',
  medium: 'border-status-warning/30 bg-status-warning/5 text-status-warning',
  low: 'border-border-subtle bg-surface-base text-text-secondary',
};

const COMMENT_KIND_TEXT: Record<TaskComment['kind'], string> = {
  note: '备注',
  requirement: '需求补充',
  review_feedback: '打回理由',
};

/**
 * 交付简报（任务控制面 RFC §4.11/§12.5）：Task Detail 内独立只读 section。
 * - 复用 Coordinator 状态文案与评论行样式；只展示 Run/文件定位信息，不渲染
 *   第二棵 AgentOutput（正文仍以 Run 时间线唯一投影为准）；
 * - 状态机：loading/current/stale/partial/error；empty 只表示真的无证据。
 */
export function DeliveryBriefPanel({ taskId, workspaceId }: { taskId: string; workspaceId: string }) {
  const brief = useBriefStore((s) => s.byWorkItem[taskId]);
  const status = useBriefStore((s) => s.statusByWorkItem[taskId] ?? 'idle');
  const error = useBriefStore((s) => s.errorByWorkItem[taskId]);
  const stale = useBriefStore((s) => s.staleByWorkItem[taskId] ?? false);
  const refreshFor = useBriefStore((s) => s.refreshFor);

  const refresh = () => {
    const scope = captureScope();
    if (scope.workspaceId !== workspaceId) return;
    void refreshFor(taskId, workspaceId);
  };

  useEffect(() => {
    const scope = captureScope();
    if (scope.workspaceId === workspaceId) void refreshFor(taskId, workspaceId);
  }, [taskId, workspaceId, refreshFor]);

  return (
    <DeliveryBriefView
      brief={brief}
      loading={status === 'loading'}
      error={error}
      stale={stale}
      onRefresh={refresh}
    />
  );
}

/** 纯展示视图（props 驱动；容器负责 store 接线）。 */
export function DeliveryBriefView({
  brief,
  loading,
  error,
  stale,
  onRefresh,
}: {
  brief?: DeliveryBrief;
  loading: boolean;
  error: string | undefined;
  stale: boolean;
  onRefresh: () => void;
}) {
  return (
    <section id={brief ? `delivery-brief-${brief.work_item.id}` : undefined} className="space-y-snug" aria-label="交付简报">
      <div className="flex items-center justify-between gap-snug">
        <h3 className="text-caption font-medium text-text-tertiary">交付简报</h3>
        <button
          type="button"
          onClick={onRefresh}
          title="刷新交付简报"
          aria-label="刷新交付简报"
          className="inline-flex h-7 w-7 items-center justify-center rounded-button text-text-tertiary transition-colors hover:bg-surface-base hover:text-text-primary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-brand-primary/40"
        >
          <ScrollText className="h-3.5 w-3.5" aria-hidden />
        </button>
      </div>

      {error && (
        <div
          role="alert"
          className="flex items-start justify-between gap-snug rounded-button border border-status-error/20 bg-status-error/5 px-snug py-tight"
        >
          <p className="flex items-start gap-tight text-caption text-status-error">
            <AlertCircle className="mt-0.5 h-3.5 w-3.5 shrink-0" aria-hidden />
            {error}
          </p>
          <Button size="sm" variant="danger-outline" onClick={onRefresh}>
            重试
          </Button>
        </div>
      )}

      {loading && !brief ? (
        <p className="text-caption text-text-tertiary" role="status">
          交付简报加载中…
        </p>
      ) : !brief ? (
        !error && (
          <p className="text-body text-text-tertiary">暂无交付简报；任务产生验收证据后会在这里汇总。</p>
        )
      ) : (
        <BriefContent brief={brief} stale={stale} onRefresh={onRefresh} />
      )}
    </section>
  );
}

function BriefContent({ brief, stale, onRefresh }: { brief: DeliveryBrief; stale: boolean; onRefresh: () => void }) {
  const freshness = brief.freshness;
  const hasEvidence =
    brief.attempts.length > 0 || brief.runs.length > 0 || brief.artifacts.length > 0
    || brief.comments.length > 0 || brief.risks.length > 0 || brief.changes !== null;

  return (
    <div className="space-y-snug rounded-card border border-border-subtle bg-surface-base p-snug">
      {/* 状态行：current / stale / partial 三态水印（文字 + 图标双通道） */}
      <div className="flex flex-wrap items-center gap-tight">
        {stale ? (
          <span
            role="status"
            className="inline-flex items-center gap-1 rounded-sm border border-status-warning/30 bg-status-warning/10 px-1.5 py-0.5 text-caption text-status-warning"
          >
            <Clock3 className="h-3 w-3" aria-hidden />
            可能有更新 · 正在后台补拉
          </span>
        ) : (
          <StatusPill title={`聚合水位：事件序号 ${freshness.as_of_event_seq}`}>
            与实时事件保持同步
          </StatusPill>
        )}
        {freshness.state === 'partial' && (
          <span
            role="status"
            title={`缺失来源：${(freshness.missing_sources ?? []).join('、')}`}
            className="inline-flex items-center gap-1 rounded-sm border border-status-warning/30 bg-status-warning/10 px-1.5 py-0.5 text-caption text-status-warning"
          >
            <TriangleAlert className="h-3 w-3" aria-hidden />
            部分来源缺失（{freshness.missing_sources?.length ?? 0}）
          </span>
        )}
        <span className="text-caption text-text-tertiary tabular-nums">
          聚合于 {formatDateTime(freshness.generated_at)}
        </span>
        {stale && (
          <Button size="sm" variant="secondary" onClick={onRefresh}>
            立即刷新
          </Button>
        )}
      </div>

      {!hasEvidence ? (
        // empty 只表示真的无证据：服务端聚合为空，而非加载失败。
        <p className="text-body text-text-tertiary">
          该任务还没有可验收的执行证据（尝试、变更、产物或评论）。
        </p>
      ) : (
        <>
          {/* 验收标准 */}
          {brief.acceptance_criteria.length > 0 && (
            <div>
              <h4 className="mb-1 text-caption font-medium text-text-tertiary">验收标准</h4>
              <ol className="list-decimal space-y-0.5 pl-5">
                {brief.acceptance_criteria.map((criterion) => (
                  <li key={criterion} className="text-body text-text-secondary">{criterion}</li>
                ))}
              </ol>
            </div>
          )}

          {/* Coordinator 结论 */}
          <div className="rounded-button border border-border-subtle bg-surface-raised px-snug py-tight">
            <div className="flex flex-wrap items-center gap-tight">
              <span className="inline-flex items-center gap-1 text-caption font-medium text-text-secondary">
                <ShieldCheck className="h-3.5 w-3.5 text-brand-primary" aria-hidden />
                Coordinator 结论
              </span>
              <span className="text-caption text-text-secondary">{coordinatorStatusText(brief.conclusion.coordinator_status)}</span>
              {brief.conclusion.stage && (
                <span className="text-caption text-text-tertiary">{coordinatorStageText(brief.conclusion.stage)}</span>
              )}
            </div>
            {brief.conclusion.summary && (
              <p className="mt-1 whitespace-pre-wrap text-body text-text-primary">{brief.conclusion.summary}</p>
            )}
            {brief.conclusion.next_action && (
              <p className="mt-1 text-caption text-text-secondary">下一步：{brief.conclusion.next_action}</p>
            )}
          </div>

          {/* 尝试序列 */}
          {brief.attempts.length > 0 && (
            <div>
              <h4 className="mb-1 text-caption font-medium text-text-tertiary">
                尝试（{brief.attempts.length}{brief.truncation.attempts ? '，已截断' : ''}）
              </h4>
              <ol className="space-y-1">
                {brief.attempts.map((attempt) => (
                  <AttemptRow key={`${attempt.run_id}:${attempt.attempt}`} attempt={attempt} />
                ))}
              </ol>
            </div>
          )}

          {/* 运行证据 */}
          {brief.runs.length > 0 && (
            <div>
              <h4 className="mb-1 text-caption font-medium text-text-tertiary">
                运行证据（{brief.runs.length}{brief.truncation.runs ? '，已截断' : ''}）
              </h4>
              <div className="space-y-1">
                {brief.runs.map((entry) => (
                  <div key={entry.run.id} className="rounded-button border border-border-subtle bg-surface-raised px-snug py-tight">
                    <div className="flex items-center justify-between gap-snug text-caption">
                      <span className="font-medium text-text-secondary">run {entry.run.id}</span>
                      <span className="text-text-tertiary">{entry.run.status}</span>
                    </div>
                    {entry.summary && <p className="mt-0.5 text-caption text-text-secondary">{entry.summary}</p>}
                    {entry.evidence.length > 0 && (
                      <ul className="mt-1 space-y-0.5">
                        {entry.evidence.map((evidence) => (
                          <li key={evidence.id} className="flex items-center justify-between gap-snug text-caption">
                            <span className="truncate text-text-secondary" title={`${evidence.source_kind} · ${evidence.source_id}`}>
                              {evidence.label || evidence.source_kind}
                            </span>
                            <span className="flex shrink-0 items-center gap-2">
                              <span className="text-text-tertiary">{evidence.trust === 'control_plane' ? '控制面' : evidence.trust === 'runtime_reported' ? '运行时上报' : '模型自述'}</span>
                              <span className={EVIDENCE_STATUS_CLASS[evidence.status]}>
                                {EVIDENCE_STATUS_TEXT[evidence.status] ?? evidence.status}
                              </span>
                            </span>
                          </li>
                        ))}
                      </ul>
                    )}
                  </div>
                ))}
              </div>
            </div>
          )}

          {/* 变更集（按 Run 分组，不混成单个 diff） */}
          {brief.changes && brief.changes.files.length > 0 && (
            <div>
              <h4 className="mb-1 flex items-center gap-1 text-caption font-medium text-text-tertiary">
                <FileDiff className="h-3.5 w-3.5" aria-hidden />
                变更（{brief.changes.total_files} 个文件 +{brief.changes.total_added} −{brief.changes.total_deleted}
                {brief.changes.truncated ? '，已截断' : ''}）
              </h4>
              <ul className="space-y-0.5">
                {brief.changes.files.map((file) => (
                  <li key={`${brief.changes!.run_id}:${file.path}`} className="flex items-center justify-between gap-snug text-caption">
                    <span className="truncate text-text-secondary" title={file.path}>{file.path}</span>
                    <span className="shrink-0 tabular-nums">
                      <span className="text-status-success">+{file.added}</span>{' '}
                      <span className="text-status-error">−{file.deleted}</span>
                    </span>
                  </li>
                ))}
              </ul>
            </div>
          )}

          {/* 产物 */}
          {brief.artifacts.length > 0 && (
            <div>
              <h4 className="mb-1 flex items-center gap-1 text-caption font-medium text-text-tertiary">
                <Package className="h-3.5 w-3.5" aria-hidden />
                产物（{brief.artifacts.length}{brief.truncation.artifacts ? '，已截断' : ''}）
              </h4>
              <ul className="space-y-0.5">
                {brief.artifacts.map((artifact) => (
                  <li key={artifact.id} className="flex items-center justify-between gap-snug text-caption text-text-secondary">
                    <span className="truncate" title={artifact.logical_path}>{artifact.logical_path}</span>
                    <span className="shrink-0 tabular-nums text-text-tertiary">
                      {(artifact.size / 1024).toFixed(1)} KB · {artifact.status}
                    </span>
                  </li>
                ))}
              </ul>
            </div>
          )}

          {/* 阻塞与残余风险 */}
          {(brief.blocker || brief.risks.length > 0) && (
            <div className="space-y-1">
              <h4 className="text-caption font-medium text-text-tertiary">阻塞与残余风险</h4>
              {brief.blocker && (
                <div className="rounded-button border border-status-error/20 bg-status-error/5 px-snug py-tight">
                  <p className="text-caption font-medium text-status-error">阻塞 · {brief.blocker.code}</p>
                  <p className="text-caption text-text-primary">{brief.blocker.message}</p>
                </div>
              )}
              {brief.risks.map((risk) => (
                <div
                  key={`${risk.source_kind}:${risk.source_id}:${risk.code}`}
                  className={`rounded-button border px-snug py-tight ${SEVERITY_CLASS[risk.severity]}`}
                >
                  <p className="text-caption font-medium">{risk.code}</p>
                  <p className="text-caption">{risk.message}</p>
                </div>
              ))}
            </div>
          )}

          {/* requirement / review_feedback 评论（只读引文） */}
          {brief.comments.length > 0 && (
            <div>
              <h4 className="mb-1 text-caption font-medium text-text-tertiary">
                需求与打回记录（{brief.comments.length}{brief.truncation.comments ? '，已截断' : ''}）
              </h4>
              <ul className="space-y-1">
                {brief.comments.map((comment) => (
                  <li key={comment.id} className="rounded-button border border-border-subtle bg-surface-raised px-snug py-tight">
                    <div className="flex items-center gap-tight text-caption text-text-tertiary">
                      <span className="rounded-sm border border-border-subtle bg-surface-base px-1 py-0.5 font-medium text-text-secondary">
                        {COMMENT_KIND_TEXT[comment.kind]}
                      </span>
                      #{comment.revision} · {formatDateTime(comment.created_at)}
                    </div>
                    <p className="mt-0.5 whitespace-pre-wrap break-words text-body text-text-primary">{comment.body}</p>
                  </li>
                ))}
              </ul>
            </div>
          )}
        </>
      )}
    </div>
  );
}

function AttemptRow({ attempt }: { attempt: AttemptSummary }) {
  return (
    <li className="flex items-start justify-between gap-snug rounded-button border border-border-subtle bg-surface-raised px-snug py-tight">
      <div className="min-w-0">
        <div className="flex flex-wrap items-center gap-tight text-caption">
          <span className="font-medium text-text-secondary">#{attempt.attempt}</span>
          <span className="text-text-tertiary">{ATTEMPT_ROLE_TEXT[attempt.role]}</span>
          <span className="text-text-secondary">{attempt.agent_name || attempt.agent_id || 'Coordinator'}</span>
          {attempt.retry_of && (
            <span className="text-status-warning" title={`重试自 run ${attempt.retry_of}`}>重试</span>
          )}
        </div>
        {attempt.failure && (
          <p className="mt-0.5 text-caption text-status-error">
            {attempt.failure.code ? `${attempt.failure.code}：` : ''}{attempt.failure.message}
          </p>
        )}
      </div>
      <div className="shrink-0 text-right">
        <p className="text-caption text-text-secondary">{attempt.status}</p>
        {attempt.finished_at && (
          <p className="text-caption text-text-tertiary tabular-nums">{formatDateTime(attempt.finished_at)}</p>
        )}
      </div>
    </li>
  );
}
