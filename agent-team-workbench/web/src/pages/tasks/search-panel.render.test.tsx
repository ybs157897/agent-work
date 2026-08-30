import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it, vi } from 'vitest';
import type { SearchItem } from '../../api/types';

// renderToStaticMarkup 走 useSyncExternalStore 的 server snapshot（zustand 初始态），
// setState 注入不可见：mock store，按用例注入 phase/items/source。
const storeState = vi.hoisted(() => ({
  query: '',
  phase: 'idle' as 'idle' | 'loading' | 'done',
  items: [] as SearchItem[],
  source: null as 'remote' | 'local' | null,
  setQuery: vi.fn(),
}));
vi.mock('../../stores/task-search.store', () => ({
  SEARCH_DEBOUNCE_MS: 300,
  useTaskSearchStore: (selector: (s: typeof storeState) => unknown) => selector(storeState),
}));

import { SearchResults, SnippetText, TaskSearch, TaskSearchPanel } from './search-panel';

const item = (over: Partial<SearchItem> = {}): SearchItem => ({
  kind: 'decision',
  work_item_id: 'wi_1',
  source_id: 'dec_1',
  title: '迁移方案',
  snippet: '…按[上线]窗口执行…',
  ...over,
});

describe('SearchResults', () => {
  it('按 kind 固定顺序分组并带徽标与计数', () => {
    const html = renderToStaticMarkup(
      <SearchResults
        items={[
          item({ kind: 'artifact', source_id: 'art_1', title: '报告.md' }),
          item({ kind: 'segment_summary', source_id: 'wi_1', title: '会话段' }),
          item(),
        ]}
        onOpenTask={() => undefined}
      />,
    );
    const order = ['片段摘要', '决策', '产物'].map((label) => html.indexOf(label));
    expect(order.every((i) => i > -1)).toBe(true);
    expect(order).toEqual([...order].sort((a, b) => a - b)); // 片段摘要 → 决策 → 产物
    expect(html).toContain('报告.md');
  });

  it('snippet 的 [命中词] 渲染为强调段，方括号不外露，… 保留', () => {
    const html = renderToStaticMarkup(<SnippetText text="…按[上线]窗口执行…" />);
    expect(html).toContain('上线');
    expect(html).toContain('…按');
    expect(html).toContain('窗口执行…');
    expect(html).toContain('<mark');
    expect(html).toContain('text-brand-accent'); // 命中强调用语义 token
    expect(html).not.toContain('[上线]');
  });

  it('每行携带点击定位所需的目标任务（data-work-item-id + 可访问名）', () => {
    const html = renderToStaticMarkup(
      <SearchResults items={[item(), item({ kind: 'artifact', source_id: 'art_1', work_item_id: 'wi_9', title: '报告.md' })]} onOpenTask={() => undefined} />,
    );
    expect(html).toContain('data-work-item-id="wi_1"'); // decision 定位其 work item
    expect(html).toContain('data-work-item-id="wi_9"'); // artifact 同样定位 work item
    expect(html).toContain('aria-label="打开任务 迁移方案"');
  });

  it('空 snippet 的行（本地回退行）只渲染标题', () => {
    const html = renderToStaticMarkup(
      <SearchResults items={[item({ snippet: '', source_id: 'wi_1', kind: 'segment_summary', title: '迁移方案' })]} onOpenTask={() => undefined} />,
    );
    expect(html).toContain('迁移方案');
    expect(html).not.toContain('<mark');
  });
});

describe('TaskSearch', () => {
  it('渲染检索输入框；面板由 focus 开启，静态渲染不展开', () => {
    storeState.query = '上线';
    storeState.phase = 'done';
    const html = renderToStaticMarkup(<TaskSearch onOpenTask={() => undefined} />);
    expect(html).toContain('aria-label="检索任务台账"');
    expect(html).toContain('value="上线"');
    expect(html).not.toContain('aria-label="检索结果"');
  });
});

describe('TaskSearchPanel 三态', () => {
  it('loading 呈现检索中（role=status）', () => {
    const html = renderToStaticMarkup(
      <TaskSearchPanel phase="loading" items={[]} source={null} onOpenTask={() => undefined} />,
    );
    expect(html).toContain('role="status"');
    expect(html).toContain('检索中…');
  });

  it('无命中呈现空态（说明覆盖范围与下一步）', () => {
    const html = renderToStaticMarkup(
      <TaskSearchPanel phase="done" items={[]} source="remote" onOpenTask={() => undefined} />,
    );
    expect(html).toContain('无检索结果');
    expect(html).toContain('换个关键词试试');
  });

  it('有命中渲染分组结果；本地回退时面板标注降级来源', () => {
    const html = renderToStaticMarkup(
      <TaskSearchPanel phase="done" items={[item()]} source="local" onOpenTask={() => undefined} />,
    );
    expect(html).toContain('aria-label="检索结果"');
    expect(html).toContain('决策');
    expect(html).toContain('迁移方案');
    expect(html).toContain('检索服务暂不可用，以上为本地标题匹配');
  });
});
