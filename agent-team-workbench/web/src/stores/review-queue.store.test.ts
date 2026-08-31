import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { CanonicalEvent, ReviewQueueResponse, WorkItem } from '../api/types';
import { SSE_REFRESH_DEBOUNCE_MS } from './events';
import { routeEvent } from './events';
import { useReviewQueueStore } from './review-queue.store';
import { useWorkspaceStore } from './workspace.store';

const task = (id: string, phase: 'review' | 'acceptance' = 'review'): WorkItem => ({
  id,
  workspace_id: 'ws_1',
  record_kind: 'task',
  title: `任务 ${id}`,
  description: '',
  status: 'in_progress',
  phase,
  priority: 'high',
  due_date: null,
  runs_count: 1,
  version: 3,
  created_at: '2026-08-30T00:00:00Z',
  updated_at: '2026-08-30T00:10:00Z',
});

const queuePage = (overrides: Partial<ReviewQueueResponse> = {}): ReviewQueueResponse => ({
  items: [
    {
      work_item: task('wi_1'),
      pending_since: '2026-08-30T00:05:00Z',
      coordinator: { status: 'waiting_user', stage: 'acceptance', updated_at: '2026-08-30T00:10:00Z', version: 7 },
      latest_run_id: 'run_1',
      source_watermark: { as_of_event_seq: 120 },
    },
  ],
  total_count: 100,
  next_cursor: null,
  generated_at: '2026-08-30T00:12:00Z',
  ...overrides,
});

const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': status >= 400 ? 'application/problem+json' : 'application/json' },
  });

const envelope = (type: string, data?: Record<string, unknown>): CanonicalEvent => ({
  contract_version: 'events/v1',
  event_id: `evt_${type}_${Math.random()}`,
  workspace_id: 'ws_1',
  stream_seq: 1,
  aggregate: { type: 'work_item', id: 'wi_1', version: 1 },
  type,
  occurred_at: '2026-08-30T00:00:00Z',
  ...(data ? { data } : {}),
});

function readyWorkspace(id = 'ws_1') {
  useWorkspaceStore.setState({
    phase: 'ready',
    error: null,
    notice: null,
    me: null,
    workspace: { id, name: 'w', timezone: 'UTC', version: 1 },
    workspaces: [{ id, name: 'w', timezone: 'UTC', version: 1 }],
    selectedWorkspaceId: id,
    generation: 0,
    switching: false,
    health: null,
    eventCursor: 0,
    sseStatus: 'online',
  });
}

beforeEach(() => {
  readyWorkspace();
  useReviewQueueStore.getState().reset();
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

describe('review-queue.store（任务控制面 RFC §4.10/§12.4）', () => {
  it('badge 使用服务端 total_count（100），而非当前页长度（1）', async () => {
    const fetchMock = vi.fn().mockImplementation(() => Promise.resolve(json(queuePage())));
    vi.stubGlobal('fetch', fetchMock);

    await useReviewQueueStore.getState().refresh();

    const s = useReviewQueueStore.getState();
    expect(s.totalCount).toBe(100);
    expect(s.items).toHaveLength(1);
    expect(s.loaded).toBe(true);
    expect(String(fetchMock.mock.calls[0][0])).toBe('/api/v1/workspaces/ws_1/review-queue?limit=50');
  });

  it('cursor 分页：loadMore 追加下一页，total_count 以最新响应为准', async () => {
    const fetchMock = vi.fn().mockImplementation((url: string) => {
      const next = Promise.resolve(
        json(queuePage({
          items: [{
            work_item: task('wi_2', 'acceptance'),
            pending_since: '2026-08-30T00:08:00Z',
            coordinator: null,
            latest_run_id: null,
            source_watermark: { as_of_event_seq: 130 },
          }],
          total_count: 120,
          next_cursor: null,
        })),
      );
      if (String(url).includes('cursor=c2')) return next;
      return Promise.resolve(json(queuePage({ next_cursor: 'c2' })));
    });
    vi.stubGlobal('fetch', fetchMock);

    await useReviewQueueStore.getState().refresh();
    await useReviewQueueStore.getState().loadMore();

    const s = useReviewQueueStore.getState();
    expect(s.items.map((i) => i.work_item.id)).toEqual(['wi_1', 'wi_2']);
    expect(s.totalCount).toBe(120);
    expect(s.nextCursor).toBeNull();
    const loadMoreUrl = String(fetchMock.mock.calls[1][0]);
    expect(loadMoreUrl).toContain('/review-queue?');
    expect(loadMoreUrl).toContain('cursor=c2');
    expect(loadMoreUrl).toContain('limit=50');
  });

  it('phase 过滤变化重置为首页并透传 phase 参数', async () => {
    const fetchMock = vi.fn().mockImplementation(() =>
      Promise.resolve(json(queuePage({ items: [], total_count: 0 }))),
    );
    vi.stubGlobal('fetch', fetchMock);

    useReviewQueueStore.getState().setPhaseFilter('acceptance');
    await vi.waitFor(() => expect(useReviewQueueStore.getState().phaseFilter).toBe('acceptance'));
    await vi.waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1));
    expect(String(fetchMock.mock.calls[0][0])).toContain('phase=acceptance');
  });

  it('phase 快速切换时，旧查询的慢响应不能覆盖新查询和 total_count', async () => {
    let resolveReview!: (response: Response) => void;
    let resolveAcceptance!: (response: Response) => void;
    const review = new Promise<Response>((resolve) => { resolveReview = resolve; });
    const acceptance = new Promise<Response>((resolve) => { resolveAcceptance = resolve; });
    vi.stubGlobal('fetch', vi.fn((url: string) =>
      String(url).includes('phase=review') ? review : acceptance,
    ));

    useReviewQueueStore.getState().setPhaseFilter('review');
    await vi.waitFor(() => expect(fetch).toHaveBeenCalledTimes(1));
    useReviewQueueStore.getState().setPhaseFilter('acceptance');
    await vi.waitFor(() => expect(fetch).toHaveBeenCalledTimes(2));

    resolveAcceptance(json(queuePage({
      items: [{
        work_item: task('wi_acceptance', 'acceptance'), pending_since: '2026-08-30T00:08:00Z',
        coordinator: null, latest_run_id: null, source_watermark: { as_of_event_seq: 130 },
      }],
      total_count: 7,
    })));
    await vi.waitFor(() => expect(useReviewQueueStore.getState().items[0]?.work_item.id).toBe('wi_acceptance'));

    resolveReview(json(queuePage({ items: [], total_count: 99 })));
    await Promise.resolve();
    await Promise.resolve();
    const state = useReviewQueueStore.getState();
    expect(state.phaseFilter).toBe('acceptance');
    expect(state.items.map((item) => item.work_item.id)).toEqual(['wi_acceptance']);
    expect(state.totalCount).toBe(7);
  });

  it('刷新失败保留旧投影与 badge，错误不伪装空队列', async () => {
    let fail = false;
    const fetchMock = vi.fn().mockImplementation(() =>
      fail ? Promise.resolve(json({ type: 'about:blank', title: '错误', status: 500, code: 'http_error', retryable: true }, 500)) : Promise.resolve(json(queuePage())),
    );
    vi.stubGlobal('fetch', fetchMock);

    await useReviewQueueStore.getState().refresh();
    fail = true;
    await useReviewQueueStore.getState().refresh();

    const s = useReviewQueueStore.getState();
    expect(s.totalCount).toBe(100);
    expect(s.items).toHaveLength(1);
    expect(s.error).toBeTruthy();
  });

  it('A→B 切换后旧 Workspace 的慢响应不回写（resolve-time guard）', async () => {
    let release!: (resp: Response) => void;
    vi.stubGlobal('fetch', vi.fn(() => new Promise<Response>((resolve) => {
      release = resolve;
    })));

    const refreshing = useReviewQueueStore.getState().refresh();
    readyWorkspace('ws_2');
    useWorkspaceStore.setState((s) => ({ generation: s.generation + 1 }));

    release(json(queuePage()));
    await refreshing;

    const s = useReviewQueueStore.getState();
    expect(s.totalCount).toBe(0);
    expect(s.loaded).toBe(false);
  });

  it('reset 清空全部投影（含 total_count）', async () => {
    vi.stubGlobal('fetch', vi.fn().mockImplementation(() => Promise.resolve(json(queuePage()))));
    await useReviewQueueStore.getState().refresh();
    useReviewQueueStore.getState().reset();
    expect(useReviewQueueStore.getState().totalCount).toBe(0);
    expect(useReviewQueueStore.getState().items).toEqual([]);
  });
});

describe('routeEvent → Review Queue 刷新条件（RFC §12.4）', () => {
  it('work_item/coordinator/run terminal/artifact/task_comment 事件触发失效重取（debounce 合并）', async () => {
    vi.useFakeTimers();
    const fetchMock = vi.fn().mockImplementation(() => Promise.resolve(json(queuePage())));
    vi.stubGlobal('fetch', fetchMock);

    routeEvent(envelope('work_item.updated', { record_kind: 'task' }));
    routeEvent(envelope('coordinator.state_changed', { record_kind: 'task' }));
    routeEvent(envelope('run.completed', { record_kind: 'task' }));
    routeEvent(envelope('artifact.created'));
    routeEvent(envelope('task_comment.created', {
      record_kind: 'task', comment_id: 'cmt_1', root_work_item_id: 'wi_root',
      work_item_id: 'wi_root', revision: 1, kind: 'note', actionable: false,
    }));

    await vi.advanceTimersByTimeAsync(SSE_REFRESH_DEBOUNCE_MS + 10);
    const queueCalls = fetchMock.mock.calls.filter((c) => String(c[0]).includes('/review-queue'));
    expect(queueCalls).toHaveLength(1); // 突发合并为一次
  });

  it('chat 记录的事件不触发队列刷新', async () => {
    vi.useFakeTimers();
    const fetchMock = vi.fn().mockImplementation(() => Promise.resolve(json(queuePage())));
    vi.stubGlobal('fetch', fetchMock);

    expect(useReviewQueueStore.getState().shouldRefreshForEvent(envelope('work_item.updated', { record_kind: 'chat' }))).toBe(false);
    routeEvent(envelope('work_item.updated', { record_kind: 'chat' }));
    await vi.advanceTimersByTimeAsync(SSE_REFRESH_DEBOUNCE_MS + 10);
    expect(fetchMock.mock.calls.filter((c) => String(c[0]).includes('/review-queue'))).toHaveLength(0);
  });
});
