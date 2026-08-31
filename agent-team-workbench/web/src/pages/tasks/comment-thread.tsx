import { AlertCircle, RefreshCw, Send } from 'lucide-react';
import { useEffect, useRef, useState } from 'react';
import { ApiError } from '../../api/client';
import type { TaskComment, TaskCommentCreateInput, WorkItem } from '../../api/types';
import { Button, Select, Textarea } from '../../components/ui';
import { useCommentsStore } from '../../stores/comments.store';
import { captureScope, isCurrentWorkspaceEntity } from '../../stores/scope';
import type { CoordinatorResolution } from '../../utils/task-phase';
import { formatDateTime } from '../../utils/format';

const KIND_LABEL: Record<TaskComment['kind'], string> = {
  note: '备注',
  requirement: '需求补充',
  review_feedback: '打回理由',
};

const KIND_CLASS: Record<TaskComment['kind'], string> = {
  note: 'bg-surface-base text-text-secondary border-border-subtle',
  requirement: 'bg-brand-primary/10 text-brand-accent border-brand-primary/20',
  review_feedback: 'bg-status-warning/10 text-status-warning border-status-warning/20',
};

/**
 * 任务评论区（任务控制面 RFC §4.9/§9.4）：
 * - 权威来源 GET /work-items/{id}/comments（根 Task 维度 append-only 流）；
 * - composer 支持 note（不触发 Coordinator）与 requirement（actionable，
 *   携带 expected_work_item_version 防基于旧版本追加）；写命令由 apiFetch
 *   自动携带 Idempotency-Key，重试复用同一 client_key 防业务意图重复；
 * - 加载/错误/空/增量状态分清：403/404/409/422 是真实拒绝，不伪装空态；
 *   刷新失败时保留已加载评论并显示可重试错误。
 */
/**
 * 评论的 revision/cursor 是根 Task 维度的事实。`parent_id` 只代表直接父节点，
 * 三级以上树不能据此猜 root；Coordinator snapshot 的 root_work_item_id 才是
 * coordinated child 的 canonical identity。
 */
export function commentRootWorkItemId(
  task: WorkItem,
  coordinatorResolution: CoordinatorResolution,
  coordinatorRootWorkItemId?: string,
): string | undefined {
  if (!task.parent_id) return task.id;
  if (coordinatorResolution === 'legacy') return task.id;
  return coordinatorResolution === 'coordinated' ? coordinatorRootWorkItemId : undefined;
}

export function CommentThread({
  task,
  coordinatorResolution,
  coordinatorRootWorkItemId,
}: {
  task: WorkItem;
  coordinatorResolution: CoordinatorResolution;
  coordinatorRootWorkItemId?: string;
}) {
  const rootWorkItemId = commentRootWorkItemId(task, coordinatorResolution, coordinatorRootWorkItemId);
  // 先无条件订阅，避免 child 在 Coordinator snapshot 到达时改变 Hook 调用顺序。
  const commentKey = rootWorkItemId ?? '';
  const comments = useCommentsStore((s) => s.byRoot[commentKey]);
  const latestRevision = useCommentsStore((s) => s.latestRevisions[commentKey]);
  const error = useCommentsStore((s) => s.errorByRoot[commentKey]);
  const loading = useCommentsStore((s) => s.loadingByRoot[commentKey]);
  const refreshFor = useCommentsStore((s) => s.refreshFor);
  const create = useCommentsStore((s) => s.create);

  useEffect(() => {
    if (!rootWorkItemId || coordinatorResolution === 'legacy') return;
    const scope = captureScope();
    if (isCurrentWorkspaceEntity(scope, task)) void refreshFor(rootWorkItemId, task.workspace_id);
  }, [coordinatorResolution, rootWorkItemId, refreshFor, task]);

  if (!rootWorkItemId) {
    return (
      <section className="space-y-snug" aria-label="任务评论">
        <h3 className="text-caption font-medium text-text-tertiary">评论与反馈</h3>
        <p className="text-caption text-text-tertiary" role="status">
          正在确认任务树归属…
        </p>
      </section>
    );
  }

  return (
    <CommentThreadView
      task={task}
      comments={comments}
      latestRevision={latestRevision}
      error={error}
      loading={loading ?? false}
      onRefresh={() => {
        const scope = captureScope();
        if (isCurrentWorkspaceEntity(scope, task)) void refreshFor(rootWorkItemId, task.workspace_id);
      }}
      onCreate={(input) => {
        const scope = captureScope();
        if (!isCurrentWorkspaceEntity(scope, task)) return Promise.reject(new Error('任务不属于当前工作区'));
        return create(rootWorkItemId, task.id, input, task.workspace_id);
      }}
      legacy={coordinatorResolution === 'legacy'}
    />
  );
}

/** 纯展示视图（props 驱动，容器负责 store 接线）。 */
export function CommentThreadView({
  task,
  comments,
  latestRevision,
  error,
  loading,
  onRefresh,
  onCreate,
  legacy = false,
}: {
  task: WorkItem;
  comments: TaskComment[] | undefined;
  latestRevision: number | undefined;
  error: string | undefined;
  loading: boolean;
  onRefresh: () => void;
  onCreate: (input: TaskCommentCreateInput) => Promise<TaskComment>;
  /** 历史 Task 树无 Coordinator comment cursor：不暴露必失败的 GET/POST 控件。 */
  legacy?: boolean;
}) {
  if (legacy) {
    return (
      <section className="space-y-snug" aria-label="任务评论">
        <h3 className="text-caption font-medium text-text-tertiary">评论与反馈</h3>
        <p className="rounded-button border border-border-subtle bg-surface-base px-snug py-tight text-body text-text-secondary" role="status">
          该历史任务树未启用 Coordinator 评论流，不支持添加评论。
        </p>
      </section>
    );
  }
  const loadedMax = comments && comments.length > 0 ? comments[comments.length - 1].revision : undefined;
  const hasMore = latestRevision !== undefined && (loadedMax === undefined || loadedMax < latestRevision);
  // completed/cancelled 后端拒绝新增评论（409 comment_terminal_work_item）：不提供入口。
  const composerVisible = task.status !== 'completed' && task.status !== 'cancelled';

  return (
    <section className="space-y-snug" aria-label="任务评论">
      <div className="flex items-center justify-between gap-snug">
        <h3 className="text-caption font-medium text-text-tertiary">
          评论与反馈{comments && comments.length > 0 ? `（${comments.length}）` : ''}
        </h3>
        <button
          type="button"
          onClick={onRefresh}
          title="刷新评论"
          aria-label="刷新评论"
          className="inline-flex h-7 w-7 items-center justify-center rounded-button text-text-tertiary transition-colors hover:bg-surface-base hover:text-text-primary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-brand-primary/40"
        >
          <RefreshCw className="h-3.5 w-3.5" aria-hidden />
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

      {loading && !comments ? (
        <p className="text-caption text-text-tertiary" role="status">
          评论加载中…
        </p>
      ) : !comments || comments.length === 0 ? (
        <p className="text-body text-text-tertiary">
          还没有评论。用备注记录上下文，或提交需求补充给 Coordinator。
        </p>
      ) : (
        <ol className="space-y-tight">
          {comments.map((comment) => (
            <CommentRow key={comment.id} comment={comment} />
          ))}
        </ol>
      )}

      {hasMore && (
        <Button size="sm" variant="secondary" onClick={onRefresh}>
          加载更多评论
        </Button>
      )}

      {composerVisible && <CommentComposer task={task} onCreate={onCreate} />}
    </section>
  );
}

function CommentRow({ comment }: { comment: TaskComment }) {
  return (
    <li className="rounded-card border border-border-subtle bg-surface-base px-snug py-tight">
      <div className="flex items-center gap-tight">
        <span
          className={`inline-flex items-center rounded-sm border px-1.5 py-0.5 text-caption font-medium ${KIND_CLASS[comment.kind]}`}
        >
          {KIND_LABEL[comment.kind]}
        </span>
        <span className="text-caption text-text-tertiary">
          {comment.actor_kind === 'user' ? comment.actor_id : comment.actor_kind} ·{' '}
          {formatDateTime(comment.created_at)} · #{comment.revision}
        </span>
      </div>
      <p className="mt-1 whitespace-pre-wrap break-words text-body text-text-primary">{comment.body}</p>
    </li>
  );
}

/** note/requirement 双模式 composer：requirement 触发 Coordinator 消费。 */
function CommentComposer({
  task,
  onCreate,
}: {
  task: WorkItem;
  onCreate: (input: TaskCommentCreateInput) => Promise<TaskComment>;
}) {
  const [kind, setKind] = useState<'note' | 'requirement'>('note');
  const [body, setBody] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);
  // 业务意图幂等键：失败重试复用同一 key（防重试造出两条评论），成功后作废。
  const clientKeyRef = useRef<string | null>(null);

  const submit = async () => {
    const trimmed = body.trim();
    if (!trimmed || submitting) return;
    setSubmitting(true);
    setError(null);
    if (!clientKeyRef.current) clientKeyRef.current = `comment:${task.id}:${crypto.randomUUID()}`;
    try {
      await onCreate({
        kind,
        body: trimmed,
        client_key: clientKeyRef.current,
        // requirement 是 actionable：携带版本防基于终态/旧 phase 追加（RFC §9.4）。
        ...(kind === 'requirement' ? { expected_work_item_version: task.version } : {}),
      });
      clientKeyRef.current = null;
      setBody('');
    } catch (err) {
      setError(
        err instanceof ApiError
          ? err.isVersionConflict
            ? '任务已被他人更新，请刷新后重试'
            : err.message
          : '提交失败，请重试',
      );
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <div className="space-y-tight rounded-card border border-brand-primary/20 bg-brand-primary/5 p-snug">
      <div className="flex items-center gap-snug">
        <label className="flex items-center gap-tight text-caption text-text-secondary">
          类型
          <Select
            aria-label="评论类型"
            value={kind}
            onChange={(e) => setKind(e.target.value === 'requirement' ? 'requirement' : 'note')}
            disabled={submitting}
            wrapperClassName="mt-0"
            className="w-auto"
          >
            <option value="note">备注（不触发执行）</option>
            <option value="requirement">需求补充（交给 Coordinator）</option>
          </Select>
        </label>
      </div>
      <Textarea
        aria-label="评论内容"
        value={body}
        onChange={(e) => setBody(e.target.value)}
        rows={2}
        invalid={error !== null}
        placeholder={kind === 'requirement' ? '例如：登录失败时补充错误提示与重试说明' : '补充上下文或记录结论…'}
        className="resize-none"
      />
      {error && (
        <p role="alert" className="text-caption text-status-error">
          {error}
        </p>
      )}
      <div className="flex justify-end">
        <Button
          variant="primary"
          size="sm"
          onClick={() => void submit()}
          disabled={!body.trim() || submitting}
        >
          <span className="inline-flex items-center gap-tight">
            <Send className="h-3.5 w-3.5" aria-hidden />
            {submitting ? '提交中…' : kind === 'requirement' ? '提交需求' : '添加备注'}
          </span>
        </Button>
      </div>
    </div>
  );
}
