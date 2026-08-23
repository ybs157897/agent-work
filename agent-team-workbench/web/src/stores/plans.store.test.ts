import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { CanonicalEvent, Plan } from '../api/types';
import { usePlansStore } from './plans.store';

const planDTO = (id: string, workItemId: string, status: Plan['status']): Plan => ({
  id,
  workspace_id: 'ws_1',
  work_item_id: workItemId,
  agent_profile_id: 'agent_lead',
  source_run_id: null,
  status,
  superseded_by: null,
  steps: [{ seq: 1, verb: 'finish', status: 'executed', payload: { summary: '完成' } }],
  version: 2,
  created_at: '2026-08-23T10:00:00Z',
  updated_at: '2026-08-23T10:01:00Z',
});

const planEvent = (type: string, planId: string, data?: Record<string, unknown>): CanonicalEvent => ({
  contract_version: 'events/v1',
  event_id: `evt_${type}_${planId}`,
  workspace_id: 'ws_1',
  stream_seq: 1,
  aggregate: { type: 'plan', id: planId, version: 1 },
  type,
  occurred_at: '2026-08-23T10:00:00Z',
  data,
});

const json = (body: unknown) =>
  new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } });

const resetStore = () => usePlansStore.setState({ byWorkItem: {}, workItemOf: {} });

describe('plans.store applyEvent', () => {
  beforeEach(resetStore);
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('plan.submitted：拉取 PlanDTO，按 work_item 建缓存并学习索引', async () => {
    const fetchMock = vi.fn().mockResolvedValue(json(planDTO('plan_1', 'wi_1', 'active')));
    vi.stubGlobal('fetch', fetchMock);

    const consumed = usePlansStore.getState().applyEvent(
      planEvent('plan.submitted', 'plan_1', { work_item_id: 'wi_1' }),
    );
    expect(consumed).toBe(true);
    await vi.waitFor(() => {
      expect(usePlansStore.getState().byWorkItem['wi_1']?.id).toBe('plan_1');
    });

    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe('/api/v1/plans/plan_1');
    expect(init.method).toBe('GET');
    expect(usePlansStore.getState().workItemOf['plan_1']).toBe('wi_1');
  });

  it('同主任务新 plan 提交：按 work item 覆盖旧 plan（supersede 投影）', async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(json(planDTO('plan_1', 'wi_1', 'waiting')))
      .mockResolvedValueOnce(json(planDTO('plan_2', 'wi_1', 'active')));
    vi.stubGlobal('fetch', fetchMock);

    usePlansStore.getState().applyEvent(planEvent('plan.submitted', 'plan_1', { work_item_id: 'wi_1' }));
    await vi.waitFor(() => {
      expect(usePlansStore.getState().byWorkItem['wi_1']?.id).toBe('plan_1');
    });
    usePlansStore.getState().applyEvent(planEvent('plan.submitted', 'plan_2', { work_item_id: 'wi_1' }));
    await vi.waitFor(() => {
      expect(usePlansStore.getState().byWorkItem['wi_1']?.id).toBe('plan_2');
    });
    expect(usePlansStore.getState().byWorkItem['wi_1']?.status).toBe('active');
  });

  it('信封只带 plan 聚合 id：仍可拉取（PlanDTO 自带 work_item_id 自定位）', async () => {
    const fetchMock = vi.fn().mockResolvedValue(json(planDTO('plan_1', 'wi_1', 'active')));
    vi.stubGlobal('fetch', fetchMock);

    usePlansStore.getState().applyEvent(planEvent('plan.step_executed', 'plan_1'));
    await vi.waitFor(() => {
      expect(usePlansStore.getState().byWorkItem['wi_1']?.id).toBe('plan_1');
    });
  });

  it('无任何 plan id 可定位：消费但不拉取', () => {
    const fetchMock = vi.fn();
    vi.stubGlobal('fetch', fetchMock);

    const ev = planEvent('plan.finished', 'irrelevant');
    ev.aggregate = { type: 'work_item', id: 'wi_1', version: 1 };
    expect(usePlansStore.getState().applyEvent(ev)).toBe(true);
    expect(fetchMock).not.toHaveBeenCalled();
    expect(usePlansStore.getState().byWorkItem).toEqual({});
  });

  it('索引已建：后续同 plan 事件无需再带 work_item_id', async () => {
    const fetchMock = vi.fn().mockResolvedValue(json(planDTO('plan_1', 'wi_1', 'active')));
    vi.stubGlobal('fetch', fetchMock);

    usePlansStore.getState().applyEvent(planEvent('plan.submitted', 'plan_1', { work_item_id: 'wi_1' }));
    await vi.waitFor(() => {
      expect(fetchMock).toHaveBeenCalledTimes(1);
    });
    usePlansStore.getState().applyEvent(planEvent('plan.step_executed', 'plan_1'));
    await vi.waitFor(() => {
      expect(fetchMock).toHaveBeenCalledTimes(2);
    });
  });

  it('非 plan 域事件：不消费', () => {
    const other = { ...planEvent('work_item.updated', 'plan_1'), aggregate: { type: 'work_item', id: 'wi_1', version: 1 } };
    expect(usePlansStore.getState().applyEvent(other)).toBe(false);
  });

  it('在途合并：事件突发只保留一次在途请求 + 一次补拉，末次状态落缓存', async () => {
    let releaseFirst: ((r: Response) => void) | undefined;
    const responses = [
      new Promise<Response>((res) => {
        releaseFirst = res;
      }),
      Promise.resolve(json(planDTO('plan_1', 'wi_1', 'finished'))),
    ];
    const fetchMock = vi.fn().mockImplementation(() => responses.shift() ?? Promise.resolve(json(planDTO('plan_1', 'wi_1', 'active'))));
    vi.stubGlobal('fetch', fetchMock);

    usePlansStore.getState().applyEvent(planEvent('plan.submitted', 'plan_1', { work_item_id: 'wi_1' }));
    usePlansStore.getState().applyEvent(planEvent('plan.step_executed', 'plan_1'));
    usePlansStore.getState().applyEvent(planEvent('plan.finished', 'plan_1'));
    expect(fetchMock).toHaveBeenCalledTimes(1); // 突发窗口内只发一次在途请求

    releaseFirst?.(json(planDTO('plan_1', 'wi_1', 'active')));
    await vi.waitFor(() => {
      expect(fetchMock).toHaveBeenCalledTimes(2); // 脏标记触发一次补拉
    });
    await vi.waitFor(() => {
      expect(usePlansStore.getState().byWorkItem['wi_1']?.status).toBe('finished');
    });
  });

  it('GET 失败不落缓存也不抛出：缓存保持旧投影，等待下一个事件重试', async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(json(planDTO('plan_1', 'wi_1', 'active')))
      .mockResolvedValueOnce(
        new Response(JSON.stringify({ title: 'boom', status: 500 }), {
          status: 500,
          headers: { 'Content-Type': 'application/problem+json' },
        }),
      );
    vi.stubGlobal('fetch', fetchMock);

    usePlansStore.getState().applyEvent(planEvent('plan.submitted', 'plan_1', { work_item_id: 'wi_1' }));
    await vi.waitFor(() => {
      expect(usePlansStore.getState().byWorkItem['wi_1']?.status).toBe('active');
    });

    await expect(usePlansStore.getState().refreshFor('wi_1')).resolves.toBeUndefined();
    expect(usePlansStore.getState().byWorkItem['wi_1']?.status).toBe('active'); // 旧投影未被破坏
  });
});

describe('plans.store refreshFor', () => {
  beforeEach(resetStore);
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('缓存已建：按缓存 plan id 重拉最新快照', async () => {
    usePlansStore.setState({
      byWorkItem: { wi_1: planDTO('plan_1', 'wi_1', 'waiting') },
      workItemOf: { plan_1: 'wi_1' },
    });
    const fetchMock = vi.fn().mockResolvedValue(json(planDTO('plan_1', 'wi_1', 'finished')));
    vi.stubGlobal('fetch', fetchMock);

    await usePlansStore.getState().refreshFor('wi_1');
    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(usePlansStore.getState().byWorkItem['wi_1']?.status).toBe('finished');
  });

  it('冷启动无索引：空操作，不发请求', async () => {
    const fetchMock = vi.fn();
    vi.stubGlobal('fetch', fetchMock);

    await usePlansStore.getState().refreshFor('wi_unknown');
    expect(fetchMock).not.toHaveBeenCalled();
  });
});
