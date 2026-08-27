import { describe, expect, it } from 'vitest';
import { currencyFlag, parseClockTime, parseSegments } from './languagegui-segments';

describe('parseSegments（LanguageGUI 部件协议增量解析）', () => {
  it('纯文本 → 单 text 段', () => {
    expect(parseSegments('你好，世界')).toEqual([{ kind: 'text', text: '你好，世界' }]);
  });

  it('文本 + 闭合 widget 块 → 按序切段', () => {
    const full = '先看结论：\n```lgui\n{"widget":"rating","question":"有帮助吗"}\n```\n以上。';
    const segs = parseSegments(full);
    expect(segs).toHaveLength(3);
    expect(segs[0]).toEqual({ kind: 'text', text: '先看结论：' });
    expect(segs[1]).toEqual({ kind: 'widget', data: { widget: 'rating', question: '有帮助吗' } });
    expect(segs[2]).toEqual({ kind: 'text', text: '以上。' });
  });

  it('未闭合围栏 → pending 占位，不吞前文', () => {
    const segs = parseSegments('结论在此：\n```lgui\n{"widget":"chart","values":[1,2');
    expect(segs[0]).toEqual({ kind: 'text', text: '结论在此：' });
    expect(segs[1]).toEqual({ kind: 'pending' });
    expect(segs).toHaveLength(2);
  });

  it('围栏标记未凑齐时按普通文本呈现（凑齐后全量重解析自动升级）', () => {
    expect(parseSegments('好\n```lg')).toEqual([{ kind: 'text', text: '好\n```lg' }]);
  });

  it('闭合但非法 JSON → broken 原样保留（防吞内容回归）', () => {
    const segs = parseSegments('```lgui\n{widget: oops}\n```');
    expect(segs[0]).toEqual({ kind: 'broken', raw: '{widget: oops}' });
  });

  it('缺 widget 字段的合法 JSON 也降级 broken', () => {
    const segs = parseSegments('```lgui\n{"foo":1}\n```');
    expect(segs[0]).toEqual({ kind: 'broken', raw: '{"foo":1}' });
  });

  it('多块连排 + 字符串内含 ``` 与花括号不误判', () => {
    const inner = { widget: 'table', title: 'a{b}c', columns: ['x'], rows: [['```lgui 不算闭合']] };
    const full = '```lgui\n' + JSON.stringify(inner) + '\n```\n```lgui\n{"widget":"rating","question":"q"}\n```';
    const segs = parseSegments(full);
    expect(segs).toHaveLength(2);
    expect(segs[0]).toEqual({ kind: 'widget', data: inner });
    expect((segs[1] as { data: { widget: string } }).data.widget).toBe('rating');
  });
});

describe('parseClockTime', () => {
  it('12 小时制带 AM/PM', () => {
    expect(parseClockTime('2:40 PM')).toEqual({ hour: 14, minute: 40 });
    expect(parseClockTime('11:44 PM')).toEqual({ hour: 23, minute: 44 });
    expect(parseClockTime('7:40 AM')).toEqual({ hour: 7, minute: 40 });
    expect(parseClockTime('12:05 AM')).toEqual({ hour: 0, minute: 5 });
    expect(parseClockTime('12:30 PM')).toEqual({ hour: 12, minute: 30 });
  });

  it('非法输入回退 12:00', () => {
    expect(parseClockTime('明天见')).toEqual({ hour: 12, minute: 0 });
  });
});

describe('currencyFlag', () => {
  it('已知币种出旗帜，未知回退地球', () => {
    expect(currencyFlag('usd')).toBe('🇺🇸');
    expect(currencyFlag('XYZ')).toBe('🌐');
  });
});
