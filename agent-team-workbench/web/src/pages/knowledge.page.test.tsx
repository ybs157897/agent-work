import { renderToStaticMarkup } from 'react-dom/server';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { useWorkspaceStore } from '../stores/workspace.store';
import type { KnowledgeSource } from '../api/knowledge';
import KnowledgePage, {
  buildKnowledgeDiff,
  buildKnowledgeChatPath,
  buildKnowledgePath,
  buildKnowledgeSourceLink,
  cleanKnowledgeExcerpt,
  formatKnowledgeScope,
  isKnowledgeLibrarianAgent,
  knowledgeKindLabel,
  knowledgeListEmptyState,
  knowledgeStatusClass,
  knowledgeHeadingId,
  parseKnowledgeUrlState,
  relationLabel,
  sourceKindLabel,
  sourceVerificationLabel,
} from './knowledge.page';

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
    expect(buildKnowledgeChatPath('agent/builtin', 'kb_1', 2, '/knowledge?ws=ws_1&item=kb_1', 'ws_1')).toBe('/chat?agent=agent%2Fbuiltin&ws=ws_1&knowledge=kb_1&version=2&return_to=%2Fknowledge%3Fws%3Dws_1%26item%3Dkb_1');
    expect(buildKnowledgeChatPath('')).toBeNull();
  });

  it('把搜索、类型、条目、版本和 Workspace 保留在可刷新深链中', () => {
    const path = buildKnowledgePath({ workspaceId: 'ws_1', query: '退款窗口', kind: 'rule', itemId: 'kb_1', version: 2 });
    expect(path).toBe('/knowledge?ws=ws_1&q=%E9%80%80%E6%AC%BE%E7%AA%97%E5%8F%A3&kind=rule&item=kb_1&version=2');
    expect(parseKnowledgeUrlState(new URLSearchParams(path.slice(path.indexOf('?') + 1)))).toEqual({ query: '退款窗口', kind: 'rule', itemId: 'kb_1', version: 2, invalidVersion: null });
    expect(parseKnowledgeUrlState(new URLSearchParams('item=kb_1&version=0'))).toMatchObject({ itemId: 'kb_1', version: null, invalidVersion: '0' });
  });

  it('首批内容没有命中但仍有分页时，明确要求继续加载而不是宣称全库无结果', () => {
    expect(knowledgeListEmptyState('通知', true)).toEqual({
      title: '已加载内容暂未匹配',
      description: '还有更多知识待加载，继续加载后再判断全库是否匹配。',
    });
    expect(knowledgeListEmptyState('通知', false).title).toBe('没有匹配的知识');
    expect(knowledgeListEmptyState('', false).title).toBe('这里还没有已发布知识');
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

  it('只为已知任务、Run 和安全 HTTP 文档构造来源链接', () => {
    const makeSource = (overrides: Partial<KnowledgeSource>): KnowledgeSource => ({ id: 'kbs_1', workspace_id: 'ws_1', submitted_by_agent_id: 'agent_1', kind: 'document', ref: 'doc', created_at: '', ...overrides });
    expect(buildKnowledgeSourceLink(makeSource({ kind: 'work_item', ref: 'wi_1' }))).toMatchObject({ href: '/tasks/wi_1', external: false });
    expect(buildKnowledgeSourceLink(makeSource({ kind: 'run', ref: 'run_1' }))).toMatchObject({ href: '/runs/run_1/journal', external: false });
    expect(buildKnowledgeSourceLink(makeSource({ kind: 'document', ref: 'spec', locator: 'https://example.com/spec' }))).toMatchObject({ href: 'https://example.com/spec', external: true });
    expect(buildKnowledgeSourceLink(makeSource({ kind: 'document', ref: 'javascript:alert(1)' }))).toBeNull();
    expect(buildKnowledgeSourceLink(makeSource({ kind: 'code', ref: 'src/main.go' }))).toBeNull();
  });

  it('生成实际的历史版本行差异，并为目录标题生成稳定锚点', () => {
    expect(buildKnowledgeDiff('标题\n旧规则\n保留', '标题\n新规则\n保留')).toEqual([
      { kind: 'same', text: '标题' },
      { kind: 'removed', text: '旧规则' },
      { kind: 'added', text: '新规则' },
      { kind: 'same', text: '保留' },
    ]);
    expect(knowledgeHeadingId('退款窗口')).toBe('knowledge-heading-退款窗口');
    expect(knowledgeHeadingId('', 2)).toBe('knowledge-heading-3');
    expect(cleanKnowledgeExcerpt('…退款 **[窗口]**（见 [说明](https://example.com)）')).toBe('…退款 [窗口]（见 [说明]）');
    const longContext = cleanKnowledgeExcerpt('这是很长的正文上下文。'.repeat(8) + '[星河退款窗口]。');
    expect(longContext).toContain('[星河退款窗口]');
    expect(longContext.startsWith('…')).toBe(true);
    expect(longContext.slice(0, longContext.indexOf('[')).length).toBeLessThanOrEqual(45);
  });
});
