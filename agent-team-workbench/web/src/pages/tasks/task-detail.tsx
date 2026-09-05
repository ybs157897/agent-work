import { ArrowLeft, Check, RotateCcw } from 'lucide-react';
import { useEffect, useRef, useState } from 'react';
import { ApiError } from '../../api/client';
import { acceptWorkItem, getWorkItem } from '../../api/endpoints';
import type { WorkItem } from '../../api/types';
import { ErrorState } from '../../components/async-state';
import { Drawer } from '../../components/drawer';
import { captureScope, isCurrent, isCurrentWorkspaceEntity } from '../../stores/scope';
import { useTasksStore } from '../../stores/tasks.store';
import { toast } from '../../stores/toast.store';
import { DispatchTimeline } from './dispatch-timeline';
import { ReturnTaskModal } from './return-modal';

const STATUS_TEXT: Record<string, string> = {
  todo: '待办',
  in_progress: '进行中',
  blocked: '阻塞',
  completed: '已完成',
  cancelled: '已取消',
};

function workItemKey(id: string): string {
  const tail = id.replace(/^wi_?/i, '').slice(-6).toUpperCase();
  return `WI-${tail || '------'}`;
}

/** 总任务详情：唯一操作是总任务验收；正文只承载 Worker 输入与最终输出。 */
export function TaskDetail({
  taskId,
  error,
  open = true,
  onClose,
  onExitComplete,
  fullPage = false,
}: {
  taskId: string | null;
  error?: string;
  open?: boolean;
  onClose: () => void;
  onExitComplete?: () => void;
  fullPage?: boolean;
}) {
  const task = useTasksStore((state) => (taskId ? state.items.find((item) => item.id === taskId) : undefined));
  const upsert = useTasksStore((state) => state.upsert);
  const [returning, setReturning] = useState(false);
  const [accepting, setAccepting] = useState(false);
  const currentTaskIdRef = useRef(taskId);
  useEffect(() => {
    currentTaskIdRef.current = taskId;
    return () => {
      if (currentTaskIdRef.current === taskId) currentTaskIdRef.current = null;
    };
  }, [taskId]);
  useEffect(() => {
    setAccepting(false);
    setReturning(false);
  }, [taskId]);

  const accept = async () => {
    if (!task || task.parent_id || task.review?.can_accept !== true || accepting) return;
    const scope = captureScope();
    if (!isCurrentWorkspaceEntity(scope, task)) {
      toast.error('该任务不属于当前工作区，无法验收。');
      onClose();
      return;
    }
    setAccepting(true);
    try {
      const updated = await acceptWorkItem(task.id, task.version);
      if (currentTaskIdRef.current !== task.id) return;
      if (!isCurrentWorkspaceEntity(scope, updated)) return;
      upsert(updated);
      toast.success(`总任务「${task.title}」验收通过`);
      onClose();
    } catch (acceptError) {
      if (!isCurrent(scope) || currentTaskIdRef.current !== task.id) return;
      if (acceptError instanceof ApiError && (acceptError.isVersionConflict || acceptError.code === 'review_state_conflict')) {
        toast.error('总任务状态已更新，请重新确认');
        const latest = await getWorkItem(task.id).catch(() => undefined);
        if (latest && isCurrentWorkspaceEntity(scope, latest)) upsert(latest);
      } else {
        toast.error(acceptError instanceof ApiError ? acceptError.message : '验收失败，请重试');
      }
    } finally {
      if (isCurrent(scope) && currentTaskIdRef.current === task.id) setAccepting(false);
    }
  };

  const content = (
    <TaskPeekContent
      task={task}
      error={error}
      accepting={accepting}
      onAccept={() => void accept()}
      onReturn={() => setReturning(true)}
      onClose={onClose}
      showBack={fullPage}
    />
  );

  return (
    <>
      {fullPage ? (
        <main className="plane-board plane-task-detail h-full min-h-0 overflow-y-auto">{content}</main>
      ) : (
        <Drawer
          open={open && taskId !== null}
          onClose={onClose}
          onExitComplete={onExitComplete}
          ariaLabel={`${task?.title ?? '总任务'}详情`}
          skin="task"
          width={880}
        >
          {content}
        </Drawer>
      )}
      <ReturnTaskModal
        task={returning && task && !task.parent_id && task.review?.can_return === true ? task : null}
        onClose={() => setReturning(false)}
        onReturned={onClose}
      />
    </>
  );
}

export function TaskPeekContent({
  task,
  error,
  accepting,
  onAccept,
  onReturn,
  onClose,
  showBack = false,
}: {
  task?: WorkItem;
  error?: string;
  accepting: boolean;
  onAccept: () => void;
  onReturn: () => void;
  onClose: () => void;
  showBack?: boolean;
}) {
  if (error) {
    return <ErrorState message={error} actionLabel="返回任务看板" onRetry={onClose} />;
  }
  if (!task || task.parent_id) {
    return (
      <div className="plane-board plane-task-peek flex min-h-48 items-center justify-center" role="status">
        <p className="text-body text-text-tertiary">正在打开总任务…</p>
      </div>
    );
  }

  const review = task.review;
  const reviewReady = review?.ready === true;
  const canAccept = review?.can_accept === true;
  const canReturn = review?.can_return === true;
  const inReviewPhase = task.status === 'in_progress' && (task.phase === 'review' || task.phase === 'acceptance');
  const isReviewReady = inReviewPhase && reviewReady;
  const statusLabel = isReviewReady ? '待验收' : STATUS_TEXT[task.status] ?? task.status;
  return (
    <div className="plane-board plane-task-peek" data-task-peek={task.id}>
      <header className="plane-task-peek-header">
        {showBack && (
          <button
            type="button"
            onClick={onClose}
            className="mb-base inline-flex min-h-8 items-center gap-tight rounded-button px-tight text-caption font-medium text-text-secondary transition-colors hover:bg-surface-sunken hover:text-text-primary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-brand-primary/40"
          >
            <ArrowLeft className="h-4 w-4" aria-hidden />
            返回任务看板
          </button>
        )}
        <div className="flex flex-wrap items-center gap-tight text-caption text-text-tertiary">
          <span className="font-mono">{workItemKey(task.id)}</span>
          <span aria-hidden>·</span>
          <span className={`rounded-button border px-tight py-micro font-medium ${isReviewReady
            ? 'border-status-warning/40 bg-status-warning/10 text-status-warning'
            : 'border-border-subtle bg-surface-sunken text-text-secondary'}`}>
            {statusLabel}
          </span>
        </div>
        <div className="mt-snug flex flex-wrap items-start justify-between gap-base">
          <h1 className="min-w-0 flex-1 text-h2 font-semibold leading-tight text-text-primary">{task.title}</h1>
          {(canAccept || canReturn) && (
            <div className="plane-task-peek-actions flex shrink-0 flex-wrap items-center gap-tight" aria-label="总任务验收">
              {canReturn && (
                <button
                  type="button"
                  onClick={onReturn}
                  className="inline-flex min-h-9 items-center gap-tight rounded-button border border-status-warning/50 bg-surface-raised px-snug text-body font-medium text-status-warning transition-colors hover:bg-status-warning/5 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-status-warning/30"
                >
                  <RotateCcw className="h-3.5 w-3.5" aria-hidden />
                  打回总任务
                </button>
              )}
              {canAccept && (
                <button
                  type="button"
                  disabled={accepting}
                  onClick={onAccept}
                  className="inline-flex min-h-9 items-center gap-tight rounded-button bg-status-success px-snug text-body font-medium text-text-inverse transition-opacity hover:opacity-90 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-status-success/40 disabled:cursor-not-allowed disabled:opacity-50"
                >
                  <Check className="h-4 w-4" aria-hidden />
                  {accepting ? '验收中…' : '总任务验收通过'}
                </button>
              )}
            </div>
          )}
        </div>
        {inReviewPhase && !review && (
          <p className="mt-snug rounded-button border border-border-subtle bg-surface-sunken px-snug py-tight text-caption text-text-secondary" role="status">
            验收状态核验中，请稍候
          </p>
        )}
        {inReviewPhase && review && (!review.can_accept || !review.can_return) && (
          <div className="mt-snug space-y-micro rounded-button border border-status-warning/30 bg-status-warning/5 px-snug py-tight text-caption text-status-warning" role="status">
            {!review.can_accept && review.accept_reason && <p>验收：{review.accept_reason}</p>}
            {!review.can_return && review.return_reason && <p>打回：{review.return_reason}</p>}
          </div>
        )}
      </header>

      <DispatchTimeline taskId={task.id} workspaceId={task.workspace_id} />
    </div>
  );
}
