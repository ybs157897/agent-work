import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { CanonicalEvent } from '../api/types';
import { useDispatchesStore } from './dispatches.store';
import { useDecisionsStore } from './decisions.store';
import { createDebounced, routeEvent, SSE_REFRESH_DEBOUNCE_MS } from './events';
import { usePlansStore } from './plans.store';
import { useWorkspaceStore } from './workspace.store';

const envelope = (type: string): CanonicalEvent => ({
  contract_version: 'events/v1',
  event_id: `evt_${type}`,
  workspace_id: 'ws_1',
  stream_seq: 1,
  aggregate: { type: 'work_item', id: 'wi_1', version: 1 },
  type,
  occurred_at: '2026-08-22T00:00:00Z',
});

describe('routeEvent SSE→refresh debounce', () => {
  const json = (body: unknown) =>
    new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } });

  beforeEach(() => {
    vi.useFakeTimers();
    useWorkspaceStore.setState({
      workspace: { id: 'ws_1', name: 'w', timezone: 'UTC', version: 1 },
    });
  });

  afterEach(() => {
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  it('窗口内多个 work_item.* 事件合并为一次 tasks + dashboard 拉取', async () => {
    const fetchMock = vi.fn().mockImplementation(() => Promise.resolve(json({ items: [], next_cursor: null })));
    vi.stubGlobal('fetch', fetchMock);

    routeEvent(envelope('work_item.created'));
    routeEvent(envelope('work_item.updated'));
    routeEvent(envelope('work_item.status_changed'));
    expect(fetchMock).not.toHaveBeenCalled(); // trailing-edge：窗口内尚未触发

    await vi.advanceTimersByTimeAsync(SSE_REFRESH_DEBOUNCE_MS + 10);
    const urls = fetchMock.mock.calls.map((c) => String(c[0]));
    expect(urls.filter((u) => u.includes('/work-items'))).toHaveLength(1);
    expect(urls.filter((u) => u.includes('/dashboard'))).toHaveLength(1);
  });

  it('窗口结束后新事件再次触发刷新（trailing 不吞后续事件）', async () => {
    const fetchMock = vi.fn().mockImplementation(() => Promise.resolve(json({ items: [], next_cursor: null })));
    vi.stubGlobal('fetch', fetchMock);

    routeEvent(envelope('work_item.created'));
    await vi.advanceTimersByTimeAsync(SSE_REFRESH_DEBOUNCE_MS + 10);
    routeEvent(envelope('work_item.updated'));
    await vi.advanceTimersByTimeAsync(SSE_REFRESH_DEBOUNCE_MS + 10);

    const urls = fetchMock.mock.calls.map((c) => String(c[0]));
    expect(urls.filter((u) => u.includes('/work-items'))).toHaveLength(2);
  });

  it('run.completed 触发 chat 会话列表刷新（同样走 debounce）', async () => {
    const { useChatStore } = await import('./chat.store');
    useChatStore.setState({
      agentId: 'agent_1',
      conversationId: null,
      conversations: [],
      runs: [],
      sending: false,
    });
    const fetchMock = vi.fn().mockImplementation(() => Promise.resolve(json({ items: [], next_cursor: null })));
    vi.stubGlobal('fetch', fetchMock);

    const ev = envelope('run.completed');
    ev.aggregate = { type: 'execution_run', id: 'run_1', version: 1 };
    routeEvent(ev);
    expect(fetchMock).not.toHaveBeenCalled();

    await vi.advanceTimersByTimeAsync(SSE_REFRESH_DEBOUNCE_MS + 10);
    const urls = fetchMock.mock.calls.map((c) => String(c[0]));
    expect(urls.some((u) => u.includes('/work-items?assignee=agent_1'))).toBe(true);
  });
});

describe('routeEvent plan 域路由', () => {
  const json = (body: unknown) =>
    new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } });

  const plan = {
    id: 'plan_1',
    workspace_id: 'ws_1',
    work_item_id: 'wi_1',
    agent_profile_id: 'agent_lead',
    source_run_id: null,
    status: 'active',
    superseded_by: null,
    steps: [],
    version: 1,
    created_at: '',
    updated_at: '',
  };

  afterEach(() => {
    vi.unstubAllGlobals();
    usePlansStore.setState({ byWorkItem: {}, workItemOf: {} });
  });

  it('plan.submitted 路由进 plans store，不触发看板/仪表盘刷新', async () => {
    const fetchMock = vi.fn().mockImplementation(() => Promise.resolve(json(plan)));
    vi.stubGlobal('fetch', fetchMock);

    const ev = envelope('plan.submitted');
    ev.aggregate = { type: 'plan', id: 'plan_1', version: 1 };
    ev.data = { work_item_id: 'wi_1' };
    routeEvent(ev);

    await vi.waitFor(() => {
      expect(usePlansStore.getState().byWorkItem['wi_1']?.id).toBe('plan_1');
    });
    const urls = fetchMock.mock.calls.map((c) => String(c[0]));
    expect(urls).toEqual(['/api/v1/plans/plan_1']); // 看板刷新由 dispatch 建子任务的 work_item.created 承担
  });
});

describe('routeEvent dispatch 域路由（会话元模型）', () => {
  const json = (body: unknown) =>
    new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } });

  afterEach(() => {
    vi.useRealTimers();
    vi.unstubAllGlobals();
    useDispatchesStore.setState({ byWorkItem: {} });
  });

  it('dispatch.created 按载荷 work_item_id 失效重取派发卡片（窗口内合并为一次）', async () => {
    vi.useFakeTimers();
    const fetchMock = vi.fn().mockImplementation(() =>
      Promise.resolve(
        json({
          items: [
            { id: 'disp_1', work_item_id: 'wi_1', trigger: 'user_message', status: 'running', runs: [], created_at: '' },
          ],
        }),
      ),
    );
    vi.stubGlobal('fetch', fetchMock);

    const ev = envelope('dispatch.created');
    ev.aggregate = { type: 'dispatch', id: 'disp_1', version: 1 };
    ev.data = { work_item_id: 'wi_1', trigger: 'user_message', status: 'running' };
    routeEvent(ev);
    routeEvent(ev);
    expect(fetchMock).not.toHaveBeenCalled(); // trailing-edge：窗口内尚未触发

    await vi.advanceTimersByTimeAsync(SSE_REFRESH_DEBOUNCE_MS + 50);
    const urls = fetchMock.mock.calls.map((c) => String(c[0]));
    expect(urls).toEqual(['/api/v1/work-items/wi_1/dispatches']);
    expect(useDispatchesStore.getState().byWorkItem['wi_1']).toHaveLength(1);
  });

  it('无 work_item_id 线索的 dispatch 事件已消费但不发请求', async () => {
    vi.useFakeTimers();
    const fetchMock = vi.fn().mockImplementation(() => Promise.resolve(json({ items: [] })));
    vi.stubGlobal('fetch', fetchMock);

    const ev = envelope('dispatch.updated');
    ev.aggregate = { type: 'dispatch', id: 'disp_9', version: 1 };
    routeEvent(ev);

    await vi.advanceTimersByTimeAsync(SSE_REFRESH_DEBOUNCE_MS + 50);
    expect(fetchMock).not.toHaveBeenCalled();
  });
});

describe('routeEvent decision 域路由（会话元模型 S2）', () => {
  const json = (body: unknown) =>
    new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } });

  afterEach(() => {
    vi.useRealTimers();
    vi.unstubAllGlobals();
    useDecisionsStore.setState({ byWorkItem: {} });
  });

  it('decision.created 按载荷 work_item_id 失效重取决策台账（窗口内合并为一次）', async () => {
    vi.useFakeTimers();
    const fetchMock = vi.fn().mockImplementation(() =>
      Promise.resolve(
        json({ items: [{ id: 'dec_1', work_item_id: 'wi_1', quote: '以周会结论为准', created_at: '' }] }),
      ),
    );
    vi.stubGlobal('fetch', fetchMock);

    const ev = envelope('decision.created');
    ev.aggregate = { type: 'decision', id: 'dec_1', version: 1 };
    ev.data = { work_item_id: 'wi_1', quote: '以周会结论为准' };
    routeEvent(ev);
    routeEvent(ev);
    expect(fetchMock).not.toHaveBeenCalled(); // trailing-edge：窗口内尚未触发

    await vi.advanceTimersByTimeAsync(SSE_REFRESH_DEBOUNCE_MS + 50);
    const urls = fetchMock.mock.calls.map((c) => String(c[0]));
    expect(urls).toEqual(['/api/v1/work-items/wi_1/decisions']);
    expect(useDecisionsStore.getState().byWorkItem['wi_1']).toHaveLength(1);
  });

  it('无 work_item_id 线索的 decision 事件已消费但不发请求', async () => {
    vi.useFakeTimers();
    const fetchMock = vi.fn().mockImplementation(() => Promise.resolve(json({ items: [] })));
    vi.stubGlobal('fetch', fetchMock);

    const ev = envelope('decision.created');
    ev.aggregate = { type: 'decision', id: 'dec_9', version: 1 };
    routeEvent(ev);

    await vi.advanceTimersByTimeAsync(SSE_REFRESH_DEBOUNCE_MS + 50);
    expect(fetchMock).not.toHaveBeenCalled();
  });
});

describe('createDebounced', () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => vi.useRealTimers());

  it('只保留窗口内最后一次调用', async () => {
    const fn = vi.fn();
    const debounced = createDebounced(fn, 100);

    debounced();
    debounced();
    debounced();
    await vi.advanceTimersByTimeAsync(50);
    expect(fn).not.toHaveBeenCalled();

    await vi.advanceTimersByTimeAsync(60);
    expect(fn).toHaveBeenCalledTimes(1);
  });
});
