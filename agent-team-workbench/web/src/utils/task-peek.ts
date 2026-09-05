import type { Location } from 'react-router-dom';
import type { WorkItem } from '../api/types';

export interface TaskPeekRouteState {
  backgroundLocation: Location;
}

/** 只接受由 /tasks 看板发起的背景路由，避免伪造 state 改变其他页面匹配。 */
export function taskPeekBackground(state: unknown): Location | undefined {
  if (!state || typeof state !== 'object') return undefined;
  const background = (state as Partial<TaskPeekRouteState>).backgroundLocation;
  if (!background || (background.pathname !== '/tasks' && background.pathname !== '/tasks/')) return undefined;
  // Keep the exact Location object stable: TaskWorkspacePage uses this value
  // in an effect dependency. Consumers that render a canonical pathname can
  // normalize the spelling at read time without cloning the route state.
  return background;
}

/** 打开详情时保留看板的 view/workspace 等查询参数。 */
export function taskPeekTarget(taskId: string, search: string): { pathname: string; search: string } {
  return { pathname: `/tasks/${encodeURIComponent(taskId)}`, search };
}

/** 直接详情返回看板时同样保留 view/workspace 查询参数。 */
export function taskBoardTarget(search: string): { pathname: string; search: string } {
  return { pathname: '/tasks', search };
}

/** Coordinator root 优先；历史任务才沿 parent_id 逐级回溯，最多 100 层。 */
export async function resolveTaskRootId(
  item: WorkItem,
  coordinatorRootFor: (workItemId: string) => Promise<string | undefined>,
  getItem: (workItemId: string) => Promise<WorkItem>,
): Promise<string> {
  if (!item.parent_id) return item.id;
  const coordinatedRoot = await coordinatorRootFor(item.id);
  if (coordinatedRoot) return coordinatedRoot;

  const seen = new Set([item.id]);
  let current = item;
  for (let depth = 0; depth < 100 && current.parent_id; depth += 1) {
    if (seen.has(current.parent_id)) throw new Error('任务父链存在循环');
    seen.add(current.parent_id);
    const parent = await getItem(current.parent_id);
    if (parent.record_kind !== 'task' || parent.workspace_id !== item.workspace_id) {
      throw new Error('任务父链越出当前工作区');
    }
    current = parent;
  }
  if (current.parent_id) throw new Error('任务父链超过 100 层');
  return current.id;
}
