/** 从 run.failure 原始 message 提取可读文案（OpenRouter/Codex 常嵌套 JSON）。 */
export function formatRunFailureMessage(code?: string, message?: string): string {
  const detail = extractFailureDetail(message);
  if (!detail) return code ? `${code}: 运行失败` : '运行失败';
  return code ? `${code}: ${detail}` : detail;
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
