import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { CanonicalEvent } from '../api/types';
import { useDispatchesStore } from './dispatches.store';
import { useDecisionsStore } from './decisions.store';
import { useCommentsStore } from './comments.store';
import { createDebounced, routeEvent, SSE_REFRESH_DEBOUNCE_MS } from './events';
import { usePlansStore } from './plans.store';
import { useWorkspaceStore } from './workspace.store';
import { useCoordinatorStore } from './coordinator.store';
import { useRunsStore } from './runs.store';

const envelope = (type: string): CanonicalEvent => ({
  contract_version: 'events/v1',
  event_id: `evt_${type}`,
  workspace_id: 'ws_1',
  stream_seq: 1,
  aggregate: { type: 'work_item', id: 'wi_1', version: 1 },
  type,
  occurred_at: '2026-08-22T00:00:00Z',
  data: { record_kind: 'task' },
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
    ev.data = { record_kind: 'chat' };
    routeEvent(ev);
    expect(fetchMock).not.toHaveBeenCalled();

    await vi.advanceTimersByTimeAsync(SSE_REFRESH_DEBOUNCE_MS + 10);
    const urls = fetchMock.mock.calls.map((c) => String(c[0]));
    expect(urls.some((u) => u.includes('/work-items?assignee=agent_1&record_kind=chat'))).toBe(true);
  });

  it('缺少 record_kind 的 work_item 事件不刷新任何记录列表', async () => {
    const fetchMock = vi.fn().mockImplementation(() => Promise.resolve(json({ items: [], next_cursor: null })));
    vi.stubGlobal('fetch', fetchMock);

    const ev = envelope('work_item.created');
    delete ev.data;
    routeEvent(ev);
    await vi.advanceTimersByTimeAsync(SSE_REFRESH_DEBOUNCE_MS + 10);
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it('chat work_item 事件只刷新 Chat 列表，不刷新任务看板或任务仪表盘', async () => {
    const { useChatStore } = await import('./chat.store');
    useChatStore.setState({ agentId: 'agent_1', conversationId: null, conversations: [], runs: [], sending: false });
    const fetchMock = vi.fn().mockImplementation(() => Promise.resolve(json({ items: [], next_cursor: null })));
    vi.stubGlobal('fetch', fetchMock);

    const ev = envelope('work_item.created');
    ev.data = { record_kind: 'chat' };
    routeEvent(ev);
    await vi.advanceTimersByTimeAsync(SSE_REFRESH_DEBOUNCE_MS + 10);

    const urls = fetchMock.mock.calls.map((c) => String(c[0]));
    expect(urls.some((url) => url.includes('/work-items?assignee=agent_1&record_kind=chat'))).toBe(true);
    expect(urls.some((url) => url.includes('/dashboard'))).toBe(false);
    expect(urls.some((url) => url.includes('record_kind=task'))).toBe(false);
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
    ev.data = { work_item_id: 'wi_1', record_kind: 'task' };
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
    useDispatchesStore.setState({ byWorkItem: {}, errorByWorkItem: {} });
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
    ev.data = { work_item_id: 'wi_1', trigger: 'user_message', status: 'running', record_kind: 'task' };
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

  it('派发列表请求失败留下可重试错误，成功重试后清除错误', async () => {
    let failed = true;
    const fetchMock = vi.fn().mockImplementation(() => {
      if (failed) return Promise.reject(new Error('network'));
      return Promise.resolve(json({ items: [] }));
    });
    vi.stubGlobal('fetch', fetchMock);

    await useDispatchesStore.getState().refreshFor('wi_1');
    expect(useDispatchesStore.getState().errorByWorkItem.wi_1).toBe('派发记录加载失败，请重试');

    failed = false;
    await useDispatchesStore.getState().refreshFor('wi_1');
    expect(useDispatchesStore.getState().errorByWorkItem.wi_1).toBeUndefined();
    expect(useDispatchesStore.getState().byWorkItem.wi_1).toEqual([]);
  });
});

describe('routeEvent Coordinator 控制线', () => {
  const json = (body: unknown) =>
    new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } });

  afterEach(() => {
    vi.useRealTimers();
    vi.unstubAllGlobals();
    useCoordinatorStore.setState({ byWorkItem: {}, resolutionByWorkItem: {}, errorByWorkItem: {} });
  });

  it('缺少 record_kind 的 Coordinator 事件 fail-closed', async () => {
    vi.useFakeTimers();
    const fetchMock = vi.fn().mockResolvedValue(json({ items: [] }));
    vi.stubGlobal('fetch', fetchMock);
    const ev = envelope('coordinator.state_changed');
    delete ev.data;
    routeEvent(ev);
    await vi.advanceTimersByTimeAsync(250);
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it('子任务事件优先刷新 root Coordinator snapshot', async () => {
    vi.useFakeTimers();
    const fetchMock = vi.fn().mockImplementation((url: string) =>
      Promise.resolve(url.endsWith('/events') ? json({ items: [] }) : json({
        work_item_id: 'root', status: 'running', attempts: [], timeline: [], updated_at: '', version: 1,
      })),
    );
    vi.stubGlobal('fetch', fetchMock);
    const ev = envelope('coordinator.attempt_updated');
    ev.aggregate = { type: 'work_item', id: 'child', version: 2 };
    ev.data = { work_item_id: 'child', root_work_item_id: 'root', record_kind: 'task' };
    routeEvent(ev);
    await vi.advanceTimersByTimeAsync(250);
    // coordinator.* 同时触发 Review Queue 失效重取（RFC §12.4），队列调用不计入快照断言。
    const coordinatorCalls = fetchMock.mock.calls.filter((call) => !String(call[0]).includes('/review-queue'));
    await vi.waitFor(() => expect(coordinatorCalls).toHaveLength(2));
    expect(coordinatorCalls.every((call) => String(call[0]).includes('/root/coordinator'))).toBe(true);
  });
});

describe('routeEvent decision 域路由（会话元模型 S2）', () => {
  const json = (body: unknown) =>
    new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } });

  afterEach(() => {
    vi.useRealTimers();
    vi.unstubAllGlobals();
    useDecisionsStore.setState({ byWorkItem: {}, errorByWorkItem: {} });
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
    ev.data = { work_item_id: 'wi_1', quote: '以周会结论为准', record_kind: 'task' };
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

  it('决策列表请求失败留下可重试错误，成功重试后清除错误', async () => {
    let failed = true;
    const fetchMock = vi.fn().mockImplementation(() => {
      if (failed) return Promise.reject(new Error('network'));
      return Promise.resolve(json({ items: [] }));
    });
    vi.stubGlobal('fetch', fetchMock);

    await useDecisionsStore.getState().refreshFor('wi_1');
    expect(useDecisionsStore.getState().errorByWorkItem.wi_1).toBe('决策记录加载失败，请重试');

    failed = false;
    await useDecisionsStore.getState().refreshFor('wi_1');
    expect(useDecisionsStore.getState().errorByWorkItem.wi_1).toBeUndefined();
    expect(useDecisionsStore.getState().byWorkItem.wi_1).toEqual([]);
  });
});

describe('routeEvent 任务控制面新事件（RFC §10）', () => {
  const json = (body: unknown) =>
    new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } });

  const envelopeOf = (type: string, data?: Record<string, unknown>): CanonicalEvent => ({
    contract_version: 'events/v1',
    event_id: `evt_${type}_${Math.random()}`,
    workspace_id: 'ws_1',
    stream_seq: 1,
    aggregate: { type: 'workspace', id: 'ws_1', version: 1 },
    type,
    occurred_at: '2026-08-30T00:00:00Z',
    ...(data ? { data } : {}),
  });

  afterEach(() => {
    vi.useRealTimers();
    vi.unstubAllGlobals();
    useCommentsStore.setState({ byRoot: {}, latestRevisions: {}, errorByRoot: {}, loadingByRoot: {} });
  });

  it('六个新事件都在 EVENT_NAMES 白名单内（SSE listener 按白名单注册）', async () => {
    const { EVENT_NAMES } = await import('../api/types');
    for (const name of [
      'execution_host.updated',
      'workspace_location.created',
      'workspace_location.updated',
      'workspace_location.unavailable',
      'work_item.development_context_updated',
      'task_comment.created',
    ]) {
      expect(EVENT_NAMES).toContain(name);
    }
  });

  it('治理事件进入 SSE 白名单但不误路由为 Run', async () => {
    const { EVENT_NAMES } = await import('../api/types');
    const names = [
      'goal.created',
      'goal.state_changed',
      'todo.created',
      'todo.state_changed',
      'todo.claim_changed',
      'turn.receipt_appended',
      'handoff.created',
      'handoff.state_changed',
      'goal.evidence_added',
      'validation.result_recorded',
      'projection.updated',
      'projection.repair_state_changed',
      'quota.reservation_changed',
      'quota.spend_recorded',
      'quota.gap_reconciled',
      'delivery_brief.snapshot_created',
    ];
    const fetchMock = vi.fn();
    vi.stubGlobal('fetch', fetchMock);
    useRunsStore.getState().reset();

    for (const name of names) {
      expect(EVENT_NAMES).toContain(name);
      routeEvent(envelopeOf(name));
    }

    expect(fetchMock).not.toHaveBeenCalled();
    expect(useRunsStore.getState().timelines).toEqual({});
  });

  it('file_changes.reverted 由 routeEvent 进入 run 时间线并推进 changes revision（其他窗口/replay 可重拉回滚态）', () => {
    useRunsStore.getState().reset();
    const reverted = envelopeOf('file_changes.reverted', { record_kind: 'task' });
    reverted.aggregate = { type: 'execution_run', id: 'run_1', version: 2 };
    reverted.run_seq = 5;
    routeEvent(reverted);
    expect(useRunsStore.getState().timelines.run_1?.[0]?.type).toBe('file_changes.reverted');
    expect(useRunsStore.getState().changesRevision.run_1).toBe(1);

    // 同一 SSE replay 不应再触发一次刷新；卡片不会陷入重复拉取。
    routeEvent(reverted);
    expect(useRunsStore.getState().changesRevision.run_1).toBe(1);
  });

  it('task_comment.created 路由进 comments store 按 root 失效重取', async () => {
    vi.useFakeTimers();
    const fetchMock = vi.fn().mockResolvedValue(json({ items: [], next_revision: null, latest_revision: 0 }));
    vi.stubGlobal('fetch', fetchMock);
    useWorkspaceStore.setState({
      workspace: { id: 'ws_1', name: 'w', timezone: 'UTC', version: 1 },
    });

    routeEvent(envelopeOf('task_comment.created', {
      record_kind: 'task', comment_id: 'cmt_1', root_work_item_id: 'wi_root',
      work_item_id: 'wi_child', revision: 18, kind: 'requirement', actionable: true,
    }));
    await vi.advanceTimersByTimeAsync(SSE_REFRESH_DEBOUNCE_MS + 10);

    expect(String(fetchMock.mock.calls[0][0])).toBe('/api/v1/work-items/wi_root/comments?limit=200');
  });

  it('Location/Host/context 事件合法消费：不刷新列表、不落入 run 域', async () => {
    vi.useFakeTimers();
    const fetchMock = vi.fn().mockResolvedValue(json({ items: [] }));
    vi.stubGlobal('fetch', fetchMock);
    useWorkspaceStore.setState({
      workspace: { id: 'ws_1', name: 'w', timezone: 'UTC', version: 1 },
    });

    routeEvent(envelopeOf('workspace_location.created', { location_id: 'wsloc_1' }));
    routeEvent(envelopeOf('workspace_location.updated', { location_id: 'wsloc_1' }));
    routeEvent(envelopeOf('workspace_location.unavailable', { location_id: 'wsloc_1' }));
    routeEvent(envelopeOf('execution_host.updated', { host_id: 'host_1' }));
    routeEvent(envelopeOf('work_item.development_context_updated', { work_item_id: 'wi_1' }));
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
