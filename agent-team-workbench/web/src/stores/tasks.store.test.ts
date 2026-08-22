import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { WorkItem } from '../api/types';
import { useTasksStore } from './tasks.store';

const item: WorkItem = {
  id: 'wi_1',
  workspace_id: 'ws_1',
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

const problem = (status: number, code: string) =>
  new Response(
    JSON.stringify({ type: `https://workbench.example/problems/${code}`, title: code, status, code }),
    { status, headers: { 'Content-Type': 'application/problem+json' } },
  );

describe('tasks.store moveOptimistic', () => {
  beforeEach(() => {
    useTasksStore.setState({ items: [item], filter: {}, viewMode: 'kanban', selectedTaskId: null });
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('成功：乐观更新后以后端响应为准', async () => {
    const updated = { ...item, status: 'in_progress', version: 4 };
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        new Response(JSON.stringify(updated), {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        }),
      ),
    );

    // 乐观更新立即生效
    const p = useTasksStore.getState().moveOptimistic('wi_1', 'in_progress');
    expect(useTasksStore.getState().items[0].status).toBe('in_progress');
    await p;
    expect(useTasksStore.getState().items[0].version).toBe(4);

    const [, init] = (fetch as unknown as ReturnType<typeof vi.fn>).mock.calls[0] as [string, RequestInit];
    expect(init.method).toBe('POST');
    expect(JSON.parse(init.body as string)).toEqual({ status: 'in_progress', expected_version: 3 });
  });

  it('409 版本冲突：回滚乐观更新并重取列表', async () => {
    const refreshed = { ...item, status: 'in_progress', version: 5 };
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(problem(409, 'version_conflict'))
      .mockResolvedValueOnce(
        new Response(JSON.stringify({ items: [refreshed], next_cursor: null }), {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        }),
      );
    vi.stubGlobal('fetch', fetchMock);
    // refresh 需要 workspace id
    const { useWorkspaceStore } = await import('./workspace.store');
    useWorkspaceStore.setState({
      workspace: { id: 'ws_1', name: 'w', timezone: 'UTC', version: 1 },
    });

    await expect(useTasksStore.getState().moveOptimistic('wi_1', 'in_progress')).rejects.toThrow();
    const items = useTasksStore.getState().items;
    expect(items[0].status).toBe('in_progress'); // 来自 refresh 的权威状态
    expect(items[0].version).toBe(5);
  });

  it('其他错误：回滚原状态', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(problem(422, 'illegal_transition')));
    await expect(useTasksStore.getState().moveOptimistic('wi_1', 'completed')).rejects.toThrow();
    expect(useTasksStore.getState().items[0].status).toBe('todo');
  });
});
