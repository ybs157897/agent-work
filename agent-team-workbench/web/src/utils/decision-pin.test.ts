import { describe, expect, it } from 'vitest';
import { canPin, decisionPayload } from './decision-pin';

describe('decisionPayload', () => {
  it('quote 原文 trim，source_run_id 透传（引文保真，不做其他改写）', () => {
    expect(decisionPayload('  以周会结论为准  ', 'run_1')).toEqual({
      quote: '以周会结论为准',
      source_run_id: 'run_1',
    });
  });

  it('无来源轮次时省略 source_run_id 字段（契约可选，钉为无来源）', () => {
    expect(decisionPayload('以终稿为准')).toEqual({ quote: '以终稿为准' });
    expect(Object.hasOwn(decisionPayload('以终稿为准', ''), 'source_run_id')).toBe(false);
  });

  it('多行原文保留换行（服务端只 trim 不改写）', () => {
    expect(decisionPayload('第一行\n第二行')).toEqual({ quote: '第一行\n第二行' });
  });
});

describe('canPin', () => {
  const quote = '有效原文';

  it('idle + 有目标任务 + 非空原文才允许发起', () => {
    expect(canPin('idle', quote, 'wi_1')).toBe(true);
  });

  it('在途（pinning）与已钉过（pinned）防重复点击', () => {
    expect(canPin('pinning', quote, 'wi_1')).toBe(false);
    expect(canPin('pinned', quote, 'wi_1')).toBe(false);
  });

  it('无目标任务（未在会话中）或空原文不发起', () => {
    expect(canPin('idle', quote, undefined)).toBe(false);
    expect(canPin('idle', quote, '')).toBe(false);
    expect(canPin('idle', '   ', 'wi_1')).toBe(false);
  });
});
