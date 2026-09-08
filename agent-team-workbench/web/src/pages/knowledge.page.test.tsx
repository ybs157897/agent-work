import { renderToStaticMarkup } from 'react-dom/server';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { useWorkspaceStore } from '../stores/workspace.store';
import type { KnowledgeItem, KnowledgeSource } from '../api/knowledge';
import KnowledgePage, {
  buildKnowledgeChatPath,
  filterKnowledgeItems,
  formatKnowledgeScope,
  isKnowledgeLibrarianAgent,
  knowledgeKindLabel,
  knowledgeListEmptyState,
  knowledgeStatusClass,
  relationLabel,
  sortKnowledgeItems,
  sourceKindLabel,
  sourceVerificationLabel,
} from './knowledge.page';

const item = (overrides: Partial<KnowledgeItem>): KnowledgeItem => ({
  id: 'kb_1',
  workspace_id: 'ws_1',
  visibility: 'workspace',
  kind: 'function',
  title: '默认知识',
  current_version: 1,
  status: 'effective',
  version: 1,
  created_at: '2026-09-07T00:00:00Z',
  updated_at: '2026-09-07T00:00:00Z',
  ...overrides,
});

describe('KnowledgePage presentation contract', () => {
  beforeEach(() => {
    useWorkspaceStore.setState({
      phase: 'ready',
      error: null,
      notice: null,
      me: null,
      workspace: null,
      workspaces: [],
      selectedWorkspaceId: null,
      generation: 0,
      switching: false,
      health: null,
      eventCursor: 0,
      sseStatus: 'online',
    });
  });

  afterEach(() => {
    useWorkspaceStore.setState({ workspace: null, selectedWorkspaceId: null });
  });

  it('没有工作区时给出等待态，不把空数据伪装成知识结果', () => {
    const html = renderToStaticMarkup(
      <MemoryRouter>
        <KnowledgePage />
      </MemoryRouter>,
    );
    expect(html).toContain('知识库');
    expect(html).toContain('等待工作区');
    expect(html).not.toContain('交资料');
    expect(html).not.toContain('发起知识调查');
  });

  it('知识浏览提供语义标签、只读搜索和真实管理员聊天路径', () => {
    expect(knowledgeKindLabel('requirement')).toBe('产品需求');
    expect(knowledgeKindLabel('agreement')).toBe('团队约定');
    expect(knowledgeKindLabel('function')).toBe('功能说明');
    expect(knowledgeKindLabel('rule')).toBe('业务规则');
    expect(knowledgeKindLabel('unknown')).toBe('其他知识');
    expect(relationLabel('depends_on')).toBe('依赖');
    expect(sourceKindLabel('artifact')).toBe('产物');
    expect(knowledgeStatusClass('effective')).toContain('text-status-success');
    expect(isKnowledgeLibrarianAgent({ kind: 'knowledge_librarian' })).toBe(true);
    expect(isKnowledgeLibrarianAgent({ kind: 'user' })).toBe(false);
    expect(buildKnowledgeChatPath('agent/builtin')).toBe('/chat?agent=agent%2Fbuiltin');
    expect(buildKnowledgeChatPath('')).toBeNull();
  });

  it('首批内容没有命中但仍有分页时，明确要求继续加载而不是宣称全库无结果', () => {
    expect(knowledgeListEmptyState('通知', true)).toEqual({
      title: '已加载内容暂未匹配',
      description: '还有更多知识待加载，继续加载后再判断全库是否匹配。',
    });
    expect(knowledgeListEmptyState('通知', false).title).toBe('没有匹配的知识');
    expect(knowledgeListEmptyState('', false).title).toBe('这里还没有已发布知识');
  });

  it('搜索标题、摘要、标签和别名，并按最近更新时间排序', () => {
    const items = [
      item({ id: 'kb_old', title: '旧规则', kind: 'rule', updated_at: '2026-09-06T00:00:00Z', tags: ['通知'] }),
      item({ id: 'kb_new', title: '新功能', summary: '支持重试策略', updated_at: '2026-09-07T00:00:00Z', aliases: ['retry'] }),
    ];
    expect(filterKnowledgeItems(items, '重试').map((entry) => entry.id)).toEqual(['kb_new']);
    expect(filterKnowledgeItems(items, 'retry').map((entry) => entry.id)).toEqual(['kb_new']);
    expect(filterKnowledgeItems(items, '通知').map((entry) => entry.id)).toEqual(['kb_old']);
    expect(sortKnowledgeItems(items).map((entry) => entry.id)).toEqual(['kb_new', 'kb_old']);
  });

  it('格式化适用范围，并区分来源核验状态', () => {
    expect(formatKnowledgeScope({ project: 'atlas', branch: 'main' })).toBe('project=atlas · branch=main');
    expect(formatKnowledgeScope(undefined)).toBe('未限定适用范围');
    const source = (overrides: Partial<KnowledgeSource>): KnowledgeSource => ({
      id: 'kbs_1',
      workspace_id: 'ws_1',
      submitted_by_agent_id: 'agent_1',
      kind: 'run',
      ref: 'run_1',
      created_at: '',
      metadata: { verification: 'run_output_verified', digest_verified: true },
      ...overrides,
    });
    expect(sourceVerificationLabel(source({}))).toBe('已核验运行记录');
    expect(sourceVerificationLabel(source({ kind: 'document', metadata: { verification: 'submitted_excerpt' } }))).toBe('已登记摘录');
    expect(sourceVerificationLabel(source({ id: '', metadata: { verification: 'run_output_verified', digest_verified: true } }))).toBe('待核验');
  });
});
