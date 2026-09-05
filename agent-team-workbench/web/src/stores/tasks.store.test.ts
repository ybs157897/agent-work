import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { WorkItem } from '../api/types';
import { useTasksStore } from './tasks.store';
import { useWorkspaceStore } from './workspace.store';

const item: WorkItem = {
  id: 'wi_1',
  workspace_id: 'ws_1',
  record_kind: 'task',
  title: '任务',
  description: '',
  status: 'todo',
  priority: 'medium',
  due_date: null,
  runs_count: 0,
  version: 3,
  created_at: '',
  updated_at: '',
};

describe('tasks.store record isolation', () => {
  beforeEach(() => {
    useWorkspaceStore.setState({
      workspace: { id: 'ws_1', name: '甲', timezone: 'UTC', version: 1 },
      selectedWorkspaceId: 'ws_1',
      generation: 0,
    });
    useTasksStore.setState({ items: [], loaded: false, loading: false, error: null, filter: {}, viewMode: 'kanban' });
  });

  afterEach(() => vi.unstubAllGlobals());

  it('hydrate 只保留 task 记录，Chat 不进入任务看板', () => {
    const chat = { ...item, id: 'chat_1', record_kind: 'chat' as const, title: '独立对话' };
    useTasksStore.getState().hydrate([item, chat]);
    expect(useTasksStore.getState().items.map((entry) => entry.id)).toEqual(['wi_1']);
  });

  it('upsert 拒绝 Chat 记录，详情/事件越界也不会写入看板', () => {
    useTasksStore.setState({ items: [], loaded: false });
    const chat = { ...item, id: 'chat_1', record_kind: 'chat' as const, title: '独立对话' };
    useTasksStore.getState().upsert(chat);
    expect(useTasksStore.getState().items).toEqual([]);
  });

  it('upsert 拒绝其他 Workspace 的 Task，裸资源响应不能污染当前看板', () => {
    useWorkspaceStore.setState({
      workspace: { id: 'ws_1', name: '甲', timezone: 'UTC', version: 1 },
      selectedWorkspaceId: 'ws_1',
      generation: 3,
    });
    useTasksStore.setState({ items: [], loaded: false });

    useTasksStore.getState().upsert({ ...item, workspace_id: 'ws_2', id: 'wi_from_ws_2' });

    expect(useTasksStore.getState().items).toEqual([]);
  });

  it('refresh 请求显式带 record_kind=task 且响应再次 fail-closed', async () => {
    const chat = { ...item, id: 'chat_1', record_kind: 'chat' as const, title: '独立对话' };
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({ items: [item, chat], next_cursor: null }), {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    }));
    vi.stubGlobal('fetch', fetchMock);
    useWorkspaceStore.setState({ workspace: { id: 'ws_1', name: 'w', timezone: 'UTC', version: 1 } });

    await useTasksStore.getState().refresh();

    expect(fetchMock).toHaveBeenCalledWith('/api/v1/workspaces/ws_1/work-items?record_kind=task', expect.any(Object));
    expect(useTasksStore.getState().items.map((entry) => entry.id)).toEqual(['wi_1']);
  });

  it('refresh 消费全部分页，且每页都只请求 task 记录', async () => {
    const child = { ...item, id: 'wi_child', parent_id: item.id, title: '子任务' };
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({ items: [item], next_cursor: 'cursor-1' }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ items: [child], next_cursor: null }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      }));
    vi.stubGlobal('fetch', fetchMock);

    await useTasksStore.getState().refresh();

    expect(fetchMock).toHaveBeenNthCalledWith(1, '/api/v1/workspaces/ws_1/work-items?record_kind=task', expect.any(Object));
    expect(fetchMock).toHaveBeenNthCalledWith(2, '/api/v1/workspaces/ws_1/work-items?record_kind=task&cursor=cursor-1', expect.any(Object));
    expect(useTasksStore.getState().items.map((entry) => entry.id)).toEqual(['wi_1', 'wi_child']);
    expect(useTasksStore.getState().error).toBeNull();
  });

  it('筛选只更新本地投影条件，不再次截断或请求完整任务集合', () => {
    useTasksStore.getState().setFilter({ priority: 'high' });

    expect(useTasksStore.getState().filter).toEqual({ priority: 'high' });
  });

  it('完整集合拉取失败时保留 bootstrap 数据并暴露 error，避免假空', async () => {
    const fetchMock = vi.fn().mockRejectedValue(new Error('网络暂不可用'));
    vi.stubGlobal('fetch', fetchMock);
    useTasksStore.getState().hydrate([item]);

    await useTasksStore.getState().refresh();

    expect(useTasksStore.getState().items).toEqual([item]);
    expect(useTasksStore.getState().loaded).toBe(true);
    expect(useTasksStore.getState().loading).toBe(false);
    expect(useTasksStore.getState().error).toBe('网络暂不可用');
  });
});
