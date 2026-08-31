import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { CanonicalEvent } from '../api/types';
import { useCommentsStore } from './comments.store';
import { SSE_REFRESH_DEBOUNCE_MS } from './events';
import { useWorkspaceStore } from './workspace.store';

const comment = (revision: number, overrides: Partial<CanonicalEvent['data']> = {}) => ({
  id: `cmt_${revision}`,
  workspace_id: 'ws_1',
  root_work_item_id: 'wi_root',
  work_item_id: 'wi_root',
  revision,
  kind: 'note',
  body: `第 ${revision} 条评论`,
  actor_kind: 'user',
  actor_id: 'u1',
  created_at: '2026-08-30T00:00:00Z',
  ...overrides,
});

const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': status >= 400 ? 'application/problem+json' : 'application/json' },
  });

const commentCreatedEvent = (root = 'wi_root'): CanonicalEvent => ({
  contract_version: 'events/v1',
  event_id: `evt_${Math.random()}`,
  workspace_id: 'ws_1',
  stream_seq: 1,
  aggregate: { type: 'task_comment', id: 'cmt_18', version: 1 },
  type: 'task_comment.created',
  occurred_at: '2026-08-30T00:00:00Z',
  data: {
    record_kind: 'task',
    comment_id: 'cmt_18',
    root_work_item_id: root,
    work_item_id: 'wi_root',
    revision: 18,
    kind: 'requirement',
    actionable: true,
  },
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
  useCommentsStore.getState().reset();
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

describe('comments.store（任务控制面 RFC §4.9）', () => {
  it('首拉不带 after_revision，写入列表与 latest_revision 并清 loading', async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      json({ items: [comment(1), comment(2)], next_revision: null, latest_revision: 2 }),
    );
    vi.stubGlobal('fetch', fetchMock);

    await useCommentsStore.getState().refreshFor('wi_root');

    expect(fetchMock.mock.calls[0][0]).toBe('/api/v1/work-items/wi_root/comments?limit=200');
    const s = useCommentsStore.getState();
    expect(s.byRoot['wi_root']).toHaveLength(2);
    expect(s.latestRevisions['wi_root']).toBe(2);
    expect(s.loadingByRoot['wi_root']).toBeUndefined();
    expect(s.errorByRoot['wi_root']).toBeUndefined();
  });

  it('已加载过的 root 走 after_revision 增量追加，不重复拉旧评论', async () => {
    let latest = 2;
    const fetchMock = vi.fn().mockImplementation((url: string) => {
      if (String(url).includes('after_revision=2')) {
        return Promise.resolve(json({ items: [comment(18)], next_revision: null, latest_revision: 18 }));
      }
      return Promise.resolve(json({ items: [comment(1), comment(2)], next_revision: null, latest_revision: latest }));
    });
    vi.stubGlobal('fetch', fetchMock);

    await useCommentsStore.getState().refreshFor('wi_root');
    latest = 18;
    await useCommentsStore.getState().refreshFor('wi_root');

    const items = useCommentsStore.getState().byRoot['wi_root'];
    expect(items.map((c) => c.revision)).toEqual([1, 2, 18]); // append-only 追加
    expect(useCommentsStore.getState().latestRevisions['wi_root']).toBe(18);
  });

  it('task_comment.created 按 root 失效重取，窗口内合并为一次请求', async () => {
    vi.useFakeTimers();
    const fetchMock = vi.fn().mockResolvedValue(
      json({ items: [comment(18, { kind: 'requirement' })], next_revision: null, latest_revision: 18 }),
    );
    vi.stubGlobal('fetch', fetchMock);

    useCommentsStore.getState().applyEvent(commentCreatedEvent());
    useCommentsStore.getState().applyEvent(commentCreatedEvent());
    expect(fetchMock).not.toHaveBeenCalled(); // trailing-edge 防抖

    await vi.advanceTimersByTimeAsync(SSE_REFRESH_DEBOUNCE_MS + 10);
    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(String(fetchMock.mock.calls[0][0])).toBe('/api/v1/work-items/wi_root/comments?limit=200');
  });

  it('child 落点的评论事件归并到 root 投影；缺 root 的事件已消费但不发请求', async () => {
    vi.useFakeTimers();
    const fetchMock = vi.fn().mockResolvedValue(json({ items: [], next_revision: null, latest_revision: 0 }));
    vi.stubGlobal('fetch', fetchMock);

    const childEvent = commentCreatedEvent('wi_root');
    childEvent.data = { ...childEvent.data, work_item_id: 'wi_child' };
    useCommentsStore.getState().applyEvent(childEvent);
    await vi.advanceTimersByTimeAsync(SSE_REFRESH_DEBOUNCE_MS + 10);
    expect(String(fetchMock.mock.calls[0][0])).toContain('/wi_root/comments');

    const orphan = commentCreatedEvent();
    delete orphan.data?.root_work_item_id;
    expect(useCommentsStore.getState().applyEvent(orphan)).toBe(true);
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it('409 comment_coordinator_required 映射为明确文案，不伪装空态', async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      json({
        type: 'about:blank', title: '冲突', status: 409, code: 'comment_coordinator_required',
        detail: '该任务未接入 Coordinator', retryable: false,
      }, 409),
    );
    vi.stubGlobal('fetch', fetchMock);

    await useCommentsStore.getState().refreshFor('wi_root');

    const s = useCommentsStore.getState();
    expect(s.byRoot['wi_root']).toBeUndefined();
    expect(s.errorByRoot['wi_root']).toContain('未接入 Coordinator');
  });

  it('刷新失败保留已加载评论，同时给出可重试错误', async () => {
    let fail = false;
    const fetchMock = vi.fn().mockImplementation(() =>
      fail
        ? Promise.resolve(json({ type: 'about:blank', title: '错误', status: 500, code: 'http_error', retryable: true }, 500))
        : Promise.resolve(json({ items: [comment(1)], next_revision: null, latest_revision: 1 })),
    );
    vi.stubGlobal('fetch', fetchMock);

    await useCommentsStore.getState().refreshFor('wi_root');
    fail = true;
    await useCommentsStore.getState().refreshFor('wi_root');

    const s = useCommentsStore.getState();
    expect(s.byRoot['wi_root']).toHaveLength(1); // 旧内容不丢
    expect(s.errorByRoot['wi_root']).toBeTruthy();
  });

  it('A→B 切换后旧 Workspace 的慢响应不回写（resolve-time guard）', async () => {
    let release!: (resp: Response) => void;
    const gate = new Promise<Response>((resolve) => {
      release = resolve;
    });
    vi.stubGlobal('fetch', vi.fn(() => gate));

    const refreshing = useCommentsStore.getState().refreshFor('wi_root');
    readyWorkspace('ws_2');
    useWorkspaceStore.setState((s) => ({ generation: s.generation + 1 }));

    release(json({ items: [comment(1)], next_revision: null, latest_revision: 1 }));
    await refreshing;

    expect(useCommentsStore.getState().byRoot['wi_root']).toBeUndefined();
  });

  it('裸 work-item id 返回其他 Workspace 的评论时拒绝接纳', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(
      json({ items: [comment(1, { workspace_id: 'ws_2' })], next_revision: null, latest_revision: 1 }),
    ));

    await useCommentsStore.getState().refreshFor('wi_root', 'ws_1');

    expect(useCommentsStore.getState().byRoot['wi_root']).toBeUndefined();
  });

  it('create 写命令成功后增量刷新；切换后创建不回写', async () => {
    const fetchMock = vi.fn().mockImplementation((url: string) => {
      if (String(url).endsWith('/comments') && String(url).includes('wi_root')) {
        return Promise.resolve(
          json({ id: 'cmt_3', workspace_id: 'ws_1', work_item_id: 'wi_root', root_work_item_id: 'wi_root', revision: 3, kind: 'note', body: 'x', actor_kind: 'user', actor_id: 'u1', created_at: '' }, 201),
        );
      }
      return Promise.resolve(json({ items: [comment(1), comment(2), comment(3)], next_revision: null, latest_revision: 3 }));
    });
    vi.stubGlobal('fetch', fetchMock);

    const created = await useCommentsStore.getState().create('wi_root', 'wi_root', {
      kind: 'note',
      body: 'x',
      client_key: 'comment:wi_root:abc',
    });
    expect(created.id).toBe('cmt_3');
    await vi.waitFor(() => expect(useCommentsStore.getState().byRoot['wi_root']).toHaveLength(3));
  });

  it('reset 清空全部投影（切换 Workspace 时由 scope 注册表调用）', async () => {
    const fetchMock = vi.fn().mockResolvedValue(json({ items: [comment(1)], next_revision: null, latest_revision: 1 }));
    vi.stubGlobal('fetch', fetchMock);
    await useCommentsStore.getState().refreshFor('wi_root');
    expect(useCommentsStore.getState().byRoot['wi_root']).toHaveLength(1);

    useCommentsStore.getState().reset();
    expect(useCommentsStore.getState().byRoot).toEqual({});
    expect(useCommentsStore.getState().latestRevisions).toEqual({});
  });
});
