import { getBootstrap, getMe, listWorkspaces } from '../api/endpoints';
import { WorkspaceEventStream } from '../api/sse';
import { useAgentsStore } from './agents.store';
import { useDashboardStore } from './dashboard.store';
import { useLogsStore } from './logs.store';
import { routeEvent } from './events';
import { useTasksStore } from './tasks.store';
import { useWorkspaceStore } from './workspace.store';

let stream: WorkspaceEventStream | null = null;

/**
 * 首屏引导：workspaces → bootstrap（dashboard/agents/board/health/event_cursor 一次返回）
 * → 以 event_cursor 开 SSE（协议 §5.2）。410 cursor_expired 时整体重跑。
 */
export async function bootstrap(): Promise<void> {
  const ws = useWorkspaceStore.getState();
  ws.setBooting();
  try {
    const me = await getMe().catch(() => null);
    const { items } = await listWorkspaces();
    const first = items[0];
    if (!first) throw new Error('当前账号没有可访问的 Workspace');

    const data = await getBootstrap(first.id);
    useDashboardStore.getState().hydrate(data.dashboard);
    useAgentsStore.getState().hydrate(data.agents.items);
    useTasksStore.getState().hydrate(data.work_items.items);
    useLogsStore.setState({ items: data.dashboard.recent_activities, loaded: true });

    ws.setReady(me, data.workspace, data.health, data.event_cursor);
    startStream(data.workspace.id, data.event_cursor);
  } catch (err) {
    ws.setError(err instanceof Error ? err.message : '加载失败');
  }
}

function startStream(workspaceId: string, cursor: number): void {
  stream?.stop();
  stream = new WorkspaceEventStream(workspaceId, {
    onEvent: routeEvent,
    onStatus: (s) => useWorkspaceStore.getState().setSseStatus(s),
    onCursorExpired: () => {
      // 游标过保留窗口：重新 bootstrap 获取新游标再订阅（协议 §6.4）。
      void bootstrap();
    },
  });
  stream.start(cursor);
}
