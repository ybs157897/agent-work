import { useState } from 'react';
import { ApiError } from '../../api/client';
import { createWorkItem } from '../../api/endpoints';
import type { Priority, WorkItem, WorkItemStatus } from '../../api/types';
import { Drawer } from '../../components/drawer';
import { useAgentsStore } from '../../stores/agents.store';
import { useTasksStore } from '../../stores/tasks.store';
import { toast } from '../../stores/toast.store';
import { sortTasksTree } from '../../utils/task-tree';
import { useWorkspaceStore } from '../../stores/workspace.store';

const STATUS_LABEL: Record<string, string> = {
  todo: '待办',
  in_progress: '进行中',
  completed: '完成',
  blocked: '阻塞',
};

/** 父任务候选：非终态任务（终态任务不再接收子任务），树序展示。 */
const parentCandidates = (items: WorkItem[]) =>
  sortTasksTree(items.filter((t) => t.status !== 'completed' && t.status !== 'cancelled'));

/** 列「+」创建任务：继承列状态（协议 §2.1）；可选父任务挂为子任务。 */
export function CreateTaskModal({
  open,
  initialStatus,
  onClose,
}: {
  open: boolean;
  initialStatus: WorkItemStatus;
  onClose: () => void;
}) {
  const workspace = useWorkspaceStore((s) => s.workspace);
  const agents = useAgentsStore((s) => s.agents);
  const tasks = useTasksStore((s) => s.items);
  const refresh = useTasksStore((s) => s.refresh);

  const [title, setTitle] = useState('');
  const [description, setDescription] = useState('');
  const [priority, setPriority] = useState<Priority>('medium');
  const [dueDate, setDueDate] = useState('');
  const [assignee, setAssignee] = useState('');
  const [parentId, setParentId] = useState('');
  const [submitting, setSubmitting] = useState(false);

  const submit = async () => {
    if (!workspace || !title.trim()) return;
    setSubmitting(true);
    try {
      await createWorkItem(workspace.id, {
        title: title.trim(),
        record_kind: 'task',
        description: description.trim(),
        status: initialStatus,
        priority,
        due_date: dueDate || null,
        agent_profile_id: assignee || undefined,
        parent_id: parentId || undefined,
      });
      await refresh();
      toast.success(`已创建任务「${title.trim()}」`);
      setTitle('');
      setDescription('');
      setPriority('medium');
      setDueDate('');
      setAssignee('');
      setParentId('');
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
    <Drawer open={open} onClose={onClose} title={`创建任务 · 初始状态：${STATUS_LABEL[initialStatus] ?? initialStatus}`} width={480}>
      <div className="p-comfortable">
        <div className="space-y-base">
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
        <div className="grid grid-cols-3 gap-snug">
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
          <label className="block">
            <span className="text-body text-text-secondary">指派人</span>
            <select value={assignee} onChange={(e) => setAssignee(e.target.value)} className={inputCls}>
              <option value="">未指派</option>
              {agents.map((a) => (
                <option key={a.id} value={a.id}>
                  {a.name}
                </option>
              ))}
            </select>
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
            {submitting ? '创建中…' : '创建'}
          </button>
        </div>
      </div>
      </div>
    </Drawer>
  );
}
