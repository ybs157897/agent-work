/**
 * FTS snippet 高亮解析（纯函数，会话元模型 S4）：
 * 服务端把命中词用 `[...]` 包裹、截断用 `…`；渲染前解析成片段，
 * 命中段做视觉强调，方括号标记本身不外露。
 */

export interface SnippetPart {
  text: string;
  hit: boolean;
}

/** 解析 snippet；未闭合的 `[` 按字面字符渲染（防御服务端/数据异常）。 */
export function parseSnippet(snippet: string): SnippetPart[] {
  const parts: SnippetPart[] = [];
  let rest = snippet;
  while (rest) {
    const start = rest.indexOf('[');
    if (start === -1) {
      parts.push({ text: rest, hit: false });
      break;
    }
    const end = rest.indexOf(']', start + 1);
    if (end === -1) {
      // 未闭合的 `[`：整体按普通文本渲染。
      parts.push({ text: rest, hit: false });
      break;
    }
    if (start > 0) parts.push({ text: rest.slice(0, start), hit: false });
    parts.push({ text: rest.slice(start + 1, end), hit: true });
    rest = rest.slice(end + 1);
  }
  return parts.filter((p) => p.text !== '');
}
