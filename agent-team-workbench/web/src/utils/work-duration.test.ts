import { describe, expect, it } from 'vitest';
import { formatWorkDurationFromRange, formatWorkDurationZh } from './work-duration';

const at = (ms: number) => new Date(ms).toISOString();

describe('formatWorkDurationZh', () => {
  it('formats sub-second, seconds, and minutes', () => {
    expect(formatWorkDurationZh(at(0), at(450))).toBe('450 毫秒');
    expect(formatWorkDurationZh(at(0), at(2_000))).toBe('2 秒');
    expect(formatWorkDurationZh(at(0), at(1_500))).toBe('1.5 秒');
    expect(formatWorkDurationZh(at(0), at(125_000))).toBe('2 分 5 秒');
    expect(formatWorkDurationZh(at(0), at(120_000))).toBe('2 分');
  });

  it('returns null for invalid input', () => {
    expect(formatWorkDurationZh()).toBeNull();
    expect(formatWorkDurationZh(at(1_000), at(0))).toBeNull();
  });
});

describe('formatWorkDurationFromRange', () => {
  it('uses earliest start and latest end', () => {
    expect(formatWorkDurationFromRange([at(1_000), at(2_000)], [at(5_000), at(4_000)])).toBe('4 秒');
  });
});
