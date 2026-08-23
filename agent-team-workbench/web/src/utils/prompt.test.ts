import { afterEach, describe, expect, it, vi } from 'vitest';
import { promptRejectionReason } from './prompt';

describe('promptRejectionReason', () => {
  afterEach(() => vi.unstubAllGlobals());

  it('用户输入文本（含空串）→ 原样返回', () => {
    vi.stubGlobal('prompt', vi.fn().mockReturnValue('太危险'));
    expect(promptRejectionReason()).toBe('太危险');
    expect(promptRejectionReason()).toBe('太危险');

    vi.stubGlobal('prompt', vi.fn().mockReturnValue(''));
    expect(promptRejectionReason()).toBe('');
  });

  it('用户取消弹窗（prompt 返回 null）→ 返回 null，调用方据此中止提交', () => {
    vi.stubGlobal('prompt', vi.fn().mockReturnValue(null));
    expect(promptRejectionReason()).toBeNull();
  });
});
