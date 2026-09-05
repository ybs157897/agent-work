import { useCallback, useEffect, useRef, useState } from 'react';
import { useLocation, useNavigate, useParams } from 'react-router-dom';
import { ApiError } from '../api/client';
import { getCoordinatorSnapshot, getWorkItem } from '../api/endpoints';
import { TaskDetail } from './tasks/task-detail';
import { useTasksStore } from '../stores/tasks.store';
import { captureScope, isCurrent, isCurrentWorkspaceEntity } from '../stores/scope';
import { resolveTaskRootId, taskBoardTarget, taskPeekBackground, taskPeekTarget } from '../utils/task-peek';

/**
 * `/tasks/:id` 同时承载看板 side-peek 和直接深链 fallback。
 * 子任务链接在渲染前解析到权威根任务，永不暴露子任务验收面。
 */
export default function TaskWorkspacePage() {
  const { taskId } = useParams<{ taskId: string }>();
  const location = useLocation();
  const navigate = useNavigate();
  const upsert = useTasksStore((state) => state.upsert);
  const backgroundLocation = taskPeekBackground(location.state);
  const [loadError, setLoadError] = useState<string>();
  const [peekOpen, setPeekOpen] = useState(true);
  const closingRef = useRef(false);

  const finishClose = useCallback(() => {
    if (backgroundLocation) navigate(-1);
    else navigate(taskBoardTarget(location.search), { replace: true });
  }, [backgroundLocation, location.search, navigate]);

  const close = useCallback(() => {
    if (backgroundLocation) {
      closingRef.current = true;
      setPeekOpen(false);
    } else finishClose();
  }, [backgroundLocation, finishClose]);

  useEffect(() => {
    closingRef.current = false;
    setPeekOpen(true);
  }, [taskId]);

  useEffect(() => {
    if (!taskId) return;
    let active = true;
    const scope = captureScope();
    const coordinatorRootFor = async (workItemId: string): Promise<string | undefined> => {
      try {
        return (await getCoordinatorSnapshot(workItemId)).root_work_item_id;
      } catch (error) {
        if (error instanceof ApiError && error.status === 404) return undefined;
        throw error;
      }
    };

    getWorkItem(taskId)
      .then(async (item) => {
        if (!active || !isCurrent(scope)) return;
        if (!isCurrentWorkspaceEntity(scope, item)) throw new Error('该任务不属于当前工作区');
        if (item.record_kind !== 'task') throw new Error('该记录属于 Chat，不能在 Task 页面打开');
        const rootTaskId = await resolveTaskRootId(item, coordinatorRootFor, getWorkItem);
        if (!active || !isCurrent(scope) || closingRef.current) return;
        if (rootTaskId !== item.id) {
          navigate(taskPeekTarget(rootTaskId, location.search), {
            replace: true,
            ...(backgroundLocation ? { state: { backgroundLocation } } : {}),
          });
          return;
        }
        setLoadError(undefined);
        upsert(item);
      })
      .catch((error: unknown) => {
        if (!active || !isCurrent(scope)) return;
        setLoadError(error instanceof ApiError ? error.message : error instanceof Error ? error.message : '任务加载失败');
      });
    return () => {
      active = false;
    };
  }, [backgroundLocation, location.search, navigate, taskId, upsert]);

  return (
    <TaskDetail
      taskId={taskId ?? null}
      error={loadError}
      open={peekOpen}
      fullPage={!backgroundLocation}
      onClose={close}
      onExitComplete={backgroundLocation ? finishClose : undefined}
    />
  );
}
