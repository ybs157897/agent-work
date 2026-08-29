import { describe, expect, it } from 'vitest';
import { activeMention, applyMention, mentionableAgents } from './mention';

describe('activeMention', () => {
  it('@ 位于开头触发，query 为至光标的已输入词', () => {
    expect(activeMention('@', 1)).toEqual({ at: 0, query: '' });
    expect(activeMention('@小', 2)).toEqual({ at: 0, query: '小' });
    expect(activeMention('@小明', 3)).toEqual({ at: 0, query: '小明' });
  });

  it('前导空白容忍（服务端 TrimLeft 后 HasPrefix 同样命中）', () => {
    expect(activeMention('  @小', 4)).toEqual({ at: 2, query: '小' });
  });

  it('@ 不在开头（有正文前缀）不触发：服务端不会把中段 @ 解析为直达', () => {
    expect(activeMention('你好 @小', 5)).toBeNull();
  });

  it('query 中出现空白即关闭：路由词已随首个空白定形', () => {
    expect(activeMention('@小明 ', 4)).toBeNull();
  });

  it('光标在 @ 之前不触发', () => {
    expect(activeMention('@小明', 0)).toBeNull();
  });
});

describe('applyMention', () => {
  it('把 @query 原位替换为 @名字+空格，光标落在插入词尾', () => {
    const mention = activeMention('@小', 2);
    expect(applyMention('@小', mention!, 2, '小明')).toEqual({ text: '@小明 ', caret: 4 });
  });

  it('保留前导空白与光标后内容', () => {
    const draft = '  @he后缀';
    const mention = activeMention(draft, 5);
    expect(mention).toEqual({ at: 2, query: 'he' });
    expect(applyMention(draft, mention!, 5, 'Han')).toEqual({ text: '  @Han 后缀', caret: 7 });
  });
});

describe('mentionableAgents', () => {
  it('过滤名字含空白的 agent（服务端 @直达 只取首个空白词，无法命中）', () => {
    expect(
      mentionableAgents([{ name: '小明' }, { name: 'Two Words' }, { name: '   ' }]),
    ).toEqual([{ name: '小明' }]);
  });
});
