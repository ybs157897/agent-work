import { useEffect, useState } from 'react';
import { ApiError } from '../../api/client';
import { blockWorkItem } from '../../api/endpoints';
import type { WorkItem } from '../../api/types';
import { Modal } from '../../components/modal';
import { useTasksStore } from '../../stores/tasks.store';
import { toast } from '../../stores/toast.store';

/** 结构化 Blocker（协议 §4.1：code/message/source）。 */
export function BlockTaskModal({ task, onClose }: { task: WorkItem | null; onClose: () => void }) {
  const upsert = useTasksStore((s) => s.upsert);
  const [code, setCode] = useState('permission');
  const [message, setMessage] = useState('');
  const [submitting, setSubmitting] = useState(false);

  useEffect(() => {
    if (task) {
      setCode('permission');
      setMessage('');
    }
  }, [task]);

  const submit = async () => {
    if (!task || !message.trim()) return;
    setSubmitting(true);
    try {
      upsert(
        await blockWorkItem(
          task.id,
          { code, message: message.trim(), source: 'user' },
          task.version,
        ),
      );
      toast.success(`已标记阻塞「${task.title}」`);
      onClose();
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : '操作失败');
    } finally {
      setSubmitting(false);
    }
  };

  const inputCls =
    'mt-1 w-full rounded-input border border-border-strong bg-surface-raised px-snug py-tight text-body outline-none focus:ring-2 focus:ring-brand-primary/30';

  return (
    <Modal open={task !== null} onClose={onClose} title={`标记阻塞${task ? ` · ${task.title}` : ''}`}>
      <div className="space-y-base">
        <label className="block">
          <span className="text-body text-text-secondary">阻塞类型</span>
          <select value={code} onChange={(e) => setCode(e.target.value)} className={inputCls}>
            <option value="permission">缺少权限</option>
            <option value="resource">资源不足</option>
            <option value="execution">执行失败</option>
            <option value="other">其他</option>
          </select>
        </label>
        <label className="block">
          <span className="text-body text-text-secondary">说明</span>
          <textarea
            value={message}
            onChange={(e) => setMessage(e.target.value)}
            rows={3}
            className={`${inputCls} resize-none`}
            placeholder="阻塞原因与需要的支持…"
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
            onClick={submit}
            disabled={!message.trim() || submitting}
            className="bg-status-error text-white rounded-button px-base py-tight font-medium transition-all duration-150 active:scale-[0.98] disabled:opacity-50 disabled:cursor-not-allowed"
          >
            {submitting ? '提交中…' : '标记阻塞'}
          </button>
        </div>
      </div>
    </Modal>
  );
}
