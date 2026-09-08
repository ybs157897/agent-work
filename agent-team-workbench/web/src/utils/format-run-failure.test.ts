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

  it('只将明确的上游 response stream 404 转成模型接口提示', () => {
    const nested = JSON.stringify({ error: { responseStreamDisconnected: { httpStatusCode: 404 } } });
    const message = 'provider_error: 模型服务没有找到请求的接口（404）。请检查模型的接口地址和协议是否匹配。';
    expect(formatRunFailureMessage('provider_error', nested)).toBe(message);
    expect(formatRunFailureMessage('provider_error', 'unexpected status 404 Not Found')).toBe(message);
  });

  it('普通文件 404 不被泛化为模型接口错误', () => {
    const raw = 'GET /artifacts/report.json returned 404 Not Found';
    expect(formatRunFailureMessage('file_not_found', raw)).toBe('file_not_found: GET /artifacts/report.json returned 404 Not Found');
  });
});
