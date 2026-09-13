import { describe, expect, it } from 'vitest';
import { ApiError } from '../../api/client';
import type { DocumentDetail, DocumentSummary } from '../../api/knowledge-library';
import {
  createKnowledgeCanvasRequestFence,
  findKnowledgeCanvasVersionChange,
  knowledgeCanvasStorageKey,
  knowledgeHeadingId,
  loadKnowledgeCanvasItems,
  makeKnowledgeCanvasReference,
  normalizeKnowledgeQuote,
  reconcileKnowledgeCanvasSelection,
  restoreKnowledgeCanvasDocument,
  stripKnowledgeFrontmatter,
  type KnowledgeCanvasItemsResult,
} from './knowledge-canvas';

function document_(id: string, version = 1, patch: Partial<DocumentSummary> = {}): DocumentSummary {
  return {
    id,
    path: `docs/${id}.md`,
    kind: 'service',
    title: `文档 ${id}`,
    summary: `摘要 ${id}`,
    domains: [],
    status: 'active',
    renamed_from: '',
    version,
    updated_at: '2026-09-08T00:00:00Z',
    ...patch,
  };
}

function detail_(id: string, version = 1, releaseId = 'rel_7'): DocumentDetail {
  return {
    document: document_(id, version),
    version: { id: `${id}_v${version}`, version, content_digest: 'd', created_at: '2026-09-08T00:00:00Z', content_markdown: `# ${id}` },
    assertions: [],
    relations: [],
    versions: [{ id: `${id}_v${version}`, version, content_digest: 'd', created_at: '2026-09-08T00:00:00Z' }],
    release_id: releaseId,
  };
}

const page = (items: DocumentSummary[], nextCursor?: string): KnowledgeCanvasItemsResult => ({ items, nextCursor, truncated: false });

describe('知识画布的读取边界', () => {
  it('按发布 release 分页读取，重复 cursor 收敛成不完整结果而不是死循环', async () => {
    const requested: Array<{ q?: string; limit?: number; cursor?: string }> = [];
    const load = async (_workspaceId: string, filter: { q?: string; limit?: number; cursor?: string }) => {
      requested.push(filter);
      return { items: [document_('doc_1'), document_('doc_1')], next_cursor: 'cursor_1' };
    };
    const first = await loadKnowledgeCanvasItems('ws_1', '  退款  ', undefined, load);
    expect(first.items.map((entry) => entry.id)).toEqual(['doc_1']);
    expect(first.nextCursor).toBe('cursor_1');
    expect(first.truncated).toBe(false);
    expect(requested[0]).toEqual({ q: '退款', cursor: undefined, limit: 40 });

    // 服务端重复给出同一个 cursor：收敛成不完整结果，而不是让「继续加载」无限循环。
    const repeated = await loadKnowledgeCanvasItems('ws_1', undefined, 'cursor_1', load);
    expect(repeated.nextCursor).toBeUndefined();
    expect(repeated.truncated).toBe(true);
  });

  it('请求围栏让切换作用域后的慢响应失效', () => {
    const fence = createKnowledgeCanvasRequestFence();
    const first = fence.begin();
    const second = fence.begin();
    expect(fence.isCurrent(first)).toBe(false);
    expect(fence.isCurrent(second)).toBe(true);
  });

  it('刷新后按一次有界详情读取找回被翻页挤掉的选中文档', async () => {
    const restored = await reconcileKnowledgeCanvasSelection(
      'ws_1',
      undefined,
      'doc_9',
      page([document_('doc_1')]),
      async () => detail_('doc_9'),
    );
    expect(restored.kind).toBe('restored');
    expect(restored.result.items.map((entry) => entry.id)).toEqual(['doc_9', 'doc_1']);

    const inPage = await reconcileKnowledgeCanvasSelection('ws_1', '退款', 'doc_9', page([document_('doc_1')]), async () => detail_('doc_9'));
    expect(inPage.kind).toBe('in-page');
    expect(inPage.result.items.map((entry) => entry.id)).toEqual(['doc_1']);
  });

  it('已下线的文档给出不可用提示，网络错误保留当前上下文', async () => {
    const removedLoad = async () => ({ ...detail_('doc_9'), document: document_('doc_9', 1, { status: 'removed' }) });
    const removed = await reconcileKnowledgeCanvasSelection('ws_1', undefined, 'doc_9', page([document_('doc_1')]), removedLoad);
    expect(removed.kind).toBe('unavailable');
    expect(removed.result.items.map((entry) => entry.id)).toEqual(['doc_1']);

    const unavailable = await restoreKnowledgeCanvasDocument('ws_1', 'doc_9', undefined, removedLoad);
    expect(unavailable).toEqual({ kind: 'unavailable', message: '上次打开的文档已不在当前发布的资料库里。' });

    const missing = await restoreKnowledgeCanvasDocument('ws_1', 'doc_9', undefined, async () => { throw new ApiError({ type: 'about:blank', title: 'not found', status: 404, code: 'not_found' }); });
    expect(missing.kind).toBe('unavailable');

    const failed = await restoreKnowledgeCanvasDocument('ws_1', 'doc_9', undefined, async () => { throw new Error('boom'); });
    expect(failed.kind).toBe('error');
    expect(failed).toMatchObject({ message: expect.stringContaining('boom') });
    const reconciled = await reconcileKnowledgeCanvasSelection('ws_1', undefined, 'doc_9', page([document_('doc_1')]), async () => { throw new Error('boom'); });
    expect(reconciled.kind).toBe('error');
    expect(reconciled.result.items.map((entry) => entry.id)).toEqual(['doc_1']);
  });

  it('同一份文档在刷新后发布了新版本会被识别出来', () => {
    const previous = new Map([['doc_1', document_('doc_1', 2)]]);
    expect(findKnowledgeCanvasVersionChange(previous, [document_('doc_1', 3)], 'doc_1')?.version).toBe(3);
    expect(findKnowledgeCanvasVersionChange(previous, [document_('doc_1', 2)], 'doc_1')).toBeNull();
    expect(findKnowledgeCanvasVersionChange(previous, [document_('doc_1', 3)], null)).toBeNull();
    expect(findKnowledgeCanvasVersionChange(new Map(), [document_('doc_1', 3)], 'doc_1')).toBeNull();
  });

  it('引用固定住文档、版本与发布，并把摘录收进有界长度', () => {
    const reference = makeKnowledgeCanvasReference('ws_1', 'a_pm', {
      documentId: 'doc_1',
      version: 2,
      releaseId: 'rel_7',
      title: '退款规则',
      quote: normalizeKnowledgeQuote('  退款期限   为 14 天。  '),
      heading: '退款窗口',
    });
    expect(reference).toEqual({
      workspaceId: 'ws_1',
      agentId: 'a_pm',
      documentId: 'doc_1',
      version: 2,
      releaseId: 'rel_7',
      title: '退款规则',
      quote: '退款期限 为 14 天。',
      heading: '退款窗口',
    });
    const long = normalizeKnowledgeQuote('文'.repeat(3000));
    expect(long).toContain('摘录已截至 2400 字符');
    expect(long.length).toBeLessThan(2500);
  });

  it('画布状态按工作空间与 Agent 分键存储', () => {
    expect(knowledgeCanvasStorageKey('ws:a', 'b')).not.toBe(knowledgeCanvasStorageKey('ws', 'a:b'));
    expect(knowledgeCanvasStorageKey('ws_1', 'a_pm')).toContain('knowledge-canvas:view');
  });

  it('正文标题锚点对中文标题稳定且重复标题递增', () => {
    expect(knowledgeHeadingId('退款窗口')).toBe(knowledgeHeadingId('退款窗口'));
    expect(knowledgeHeadingId('退款窗口')).toContain('退款窗口');
    expect(knowledgeHeadingId('', 3)).toBe('knowledge-heading-4');
  });
});

describe('画布阅读区的 frontmatter 剥离', () => {
  const body = '# 退款规则\n\n退款期限为 14 天。\n';

  it('剥掉开头的 yaml 头，正文原样保留', () => {
    const markdown = `---\nschema_version: kb-note/0.2-draft\nid: doc_1\n---\n\n${body}`;
    expect(stripKnowledgeFrontmatter(markdown)).toBe(body);
  });

  it('没有 frontmatter 的正文一字不动', () => {
    expect(stripKnowledgeFrontmatter(body)).toBe(body);
    expect(stripKnowledgeFrontmatter('')).toBe('');
  });

  it('正文里的水平线不会被误剥', () => {
    const withRule = `# 退款规则\n\n---\n\n退款期限为 14 天。\n`;
    expect(stripKnowledgeFrontmatter(withRule)).toBe(withRule);
    const ruleFirst = `---\n\n正文第一段\n`;
    expect(stripKnowledgeFrontmatter(ruleFirst)).toBe(ruleFirst);
    const unterminated = `---\nschema_version: kb-note/0.2-draft\n`;
    expect(stripKnowledgeFrontmatter(unterminated)).toBe(unterminated);
  });

  it('frontmatter 之后的水平线属于正文', () => {
    const markdown = `---\nid: doc_1\n---\n\n# 标题\n\n---\n\n正文\n`;
    expect(stripKnowledgeFrontmatter(markdown)).toBe('# 标题\n\n---\n\n正文\n');
  });
});
