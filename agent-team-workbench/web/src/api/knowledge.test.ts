import { afterEach, describe, expect, it, vi } from 'vitest';
import { getKnowledgeConfig, getKnowledgeItem, getKnowledgeVersion, listKnowledgeItems, listKnowledgeRelations, listKnowledgeVersions } from './knowledge';

const json = (body: unknown) => new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } });

describe('只读知识库 API', () => {
  afterEach(() => vi.unstubAllGlobals());

  it('读取系统管理员配置用于展示，不提供人工选择或写入请求', async () => {
    const config = { workspace_id: 'ws_1', librarian_agent_id: 'agent_knowledge_librarian_ws_1', enabled: true, version: 2 };
    const fetch = vi.fn().mockResolvedValue(json(config));
    vi.stubGlobal('fetch', fetch);
    await expect(getKnowledgeConfig('ws_1')).resolves.toEqual(config);
    expect(fetch.mock.calls[0][0]).toBe('/api/v1/workspaces/ws_1/knowledge/config');
    expect(fetch.mock.calls[0][1].method).toBe('GET');
  });

  it('浏览已发布知识时保留分类、范围和分页条件', async () => {
    const fetch = vi.fn().mockResolvedValue(json({ items: [], next_cursor: 'kb_10' }));
    vi.stubGlobal('fetch', fetch);
    await expect(listKnowledgeItems('ws_1', { status: 'effective', kind: 'requirement', scope: 'project=atlas', agent_id: 'agent_pm', cursor: 'kb_20', limit: 200 })).resolves.toMatchObject({ next_cursor: 'kb_10' });
    expect(fetch.mock.calls[0][0]).toBe('/api/v1/workspaces/ws_1/knowledge/items?status=effective&scope=project%3Datlas&kind=requirement&agent_id=agent_pm&cursor=kb_20&limit=200');
    expect(fetch.mock.calls[0][1].method).toBe('GET');
  });

  it('把正文搜索交给服务端，并与分类及分页条件一起编码', async () => {
    const fetch = vi.fn().mockResolvedValue(json({ items: [], next_cursor: null }));
    vi.stubGlobal('fetch', fetch);
    await listKnowledgeItems('ws_1', { q: '星河退款窗口', status: 'effective', kind: 'rule', limit: 200 });
    expect(fetch.mock.calls[0][0]).toBe('/api/v1/workspaces/ws_1/knowledge/items?q=%E6%98%9F%E6%B2%B3%E9%80%80%E6%AC%BE%E7%AA%97%E5%8F%A3&status=effective&kind=rule&limit=200');
  });

  it('归属筛选独立于读取身份，搜索和分页保留同一 owner', async () => {
    const fetch = vi.fn().mockImplementation(() => Promise.resolve(json({ items: [] })));
    vi.stubGlobal('fetch', fetch);
    await listKnowledgeItems('ws_1', { q: '退款', owner_agent_id: 'agent/pm', agent_id: 'agent_reader', cursor: 'kb_3', limit: 40 });
    const url = new URL(fetch.mock.calls[0][0], 'https://workbench.test');
    expect(url.searchParams.get('owner_agent_id')).toBe('agent/pm');
    expect(url.searchParams.get('agent_id')).toBe('agent_reader');
    expect(url.searchParams.get('q')).toBe('退款');
    expect(url.searchParams.get('cursor')).toBe('kb_3');
    await listKnowledgeItems('ws_1', { owner_agent_id: 'agent/pm' });
    expect(fetch.mock.calls[1][0]).toBe('/api/v1/workspaces/ws_1/knowledge/items?owner_agent_id=agent%2Fpm');
  });

  it('正文、历史版本及双向关系始终读取同一工作区和条目', async () => {
    const fetch = vi.fn().mockImplementation(() => Promise.resolve(json({ items: [] })));
    vi.stubGlobal('fetch', fetch);
    await getKnowledgeItem('ws_1', 'kb_1', 'agent_pm');
    await listKnowledgeVersions('ws_1', 'kb_1', 'agent_pm');
    await getKnowledgeVersion('ws_1', 'kb_1', 2, 'agent_pm');
    await listKnowledgeRelations('ws_1', 'kb_1', 'both', 'agent_pm');
    expect(fetch.mock.calls.map((call) => call[0])).toEqual([
      '/api/v1/workspaces/ws_1/knowledge/items/kb_1?agent_id=agent_pm',
      '/api/v1/workspaces/ws_1/knowledge/items/kb_1/versions?agent_id=agent_pm',
      '/api/v1/workspaces/ws_1/knowledge/items/kb_1/versions/2?agent_id=agent_pm',
      '/api/v1/workspaces/ws_1/knowledge/items/kb_1/relations?direction=both&agent_id=agent_pm',
    ]);
    expect(fetch.mock.calls.every((call) => call[1].method === 'GET')).toBe(true);
  });
});
