import { afterEach, describe, expect, it, vi } from 'vitest';
import {
  createKnowledgeInquiry,
  curateKnowledgeSubmission,
  cancelKnowledgeJob,
  getKnowledgeConfig,
  getKnowledgeItem,
  listKnowledgeItems,
  listKnowledgeRelations,
  listKnowledgeVersions,
  patchKnowledgeConfig,
  publishKnowledgeSubmission,
  repealKnowledgeItem,
  submitKnowledgeCandidate,
} from './knowledge';

const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });

describe('knowledge API endpoints', () => {
  afterEach(() => vi.unstubAllGlobals());

  it('读取和更新管理员配置沿 workspace 路由并带乐观锁', async () => {
    const config = {
      workspace_id: 'ws_1',
      librarian_agent_id: 'agent_pm',
      enabled: true,
      auto_collect: false,
      version: 2,
    };
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(json(config))
      .mockResolvedValueOnce(json({ ...config, auto_collect: true, version: 3 }));
    vi.stubGlobal('fetch', fetchMock);

    await expect(getKnowledgeConfig('ws_1')).resolves.toEqual(config);
    await expect(patchKnowledgeConfig('ws_1', {
      librarian_agent_id: 'agent_pm',
      enabled: true,
      auto_collect: true,
      expected_version: 2,
    })).resolves.toMatchObject({ version: 3, auto_collect: true });

    const [readUrl, readInit] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(readUrl).toBe('/api/v1/workspaces/ws_1/knowledge/config');
    expect(readInit.method).toBe('GET');
    const [writeUrl, writeInit] = fetchMock.mock.calls[1] as [string, RequestInit];
    expect(writeUrl).toBe('/api/v1/workspaces/ws_1/knowledge/config');
    expect(writeInit.method).toBe('PATCH');
    expect(JSON.parse(writeInit.body as string)).toEqual({
      librarian_agent_id: 'agent_pm',
      enabled: true,
      auto_collect: true,
      expected_version: 2,
    });
    expect((writeInit.headers as Record<string, string>)['Idempotency-Key']).toBeTruthy();
  });

  it('发布知识列表支持 effective、scope 和私有 Agent 视图过滤', async () => {
    const fetchMock = vi.fn().mockResolvedValue(json({ items: [], next_cursor: 'kb_10' }));
    vi.stubGlobal('fetch', fetchMock);

    await expect(listKnowledgeItems('ws_1', {
      status: 'effective',
      scope: 'project=atlas',
      agent_id: 'agent_pm',
      cursor: 'kb_20',
      limit: 200,
    })).resolves.toMatchObject({ next_cursor: 'kb_10' });

    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe('/api/v1/workspaces/ws_1/knowledge/items?status=effective&scope=project%3Datlas&agent_id=agent_pm&cursor=kb_20&limit=200');
    expect(init.method).toBe('GET');
  });

  it('详情、版本和正反向关系使用同一 workspace scope', async () => {
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(json({ item: { id: 'kb_1' }, version: { id: 'kbv_1' }, sources: [] }))
      .mockResolvedValueOnce(json({ items: [{ id: 'kbv_1', version: 1 }] }))
      .mockResolvedValueOnce(json({ items: [{ id: 'kbr_1', from_item_id: 'kb_1', to_item_id: 'kb_2' }] }));
    vi.stubGlobal('fetch', fetchMock);

    await getKnowledgeItem('ws_1', 'kb_1', 'agent_pm');
    await listKnowledgeVersions('ws_1', 'kb_1', 'agent_pm');
    await listKnowledgeRelations('ws_1', 'kb_1', 'both', 'agent_pm');

    expect((fetchMock.mock.calls[0] as [string, RequestInit])[0]).toBe('/api/v1/workspaces/ws_1/knowledge/items/kb_1?agent_id=agent_pm');
    expect((fetchMock.mock.calls[1] as [string, RequestInit])[0]).toBe('/api/v1/workspaces/ws_1/knowledge/items/kb_1/versions?agent_id=agent_pm');
    expect((fetchMock.mock.calls[2] as [string, RequestInit])[0]).toBe('/api/v1/workspaces/ws_1/knowledge/items/kb_1/relations?direction=both&agent_id=agent_pm');
  });

  it('废止条目提交当前版本、原因和幂等键', async () => {
    const fetchMock = vi.fn().mockResolvedValue(json({ id: 'kb_1', status: 'repealed', current_version: 3 }));
    vi.stubGlobal('fetch', fetchMock);

    await expect(repealKnowledgeItem('ws_1', 'kb_1', {
      expected_version: 3,
      reason: '产品规则已被新版本取代',
    }, 'agent_pm', 'repeal_1')).resolves.toMatchObject({ status: 'repealed' });

    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe('/api/v1/workspaces/ws_1/knowledge/items/kb_1/repeal?agent_id=agent_pm');
    expect(init.method).toBe('POST');
    expect(JSON.parse(init.body as string)).toEqual({
      expected_version: 3,
      reason: '产品规则已被新版本取代',
    });
    expect((init.headers as Record<string, string>)['Idempotency-Key']).toBe('repeal_1');
  });

  it('调查请求使用显式 client key 并沿知识作业路由', async () => {
    const fetchMock = vi.fn().mockResolvedValue(json({ id: 'kbj_1', status: 'queued' }));
    vi.stubGlobal('fetch', fetchMock);

    await createKnowledgeInquiry('ws_1', {
      question: '功能 A 涉及哪些功能 B？',
      context: '给开发 Agent 规划实现范围',
      scope: { project: 'atlas' },
    }, undefined, 'inq_1');

    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe('/api/v1/workspaces/ws_1/knowledge/inquiries');
    expect(init.method).toBe('POST');
    expect(JSON.parse(init.body as string)).toEqual({
      question: '功能 A 涉及哪些功能 B？',
      context: '给开发 Agent 规划实现范围',
      scope: { project: 'atlas' },
      client_key: 'inq_1',
    });
    expect((init.headers as Record<string, string>)['Idempotency-Key']).toBe('inq_1');
  });

  it('候选、整理、发布和取消都保留幂等键', async () => {
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(json({ id: 'kss_1', status: 'received' }))
      .mockResolvedValueOnce(json({ id: 'kbj_1', status: 'queued' }))
      .mockResolvedValueOnce(json({ id: 'kss_1', status: 'merged' }))
      .mockResolvedValueOnce(json({ id: 'kbj_1', status: 'cancelled' }));
    vi.stubGlobal('fetch', fetchMock);

    await submitKnowledgeCandidate('ws_1', {
      agent_id: 'agent_pm',
      client_key: 'submission_1',
      changes: [{
        base_version: 0,
        title: '登录事实',
        body: '登录需要 OAuth。',
        kind: 'fact',
        sources: [{ kind: 'document', ref: 'docs/login.md', excerpt: '登录规则原文：用户必须完成 OAuth。' }],
      }],
    });
    await curateKnowledgeSubmission('ws_1', 'kss_1', 'curate_1');
    await publishKnowledgeSubmission('ws_1', 'kss_1', 'publish_1');
    await cancelKnowledgeJob('ws_1', 'kbj_1', 'cancel_1');

    expect((fetchMock.mock.calls[0] as [string, RequestInit])[0]).toBe('/api/v1/workspaces/ws_1/knowledge/submissions');
    expect(JSON.parse((fetchMock.mock.calls[0] as [string, RequestInit])[1].body as string).changes[0]).toMatchObject({
      body: '登录需要 OAuth。',
      sources: [{ ref: 'docs/login.md', excerpt: '登录规则原文：用户必须完成 OAuth。' }],
    });
    expect((fetchMock.mock.calls[1] as [string, RequestInit])[0]).toBe('/api/v1/workspaces/ws_1/knowledge/submissions/kss_1/curate');
    expect((fetchMock.mock.calls[2] as [string, RequestInit])[0]).toBe('/api/v1/workspaces/ws_1/knowledge/submissions/kss_1/publish');
    expect((fetchMock.mock.calls[3] as [string, RequestInit])[0]).toBe('/api/v1/workspaces/ws_1/knowledge/jobs/kbj_1/cancel');
    expect((fetchMock.mock.calls[0] as [string, RequestInit])[1].method).toBe('POST');
    for (const key of ['submission_1', 'curate_1', 'publish_1', 'cancel_1']) {
      expect((fetchMock.mock.calls[['submission_1', 'curate_1', 'publish_1', 'cancel_1'].indexOf(key)] as [string, RequestInit])[1].headers).toMatchObject({ 'Idempotency-Key': key });
    }
  });
});
