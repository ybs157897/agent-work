import { useEffect, useState } from 'react';
import { ApiError } from '../../api/client';
import { getWorkItem, returnWorkItem } from '../../api/endpoints';
import type { WorkItem } from '../../api/types';
import { Modal } from '../../components/modal';
import { captureScope, isCurrent, isCurrentWorkspaceEntity } from '../../stores/scope';
import { useTasksStore } from '../../stores/tasks.store';
import { toast } from '../../stores/toast.store';

/**
 * 打回重做（commands/return）：review/acceptance 退回 execution。
 * reason 必填（RFC §9.4：生成不可变 review_feedback comment，空理由返回
 * 422 review_feedback_required——前端 trim 空直接禁止提交，对齐契约）。
 * 控制平面不自动重派——重做由负责人基于理由的下一份 plan 表达（M2 语义）。
 */
export function ReturnTaskModal({ task, onClose }: { task: WorkItem | null; onClose: () => void }) {
  const upsert = useTasksStore((s) => s.upsert);
  const [reason, setReason] = useState('');
  const [touched, setTouched] = useState(false);
  const [submitting, setSubmitting] = useState(false);

  useEffect(() => {
    if (task) {
      setReason('');
      setTouched(false);
    }
  }, [task]);

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
      toast.success(`已打回重做「${task.title}」`);
      onClose();
    } catch (err) {
      if (!isCurrent(scope)) return;
      if (err instanceof ApiError && err.isVersionConflict) {
        toast.error('任务已被他人修改，已为你刷新最新数据');
        getWorkItem(task.id)
          .then((latest) => {
            if (isCurrentWorkspaceEntity(scope, latest)) upsert(latest);
          })
          // 只吞刷新失败：面板保持旧投影，SSE work_item.* 会再触发列表刷新。
          .catch(() => undefined);
        onClose();
      } else {
        toast.error(err instanceof ApiError ? err.message : '操作失败');
      }
    } finally {
      if (isCurrent(scope)) setSubmitting(false);
    }
  };

  const inputCls =
    'mt-1 w-full rounded-input border bg-surface-raised px-snug py-tight text-body outline-none focus:ring-2';

  return (
    <Modal open={task !== null} onClose={onClose} title={`打回重做${task ? ` · ${task.title}` : ''}`}>
      <div className="space-y-base">
        <p className="text-body text-text-secondary">
          打回后任务回到执行阶段，负责人将基于理由重新编排；不会自动重派。
        </p>
        <label className="block">
          <span className="text-body text-text-secondary">打回理由（必填）</span>
          <textarea
            value={reason}
            onChange={(e) => setReason(e.target.value)}
            onBlur={() => setTouched(true)}
            rows={3}
            aria-invalid={reasonInvalid || undefined}
            aria-label="打回理由"
            aria-describedby={reasonInvalid ? 'return-reason-error' : undefined}
            className={`${inputCls} resize-none ${
              reasonInvalid
                ? 'border-status-error/60 focus:border-status-error focus:ring-status-error/20'
                : 'border-border-strong focus:ring-brand-primary/30'
            }`}
            placeholder="未达验收标准的具体条目…"
          />
          {reasonInvalid && (
            <p id="return-reason-error" role="alert" className="mt-1 text-caption text-status-error">
              打回理由不能为空；它会作为打回记录留存在任务上
            </p>
          )}
        </label>
        <div className="flex justify-end gap-snug pt-tight">
          <button
            onClick={onClose}
            className="bg-transparent border border-border-strong text-text-secondary rounded-button px-base py-tight font-medium hover:bg-surface-base transition-colors"
          >
            取消
          </button>
          <button
            onClick={() => void submit()}
            disabled={submitting}
            className="bg-status-warning text-text-inverse rounded-button px-base py-tight font-medium transition-all hover:opacity-90 active:scale-[0.98] disabled:opacity-50 disabled:cursor-not-allowed"
          >
            {submitting ? '提交中…' : '打回重做'}
          </button>
        </div>
      </div>
    </Modal>
  );
}
