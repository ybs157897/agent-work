import { describe, expect, it } from 'vitest';
import { tailTruncate } from './output-truncate';

describe('tailTruncate', () => {
  it('短于上限原样返回', () => {
    expect(tailTruncate('hello', 10)).toEqual({ text: 'hello', truncated: false });
  });

  it('超出上限保留尾部并加前缀', () => {
    const long = 'a'.repeat(100);
    const { text, truncated } = tailTruncate(long, 20);
    expect(truncated).toBe(true);
    expect(text.startsWith('[output truncated] ')).toBe(true);
    expect(text.endsWith('a'.repeat(20))).toBe(true);
  });
});
