/**
 * 弹窗收集审批拒绝原因（可选填）。
 * 返回 null 表示用户取消了弹窗——调用方必须中止提交，不得把 null 当空串吞掉。
 * 用 globalThis 而非 window：浏览器上等价，且便于在 node 测试环境打桩。
 */
export function promptRejectionReason(): string | null {
  return globalThis.prompt?.('拒绝原因（可选）') ?? null;
}
