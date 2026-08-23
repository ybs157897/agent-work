import { useEffect, useState } from 'react';
import { ApiError } from '../../api/client';
import { getWorkItem, returnWorkItem } from '../../api/endpoints';
import type { WorkItem } from '../../api/types';
import { Modal } from '../../components/modal';
import { useTasksStore } from '../../stores/tasks.store';
import { toast } from '../../stores/toast.store';

/**
 * 打回重做（commands/return）：review/acceptance 退回 execution。
 * 控制平面不自动重派——重做由负责人基于理由的下一份 plan 表达（M2 语义）。
 */
export function ReturnTaskModal({ task, onClose }: { task: WorkItem | null; onClose: () => void }) {
  const upsert = useTasksStore((s) => s.upsert);
  const [reason, setReason] = useState('');
  const [submitting, setSubmitting] = useState(false);

  useEffect(() => {
    if (task) setReason('');
  }, [task]);

  const submit = async () => {
    if (!task || submitting) return;
    setSubmitting(true);
    try {
      upsert(await returnWorkItem(task.id, reason.trim() || undefined, task.version));
      toast.success(`已打回重做「${task.title}」`);
      onClose();
    } catch (err) {
      if (err instanceof ApiError && err.isVersionConflict) {
        toast.error('任务已被他人修改，已为你刷新最新数据');
        getWorkItem(task.id)
          .then(upsert)
          // 只吞刷新失败：面板保持旧投影，SSE work_item.* 会再触发列表刷新。
          .catch(() => undefined);
        onClose();
      } else {
        toast.error(err instanceof ApiError ? err.message : '操作失败');
      }
    } finally {
      setSubmitting(false);
    }
  };

  const inputCls =
    'mt-1 w-full rounded-input border border-border-strong bg-surface-raised px-snug py-tight text-body outline-none focus:ring-2 focus:ring-brand-primary/30';

  return (
    <Modal open={task !== null} onClose={onClose} title={`打回重做${task ? ` · ${task.title}` : ''}`}>
      <div className="space-y-base">
        <p className="text-body text-text-secondary">
          打回后任务回到执行阶段，负责人将基于理由重新编排；不会自动重派。
        </p>
        <label className="block">
          <span className="text-body text-text-secondary">打回理由（可选）</span>
          <textarea
            value={reason}
            onChange={(e) => setReason(e.target.value)}
            rows={3}
            className={`${inputCls} resize-none`}
            placeholder="未达验收标准的具体条目…"
          />
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
            className="bg-status-warning text-white rounded-button px-base py-tight font-medium transition-all hover:opacity-90 active:scale-[0.98] disabled:opacity-50 disabled:cursor-not-allowed"
          >
            {submitting ? '提交中…' : '打回重做'}
          </button>
        </div>
      </div>
    </Modal>
  );
}
