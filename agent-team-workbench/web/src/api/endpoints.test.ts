import { afterEach, describe, expect, it, vi } from 'vitest';
import type { Plan, WorkItem } from './types';
import {
  acceptWorkItem,
  createPlan,
  getPlan,
  getRunChangeDiff,
  getWorkItemPlan,
  getWorkItemTree,
  listRunChanges,
  listWorkItems,
  returnWorkItem,
  revertRunChanges,
} from './endpoints';

const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });

const workItem: WorkItem = {
  id: 'wi_1',
  workspace_id: 'ws_1',
  title: '主任务',
  description: '',
  status: 'in_progress',
  phase: 'acceptance',
  priority: 'medium',
  due_date: null,
  runs_count: 2,
  version: 8,
  created_at: '2026-08-23T10:00:00Z',
  updated_at: '2026-08-23T10:01:00Z',
};

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

  it('getWorkItemPlan: GET /work-items/{id}/plan（主任务最新一份）', async () => {
    const fetchMock = vi.fn().mockResolvedValue(json(plan));
    vi.stubGlobal('fetch', fetchMock);

    const got = await getWorkItemPlan('wi_1');
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe('/api/v1/work-items/wi_1/plan');
    expect(init.method).toBe('GET');
    expect(got.id).toBe('plan_1');
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

describe('work item accept / return commands', () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('acceptWorkItem: POST commands/accept，body 只带 expected_version', async () => {
    const fetchMock = vi.fn().mockResolvedValue(json(workItem));
    vi.stubGlobal('fetch', fetchMock);

    const got = await acceptWorkItem('wi_1', 8);
    expect(got.id).toBe('wi_1');
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe('/api/v1/work-items/wi_1/commands/accept');
    expect(init.method).toBe('POST');
    expect(JSON.parse(init.body as string)).toEqual({ expected_version: 8 });
  });

  it('returnWorkItem: POST commands/return，理由与乐观锁版本随 body 提交', async () => {
    const fetchMock = vi.fn().mockResolvedValue(json(workItem));
    vi.stubGlobal('fetch', fetchMock);

    const got = await returnWorkItem('wi_1', '验收标准第 2 条未达成', 8);
    expect(got.id).toBe('wi_1');
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe('/api/v1/work-items/wi_1/commands/return');
    expect(init.method).toBe('POST');
    expect(JSON.parse(init.body as string)).toEqual({ reason: '验收标准第 2 条未达成', expected_version: 8 });
  });

  it('returnWorkItem: 理由缺省时 body 不携带 reason 字段', async () => {
    const fetchMock = vi.fn().mockResolvedValue(json(workItem));
    vi.stubGlobal('fetch', fetchMock);

    await returnWorkItem('wi_1', undefined, 8);
    const [, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(JSON.parse(init.body as string)).toEqual({ expected_version: 8 });
  });
});

describe('run file changes endpoints', () => {
  afterEach(() => { vi.unstubAllGlobals(); });

  it('reads the run-scoped summary and safely encodes a diff path', async () => {
    const fetchMock = vi.fn().mockImplementation(() => Promise.resolve(json({
      file_count: 0, additions: 0, deletions: 0, files: [], state: 'ready', can_revert: false, version: 1,
    })));
    vi.stubGlobal('fetch', fetchMock);

    await listRunChanges('run_1');
    await getRunChangeDiff('run_1', 'src/a file.ts');

    expect(fetchMock.mock.calls[0]?.[0]).toBe('/api/v1/runs/run_1/changes');
    expect(fetchMock.mock.calls[1]?.[0]).toBe('/api/v1/runs/run_1/changes/diff?path=src%2Fa+file.ts');
  });

  it('revert sends the same idempotency key in the header and body', async () => {
    const fetchMock = vi.fn().mockResolvedValue(json({ file_count: 1, additions: 0, deletions: 0, files: [], state: 'reverted', can_revert: false, version: 2 }));
    vi.stubGlobal('fetch', fetchMock);

    await revertRunChanges('run_1', 'revert-key');
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe('/api/v1/runs/run_1/commands/revert-changes');
    expect(init.method).toBe('POST');
    expect((init.headers as Record<string, string>)['Idempotency-Key']).toBe('revert-key');
    expect(JSON.parse(init.body as string)).toEqual({ idempotency_key: 'revert-key' });
  });
});
