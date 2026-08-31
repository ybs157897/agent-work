import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { CanonicalEvent, DeliveryBrief, WorkItem } from '../api/types';
import { routeEvent, SSE_REFRESH_DEBOUNCE_MS } from './events';
import { useBriefStore } from './brief.store';
import { useWorkspaceStore } from './workspace.store';

const workItem: WorkItem = {
  id: 'wi_root',
  workspace_id: 'ws_1',
  record_kind: 'task',
  title: '发布版本',
  description: '',
  status: 'in_progress',
  phase: 'acceptance',
  priority: 'high',
  due_date: null,
  runs_count: 2,
  version: 8,
  created_at: '2026-08-30T00:00:00Z',
  updated_at: '2026-08-30T01:00:00Z',
};

let asOf = 120;

const brief = (overrides: Partial<DeliveryBrief> = {}): DeliveryBrief => ({
  work_item: workItem,
  acceptance_criteria: ['登录成功', '错误有提示'],
  conclusion: { coordinator_status: 'waiting_user', stage: 'acceptance', summary: '已完成', next_action: '等待验收', version: 7 },
  attempts: [{ attempt: 1, role: 'worker', run_id: 'run_1', agent_name: 'Atlas', status: 'succeeded', started_at: null, finished_at: null, retry_of: null, failure: null }],
  runs: [{ run: { id: 'run_1', work_item_id: 'wi_root', status: 'succeeded', version: 3, created_at: '', updated_at: '' }, summary: '一次成功', evidence: [{ id: 'ev_1', source_kind: 'run_status', source_id: 'run_1', label: '运行成功', status: 'passed', trust: 'control_plane', occurred_at: '2026-08-30T01:00:00Z' }], truncated: false }],
  changes: { run_id: 'run_1', files: [{ path: 'src/a.ts', added: 3, deleted: 1, status: 'modified' }], total_files: 1, total_added: 3, total_deleted: 1, truncated: false },
  artifacts: [],
  blocker: null,
  risks: [],
  comments: [],
  freshness: { generated_at: '2026-08-30T01:05:00Z', as_of_event_seq: asOf, state: 'current', missing_sources: [] },
  truncation: { attempts: false, runs: false, files: false, artifacts: false, comments: false },
  ...overrides,
});

const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': status >= 400 ? 'application/problem+json' : 'application/json' },
  });

const envelope = (type: string, data?: Record<string, unknown>, aggregateType = 'work_item'): CanonicalEvent => ({
  contract_version: 'events/v1',
  event_id: `evt_${type}_${Math.random()}`,
  workspace_id: 'ws_1',
  stream_seq: 1,
  aggregate: { type: aggregateType, id: 'wi_root', version: 1 },
  type,
  occurred_at: '2026-08-30T00:00:00Z',
  ...(data ? { data } : {}),
});

function readyWorkspace(eventCursor = 0) {
  useWorkspaceStore.setState({
    phase: 'ready',
    error: null,
    notice: null,
    me: null,
    workspace: { id: 'ws_1', name: 'w', timezone: 'UTC', version: 1 },
    workspaces: [{ id: 'ws_1', name: 'w', timezone: 'UTC', version: 1 }],
    selectedWorkspaceId: 'ws_1',
    generation: 0,
    switching: false,
    health: null,
    eventCursor,
    sseStatus: 'online',
  });
}

beforeEach(() => {
  readyWorkspace();
  asOf = 120;
  useBriefStore.getState().reset();
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

describe('brief.store（任务控制面 RFC §4.11/§12.5）', () => {
  it('首拉写入 brief；eventCursor ≤ as_of 时为 current（非 stale）', async () => {
    vi.stubGlobal('fetch', vi.fn().mockImplementation(() => Promise.resolve(json(brief()))));
    await useBriefStore.getState().refreshFor('wi_root');

    const s = useBriefStore.getState();
    expect(s.byWorkItem['wi_root'].conclusion.summary).toBe('已完成');
    expect(s.statusByWorkItem['wi_root']).toBe('ready');
    expect(s.staleByWorkItem['wi_root']).toBe(false);
    expect(String((fetch as ReturnType<typeof vi.fn>).mock.calls[0][0])).toBe('/api/v1/work-items/wi_root/delivery-brief');
  });

  it('eventCursor > as_of_event_seq：标 stale（UI 先显示「可能有更新」）', async () => {
    asOf = 100;
    vi.stubGlobal('fetch', vi.fn().mockImplementation(() => Promise.resolve(json(brief()))));
    readyWorkspace(120); // 游标已在聚合之后
    await useBriefStore.getState().refreshFor('wi_root');

    expect(useBriefStore.getState().staleByWorkItem['wi_root']).toBe(true);
  });

  it('SSE 事件命中缓存 brief：标 stale 后自动后台补拉，水位追上前 stale 保持', async () => {
    vi.useFakeTimers();
    asOf = 120;
    const fetchMock = vi.fn().mockImplementation(() => Promise.resolve(json(brief())));
    vi.stubGlobal('fetch', fetchMock);
    await useBriefStore.getState().refreshFor('wi_root');

    // 新事件推进游标越过聚合水位
    readyWorkspace(150);
    useBriefStore.getState().applyEvent(envelope('work_item.updated', { record_kind: 'task', work_item_id: 'wi_root' }));
    expect(useBriefStore.getState().staleByWorkItem['wi_root']).toBe(true); // 先标 stale

    await vi.advanceTimersByTimeAsync(SSE_REFRESH_DEBOUNCE_MS + 10);
    // 首拉 + 后台补拉；补拉返回的 brief 仍是旧聚合（as_of=120 < 150），stale 如实保持。
    expect(fetchMock.mock.calls.filter((c) => String(c[0]).includes('/delivery-brief'))).toHaveLength(2);
    expect(useBriefStore.getState().staleByWorkItem['wi_root']).toBe(true);
    expect(useBriefStore.getState().errorByWorkItem['wi_root']).toBeUndefined();
  });

  it('真实 routeEvent 链路会把已缓存 Brief 标 stale 并后台补拉', async () => {
    vi.useFakeTimers();
    const fetchMock = vi.fn().mockImplementation((url: string) => {
      if (String(url).includes('/delivery-brief')) return Promise.resolve(json(brief()));
      if (String(url).includes('/dashboard')) return Promise.resolve(json({ recent_activities: [] }));
      return Promise.resolve(json({ items: [], next_cursor: null, total_count: 0, generated_at: '' }));
    });
    vi.stubGlobal('fetch', fetchMock);
    await useBriefStore.getState().refreshFor('wi_root');

    // bootstrap SSE callback 会先推进 cursor 再调用 routeEvent；这里复现同一顺序。
    readyWorkspace(150);
    routeEvent(envelope('work_item.updated', { record_kind: 'task', work_item_id: 'wi_root' }));
    expect(useBriefStore.getState().staleByWorkItem.wi_root).toBe(true);

    await vi.advanceTimersByTimeAsync(SSE_REFRESH_DEBOUNCE_MS + 10);
    expect(fetchMock.mock.calls.filter((call) => String(call[0]).includes('/delivery-brief'))).toHaveLength(2);
  });

  it('不含 work_item_id 的 artifact SSE 保守失效所有已缓存 Brief', async () => {
    vi.useFakeTimers();
    const fetchMock = vi.fn().mockImplementation((url: string) => {
      if (String(url).includes('/delivery-brief')) return Promise.resolve(json(brief()));
      return Promise.resolve(json({ items: [] }));
    });
    vi.stubGlobal('fetch', fetchMock);
    await useBriefStore.getState().refreshFor('wi_root');

    readyWorkspace(150);
    routeEvent(envelope('artifact.created', { record_kind: 'task' }, 'artifact'));
    expect(useBriefStore.getState().staleByWorkItem.wi_root).toBe(true);

    await vi.advanceTimersByTimeAsync(SSE_REFRESH_DEBOUNCE_MS + 10);
    expect(fetchMock.mock.calls.filter((call) => String(call[0]).includes('/delivery-brief'))).toHaveLength(2);
  });

  it('后台补拉失败：保留旧内容并给出「可能有更新」提示，不清空为空态', async () => {
    vi.useFakeTimers();
    asOf = 120;
    let fail = false;
    const fetchMock = vi.fn().mockImplementation(() => {
      if (fail) return Promise.resolve(json({ type: 'about:blank', title: '错误', status: 500, code: 'http_error', retryable: true }, 500));
      return Promise.resolve(json(brief()));
    });
    vi.stubGlobal('fetch', fetchMock);
    await useBriefStore.getState().refreshFor('wi_root');

    readyWorkspace(150);
    useBriefStore.getState().applyEvent(envelope('run.failed', { record_kind: 'task', work_item_id: 'wi_root' }, 'execution_run'));
    fail = true;
    await vi.advanceTimersByTimeAsync(SSE_REFRESH_DEBOUNCE_MS + 10);

    const s = useBriefStore.getState();
    expect(s.byWorkItem['wi_root'].conclusion.summary).toBe('已完成'); // 旧内容保留
    expect(s.staleByWorkItem['wi_root']).toBe(true);
    expect(s.errorByWorkItem['wi_root']).toContain('可能不是最新');
    expect(s.statusByWorkItem['wi_root']).toBe('ready'); // 不回退成 loading/空态
  });

  it('首拉失败（无旧内容）显式 error 态；partial 与 missing_sources 原样保留给 UI', async () => {
    const fetchMock = vi.fn().mockImplementation((url: string) => {
      if (String(url).includes('delivery-brief')) {
        return Promise.resolve(json({ type: 'about:blank', title: '禁止', status: 403, code: 'workspace_path_forbidden', detail: '无权限', retryable: false }, 403));
      }
      return Promise.resolve(json({ items: [] }));
    });
    vi.stubGlobal('fetch', fetchMock);
    await useBriefStore.getState().refreshFor('wi_root');
    const failed = useBriefStore.getState();
    expect(failed.byWorkItem['wi_root']).toBeUndefined();
    expect(failed.errorByWorkItem['wi_root']).toBeTruthy();

    // partial：服务端 state=partial + missing_sources 是数据事实，store 原样保留
    vi.stubGlobal('fetch', vi.fn().mockImplementation(() =>
      Promise.resolve(json(brief({ freshness: { generated_at: '2026-08-30T01:05:00Z', as_of_event_seq: 120, state: 'partial', missing_sources: ['evaluation_verdict'] } }))),
    ));
    await useBriefStore.getState().refreshFor('wi_root');
    const partial = useBriefStore.getState();
    expect(partial.byWorkItem['wi_root'].freshness.state).toBe('partial');
    expect(partial.byWorkItem['wi_root'].freshness.missing_sources).toEqual(['evaluation_verdict']);
  });

  it('A→B 切换后旧 Workspace 的慢响应不回写（resolve-time guard）', async () => {
    let release!: (resp: Response) => void;
    vi.stubGlobal('fetch', vi.fn(() => new Promise<Response>((resolve) => {
      release = resolve;
    })));

    const refreshing = useBriefStore.getState().refreshFor('wi_root');
    useWorkspaceStore.setState({
      workspace: { id: 'ws_2', name: '乙', timezone: 'UTC', version: 1 },
      workspaces: [{ id: 'ws_2', name: '乙', timezone: 'UTC', version: 1 }],
      selectedWorkspaceId: 'ws_2',
    });
    useWorkspaceStore.setState((s) => ({ generation: s.generation + 1 }));

    release(json(brief()));
    await refreshing;

    expect(useBriefStore.getState().byWorkItem['wi_root']).toBeUndefined();
  });

  it('裸 work-item id 返回其他 Workspace 的 brief 时拒绝接纳', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockImplementation(() => Promise.resolve(json(brief({
        work_item: { ...workItem, workspace_id: 'ws_2' },
      })))),
    );

    await useBriefStore.getState().refreshFor('wi_root', 'ws_1');

    expect(useBriefStore.getState().byWorkItem['wi_root']).toBeUndefined();
  });

  it('不相关事件不消费；命中缓存才失效，未命中直接丢弃', () => {
    expect(useBriefStore.getState().applyEvent(envelope('dashboard.metrics.updated'))).toBe(false);
    expect(useBriefStore.getState().applyEvent(envelope('work_item.updated', { record_kind: 'task' }))).toBe(true);
    expect(useBriefStore.getState().staleByWorkItem['wi_root']).toBeUndefined();
  });

  it('reset 清空全部投影', async () => {
    vi.stubGlobal('fetch', vi.fn().mockImplementation(() => Promise.resolve(json(brief()))));
    await useBriefStore.getState().refreshFor('wi_root');
    useBriefStore.getState().reset();
    const s = useBriefStore.getState();
    expect(s.byWorkItem).toEqual({});
    expect(s.statusByWorkItem).toEqual({});
  });
});
