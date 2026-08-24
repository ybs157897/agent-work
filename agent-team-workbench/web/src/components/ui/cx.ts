/** 条件 className 拼接：过滤假值后按传入顺序连接（不排序、不去重）。 */
export function cx(...parts: Array<string | false | null | undefined>): string {
  return parts.filter(Boolean).join(' ');
}
