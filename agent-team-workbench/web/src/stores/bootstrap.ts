import { getBootstrap, getMe, listWorkspaces } from '../api/endpoints';
import { getWorkspace } from '../api/workspaces';
import { ApiError } from '../api/client';
import { WorkspaceEventStream } from '../api/sse';
import type { Bootstrap, Workspace } from '../api/types';
import { useAgentsStore } from './agents.store';
import { useDashboardStore } from './dashboard.store';
import { useLogsStore } from './logs.store';
import { routeEvent } from './events';
import { captureScope, isCurrent, resetWorkspaceScopedStores, type Scope } from './scope';
import { useTasksStore } from './tasks.store';
import { useWorkspaceStore } from './workspace.store';

let stream: WorkspaceEventStream | null = null;

function validWorkspace(value: Workspace | null): value is Workspace {
  return Boolean(value && typeof value.id === 'string' && value.id.length > 0 && typeof value.name === 'string');
}

/** 选中 Workspace 的持久化（localStorage + URL ?ws=；刷新页恢复）。 */
const SELECTED_WORKSPACE_KEY = 'workbench.selected-workspace';
const WORKSPACE_URL_PARAM = 'ws';

/**
 * 首屏引导：workspaces → 恢复上次选中（URL ?ws= > localStorage > 列表第一项，
 * 不可访问时回退第一项并明确提示）→ bootstrap（dashboard/agents/board/health/
 * event_cursor 一次返回）→ 以 event_cursor 开 SSE（协议 §5.2）。
 * 410 cursor_expired 只重拉当前 Workspace（resyncWorkspace），不再整体重跑。
 */
export async function bootstrap(): Promise<void> {
  useWorkspaceStore.getState().setBooting();
  try {
    const { items } = await listWorkspaces();
    if (items.length === 0) throw new Error('当前账号没有可访问的 Workspace');

    useWorkspaceStore.getState().setWorkspaces(items);
    const persisted = readPersistedWorkspaceId();
    const target = items.find((w) => w.id === persisted) ?? items[0];
    if (persisted && target.id !== persisted) {
      useWorkspaceStore.getState().setNotice(`原选择的 Workspace 不可访问，已回退到「${target.name}」`);
    }
    // 先落 selectedWorkspaceId 再 enterWorkspace：scope 捕获必须已指向目标，
    // 否则 hydrate 后的 isCurrent guard 与 startStream 都会因空 id 被拦截。
    // beginSwitch 不清 notice（回退提示要保留到 ready 之后播报）。
    useWorkspaceStore.getState().beginSwitch(target.id);
    persistSelectedWorkspace(target.id);
    await enterWorkspace(target.id);
  } catch (err) {
    useWorkspaceStore.getState().setError(err instanceof Error ? err.message : '加载失败');
  }
}

/**
 * 切换 Workspace：先用目标 scope 预加载完整快照。预加载失败时继续保留
 * 当前 Workspace、SSE 和全部页面内容；只有目标快照成功后才推进 generation
 * 并清理旧 Workspace 投影。
 */
export async function switchWorkspace(workspaceId: string): Promise<void> {
  const ws = useWorkspaceStore.getState();
  if (ws.switching) return;
  if (ws.selectedWorkspaceId === workspaceId && ws.phase === 'ready') return;
  const target = ws.workspaces.find((w) => w.id === workspaceId);
  if (!target) return;

  ws.setSwitching(true);
  ws.setNotice(`正在加载「${target.name}」…`);
  try {
    const { data, authoritativeWorkspace } = await preloadWorkspace(workspaceId);
    const beforeCommit = useWorkspaceStore.getState();
    // A future caller may have changed the target while this request was in
    // flight. Keep the old projection unless this request is still current.
    if (beforeCommit.switching === false || beforeCommit.selectedWorkspaceId !== ws.selectedWorkspaceId) return;
    beforeCommit.beginSwitch(workspaceId);
    stopStream();
    resetWorkspaceScopedStores();
    persistSelectedWorkspace(workspaceId);
    if (validWorkspace(authoritativeWorkspace)) data.workspace = authoritativeWorkspace;
    const scope = captureScope();
    hydrateBootstrap(data);
    useWorkspaceStore.getState().setReady(beforeCommit.me, data.workspace, data.health, data.event_cursor);
    startStream(scope, data.event_cursor);
    useWorkspaceStore.getState().setNotice(`已切换到「${target.name}」`);
  } catch (err) {
    useWorkspaceStore.getState().setSwitching(false);
    useWorkspaceStore.getState().setNotice(`切换到「${target.name}」失败，已保留当前工作区：${err instanceof Error ? err.message : '目标工作区暂时不可用'}`);
  }
}

async function preloadWorkspace(workspaceId: string): Promise<{ data: Bootstrap; authoritativeWorkspace: Workspace | null }> {
  const [data, authoritativeWorkspace] = await Promise.all([
    // Explicit target header is required while the current header still names A.
    getBootstrap(workspaceId, workspaceId),
    getWorkspace(workspaceId, workspaceId).catch((error: unknown) => {
      // Older test/preview servers may not expose GET Workspace yet. A missing
      // route may fall back to bootstrap; real failures must abort the switch.
      if (error instanceof ApiError && error.status === 404) return null;
      throw error;
    }),
  ]);
  return { data, authoritativeWorkspace };
}

/** 进入目标 Workspace：拉 bootstrap；guard 通过才 hydrate + 起流（第 4-7 步）。 */
async function enterWorkspace(workspaceId: string): Promise<void> {
  useWorkspaceStore.getState().setBooting();
  const scope = captureScope();
  try {
    const [me, data, authoritativeWorkspace] = await Promise.all([
      getMe().catch(() => null),
      getBootstrap(workspaceId, workspaceId),
      getWorkspace(workspaceId, workspaceId).catch(() => null),
    ]);
    // 期间已切到别的 Workspace/generation：这份数据作废，不 hydrate。
    if (!isCurrent(scope)) return;
    if (validWorkspace(authoritativeWorkspace)) data.workspace = authoritativeWorkspace;
    hydrateBootstrap(data);
    useWorkspaceStore.getState().setReady(me, data.workspace, data.health, data.event_cursor);
    startStream(scope, data.event_cursor);
  } catch (err) {
    if (!isCurrent(scope)) return;
    useWorkspaceStore.getState().setError(err instanceof Error ? err.message : '加载失败');
  }
}

/**
 * 游标过保留窗口（410 cursor_expired）：只重拉当前 Workspace 的 bootstrap
 * 换新游标再订阅（协议 §6.4）；不许回退「选第一个 Workspace」的旧 bootstrap。
 */
async function resyncWorkspace(scope: Scope): Promise<void> {
  try {
    const [data, authoritativeWorkspace] = await Promise.all([getBootstrap(scope.workspaceId, scope.workspaceId), getWorkspace(scope.workspaceId, scope.workspaceId).catch(() => null)]);
    if (!isCurrent(scope)) return;
    if (validWorkspace(authoritativeWorkspace)) data.workspace = authoritativeWorkspace;
    hydrateBootstrap(data);
    useWorkspaceStore.getState().setReady(useWorkspaceStore.getState().me, data.workspace, data.health, data.event_cursor);
    startStream(scope, data.event_cursor);
  } catch (err) {
    if (!isCurrent(scope)) return;
    useWorkspaceStore.getState().setError(err instanceof Error ? err.message : '加载失败');
  }
}

function hydrateBootstrap(data: Bootstrap): void {
  useDashboardStore.getState().hydrate(data.dashboard);
  useAgentsStore.getState().hydrate(data.agents.items);
  useTasksStore.getState().hydrate(data.work_items.items);
  useLogsStore.setState({ items: data.dashboard.recent_activities, loaded: true });
}

/**
 * 启动全局唯一 SSE。回调全部带 workspace+generation fencing：
 * - 事件先校验 ev.workspace_id 与 scope，再原子推进 eventCursor（max 语义），最后路由；
 * - onStatus/onCursorExpired 同样 fencing，旧流回调不得污染新 Workspace。
 */
function startStream(scope: Scope, cursor: number): void {
  if (!isCurrent(scope)) return;
  stopStream();
  stream = new WorkspaceEventStream(scope.workspaceId, {
    onEvent: (ev) => {
      if (ev.workspace_id !== scope.workspaceId || !isCurrent(scope)) return;
      useWorkspaceStore.getState().advanceEventCursor(ev.stream_seq);
      routeEvent(ev);
    },
    onStatus: (status) => {
      if (isCurrent(scope)) useWorkspaceStore.getState().setSseStatus(status);
    },
    onCursorExpired: () => {
      if (isCurrent(scope)) void resyncWorkspace(scope);
    },
  });
  stream.start(cursor);
}

function stopStream(): void {
  stream?.stop();
  stream = null;
}

function readPersistedWorkspaceId(): string | null {
  if (typeof window === 'undefined') return null;
  const fromUrl = new URLSearchParams(window.location.search).get(WORKSPACE_URL_PARAM);
  if (fromUrl) return fromUrl;
  try {
    return window.localStorage.getItem(SELECTED_WORKSPACE_KEY);
  } catch {
    return null; // 隐私模式等 storage 不可用：回退列表第一项
  }
}

function persistSelectedWorkspace(workspaceId: string): void {
  try {
    window.localStorage.setItem(SELECTED_WORKSPACE_KEY, workspaceId);
  } catch {
    // storage 不可用时仅本次会话内生效
  }
  if (typeof window === 'undefined' || !window.history) return;
  const url = new URL(window.location.href);
  url.searchParams.set(WORKSPACE_URL_PARAM, workspaceId);
  window.history.replaceState(null, '', url);
}
