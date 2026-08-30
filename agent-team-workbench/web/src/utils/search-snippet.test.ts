import { describe, expect, it } from 'vitest';
import { parseSnippet } from './search-snippet';

describe('parseSnippet（FTS snippet 高亮解析）', () => {
  it('[命中词] 解析为 hit 段，方括号标记不外露', () => {
    expect(parseSnippet('按[上线]窗口执行')).toEqual([
      { text: '按', hit: false },
      { text: '上线', hit: true },
      { text: '窗口执行', hit: false },
    ]);
  });

  it('多命中与…截断符共存，…随普通文本保留', () => {
    expect(parseSnippet('…[决策]原话：[上线]日期…')).toEqual([
      { text: '…', hit: false },
      { text: '决策', hit: true },
      { text: '原话：', hit: false },
      { text: '上线', hit: true },
      { text: '日期…', hit: false },
    ]);
  });

  it('无命中标记时整段按普通文本返回', () => {
    expect(parseSnippet('没有任何命中')).toEqual([{ text: '没有任何命中', hit: false }]);
  });

  it('未闭合的 [ 防御性按字面渲染，不吞内容', () => {
    // 服务端标记成对（[...]）；数据异常出现单边 [ 时不当作标记解析。
    expect(parseSnippet('数组写法 arr[0 表示')).toEqual([{ text: '数组写法 arr[0 表示', hit: false }]);
  });

  it('空 snippet 返回空片段列表', () => {
    expect(parseSnippet('')).toEqual([]);
  });
});
