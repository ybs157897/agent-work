/**
 * PromptBox @ 提及解析（纯函数）。服务端 @直达 只认 instruction 开头的
 * `@名字` 首词（internal/application/dispatch.go resolveAtMentionAgent），
 * 因此弹层只在 '@' 位于指令开头（允许前导空白）时触发；选中后把 @query
 * 原位替换为纯文本 `@名字 `，解析全在服务端。
 */

export interface MentionState {
  /** '@' 在 draft 中的下标（插入替换起点）。 */
  at: number;
  /** '@' 到光标之间已输入的过滤词（不含 '@'，不含空白）。 */
  query: string;
}

export interface MentionInsertion {
  text: string;
  caret: number;
}

/**
 * 光标处的提及态：'@' 须位于指令开头（服务端 TrimLeft 后 HasPrefix "@"，
 * 前导空白可容忍），且至光标之间无空白——一旦敲出空格即视为路由词已定，
 * 弹层关闭。
 */
export function activeMention(text: string, caret: number): MentionState | null {
  const match = /^\s*@([^\s]*)$/.exec(text.slice(0, Math.max(0, caret)));
  if (!match) return null;
  return { at: caret - 1 - match[1].length, query: match[1] };
}

/** 选中 agent：把 @query 原位替换为 `@名字 `，返回新文本与新光标位置。 */
export function applyMention(text: string, mention: MentionState, caret: number, name: string): MentionInsertion {
  const token = `@${name} `;
  return {
    text: text.slice(0, mention.at) + token + text.slice(caret),
    caret: mention.at + token.length,
  };
}

/** 名字含空白的 agent 无法被服务端 @直达 命中（只取首个空白词），不进弹层。 */
export function mentionableAgents<T extends { name: string }>(agents: readonly T[]): T[] {
  return agents.filter((a) => a.name.trim() !== '' && !/\s/.test(a.name.trim()));
}
