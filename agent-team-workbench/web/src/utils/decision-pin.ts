/**
 * 「钉为决策」（会话元模型 S2）的载荷组装与防重判定（纯函数）：
 * 把 chat 用户消息原文作 quote 钉进任务台账，解析/校验都在服务端。
 */

export type PinState = 'idle' | 'pinning' | 'pinned';

/**
 * POST /work-items/{id}/decisions 载荷：quote 原文 trim（服务端同样 trim，
 * 引文保真不转述）；source_run_id 缺省时省略（契约可选，钉为无来源）。
 */
export function decisionPayload(
  quote: string,
  sourceRunId?: string,
): { quote: string; source_run_id?: string } {
  const trimmed = quote.trim();
  return sourceRunId ? { quote: trimmed, source_run_id: sourceRunId } : { quote: trimmed };
}

/** 防重判定：请求在途/已钉过、无目标任务、空原文时不得发起。 */
export function canPin(state: PinState, quote: string, workItemId?: string): boolean {
  return state === 'idle' && !!workItemId && quote.trim() !== '';
}
