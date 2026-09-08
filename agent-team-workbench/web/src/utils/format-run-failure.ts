/** 从 run.failure 原始 message 提取可读文案（OpenRouter/Codex 常嵌套 JSON）。 */
export function formatRunFailureMessage(code?: string, message?: string): string {
  const detail = isUpstreamInterface404(message)
    ? '模型服务没有找到请求的接口（404）。请检查模型的接口地址和协议是否匹配。'
    : extractFailureDetail(message);
  if (!detail) return code ? `${code}: 运行失败` : '运行失败';
  return code ? `${code}: ${detail}` : detail;
}

function isUpstreamInterface404(message?: string): boolean {
  if (!message?.trim()) return false;
  if (/unexpected\s+status\s+404\s+Not\s+Found/i.test(message)) return true;
  if (/responseStreamDisconnected[\s\S]{0,240}(?:httpStatusCode|http_status_code)\s*[=:]\s*["']?404\b/i.test(message)) return true;
  const parsed = tryParseJSON(message);
  return parsed ? containsResponseStream404(parsed) : false;
}

function containsResponseStream404(value: unknown, streamDisconnected = false): boolean {
  if (!value || typeof value !== 'object') return false;
  const record = value as Record<string, unknown>;
  const streamContext = streamDisconnected
    || record.type === 'responseStreamDisconnected'
    || record.name === 'responseStreamDisconnected';
  if (streamContext && (record.httpStatusCode === 404 || record.http_status_code === 404)) return true;
  return Object.entries(record).some(([key, child]) =>
    containsResponseStream404(child, streamContext || key === 'responseStreamDisconnected'),
  );
}

function extractFailureDetail(message?: string): string | undefined {
  if (!message?.trim()) return undefined;
  const parsed = tryParseJSON(message);
  if (!parsed) return truncate(message);
  const deepest = deepestMessage(parsed);
  if (deepest) return deepest;
  return truncate(message);
}

function deepestMessage(value: unknown): string | undefined {
  if (!value || typeof value !== 'object') return undefined;
  const rec = value as Record<string, unknown>;
  const err = rec.error;
  if (err && typeof err === 'object') {
    const errRec = err as Record<string, unknown>;
    if (typeof errRec.message === 'string') {
      const nested = tryParseJSON(errRec.message);
      if (nested) {
        const inner = deepestMessage(nested);
        if (inner) return inner;
      }
      if (errRec.message.trim()) return errRec.message.trim();
    }
  }
  if (typeof rec.message === 'string' && rec.message.trim()) return rec.message.trim();
  return undefined;
}

function tryParseJSON(text: string): Record<string, unknown> | null {
  try {
    const value = JSON.parse(text) as unknown;
    return value && typeof value === 'object' ? (value as Record<string, unknown>) : null;
  } catch {
    return null;
  }
}

function truncate(text: string, max = 240): string {
  const trimmed = text.trim();
  return trimmed.length > max ? `${trimmed.slice(0, max)}…` : trimmed;
}
