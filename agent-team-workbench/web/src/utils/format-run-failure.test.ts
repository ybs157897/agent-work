import { describe, expect, it } from 'vitest';
import { formatRunFailureMessage } from './format-run-failure';

describe('formatRunFailureMessage', () => {
  it('解析嵌套 OpenRouter/Codex JSON 错误', () => {
    const raw =
      '{"error":{"message":"{\\"error\\":{\\"message\\":\\"Reasoning is mandatory for this endpoint and cannot be disabled.\\",\\"code\\":400}}"}}';
    expect(formatRunFailureMessage('codex_error', raw)).toBe(
      'codex_error: Reasoning is mandatory for this endpoint and cannot be disabled.',
    );
  });

  it('无 message 时回退 code', () => {
    expect(formatRunFailureMessage('timeout', undefined)).toBe('timeout: 运行失败');
  });
});
