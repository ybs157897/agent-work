import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { WorkItem } from '../api/types';
import { useTaskSearchStore } from './task-search.store';
import { useTasksStore } from './tasks.store';
import { useToastStore } from './toast.store';
import { useWorkspaceStore } from './workspace.store';

const json = (body: unknown) =>
  new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } });

const workItem = (id: string, title: string): WorkItem => ({
  id,
  workspace_id: 'ws_1',
  record_kind: 'task',
  title,
  description: '',
  status: 'in_progress',
  priority: 'medium',
  due_date: null,
  runs_count: 0,
  version: 1,
  created_at: '',
  updated_at: '',
});

describe('task-search store（S4 FTS）', () => {
  beforeEach(() => {
    vi.useFakeTimers();
    useWorkspaceStore.setState({ workspace: { id: 'ws_1', name: 'w', timezone: 'UTC', version: 1 } });
    useTasksStore.setState({ items: [workItem('wi_1', '迁移方案'), workItem('wi_2', '检索优化')] });
    useTaskSearchStore.setState({ query: '', phase: 'idle', items: [], source: null });
    useToastStore.setState({ toasts: [] });
  });

  afterEach(() => {
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  it('输入突发在防抖窗口内合并为一次请求，末次 query 胜出', async () => {
    const fetchMock = vi.fn().mockImplementation(() =>
      Promise.resolve(json({ items: [{ kind: 'decision', work_item_id: 'wi_1', source_id: 'dec_1', title: 'q', snippet: '' }] })),
    );
    vi.stubGlobal('fetch', fetchMock);

    useTaskSearchStore.getState().setQuery('检索');
    useTaskSearchStore.getState().setQuery('检索词');
    useTaskSearchStore.getState().setQuery('检索关键词');
    expect(fetchMock).not.toHaveBeenCalled(); // 窗口内不触发
    expect(useTaskSearchStore.getState().phase).toBe('loading');

    await vi.advanceTimersByTimeAsync(300 + 10);
    expect(fetchMock).toHaveBeenCalledTimes(1);
    const [url] = fetchMock.mock.calls[0] as [string];
    expect(decodeURIComponent(String(url))).toBe('/api/v1/workspaces/ws_1/search?q=检索关键词&record_kind=task');
    expect(useTaskSearchStore.getState().phase).toBe('done');
    expect(useTaskSearchStore.getState().source).toBe('remote');
    expect(useTaskSearchStore.getState().items).toHaveLength(1);
  });

  it('空/纯空白 query 立即回退全量（phase=idle 不发请求），并取消未触发的防抖', async () => {
    const fetchMock = vi.fn().mockImplementation(() => Promise.resolve(json({ items: [] })));
    vi.stubGlobal('fetch', fetchMock);

    useTaskSearchStore.getState().setQuery('检索');
    useTaskSearchStore.getState().setQuery('   ');
    await vi.advanceTimersByTimeAsync(300 + 10);
    expect(fetchMock).not.toHaveBeenCalled();
    expect(useTaskSearchStore.getState().phase).toBe('idle');

    useTaskSearchStore.getState().setQuery('x');
    useTaskSearchStore.getState().setQuery(''); // 防抖窗口内清空：取消请求
    await vi.advanceTimersByTimeAsync(300 + 10);
    expect(fetchMock).not.toHaveBeenCalled();
    expect(useTaskSearchStore.getState().phase).toBe('idle');
  });

  it('请求失败：toast 提示并回退本地标题过滤（source=local）', async () => {
    const fetchMock = vi.fn().mockRejectedValue(new TypeError('network down'));
    vi.stubGlobal('fetch', fetchMock);

    useTaskSearchStore.getState().setQuery('检索');
    await vi.advanceTimersByTimeAsync(300 + 10);

    expect(useToastStore.getState().toasts.some((t) => t.kind === 'error' && t.message.includes('回退本地标题过滤'))).toBe(true);
    const state = useTaskSearchStore.getState();
    expect(state.phase).toBe('done');
    expect(state.source).toBe('local');
    expect(state.items).toEqual([
      { kind: 'segment_summary', work_item_id: 'wi_2', source_id: 'wi_2', title: '检索优化', snippet: '' },
    ]);
  });

  it('慢返回的旧响应被序号守卫丢弃，不覆盖新结果', async () => {
    let resolveSlow: (v: Response) => void = () => undefined;
    const fetchMock = vi.fn().mockImplementation((url: string | URL) => {
      const u = String(url);
      if (u.endsWith('q=old')) {
        return new Promise<Response>((resolve) => {
          resolveSlow = resolve;
        });
      }
      return Promise.resolve(json({ items: [{ kind: 'decision', work_item_id: 'wi_1', source_id: 'dec_new', title: '新', snippet: '' }] }));
    });
    vi.stubGlobal('fetch', fetchMock);

    useTaskSearchStore.getState().setQuery('old');
    await vi.advanceTimersByTimeAsync(300 + 10); // 发出「old」请求（挂起）
    useTaskSearchStore.getState().setQuery('new');
    await vi.advanceTimersByTimeAsync(300 + 10); // 发出「new」请求并完成

    resolveSlow(json({ items: [{ kind: 'decision', work_item_id: 'wi_1', source_id: 'dec_old', title: '旧', snippet: '' }] }));
    await Promise.resolve();
    await Promise.resolve();

    expect(useTaskSearchStore.getState().items[0]?.source_id).toBe('dec_new');
    expect(useTaskSearchStore.getState().source).toBe('remote');
  });
});
