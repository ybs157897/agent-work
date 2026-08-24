/** 微型 clsx：过滤掉 false/null/undefined，其余以空格拼接成一个 class 字符串。 */
export function cx(...parts: Array<string | false | null | undefined>): string {
  let out = '';
  for (const part of parts) {
    if (!part) continue;
    out = out === '' ? part : `${out} ${part}`;
  }
  return out;
}
