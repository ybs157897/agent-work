import { useEffect, useRef, useState } from 'react';
import { ApiError } from '../../api/client';
import { getWorkItem, returnWorkItem } from '../../api/endpoints';
import type { WorkItem } from '../../api/types';
import { Modal } from '../../components/modal';
import { captureScope, isCurrent, isCurrentWorkspaceEntity } from '../../stores/scope';
import { useTasksStore } from '../../stores/tasks.store';
import { toast } from '../../stores/toast.store';

export interface TaskDraftCache {
  read(taskId: string): string;
  write(taskId: string, value: string): void;
  clear(taskId: string): void;
}

export function createTaskDraftCache(): TaskDraftCache {
  const drafts = new Map<string, string>();
  return {
    read: (taskId) => drafts.get(taskId) ?? '',
    write: (taskId, value) => drafts.set(taskId, value),
    clear: (taskId) => drafts.delete(taskId),
  };
}

/**
 * 打回重做（commands/return）：review/acceptance 退回 execution。
 * reason 必填（RFC §9.4：生成不可变 review_feedback comment，空理由返回
 * 422 review_feedback_required——前端 trim 空直接禁止提交，对齐契约）。
 * 提交 review_feedback 后由 Coordinator 自动唤醒，进入下一轮调度。
 */
export function ReturnTaskModal({
  task,
  onClose,
  onReturned,
}: {
  task: WorkItem | null;
  onClose: () => void;
  onReturned?: () => void;
}) {
  const upsert = useTasksStore((s) => s.upsert);
  const [draftCache] = useState(createTaskDraftCache);
  const [reason, setReason] = useState('');
  const [touched, setTouched] = useState(false);
  const [submitting, setSubmitting] = useState(false);
  const taskId = task?.id ?? null;
  const activeTaskIdRef = useRef<string | null>(null);
  useEffect(() => {
    activeTaskIdRef.current = taskId;
  }, [taskId]);

  useEffect(() => {
    if (!taskId) return;
    setReason(draftCache.read(taskId));
    setTouched(false);
  }, [draftCache, taskId]);

  const reasonInvalid = touched && reason.trim().length === 0;

  const submit = async () => {
    if (!task || submitting) return;
    setTouched(true);
    if (reason.trim().length === 0) return; // 对齐 review_feedback_required：空理由不提交
    const scope = captureScope();
    if (!isCurrentWorkspaceEntity(scope, task)) {
      toast.error('该任务不属于当前工作区，无法操作。');
      onClose();
      return;
    }
    setSubmitting(true);
    try {
      const updated = await returnWorkItem(task.id, reason.trim(), task.version);
      if (!isCurrentWorkspaceEntity(scope, updated)) return;
      upsert(updated);
      draftCache.clear(task.id);
      toast.success(`已打回重做「${task.title}」`);
      if (activeTaskIdRef.current !== task.id) return;
      setReason('');
      setTouched(false);
      onClose();
      onReturned?.();
    } catch (err) {
      if (!isCurrent(scope)) return;
      if (err instanceof ApiError && (err.isVersionConflict || err.code === 'review_state_conflict')) {
        toast.error('任务已被他人修改，已为你刷新最新数据');
        getWorkItem(task.id)
          .then((latest) => {
            if (isCurrentWorkspaceEntity(scope, latest)) upsert(latest);
          })
          // 只吞刷新失败：面板保持旧投影，SSE work_item.* 会再触发列表刷新。
          .catch(() => undefined);
        if (activeTaskIdRef.current === task.id) onClose();
      } else {
        toast.error(err instanceof ApiError ? err.message : '操作失败');
      }
    } finally {
      if (isCurrent(scope)) setSubmitting(false);
    }
  };

  const inputCls =
    'mt-1 w-full rounded-input border bg-surface-raised px-snug py-tight text-body outline-none focus:ring-2';

  const footer = (
    <div className="flex justify-end gap-snug">
      <button
        type="button"
        onClick={onClose}
        className="rounded-button border border-border-strong bg-transparent px-base py-tight font-medium text-text-secondary transition-colors hover:bg-surface-base"
      >
        取消
      </button>
      <button
        type="button"
        onClick={() => void submit()}
        disabled={submitting}
        className="rounded-button bg-status-warning px-base py-tight font-medium text-text-inverse transition-all hover:opacity-90 active:scale-[0.98] disabled:cursor-not-allowed disabled:opacity-50"
      >
        {submitting ? '提交中…' : '打回重做'}
      </button>
    </div>
  );

  return (
    <Modal
      open={task !== null}
      onClose={onClose}
      title={`打回重做${task ? ` · ${task.title}` : ''}`}
      skin="task"
      footer={footer}
    >
      <div className="space-y-base">
        <p className="text-body text-text-secondary">
          说明需要调整的内容，提交后任务返回执行流程。
        </p>
        <label className="block">
          <span className="text-body text-text-secondary">调整说明（必填）</span>
          <textarea
            value={reason}
            onChange={(e) => {
              const value = e.target.value;
              setReason(value);
              if (taskId) draftCache.write(taskId, value);
            }}
            onBlur={() => setTouched(true)}
            rows={3}
            aria-invalid={reasonInvalid || undefined}
            aria-label="调整说明"
            aria-describedby={reasonInvalid ? 'return-reason-error' : undefined}
            className={`${inputCls} resize-none ${
              reasonInvalid
                ? 'border-status-error/60 focus:border-status-error focus:ring-status-error/20'
                : 'border-border-strong focus:ring-brand-primary/30'
            }`}
            placeholder="请输入需要调整的内容"
          />
          {reasonInvalid && (
            <p id="return-reason-error" role="alert" className="mt-1 text-caption text-status-error">
              调整说明不能为空；它会作为打回记录留存在任务上
            </p>
          )}
        </label>
      </div>
    </Modal>
  );
}
