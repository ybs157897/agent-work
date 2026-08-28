export type StreamingMarkdownPendingKind =
  | 'code'
  | 'languagegui'
  | 'mermaid'
  | 'math'
  | 'callout';

export interface StreamingMarkdownSlice {
  visibleText: string;
  pendingText: string;
  pendingKind?: StreamingMarkdownPendingKind;
}

export interface StreamingMarkdownBlocks {
  completedBlocks: string[];
  currentBlock: string;
  unsafePending: string;
  pendingKind?: StreamingMarkdownPendingKind;
}

interface OpenFence {
  marker: '`' | '~';
  length: number;
  start: number;
  kind: StreamingMarkdownPendingKind;
}

interface PendingCandidate {
  start: number;
  kind: StreamingMarkdownPendingKind;
}

function fenceKind(info: string): StreamingMarkdownPendingKind {
  const language = info.trim().split(/[ \t]+/, 1)[0]?.toLowerCase();
  if (language === 'languagegui' || language === 'lgui') return 'languagegui';
  if (language === 'mermaid') return 'mermaid';
  return 'code';
}

function openingFence(line: string, start: number): OpenFence | undefined {
  const match = line.match(/^ {0,3}(`{3,}|~{3,})(.*)$/);
  if (!match) return undefined;
  const run = match[1];
  const info = match[2] ?? '';
  // CommonMark 不允许反引号 fence 的 info string 再包含反引号。
  if (run[0] === '`' && info.includes('`')) return undefined;
  return {
    marker: run[0] as '`' | '~',
    length: run.length,
    start,
    kind: fenceKind(info),
  };
}

function closesFence(line: string, fence: OpenFence): boolean {
  const match = line.match(/^ {0,3}([`~]+)[ \t]*$/);
  return !!match && match[1][0] === fence.marker && match[1].length >= fence.length;
}

function doubleDollarPositions(line: string, lineStart: number): number[] {
  const positions: number[] = [];
  let inlineTicks = 0;
  for (let index = 0; index < line.length;) {
    if (line[index] === '\\') {
      index += 2;
      continue;
    }
    if (line[index] === '`') {
      let end = index + 1;
      while (line[end] === '`') end += 1;
      const run = end - index;
      if (inlineTicks === 0) inlineTicks = run;
      else if (inlineTicks === run) inlineTicks = 0;
      index = end;
      continue;
    }
    if (inlineTicks === 0 && line[index] === '$' && line[index + 1] === '$') {
      positions.push(lineStart + index);
      index += 2;
      continue;
    }
    index += 1;
  }
  return positions;
}

/**
 * 流式 Markdown 只把“可以安全解析的前缀”交给 ReactMarkdown。
 * 未闭合的 fence、块级数学和 ::: 容器留在尾部缓冲；结构闭合后，原文本会
 * 整块进入同一渲染树。函数不 trim，保证 delta 累计文本与完成态逐字一致。
 */
export function splitStreamingMarkdown(text: string): StreamingMarkdownSlice {
  let cursor = 0;
  let fence: OpenFence | undefined;
  let calloutStart: number | undefined;
  let mathStart: number | undefined;

  while (cursor < text.length) {
    const newline = text.indexOf('\n', cursor);
    const lineEnd = newline === -1 ? text.length : newline;
    const contentEnd = lineEnd > cursor && text[lineEnd - 1] === '\r' ? lineEnd - 1 : lineEnd;
    const line = text.slice(cursor, contentEnd);

    if (fence) {
      if (closesFence(line, fence)) fence = undefined;
      cursor = newline === -1 ? text.length : newline + 1;
      continue;
    }

    const opened = openingFence(line, cursor);
    if (opened) {
      fence = opened;
      cursor = newline === -1 ? text.length : newline + 1;
      continue;
    }

    if (calloutStart === undefined && /^ {0,3}:::[A-Za-z][\w-]*[ \t]*$/.test(line)) {
      calloutStart = cursor;
    } else if (calloutStart !== undefined && /^ {0,3}:::[ \t]*$/.test(line)) {
      calloutStart = undefined;
    }

    for (const position of doubleDollarPositions(line, cursor)) {
      mathStart = mathStart === undefined ? position : undefined;
    }

    cursor = newline === -1 ? text.length : newline + 1;
  }

  const candidates: PendingCandidate[] = [];
  if (fence) candidates.push({ start: fence.start, kind: fence.kind });
  if (calloutStart !== undefined) candidates.push({ start: calloutStart, kind: 'callout' });
  if (mathStart !== undefined) candidates.push({ start: mathStart, kind: 'math' });
  candidates.sort((left, right) => left.start - right.start);

  const pending = candidates[0];
  if (!pending) return { visibleText: text, pendingText: '' };
  return {
    visibleText: text.slice(0, pending.start),
    pendingText: text.slice(pending.start),
    pendingKind: pending.kind,
  };
}

function canSplitAtBlankLine(before: string, after: string): boolean {
  const previous = before.split(/\r?\n/).at(-1)?.trim() ?? '';
  const next = after.split(/\r?\n/, 1)[0]?.trim() ?? '';
  // Keep list and quote continuations in one Markdown tree. Splitting these can
  // change a single list/blockquote into several unrelated nodes.
  const isListOrQuote = (line: string) => /^(?:[-+*]|\d+[.)]|>)(?:[ \t]|$)/.test(line);
  // A blank line terminates a list/blockquote when the following block is a
  // normal block; only continuation markers on both sides must stay joined.
  return !isListOrQuote(next) || !isListOrQuote(previous);
}

/**
 * Splits the safe Markdown prefix into append-only blocks. The final block is
 * intentionally kept mutable so React can stream it without remounting the
 * already-rendered blocks. The concatenation of all returned fields is the
 * original input byte-for-byte; no trimming or normalization is performed.
 */
export function splitStreamingMarkdownBlocks(text: string): StreamingMarkdownBlocks {
  const slice = splitStreamingMarkdown(text);
  const safe = slice.visibleText;
  const completedBlocks: string[] = [];
  let blockStart = 0;
  const blankLine = /(?:\r?\n[ \t]*){2,}/g;
  let match: RegExpExecArray | null;
  while ((match = blankLine.exec(safe)) !== null) {
    const boundaryEnd = match.index + match[0].length;
    if (!canSplitAtBlankLine(safe.slice(blockStart, match.index), safe.slice(boundaryEnd))) {
      continue;
    }
    completedBlocks.push(safe.slice(blockStart, boundaryEnd));
    blockStart = boundaryEnd;
  }
  return {
    completedBlocks,
    currentBlock: safe.slice(blockStart),
    unsafePending: slice.pendingText,
    pendingKind: slice.pendingKind,
  };
}
