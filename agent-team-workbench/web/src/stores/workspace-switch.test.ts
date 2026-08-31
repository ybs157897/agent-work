import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { Bootstrap, CanonicalEvent, Workspace } from '../api/types';
// import bootstrap 模块即完成全部 workspace-scoped store 的 reset 注册。
import { bootstrap, switchWorkspace } from './bootstrap';
import { useRunsStore } from './runs.store';
import { useTasksStore } from './tasks.store';
import { useWorkspaceStore } from './workspace.store';

/** 模拟 EventSource：记录实例与关闭状态，手动触发 open/message/error（同 sse.test.ts）。 */
class FakeEventSource {
  static CONNECTING = 0;
  static OPEN = 1;
  static CLOSED = 2;

  static instances: FakeEventSource[] = [];

  readonly url: string;
  readyState = FakeEventSource.CONNECTING;
  onopen: (() => void) | null = null;
  onerror: (() => void) | null = null;
  private listeners = new Map<string, ((e: MessageEvent) => void)[]>();
  closed = false;

  constructor(url: string) {
    this.url = url;
    FakeEventSource.instances.push(this);
  }

  addEventListener(type: string, cb: (e: MessageEvent) => void) {
    const list = this.listeners.get(type) ?? [];
    list.push(cb);
    this.listeners.set(type, list);
  }

  close() {
    this.closed = true;
    this.readyState = FakeEventSource.CLOSED;
  }

  emitOpen() {
    this.readyState = FakeEventSource.OPEN;
    this.onopen?.();
  }

  emitEvent(type: string, data: unknown) {
    for (const cb of this.listeners.get(type) ?? []) {
      cb({ data: JSON.stringify(data), lastEventId: '' } as MessageEvent);
    }
  }

  emitErrorClosed() {
    this.readyState = FakeEventSource.CLOSED;
    this.onerror?.();
  }
}

const workspaceOf = (id: string, name: string): Workspace => ({
  id,
  name,
  timezone: 'UTC',
  version: 1,
});

const bootstrapPayload = (workspace: Workspace, cursor: number): Bootstrap => ({
  workspace,
  dashboard: {
    active_agents: 0,
    running_tasks: 0,
    completed_today: 0,
    board_counts: { todo: 0, in_progress: 0, blocked: 0, completed: 0 },
    recent_activities: [],
  },
  agents: { items: [] },
  work_items: { items: [] },
  health: { control_plane: 'ok', runners: [] },
  event_cursor: cursor,
});

const eventOn = (workspaceId: string, seq: number): CanonicalEvent => ({
  contract_version: 'events/v1',
  event_id: `evt_${workspaceId}_${seq}`,
  workspace_id: workspaceId,
  stream_seq: seq,
  aggregate: { type: 'work_item', id: 'wi_1', version: 1 },
  type: 'work_item.updated',
  occurred_at: '2026-08-30T00:00:00Z',
  data: { record_kind: 'task' },
});

const json = (body: unknown) =>
  new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } });

interface DeferredResponse {
  promise: Promise<Response>;
  resolve: (resp: Response) => void;
}

function deferred(): DeferredResponse {
  let resolve!: DeferredResponse['resolve'];
  const promise = new Promise<Response>((res) => {
    resolve = res;
  });
  return { promise, resolve };
}

/**
 * 按 URL 子串路由的 fetch mock；未命中路由返回 404。
 * 注意：路由按对象插入顺序匹配，具体片段（如 /ws_A/bootstrap）必须排在
 * 宽泛片段（如 /workspaces）之前，否则 bootstrap URL 会被宽泛路由吞掉。
 */
function stubFetchRouter(routes: Record<string, () => Response | Promise<Response>>): ReturnType<typeof vi.fn> {
  const fetchMock = vi.fn((input: RequestInfo | URL) => {
    const url = String(input);
    for (const [fragment, handler] of Object.entries(routes)) {
      if (url.includes(fragment)) return Promise.resolve().then(handler);
    }
    return Promise.resolve(new Response('{}', { status: 404 }));
  });
  vi.stubGlobal('fetch', fetchMock);
  return fetchMock;
}

const WS_A = workspaceOf('ws_A', '工作区甲');
const WS_B = workspaceOf('ws_B', '工作区乙');

async function prepareWindow(opts: { persisted?: string; urlSearch?: string } = {}) {
  const store = new Map<string, string>(opts.persisted ? [['workbench.selected-workspace', opts.persisted]] : []);
  vi.stubGlobal('window', {
    location: { search: opts.urlSearch ?? '', href: `http://localhost/${opts.urlSearch ?? ''}` },
    localStorage: {
      getItem: (key: string) => store.get(key) ?? null,
      setItem: (key: string, value: string) => void store.set(key, value),
    },
    history: { replaceState: vi.fn() },
  });
}

function resetWorkspaceStore() {
  useWorkspaceStore.setState({
    phase: 'booting',
    error: null,
    notice: null,
    me: null,
    workspace: null,
    workspaces: [],
    selectedWorkspaceId: null,
    generation: 0,
    switching: false,
    health: null,
    eventCursor: 0,
    sseStatus: 'connecting',
  });
  useTasksStore.setState({ items: [], loaded: false, filter: {}, selectedTaskId: null });
  useRunsStore.getState().reset();
}

beforeEach(() => {
  FakeEventSource.instances = [];
  vi.stubGlobal('EventSource', FakeEventSource);
  resetWorkspaceStore();
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

describe('bootstrap 选中恢复（RFC §12.1）', () => {
  it('localStorage 指向已删除 Workspace 时回退列表第一项并明确提示', async () => {
    await prepareWindow({ persisted: 'ws_gone' });
    stubFetchRouter({
      '/me': () => json({ user_id: 'u1', name: 'o', role: 'owner', feature_flags: {} }),
      '/workspaces/ws_A/bootstrap': () => json(bootstrapPayload(WS_A, 10)),
      '/workspaces/ws_B/bootstrap': () => json(bootstrapPayload(WS_B, 20)),
      '/workspaces': () => json({ items: [WS_A, WS_B] }),
    });

    await bootstrap();

    const ws = useWorkspaceStore.getState();
    expect(ws.selectedWorkspaceId).toBe('ws_A');
    expect(ws.phase).toBe('ready');
    expect(ws.notice).toContain('不可访问');
    expect(ws.notice).toContain('工作区甲');
    expect(FakeEventSource.instances).toHaveLength(1);
    expect(FakeEventSource.instances[0].url).toContain('/workspaces/ws_A/events?cursor=10');
  });

  it('localStorage 有效时恢复上次选中', async () => {
    await prepareWindow({ persisted: 'ws_B' });
    stubFetchRouter({
      '/me': () => json({ user_id: 'u1', name: 'o', role: 'owner', feature_flags: {} }),
      '/workspaces/ws_A/bootstrap': () => json(bootstrapPayload(WS_A, 10)),
      '/workspaces/ws_B/bootstrap': () => json(bootstrapPayload(WS_B, 20)),
      '/workspaces': () => json({ items: [WS_A, WS_B] }),
    });

    await bootstrap();

    expect(useWorkspaceStore.getState().selectedWorkspaceId).toBe('ws_B');
    expect(useWorkspaceStore.getState().notice).toBeNull();
    expect(FakeEventSource.instances[0].url).toContain('/workspaces/ws_B/events');
  });
});

describe('switchWorkspace 固定顺序（RFC §12.1/12.2）', () => {
  async function bootIntoA() {
    await prepareWindow({ persisted: 'ws_A' });
    stubFetchRouter({
      '/me': () => json({ user_id: 'u1', name: 'o', role: 'owner', feature_flags: {} }),
      '/workspaces/ws_A/bootstrap': () => json(bootstrapPayload(WS_A, 10)),
      '/workspaces/ws_B/bootstrap': () => json(bootstrapPayload(WS_B, 20)),
      '/workspaces': () => json({ items: [WS_A, WS_B] }),
    });
    await bootstrap();
    FakeEventSource.instances[0].emitOpen();
    expect(useWorkspaceStore.getState().phase).toBe('ready');
    expect(useWorkspaceStore.getState().sseStatus).toBe('online');
  }

  it('切换后旧 EventSource 必须 close，全局同时只有一个存活流', async () => {
    await bootIntoA();
    expect(FakeEventSource.instances).toHaveLength(1);

    await switchWorkspace('ws_B');

    expect(useWorkspaceStore.getState().selectedWorkspaceId).toBe('ws_B');
    // 初启 bootstrap 走 beginSwitch（+1），本次切换再 +1。
    expect(useWorkspaceStore.getState().generation).toBe(2);
    expect(FakeEventSource.instances).toHaveLength(2);
    expect(FakeEventSource.instances[0].closed).toBe(true); // 旧流必须 close，不只是废弃引用
    expect(FakeEventSource.instances[1].closed).toBe(false);
    expect(FakeEventSource.instances[1].url).toContain('/workspaces/ws_B/events?cursor=20');
  });

  it('旧 Workspace 的 SSE 事件/status/cursor-expired 不污染新 Workspace', async () => {
    await bootIntoA();
    await switchWorkspace('ws_B');
    const staleSource = FakeEventSource.instances[0];
    const freshSource = FakeEventSource.instances[1];
    freshSource.emitOpen();

    const cursorBefore = useWorkspaceStore.getState().eventCursor;
    const bootstraps = (fetch as ReturnType<typeof vi.fn>).mock.calls.filter((call) =>
      String(call[0]).includes('/ws_A/bootstrap'),
    );

    // 旧流（已 close）仍可能有待分发回调：事件、状态、410 都必须被 generation/Workspace fencing 丢弃。
    staleSource.emitEvent('work_item.updated', eventOn('ws_A', 500));
    staleSource.emitOpen();
    staleSource.emitErrorClosed();
    await vi.waitFor(() => expect((fetch as ReturnType<typeof vi.fn>).mock.calls.filter((call) =>
      String(call[0]).includes('/ws_A/bootstrap'),
    )).toHaveLength(bootstraps.length));

    const ws = useWorkspaceStore.getState();
    expect(ws.eventCursor).toBe(cursorBefore);
    expect(ws.sseStatus).toBe('online'); // 旧流 onStatus('online'/'connecting') 未污染
    expect(ws.phase).toBe('ready');
  });

  it('cursor_expired 只重拉当前 Workspace，不回退「选第一个」', async () => {
    await prepareWindow({ persisted: 'ws_B' });
    let wsBBootstrapCalls = 0;
    stubFetchRouter({
      // SSE/探测 URL 必须先于 '/workspaces' 路由命中：探测 410 才触发 onCursorExpired。
      '/events': () => new Response('', { status: 410 }),
      '/me': () => json({ user_id: 'u1', name: 'o', role: 'owner', feature_flags: {} }),
      '/workspaces/ws_A/bootstrap': () => json(bootstrapPayload(WS_A, 10)),
      '/workspaces/ws_B/bootstrap': () => {
        wsBBootstrapCalls += 1;
        return json(bootstrapPayload(WS_B, 30));
      },
      '/workspaces': () => json({ items: [WS_A, WS_B] }),
    });
    await bootstrap();
    expect(wsBBootstrapCalls).toBe(1);

    const fresh = FakeEventSource.instances[0];
    fresh.emitOpen();
    fresh.emitErrorClosed(); // 探测 410 → onCursorExpired → 只 resync ws_B

    await vi.waitFor(() => expect(wsBBootstrapCalls).toBe(2));
    expect(useWorkspaceStore.getState().selectedWorkspaceId).toBe('ws_B');
    expect(useWorkspaceStore.getState().phase).toBe('ready');
    expect(useWorkspaceStore.getState().eventCursor).toBe(30);
    expect((fetch as ReturnType<typeof vi.fn>).mock.calls.some((call) => String(call[0]).includes('/ws_A/bootstrap'))).toBe(false);
  });

  it('A 的慢 tasks 响应在切换到 B 后不得写入 B（resolve-time guard）', async () => {
    await bootIntoA();
    const staleList = deferred();
    (fetch as ReturnType<typeof vi.fn>).mockImplementation((input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes('/work-items')) return staleList.promise;
      if (url.includes('/ws_B/bootstrap')) return Promise.resolve(json(bootstrapPayload(WS_B, 20)));
      if (url.includes('/bootstrap')) return Promise.resolve(json(bootstrapPayload(WS_A, 10)));
      return Promise.resolve(json({ items: [] }));
    });

    const refreshing = useTasksStore.getState().refresh();
    await switchWorkspace('ws_B');

    // B 的 bootstrap hydrate 后看板为空、已加载
    expect(useTasksStore.getState().items).toEqual([]);
    expect(useTasksStore.getState().loaded).toBe(true);

    staleList.resolve(json({ items: [{ id: 'wi_A', workspace_id: 'ws_A', record_kind: 'task', title: 'A 的任务', description: '', status: 'todo', priority: 'medium', due_date: null, runs_count: 0, version: 1, created_at: '', updated_at: '' }], next_cursor: null }));
    await refreshing;
    await Promise.resolve();

    expect(useTasksStore.getState().items).toEqual([]); // A 的慢响应被丢弃
    expect(useWorkspaceStore.getState().selectedWorkspaceId).toBe('ws_B');
  });

  it('reset 后旧 run 快照 Promise 不回写（runs.store 清理含 in-flight promise）', async () => {
    await bootIntoA();
    const staleRun = deferred();
    (fetch as ReturnType<typeof vi.fn>).mockImplementation((input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes('/runs/run_1/approvals') || url.includes('/runs/run_1/artifacts') || url.includes('/runs/run_1/events')) {
        return Promise.resolve(json({ items: [] }));
      }
      if (url.endsWith('/runs/run_1')) return staleRun.promise;
      if (url.includes('/ws_B/bootstrap')) return Promise.resolve(json(bootstrapPayload(WS_B, 20)));
      if (url.includes('/bootstrap')) return Promise.resolve(json(bootstrapPayload(WS_A, 10)));
      return Promise.resolve(json({ items: [] }));
    });

    useRunsStore.getState().watchRun('run_1');
    expect(useRunsStore.getState().watching['run_1']).toBe(1);

    await switchWorkspace('ws_B');
    expect(useRunsStore.getState().watching).toEqual({}); // reset 已清 watching

    staleRun.resolve(json({ id: 'run_1', work_item_id: 'wi_A', status: 'running', version: 1, created_at: '', updated_at: '' }));
    await vi.waitFor(() => expect(useRunsStore.getState().runs['run_1']).toBeUndefined());
    expect(useRunsStore.getState().timelines).toEqual({});
  });

  it('切换期间 Selector 数据源更新：notice 播报切换完成', async () => {
    await bootIntoA();
    await switchWorkspace('ws_B');
    expect(useWorkspaceStore.getState().notice).toContain('工作区乙');
    expect(useWorkspaceStore.getState().switching).toBe(false);
  });
});
