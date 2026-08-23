import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { CanonicalEvent } from '../api/types';
import { createDebounced, routeEvent, SSE_REFRESH_DEBOUNCE_MS } from './events';
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
