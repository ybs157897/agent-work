/** 对话页错误码：先落日志与通用文案，后续按码定制提示语。 */
export type ChatErrorCode = 'reply_timeout' | 'user_stopped' | 'stop_failed';

export const REPLY_TIMEOUT_MS = 60_000;

const MESSAGES: Record<ChatErrorCode, string> = {
  reply_timeout: '等待回复超时（60 秒），已自动停止',
  user_stopped: '已停止等待',
  stop_failed: '停止失败，请稍后重试',
};

/** 结构化错误日志（console）；后续可接日志页 / 遥测。 */
export function logChatError(code: ChatErrorCode, context: Record<string, unknown>): void {
  console.error(`[chat:${code}]`, { code, ...context, at: new Date().toISOString() });
}

export function chatErrorMessage(code: ChatErrorCode): string {
  return MESSAGES[code];
}
