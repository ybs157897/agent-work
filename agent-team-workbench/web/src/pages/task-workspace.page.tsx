import { useEffect, useState } from 'react';
import { useNavigate, useParams } from 'react-router-dom';
import { ApiError } from '../api/client';
import { acceptWorkItem, getWorkItem, unblockWorkItem } from '../api/endpoints';
import type { WorkItem, WorkItemStatus } from '../api/types';
import { ErrorState } from '../components/async-state';
import { BlockTaskModal } from './tasks/block-modal';
import { TaskDetail } from './tasks/task-detail';
import { useTasksStore } from '../stores/tasks.store';
import { toast } from '../stores/toast.store';

/**
 * Task 的独立全页入口。Task 不再把详情当作 Chat 抽屉，也不在 URL 中创建
 * Chat conversation；子任务导航保持在 /tasks/:id 内。
 */
export default function TaskWorkspacePage() {
  const { taskId } = useParams<{ taskId: string }>();
  const navigate = useNavigate();
  const upsert = useTasksStore((state) => state.upsert);
  const task = useTasksStore((state) => (taskId ? state.items.find((item) => item.id === taskId) : undefined));
  const [blocking, setBlocking] = useState<WorkItem | null>(null);
  const [loadError, setLoadError] = useState<string | undefined>();

  useEffect(() => {
    if (!taskId) return;
    let active = true;
    getWorkItem(taskId)
      .then((item) => {
        if (!active) return;
        if (item.record_kind !== 'task') {
          setLoadError('该记录属于 Chat，不能在 Task 页面打开。');
          return;
        }
        setLoadError(undefined);
        upsert(item);
      })
      .catch((error: unknown) => {
        if (!active) return;
        setLoadError(error instanceof ApiError ? error.message : '任务加载失败');
      });
    return () => {
      active = false;
    };
  }, [taskId, upsert]);

  const transitionTask = async (item: WorkItem, to: WorkItemStatus) => {
    if (item.record_kind !== 'task') return;
    try {
      if (to === 'blocked') {
        if (item.status !== 'in_progress') {
          toast.error('只有进行中的任务才能标记阻塞');
          return;
        }
        setBlocking(item);
        return;
      }
      if (item.status === 'blocked' && to === 'in_progress') {
        upsert(await unblockWorkItem(item.id, item.version));
        toast.success(`已解除阻塞「${item.title}」`);
        return;
      }
      if (to === 'completed' && (item.phase === 'review' || item.phase === 'acceptance')) {
        upsert(await acceptWorkItem(item.id, item.version));
        toast.success(`任务「${item.title}」验收通过`);
        return;
      }
      toast.error('该状态由 Coordinator 管理，当前操作不可用');
    } catch (error: unknown) {
      if (error instanceof ApiError && error.isVersionConflict) {
        toast.error('任务状态已更新，请刷新后重试');
        const latest = await getWorkItem(item.id).catch(() => undefined);
        if (latest?.record_kind === 'task') upsert(latest);
      } else {
        toast.error(error instanceof ApiError ? error.message : '操作失败，请重试');
      }
    }
  };

  if (loadError) {
    return (
      <main className="page-shell flex min-h-full items-center justify-center">
        <ErrorState message={loadError} actionLabel="返回任务看板" onRetry={() => navigate('/tasks')} />
      </main>
    );
  }

  return (
    <>
      <TaskDetail
        taskId={taskId ?? null}
        fullPage
        onClose={() => navigate('/tasks')}
        onTransition={transitionTask}
      />
      <BlockTaskModal task={blocking} onClose={() => setBlocking(null)} />
      {!task && !loadError && (
        <div className="page-shell" role="status" aria-label="任务加载中">
          <p className="text-body text-text-tertiary">任务加载中…</p>
        </div>
      )}
    </>
  );
}
