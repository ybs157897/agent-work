import { describe, expect, it, vi } from 'vitest';
import { chatErrorMessage, logChatError, REPLY_TIMEOUT_MS } from './chat-errors';

describe('chat-errors', () => {
  it('REPLY_TIMEOUT_MS 为 60 秒', () => {
    expect(REPLY_TIMEOUT_MS).toBe(60_000);
  });

  it('chatErrorMessage 覆盖已知错误码', () => {
    expect(chatErrorMessage('reply_timeout')).toContain('60');
    expect(chatErrorMessage('user_stopped')).toContain('停止');
  });

  it('logChatError 写入 console.error', () => {
    const spy = vi.spyOn(console, 'error').mockImplementation(() => {});
    logChatError('stop_failed', { runId: 'run_1' });
    expect(spy).toHaveBeenCalledWith('[chat:stop_failed]', expect.objectContaining({ code: 'stop_failed', runId: 'run_1' }));
    spy.mockRestore();
  });
});
