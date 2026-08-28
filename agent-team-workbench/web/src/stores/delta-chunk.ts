/**
 * message.delta 的 raw.chunk 线形解析（协议契约：data.raw.chunk.{type,text}）。
 * 叶子模块：chat.store（正文/推理缓冲）与 runs.store（截断折叠）共用，
 * 任何一方不得各自另写解析以致漂移。
 */
export function extractDeltaChunk(data?: Record<string, unknown>): { type?: string; text?: string } | null {
  const raw = data?.raw;
  if (!raw || typeof raw !== 'object') return null;
  const chunk = (raw as Record<string, unknown>).chunk;
  if (!chunk || typeof chunk !== 'object') return null;
  const c = chunk as Record<string, unknown>;
  return {
    type: typeof c.type === 'string' ? c.type : undefined,
    text: typeof c.text === 'string' ? c.text : undefined,
  };
}
