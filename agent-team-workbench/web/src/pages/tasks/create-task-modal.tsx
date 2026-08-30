import { useRef, useState } from 'react';
import { ApiError } from '../../api/client';
import { createWorkItem } from '../../api/endpoints';
import type { Priority, WorkItem } from '../../api/types';
import { Drawer } from '../../components/drawer';
import { useTasksStore } from '../../stores/tasks.store';
import { toast } from '../../stores/toast.store';
import { sortTasksTree } from '../../utils/task-tree';
import { useWorkspaceStore } from '../../stores/workspace.store';

/** 父任务候选：非终态任务（终态任务不再接收子任务），树序展示。 */
const parentCandidates = (items: WorkItem[]) =>
  sortTasksTree(items.filter((t) => t.status !== 'completed' && t.status !== 'cancelled'));

/** 发布 Task：根任务统一进入 Coordinator 队列；可选父任务挂为子任务。 */
export function CreateTaskModal({
  open,
  onClose,
}: {
  open: boolean;
  onClose: () => void;
}) {
  const workspace = useWorkspaceStore((s) => s.workspace);
  const tasks = useTasksStore((s) => s.items);
  const refresh = useTasksStore((s) => s.refresh);

  const [title, setTitle] = useState('');
  const [description, setDescription] = useState('');
  const [acceptanceCriteria, setAcceptanceCriteria] = useState('');
  const [priority, setPriority] = useState<Priority>('medium');
  const [dueDate, setDueDate] = useState('');
  const [parentId, setParentId] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const clientKey = useRef<string>();

  const submit = async () => {
    if (!workspace || !title.trim()) return;
    setSubmitting(true);
    try {
      clientKey.current ??= `task:${crypto.randomUUID()}`;
      await createWorkItem(workspace.id, {
        title: title.trim(),
        record_kind: 'task',
        description: description.trim(),
        // Coordinator-managed root Tasks always enter through the auto-accept queue.
        // The column that opened this form must not manufacture completed/blocked Tasks.
        status: 'todo',
        priority,
        due_date: dueDate || null,
        parent_id: parentId || undefined,
        acceptance_criteria: acceptanceCriteria
          .split('\n')
          .map((criterion) => criterion.trim())
          .filter(Boolean),
        client_key: clientKey.current,
      });
      await refresh();
      toast.success(
        parentId
          ? `已创建子任务「${title.trim()}」，将通知根 Coordinator 重新规划`
          : `已发布任务「${title.trim()}」，Coordinator 已自动接取`,
      );
      setTitle('');
      setDescription('');
      setAcceptanceCriteria('');
      setPriority('medium');
      setDueDate('');
      setParentId('');
      clientKey.current = undefined;
      onClose();
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : '创建失败');
    } finally {
      setSubmitting(false);
    }
  };

  const inputCls =
    'mt-1 w-full rounded-input border border-border-strong bg-surface-raised px-snug py-tight text-body outline-none focus:ring-2 focus:ring-brand-primary/30';

  return (
    <Drawer open={open} onClose={onClose} title="发布任务" width={480}>
      <div className="p-comfortable">
        <div className="space-y-base">
        <div className="rounded-card border border-brand-primary/20 bg-brand-primary/5 px-snug py-tight" role="status">
          <p className="text-body font-medium text-text-primary">发布后自动接取</p>
          <p className="mt-0.5 text-caption text-text-secondary">
            {parentId ? '这是子任务，根任务 Coordinator 会收到变更并重新规划。' : '系统 Coordinator 会自动拆分任务、选择 Agent 并开始执行。'}
          </p>
        </div>
        <label className="block">
          <span className="text-body text-text-secondary">标题</span>
          <input value={title} onChange={(e) => setTitle(e.target.value)} className={inputCls} placeholder="任务标题" />
        </label>
        <label className="block">
          <span className="text-body text-text-secondary">描述</span>
          <textarea
            value={description}
            onChange={(e) => setDescription(e.target.value)}
            rows={3}
            className={`${inputCls} resize-none`}
            placeholder="背景、目标、验收标准…"
          />
        </label>
        <label className="block">
          <span className="text-body text-text-secondary">验收标准 <span className="text-caption text-text-tertiary">（可选，每行一条）</span></span>
          <textarea
            value={acceptanceCriteria}
            onChange={(e) => setAcceptanceCriteria(e.target.value)}
            rows={3}
            className={`${inputCls} resize-none`}
            placeholder="例如：登录流程有自动化测试\n例如：失败时能看到恢复原因"
          />
        </label>
        <div className="grid grid-cols-2 gap-snug">
          <label className="block">
            <span className="text-body text-text-secondary">优先级</span>
            <select value={priority} onChange={(e) => setPriority(e.target.value as Priority)} className={inputCls}>
              <option value="low">低优</option>
              <option value="medium">中优</option>
              <option value="high">高优</option>
              <option value="urgent">紧急</option>
            </select>
          </label>
          <label className="block">
            <span className="text-body text-text-secondary">截止日</span>
            <input type="date" value={dueDate} onChange={(e) => setDueDate(e.target.value)} className={inputCls} />
          </label>
        </div>
        <label className="block">
          <span className="text-body text-text-secondary">父任务（可选）</span>
          <select value={parentId} onChange={(e) => setParentId(e.target.value)} className={inputCls}>
            <option value="">无 · 作为根任务</option>
            {parentCandidates(tasks).map((entry) => (
              <option key={entry.item.id} value={entry.item.id}>
                {`${'　'.repeat(entry.depth)}${entry.item.title}`}
              </option>
            ))}
          </select>
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
            disabled={!title.trim() || submitting}
            className="bg-brand-primary text-text-inverse rounded-button px-base py-tight font-medium transition-all duration-150 hover:bg-brand-accent active:scale-[0.98] disabled:opacity-50 disabled:cursor-not-allowed"
          >
            {submitting ? '发布中…' : '发布任务'}
          </button>
        </div>
      </div>
      </div>
    </Drawer>
  );
}
