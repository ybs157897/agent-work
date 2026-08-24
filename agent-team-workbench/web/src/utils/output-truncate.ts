const TRUNCATED_PREFIX = '[output truncated] ';

/** 保留尾部 maxChars 字符；超出时在开头加 Codex 风格截断标记。 */
export function tailTruncate(
  text: string,
  maxChars = 20_000,
): { text: string; truncated: boolean } {
  if (text.length <= maxChars) return { text, truncated: false };
  return { text: TRUNCATED_PREFIX + text.slice(text.length - maxChars), truncated: true };
}
