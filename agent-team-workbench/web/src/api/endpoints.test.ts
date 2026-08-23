import { afterEach, describe, expect, it, vi } from 'vitest';
import type { Plan } from './types';
import { createPlan, getPlan, getWorkItemTree, listWorkItems } from './endpoints';

const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });

const plan: Plan = {
  id: 'plan_1',
  workspace_id: 'ws_1',
  work_item_id: 'wi_1',
  agent_profile_id: 'agent_lead',
  source_run_id: null,
  status: 'finished',
  superseded_by: null,
  steps: [
    {
      seq: 1,
      verb: 'dispatch',
      status: 'executed',
      payload: { agent_id: 'agent_2', title: '子任务', instruction: '去做' },
      result_work_item_id: 'wi_2',
      result_run_id: 'run_1',
    },
    { seq: 2, verb: 'finish', status: 'executed', payload: { summary: '完成' } },
  ],
  version: 3,
  created_at: '2026-08-23T10:00:00Z',
  updated_at: '2026-08-23T10:01:00Z',
};

describe('plan / tree endpoints', () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('createPlan: POST /workspaces/{ws}/plans，body 携带完整批次', async () => {
    const fetchMock = vi.fn().mockResolvedValue(json(plan, 201));
    vi.stubGlobal('fetch', fetchMock);

    const got = await createPlan('ws_1', {
      work_item_id: 'wi_1',
      agent_profile_id: 'agent_lead',
      source_run_id: 'run_0',
      steps: [
        { verb: 'dispatch', agent_id: 'agent_2', title: '子任务', instruction: '去做', acceptance: ['ok'] },
        { verb: 'defer', reason: '等子任务', wake_at: '2026-08-24T00:00:00Z' },
        { verb: 'finish', summary: '完成' },
      ],
    });

    expect(got.id).toBe('plan_1');
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe('/api/v1/workspaces/ws_1/plans');
    expect(init.method).toBe('POST');
    expect(init.headers).toMatchObject({ 'Content-Type': 'application/json' });
    expect((init.headers as Record<string, string>)['Idempotency-Key']).toBeTruthy();
    expect(JSON.parse(init.body as string)).toEqual({
      work_item_id: 'wi_1',
      agent_profile_id: 'agent_lead',
      source_run_id: 'run_0',
      steps: [
        { verb: 'dispatch', agent_id: 'agent_2', title: '子任务', instruction: '去做', acceptance: ['ok'] },
        { verb: 'defer', reason: '等子任务', wake_at: '2026-08-24T00:00:00Z' },
        { verb: 'finish', summary: '完成' },
      ],
    });
  });

  it('getPlan: GET /plans/{id}', async () => {
    const fetchMock = vi.fn().mockResolvedValue(json(plan));
    vi.stubGlobal('fetch', fetchMock);

    await getPlan('plan_1');
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe('/api/v1/plans/plan_1');
    expect(init.method).toBe('GET');
    expect(init.body).toBeUndefined();
    expect((init.headers as Record<string, string>)['Idempotency-Key']).toBeUndefined(); // 读命令不带幂等键
  });

  it('getWorkItemTree: GET /work-items/{id}/tree', async () => {
    const fetchMock = vi.fn().mockResolvedValue(json({ items: [] }));
    vi.stubGlobal('fetch', fetchMock);

    await getWorkItemTree('wi_1');
    const [url] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe('/api/v1/work-items/wi_1/tree');
  });

  it('listWorkItems: parent_id=none 只查根任务，任务 id 精确匹配', async () => {
    const fetchMock = vi.fn().mockImplementation(() => Promise.resolve(json({ items: [], next_cursor: null })));
    vi.stubGlobal('fetch', fetchMock);

    await listWorkItems('ws_1', { parent_id: 'none' });
    await listWorkItems('ws_1', { parent_id: 'wi_parent' });
    const urls = fetchMock.mock.calls.map((c) => String(c[0]));
    expect(urls[0]).toBe('/api/v1/workspaces/ws_1/work-items?parent_id=none');
    expect(urls[1]).toBe('/api/v1/workspaces/ws_1/work-items?parent_id=wi_parent');
  });
});
