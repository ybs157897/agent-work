import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { WorkspaceEventStream, type SseStatus } from './sse';
import type { CanonicalEvent } from './types';
import { clearOutputTrace, getOutputTrace } from '../utils/output-trace';

/** 模拟 EventSource：记录监听、手动触发 open/message/error。 */
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

const makeEvent = (seq: number, type = 'work_item.updated'): CanonicalEvent => ({
  contract_version: 'events/v1',
  event_id: `evt_${seq}`,
  workspace_id: 'ws_1',
  stream_seq: seq,
  aggregate: { type: 'work_item', id: 'wi_1', version: seq },
  type,
  occurred_at: '2026-08-21T00:00:00Z',
});

describe('WorkspaceEventStream', () => {
  beforeEach(() => {
    FakeEventSource.instances = [];
    clearOutputTrace();
    vi.stubGlobal('EventSource', FakeEventSource);
  });

  afterEach(() => {
    clearOutputTrace();
    vi.unstubAllGlobals();
    vi.useRealTimers();
  });

  it('解析白名单事件并回调 onEvent；open 后状态为 online', () => {
    const events: CanonicalEvent[] = [];
    const statuses: SseStatus[] = [];
    const stream = new WorkspaceEventStream('ws_1', {
      onEvent: (e) => events.push(e),
      onStatus: (s) => statuses.push(s),
      onCursorExpired: () => undefined,
    });
    stream.start(10);

    const es = FakeEventSource.instances[0];
    expect(es.url).toBe('/api/v1/workspaces/ws_1/events?cursor=10');

    es.emitOpen();
    es.emitEvent('work_item.updated', makeEvent(11));
    expect(events).toHaveLength(1);
    expect(statuses).toEqual(['connecting', 'online']);
    stream.stop();
  });

  it('订阅 subagent.updated，使 Kimi 蜂群成员更新进入 run 时间线', () => {
    const events: CanonicalEvent[] = [];
    const stream = new WorkspaceEventStream('ws_1', {
      onEvent: (event) => events.push(event),
      onStatus: () => undefined,
      onCursorExpired: () => undefined,
    });
    stream.start(0);
    const event: CanonicalEvent = {
      ...makeEvent(1, 'subagent.updated'),
      aggregate: { type: 'execution_run', id: 'run_1', version: 1 },
      run_seq: 2,
      data: { runtime: 'kimi', role: 'member', subagent_id: 'member-1' },
    };
    FakeEventSource.instances[0].emitEvent('subagent.updated', event);
    expect(events).toEqual([event]);
    stream.stop();
  });

  it('订阅 file_changes.reverted，使其他窗口与 SSE replay 可接收变更回滚', () => {
    const events: CanonicalEvent[] = [];
    const stream = new WorkspaceEventStream('ws_1', {
      onEvent: (event) => events.push(event),
      onStatus: () => undefined,
      onCursorExpired: () => undefined,
    });
    stream.start(0);
    const event: CanonicalEvent = {
      ...makeEvent(1, 'file_changes.reverted'),
      aggregate: { type: 'execution_run', id: 'run_1', version: 2 },
      run_seq: 5,
      data: { record_kind: 'task' },
    };
    FakeEventSource.instances[0].emitEvent('file_changes.reverted', event);
    expect(events).toEqual([event]);
    stream.stop();
  });

  it('event_id 去重；非 events/v1 与未知类型不进入回调', () => {
    const events: CanonicalEvent[] = [];
    const stream = new WorkspaceEventStream('ws_1', {
      onEvent: (e) => events.push(e),
      onStatus: () => undefined,
      onCursorExpired: () => undefined,
    });
    stream.start(0);
    const es = FakeEventSource.instances[0];
    es.emitOpen();
    es.emitEvent('work_item.updated', makeEvent(1));
    es.emitEvent('work_item.updated', makeEvent(1)); // 重复 event_id
    es.emitEvent('work_item.updated', { ...makeEvent(2), contract_version: 'events/v0' });
    expect(events.map((e) => e.event_id)).toEqual(['evt_1']);
    stream.stop();
  });

  it('只在协议校验和去重通过后记录 sse.received', () => {
    vi.stubGlobal('window', {
      location: { search: '?outputTrace=1&outputTraceContent=1' },
      localStorage: { getItem: () => null },
    });
    const stream = new WorkspaceEventStream('ws_1', {
      onEvent: () => undefined,
      onStatus: () => undefined,
      onCursorExpired: () => undefined,
    });
    stream.start(0);
    const event: CanonicalEvent = {
      ...makeEvent(1, 'message.delta'),
      aggregate: { type: 'execution_run', id: 'run_1', version: 1 },
      run_seq: 4,
      data: { raw: { chunk: { type: 'text-delta', text: '**流式**' } } },
    };
    const source = FakeEventSource.instances[0];
    source.emitEvent('message.delta', event);
    source.emitEvent('message.delta', event);
    source.emitEvent('message.delta', { ...event, event_id: 'bad', contract_version: 'events/v0' });

    const traces = getOutputTrace().filter((entry) => entry.stage === 'sse.received');
    expect(traces).toHaveLength(1);
    expect(traces[0]).toMatchObject({
      runId: 'run_1',
      eventId: 'evt_1',
      runSeq: 4,
      text: { content: '**流式**' },
    });
    stream.stop();
  });

  it('连接 CLOSED 且探测为 410 时触发 onCursorExpired（不重连）', async () => {
    const expired = vi.fn();
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(new Response('', { status: 410 })),
    );
    const stream = new WorkspaceEventStream('ws_1', {
      onEvent: () => undefined,
      onStatus: () => undefined,
      onCursorExpired: expired,
    });
    stream.start(7);
    const es = FakeEventSource.instances[0];
    es.emitOpen();
    es.emitErrorClosed();

    await vi.waitFor(() => expect(expired).toHaveBeenCalledTimes(1));
    expect(FakeEventSource.instances).toHaveLength(1);
  });

  it('连接 CLOSED 且探测非 410 时退避重连', async () => {
    vi.useFakeTimers();
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response('', { status: 502 })));
    const stream = new WorkspaceEventStream('ws_1', {
      onEvent: () => undefined,
      onStatus: () => undefined,
      onCursorExpired: () => undefined,
    });
    stream.start(0);
    FakeEventSource.instances[0].emitErrorClosed();
    await vi.runAllTimersAsync();
    expect(FakeEventSource.instances.length).toBeGreaterThan(1);
    stream.stop();
  });
});
