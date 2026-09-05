import { useRef, useState } from 'react';
import { ApiError } from '../../api/client';
import { createWorkItem } from '../../api/endpoints';
import type { Priority } from '../../api/types';
import { Drawer } from '../../components/drawer';
import { Button, Input, Select, Textarea } from '../../components/ui';
import { useTasksStore } from '../../stores/tasks.store';
import { toast } from '../../stores/toast.store';
import { useWorkspaceStore } from '../../stores/workspace.store';
import { captureScope, isCurrent } from '../../stores/scope';

export function parseAcceptanceCriteria(value: string): string[] {
  return value.split('\n').map((criterion) => criterion.trim()).filter(Boolean);
}

/** 用户发布入口只创建总任务；拆分由任务执行流程负责。 */
export function CreateTaskModal({
  open,
  onClose,
}: {
  open: boolean;
  onClose: () => void;
}) {
  const workspace = useWorkspaceStore((s) => s.workspace);
  const refresh = useTasksStore((s) => s.refresh);

  const [title, setTitle] = useState('');
  const [description, setDescription] = useState('');
  const [acceptanceCriteria, setAcceptanceCriteria] = useState('');
  const [priority, setPriority] = useState<Priority>('medium');
  const [dueDate, setDueDate] = useState('');
  const [acceptanceTouched, setAcceptanceTouched] = useState(false);
  const [submitting, setSubmitting] = useState(false);
  const clientKey = useRef<string>();
  const acceptanceItems = parseAcceptanceCriteria(acceptanceCriteria);
  const acceptanceMissing = acceptanceItems.length === 0;
  const acceptanceInvalid = acceptanceTouched && acceptanceMissing;

  const submit = async () => {
    if (!workspace || submitting) return;
    setAcceptanceTouched(true);
    if (!title.trim() || acceptanceMissing) return;
    const scope = captureScope();
    if (scope.workspaceId !== workspace.id) return;
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
        acceptance_criteria: acceptanceItems,
        client_key: clientKey.current,
      });
      if (!isCurrent(scope)) return;
      await refresh();
      if (!isCurrent(scope)) return;
      toast.success(`已发布任务「${title.trim()}」，已进入执行队列`);
      setTitle('');
      setDescription('');
      setAcceptanceCriteria('');
      setPriority('medium');
      setDueDate('');
      setAcceptanceTouched(false);

      clientKey.current = undefined;
      onClose();
    } catch (err) {
      if (!isCurrent(scope)) return;
      toast.error(err instanceof ApiError ? err.message : '创建失败');
    } finally {
      if (isCurrent(scope)) setSubmitting(false);
    }
  };

  return (
    <Drawer open={open} onClose={onClose} title="发布任务" width={480} skin="task">
      <div className="p-comfortable">
        <form className="space-y-base" onSubmit={(event) => { event.preventDefault(); void submit(); }}>
          <p className="text-body text-text-secondary">描述目标与验收标准，系统会安排 Agent 执行。完成后由你统一验收。</p>
          <label className="block">
            <span className="text-body text-text-secondary">标题 <span className="text-caption">（必填）</span></span>
            <Input required value={title} onChange={(e) => setTitle(e.target.value)} placeholder="这次要完成什么？" />
          </label>
          <label className="block">
            <span className="text-body text-text-secondary">描述 <span className="text-caption">（可选）</span></span>
            <Textarea
              value={description}
              onChange={(e) => setDescription(e.target.value)}
              rows={3}
              className="resize-y"
              placeholder="补充背景、范围和需要注意的事项"
            />
          </label>
          <label className="block">
            <span className="text-body text-text-secondary">
              验收标准 <span className="text-caption text-text-tertiary">（必填，每行一条）</span>
            </span>
            <Textarea
              value={acceptanceCriteria}
              onChange={(e) => setAcceptanceCriteria(e.target.value)}
              rows={3}
              onBlur={() => setAcceptanceTouched(true)}
              className="resize-y"
              placeholder={'例如：登录流程有自动化测试\n例如：失败时能看到恢复原因'}
              aria-required="true"
              invalid={acceptanceInvalid}
              aria-describedby={acceptanceInvalid ? 'root-acceptance-error' : 'root-acceptance-hint'}
            />
            {acceptanceInvalid && (
              <p id="root-acceptance-error" className="mt-1 text-caption text-status-error" role="alert">
                请至少填写一条验收标准。
              </p>
            )}
            {!acceptanceInvalid && (
              <p id="root-acceptance-hint" className="mt-micro text-caption text-text-tertiary">写清满足什么条件才算完成，方便最后验收。</p>
            )}
          </label>
          <div className="grid grid-cols-2 gap-snug">
            <label className="block">
              <span className="text-body text-text-secondary">优先级</span>
              <Select value={priority} onChange={(e) => setPriority(e.target.value as Priority)}>
                <option value="low">低优</option>
                <option value="medium">中优</option>
                <option value="high">高优</option>
                <option value="urgent">紧急</option>
              </Select>
            </label>
            <label className="block">
              <span className="text-body text-text-secondary">截止日</span>
              <Input type="date" value={dueDate} onChange={(e) => setDueDate(e.target.value)} />
            </label>
          </div>
          <div className="flex justify-end gap-snug pt-tight">
            <Button
              type="button"
              onClick={onClose}
            >
              取消
            </Button>
            <Button
              type="submit"
              variant="primary"
              disabled={!title.trim() || acceptanceMissing || submitting}
            >
              {submitting ? '发布中…' : '发布任务'}
            </Button>
          </div>
        </form>
      </div>
    </Drawer>
  );
}
