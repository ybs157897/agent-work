import { describe, expect, it, vi } from 'vitest';
import type { KnowledgeItem, KnowledgeItemDetails, KnowledgeRelation } from '../../api/knowledge';
import {
  createKnowledgeCanvasRequestFence,
  findKnowledgeCanvasVersionChange,
  knowledgeCanvasStorageKey,
  loadKnowledgeCanvasItems,
  makeKnowledgeCanvasReference,
  normalizeKnowledgeQuote,
  reconcileKnowledgeCanvasSelection,
  restoreKnowledgeCanvasItem,
} from './knowledge-canvas';
import { buildKnowledgeGraph, loadVisibleKnowledgeRelations, reconcileKnowledgeGraphNodes } from './knowledge-graph';

function item(id: string, version = 1): KnowledgeItem {
  return {
    id,
    workspace_id: 'ws_1',
    owner_agent_id: 'agent_pm',
    visibility: 'workspace',
    kind: 'requirement',
    title: `文档 ${id}`,
    summary: `摘要 ${id}`,
    current_version: version,
    status: 'effective',
    version,
    created_at: '2026-09-08T00:00:00Z',
    updated_at: '2026-09-08T00:00:00Z',
  };
}

function relation(id: string, from: string, to: string): KnowledgeRelation {
  return {
    id,
    workspace_id: 'ws_1',
    source_version_id: `${from}_v1`,
    from_item_id: from,
    to_item_id: to,
    kind: 'depends_on',
    created_at: '2026-09-08T00:00:00Z',
  };
}

it('refreshes graph content and visibility without overwriting dragged node positions', () => {
  const current = buildKnowledgeGraph([item('kb_a'), item('kb_removed')], [], null, () => undefined).nodes;
  current[0].position = { x: 128, y: 72 };
  const next = buildKnowledgeGraph([item('kb_a', 2), item('kb_new')], [], null, () => undefined).nodes;
  const reconciled = reconcileKnowledgeGraphNodes(current, next);
  expect(reconciled.map((node) => node.id)).toEqual(['kb_a', 'kb_new']);
  expect(reconciled[0].position).toEqual({ x: 128, y: 72 });
  expect(reconciled[0].data).toBe(next[0].data);
  expect(reconciled[0].data.item.current_version).toBe(2);
  expect(reconciled[1].position).toEqual(next[1].position);
});

function details(id: string, version = 2): KnowledgeItemDetails {
  const knowledgeItem = item(id, version);
  return {
    item: knowledgeItem,
    version: {
      id: `${id}_v${version}`,
      item_id: id,
      version,
      base_version: Math.max(0, version - 1),
      status: 'effective',
      kind: knowledgeItem.kind,
      title: knowledgeItem.title,
      summary: knowledgeItem.summary,
      body_markdown: `# ${knowledgeItem.title}\n\n正文`,
      tags: [],
      aliases: [],
      scope: {},
      metadata: {},
      content_digest: `${id}-digest`,
      created_by_agent_id: 'agent_pm',
      created_at: '2026-09-08T00:00:00Z',
      published_at: '2026-09-08T00:00:00Z',
    },
    sources: [],
  };
}

describe('KnowledgeCanvas data boundaries', () => {
  it('passes effective owner and requester filters through every list page', async () => {
    const load = vi.fn().mockResolvedValueOnce({ items: [item('a')], next_cursor: 'next' });

    const result = await loadKnowledgeCanvasItems('ws_1', 'agent_pm', 'agent_owner', 'roadmap', undefined, load);

    expect(result.items.map((entry) => entry.id)).toEqual(['a']);
    expect(load).toHaveBeenNthCalledWith(1, 'ws_1', expect.objectContaining({
      status: 'effective',
      owner_agent_id: 'agent_pm',
      agent_id: 'agent_owner',
      q: 'roadmap',
      limit: 40,
    }));
    expect(result.nextCursor).toBe('next');
  });

  it('fences a slow response after the workspace or agent scope switches', () => {
    const fence = createKnowledgeCanvasRequestFence();
    const oldRequest = fence.begin();
    const newRequest = fence.begin();

    expect(fence.isCurrent(oldRequest)).toBe(false);
    expect(fence.isCurrent(newRequest)).toBe(true);
  });

  it('restores a persisted second-page document with one bounded detail read', async () => {
    const list = vi.fn().mockResolvedValue({ items: [item('page-one')], next_cursor: 'page-2' });
    const firstPage = await loadKnowledgeCanvasItems('ws_1', 'agent_pm', 'agent_pm', undefined, undefined, list);
    const detailLoad = vi.fn().mockResolvedValue(details('page-two'));

    const restored = await restoreKnowledgeCanvasItem('ws_1', 'agent_pm', 'agent_pm', 'page-two', detailLoad);

    expect(firstPage.items.map((entry) => entry.id)).toEqual(['page-one']);
    expect(firstPage.nextCursor).toBe('page-2');
    expect(list).toHaveBeenCalledTimes(1);
    expect(detailLoad).toHaveBeenCalledWith('ws_1', 'page-two', 'agent_pm');
    expect(restored).toMatchObject({ kind: 'restored', details: { item: { id: 'page-two', owner_agent_id: 'agent_pm', status: 'effective' } } });
  });

  it('keeps the restored second-page selection on refresh and detects a newer version', async () => {
    const list = vi.fn()
      .mockResolvedValueOnce({ items: [item('page-one')], next_cursor: 'page-2' })
      .mockResolvedValueOnce({ items: [item('page-one')], next_cursor: 'page-2' });
    const detailLoad = vi.fn()
      .mockResolvedValueOnce(details('page-two', 2))
      .mockResolvedValueOnce(details('page-two', 3));

    const firstPage = await loadKnowledgeCanvasItems('ws_1', 'agent_pm', 'agent_pm', undefined, undefined, list);
    const restored = await reconcileKnowledgeCanvasSelection('ws_1', 'agent_pm', 'agent_pm', '', 'page-two', firstPage, detailLoad);
    const previousItems = new Map(restored.result.items.map((entry) => [entry.id, entry]));
    const refreshedPage = await loadKnowledgeCanvasItems('ws_1', 'agent_pm', 'agent_pm', undefined, undefined, list);
    const refreshed = await reconcileKnowledgeCanvasSelection('ws_1', 'agent_pm', 'agent_pm', '', 'page-two', refreshedPage, detailLoad);

    expect(restored.kind).toBe('restored');
    expect(refreshed.kind).toBe('restored');
    expect(refreshed.result.items.find((entry) => entry.id === 'page-two')?.current_version).toBe(3);
    expect(findKnowledgeCanvasVersionChange(previousItems, refreshed.result.items, 'page-two')).toMatchObject({ id: 'page-two', current_version: 3 });
    expect(list).toHaveBeenCalledTimes(2);
    expect(detailLoad).toHaveBeenCalledTimes(2);
  });

  it('keeps the exact current document version in a reference', () => {
    expect(makeKnowledgeCanvasReference('ws_1', 'agent_pm', {
      itemId: 'kb_1',
      version: 4,
      title: '退款规则',
      quote: '工作日内处理。',
      heading: '处理时限',
    })).toEqual({
      workspaceId: 'ws_1',
      agentId: 'agent_pm',
      itemId: 'kb_1',
      version: 4,
      title: '退款规则',
      quote: '工作日内处理。',
      heading: '处理时限',
    });
  });

  it('marks a long selected quote instead of silently dropping its tail', () => {
    const quote = normalizeKnowledgeQuote('甲'.repeat(2405));

    expect(quote).toContain('摘录已截至 2400 字符');
    expect(quote.length).toBeGreaterThan(2400);
  });

  it('keeps graph edges bounded to loaded readable nodes', () => {
    const graph = buildKnowledgeGraph([item('a'), item('b')], [relation('visible', 'a', 'b'), relation('hidden', 'a', 'c')], null, () => undefined);

    expect(graph.nodes).toHaveLength(2);
    expect(graph.edges.map((edge) => edge.id)).toEqual(['visible']);
  });

  it('loads graph relations with no more than four concurrent requests and reports failures', async () => {
    const entries = Array.from({ length: 7 }, (_, index) => item(String(index)));
    let active = 0;
    let maxActive = 0;
    const load = vi.fn(async (_workspaceId: string, itemId: string) => {
      active += 1;
      maxActive = Math.max(maxActive, active);
      await new Promise((resolve) => setTimeout(resolve, 2));
      active -= 1;
      if (itemId === '6') throw new Error('relation unavailable');
      return { items: [relation(`r-${itemId}`, itemId, '0'), relation(`hidden-${itemId}`, itemId, 'outside')] };
    });

    const result = await loadVisibleKnowledgeRelations('ws_1', entries, undefined, load);

    expect(maxActive).toBeLessThanOrEqual(4);
    expect(result.failures).toBe(1);
    expect(result.relations.every((entry) => entry.to_item_id !== 'outside')).toBe(true);
    expect(result.relations.every((entry) => entries.some((entryItem) => entryItem.id === entry.to_item_id))).toBe(true);
  });

  it('uses a stable workspace and agent key for view persistence', () => {
    expect(knowledgeCanvasStorageKey('ws/a', 'agent pm')).toBe('knowledge-canvas:view:ws%2Fa:agent%20pm');
  });
});
