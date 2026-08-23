/**
 * 请求序号守卫：每次发请求前领取单调递增票号，响应返回时只有最新票号
 * 允许写入 store。快速切换会话/筛选时，慢返回的旧响应（票号已过期）
 * 直接丢弃，避免覆盖新数据。
 */
export function createRequestGuard() {
  let seq = 0;
  return {
    /** 发起请求前领取票号；返回的 isStale() 在已有更新请求发出后为 true。 */
    begin(): () => boolean {
      const ticket = ++seq;
      return () => ticket !== seq;
    },
  };
}
