import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { useCoordinatorStore } from './coordinator.store';
import { useWorkspaceStore } from './workspace.store';

const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), {
  status,
  headers: { 'Content-Type': status >= 400 ? 'application/problem+json' : 'application/json' },
});

beforeEach(() => {
  useWorkspaceStore.setState({
    workspace: { id: 'ws_1', name: '甲', timezone: 'UTC', version: 1 },
    selectedWorkspaceId: 'ws_1',
    generation: 1,
  });
  useCoordinatorStore.getState().reset();
});

afterEach(() => vi.unstubAllGlobals());

describe('Coordinator identity resolution', () => {
  it('首个 Coordinator 请求尚未完成时是 loading，成功后标为 coordinated', async () => {
    let release!: (response: Response) => void;
    const pending = new Promise<Response>((resolve) => { release = resolve; });
    vi.stubGlobal('fetch', vi.fn((url: string) =>
      String(url).endsWith('/events') ? Promise.resolve(json({ items: [] })) : pending,
    ));

    const refreshing = useCoordinatorStore.getState().refreshFor('wi_child');
    expect(useCoordinatorStore.getState().resolutionByWorkItem.wi_child).toBe('loading');

    release(json({
      root_work_item_id: 'wi_root', work_item_id: 'wi_child', status: 'running',
      attempts: [], timeline: [], updated_at: '', version: 1,
    }));
    await refreshing;

    expect(useCoordinatorStore.getState().resolutionByWorkItem.wi_child).toBe('coordinated');
    expect(useCoordinatorStore.getState().byWorkItem.wi_child?.root_work_item_id).toBe('wi_root');
  });

  it('Coordinator snapshot 404 是 legacy 事实，不展示成连接错误', async () => {
    vi.stubGlobal('fetch', vi.fn(() => Promise.resolve(json({
      type: 'about:blank', title: 'Not found', status: 404, code: 'not_found', detail: 'no coordinator',
    }, 404))));

    await useCoordinatorStore.getState().refreshFor('wi_legacy_child');

    const state = useCoordinatorStore.getState();
    expect(state.resolutionByWorkItem.wi_legacy_child).toBe('legacy');
    expect(state.errorByWorkItem.wi_legacy_child).toBeUndefined();
  });
});
