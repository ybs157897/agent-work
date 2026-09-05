import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { useRunsStore } from './runs.store';
import { useWorkspaceStore } from './workspace.store';

const response = (text: string) => new Response(JSON.stringify({ items: [{
  run_seq: 1, event_type: 'message.completed', agent_id: 'main',
  occurred_at: '2026-09-05T00:00:00Z', payload: { role: 'assistant', text },
}] }), { status: 200, headers: { 'Content-Type': 'application/json' } });

beforeEach(() => {
  useRunsStore.getState().reset();
  useWorkspaceStore.setState({
    workspace: { id: 'ws_1', name: '工作区', timezone: 'UTC', version: 1 },
    selectedWorkspaceId: 'ws_1', generation: 0,
  });
});
afterEach(() => {
  useRunsStore.getState().reset();
  vi.unstubAllGlobals();
});

describe('Run history failure recovery', () => {
  it('失败按 Run 隔离，就地重试恢复正文并清除错误', async () => {
    const request = vi.fn()
      .mockResolvedValueOnce(response('另一个 Agent 的结果'))
      .mockRejectedValueOnce(new TypeError('Failed to fetch'))
      .mockResolvedValueOnce(response('重试后的结果'));
    vi.stubGlobal('fetch', request);
    await useRunsStore.getState().loadHistory('run_ok');
    await useRunsStore.getState().loadHistory('run_failed');
    expect(useRunsStore.getState().historyErrors.run_failed).toContain('加载失败');
    expect(useRunsStore.getState().historyLoading.run_failed).toBe(false);
    expect(useRunsStore.getState().timelines.run_ok[0].text).toBe('另一个 Agent 的结果');
    await useRunsStore.getState().loadHistory('run_failed');
    expect(useRunsStore.getState().historyErrors.run_failed).toBeUndefined();
    expect(useRunsStore.getState().historyLoaded.run_failed).toBe(true);
    expect(useRunsStore.getState().timelines.run_failed[0].text).toBe('重试后的结果');
    expect(request).toHaveBeenCalledTimes(3);
  });

  it('重试进行中不会并发发起相同历史请求', async () => {
    let resolve!: (value: Response) => void;
    const request = vi.fn(() => new Promise<Response>((done) => { resolve = done; }));
    vi.stubGlobal('fetch', request);
    const first = useRunsStore.getState().loadHistory('run_1');
    const second = useRunsStore.getState().loadHistory('run_1');
    expect(request).toHaveBeenCalledTimes(1);
    expect(useRunsStore.getState().historyLoading.run_1).toBe(true);
    resolve(response('完成'));
    await Promise.all([first, second]);
    expect(useRunsStore.getState().historyLoading.run_1).toBe(false);
  });

  it('切换工作区清除错误，旧请求失败不写回新工作区', async () => {
    let reject!: (reason: Error) => void;
    vi.stubGlobal('fetch', vi.fn(() => new Promise<Response>((_, fail) => { reject = fail; })));
    const pending = useRunsStore.getState().loadHistory('run_1');
    useWorkspaceStore.setState({ selectedWorkspaceId: 'ws_2', generation: 1 });
    useRunsStore.getState().reset();
    reject(new Error('offline'));
    await pending;
    expect(useRunsStore.getState().historyErrors).toEqual({});
    expect(useRunsStore.getState().historyLoading).toEqual({});
  });
});
