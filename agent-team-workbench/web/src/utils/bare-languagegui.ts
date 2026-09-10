const MAX_DOCUMENT_LENGTH = 250_000;

interface MarkdownLine {
  start: number;
  end: number;
  contentEnd: number;
  text: string;
}

interface FenceState {
  marker: '`' | '~';
  length: number;
}

interface JsonScan {
  kind: 'balanced' | 'incomplete' | 'invalid' | 'over-limit' | 'budget-exhausted';
  end?: number;
  scanned: number;
}

interface Replacement {
  start: number;
  end: number;
  text: string;
}

function splitLines(markdown: string): MarkdownLine[] {
  const lines: MarkdownLine[] = [];
  let start = 0;
  while (start < markdown.length) {
    const newline = markdown.indexOf('\n', start);
    const end = newline === -1 ? markdown.length : newline;
    const textEnd = end > start && markdown[end - 1] === '\r' ? end - 1 : end;
    lines.push({ start, end, contentEnd: textEnd, text: markdown.slice(start, textEnd) });
    start = newline === -1 ? markdown.length : newline + 1;
  }
  if (markdown.length === 0 || markdown.endsWith('\n')) {
    lines.push({ start: markdown.length, end: markdown.length, contentEnd: markdown.length, text: '' });
  }
  return lines;
}

function isBlankLine(line: MarkdownLine): boolean {
  return /^[ \t]*$/.test(line.text);
}

function openingFence(line: string): FenceState | undefined {
  const match = line.match(/^ {0,3}(`{3,}|~{3,})(.*)$/);
  if (!match) return undefined;
  const marker = match[1][0] as '`' | '~';
  if (marker === '`' && match[2].includes('`')) return undefined;
  return { marker, length: match[1].length };
}

function closingFence(line: string, fence: FenceState): boolean {
  const match = line.match(/^ {0,3}([`~]+)[ \t]*$/);
  return !!match && match[1][0] === fence.marker && match[1].length >= fence.length;
}

function fencedLines(lines: MarkdownLine[]): boolean[] {
  const result: boolean[] = [];
  let fence: FenceState | undefined;
  for (const line of lines) {
    result.push(!!fence || !!openingFence(line.text));
    if (fence) {
      if (closingFence(line.text, fence)) fence = undefined;
    } else {
      fence = openingFence(line.text);
    }
  }
  return result;
}

function isJsonStringStart(value: string, index: number): boolean {
  return value[index] === '"';
}

function readJsonString(value: string, start: number): { value: string; end: number } | undefined {
  if (!isJsonStringStart(value, start)) return undefined;
  let escaped = false;
  for (let index = start + 1; index < value.length; index += 1) {
    const character = value[index];
    if (escaped) {
      escaped = false;
      continue;
    }
    if (character === '\\') {
      escaped = true;
      continue;
    }
    if (character !== '"') continue;
    const token = value.slice(start, index + 1);
    try {
      const parsed = JSON.parse(token) as unknown;
      return typeof parsed === 'string' ? { value: parsed, end: index + 1 } : undefined;
    } catch {
      return undefined;
    }
  }
  return undefined;
}

function skipWhitespace(value: string, start: number): number {
  let index = start;
  while (index < value.length && /\s/.test(value[index])) index += 1;
  return index;
}

/** Detects the v1 declaration without accepting a nested or quoted example. */
function declaresLanguageGuiV1(value: string): boolean {
  let index = skipWhitespace(value, 0);
  if (value[index] !== '{') return false;
  const stack: Array<'{' | '['> = ['{'];
  index += 1;
  while (index < value.length && stack.length) {
    const character = value[index];
    if (character === '"') {
      const token = readJsonString(value, index);
      if (!token) return false;
      const afterKey = skipWhitespace(value, token.end);
      if (stack.length === 1 && value[afterKey] === ':') {
        const afterColon = skipWhitespace(value, afterKey + 1);
        const version = readJsonString(value, afterColon);
        if (token.value === 'version' && version?.value === 'languagegui/v1') return true;
        if (version) {
          index = version.end;
          continue;
        }
      }
      index = token.end;
      continue;
    }
    if (character === '{' || character === '[') {
      stack.push(character);
      index += 1;
      continue;
    }
    if (character === '}' || character === ']') {
      const expected = character === '}' ? '{' : '[';
      if (stack.at(-1) !== expected) return false;
      stack.pop();
    }
    index += 1;
  }
  return false;
}

function findFenceStarts(lines: MarkdownLine[]): Set<number> {
  const starts = new Set<number>();
  for (const line of lines) {
    if (openingFence(line.text)) starts.add(line.start);
  }
  return starts;
}

function scanJson(markdown: string, start: number, fenceStarts: Set<number>, maxChars: number): JsonScan {
  if (markdown[start] !== '{') return { kind: 'invalid', scanned: 0 };
  const stack: Array<'{' | '['> = ['{'];
  let inString = false;
  let escaped = false;
  let scanned = 0;
  for (let index = start + 1; index < markdown.length; index += 1) {
    if (scanned >= maxChars) {
      return { kind: maxChars < MAX_DOCUMENT_LENGTH ? 'budget-exhausted' : 'over-limit', scanned };
    }
    scanned += 1;
    if (!inString && fenceStarts.has(index)) return { kind: 'incomplete', scanned };
    const character = markdown[index];
    if (inString) {
      if (escaped) {
        escaped = false;
      } else if (character === '\\') {
        escaped = true;
      } else if (character === '"') {
        inString = false;
      }
      continue;
    }
    if (character === '"') {
      inString = true;
      continue;
    }
    if (character === '{' || character === '[') {
      stack.push(character);
      continue;
    }
    if (character !== '}' && character !== ']') continue;
    const expected = character === '}' ? '{' : '[';
    if (stack.at(-1) !== expected) return { kind: 'invalid', scanned };
    stack.pop();
    if (stack.length === 0) return { kind: 'balanced', end: index + 1, scanned };
  }
  return { kind: 'incomplete', scanned };
}

function horizontalWhitespaceEnd(markdown: string, end: number): number {
  let index = end;
  while (index < markdown.length && (markdown[index] === ' ' || markdown[index] === '\t')) index += 1;
  return index;
}

function lineIndexAtOrAfter(lines: MarkdownLine[], position: number): number {
  let low = 0;
  let high = lines.length;
  while (low < high) {
    const middle = low + Math.floor((high - low) / 2);
    if (lines[middle].start < position) low = middle + 1;
    else high = middle;
  }
  return Math.max(0, low - 1);
}

function firstBlankLineStart(lines: MarkdownLine[], startLine: number, fallback: number): number {
  for (let index = startLine + 1; index < lines.length; index += 1) {
    if (isBlankLine(lines[index])) return lines[index].start;
  }
  return fallback;
}

function onlyWhitespace(markdown: string): boolean {
  return /^[\s]*$/.test(markdown);
}

function lineEnding(markdown: string, line: MarkdownLine): string {
  if (line.end < markdown.length && markdown[line.end] === '\n') {
    return line.end > line.start && markdown[line.end - 1] === '\r' ? '\r\n' : '\n';
  }
  const crlf = markdown.indexOf('\r\n');
  return crlf !== -1 ? '\r\n' : '\n';
}

function fencedDocument(kind: 'languagegui' | 'json', raw: string, newline: string, closed: boolean): string {
  const opening = `\`\`\`${kind}${newline}`;
  return closed ? `${opening}${raw}${newline}\`\`\`` : `${opening}${raw}`;
}

function isStandaloneCompleteCandidate(
  markdown: string,
  lines: MarkdownLine[],
  rootEnd: number,
): number | undefined {
  const end = horizontalWhitespaceEnd(markdown, rootEnd);
  const rootLine = lineIndexAtOrAfter(lines, rootEnd);
  const paragraphEnd = firstBlankLineStart(lines, rootLine, markdown.length);
  if (!onlyWhitespace(markdown.slice(end, paragraphEnd))) return undefined;
  return end;
}

function candidateRawEnd(lines: MarkdownLine[], startLine: number, fallback: number, fenceStarts: Set<number>, start: number): number {
  for (let index = startLine + 1; index < lines.length; index += 1) {
    if (isBlankLine(lines[index])) return lines[index - 1]?.contentEnd ?? lines[index].start;
    if (lines[index].start > start && fenceStarts.has(lines[index].start)) {
      return lines[index - 1]?.contentEnd ?? lines[index].start;
    }
  }
  return fallback;
}

function upperBound(values: number[], target: number): number {
  let low = 0;
  let high = values.length;
  while (low < high) {
    const middle = low + Math.floor((high - low) / 2);
    if (values[middle] <= target) low = middle + 1;
    else high = middle;
  }
  return low;
}

/**
 * Indexes only the explicit v1 spelling. Candidates without a hint do not
 * enter the bounded JSON scanner, keeping long ordinary output near-linear.
 */
function protocolCandidateStarts(markdown: string, lines: MarkdownLine[], inFence: boolean[]): Set<number> {
  const starts: number[] = [];
  for (let index = 0; index < lines.length; index += 1) {
    if (inFence[index] || !lines[index].text.startsWith('{')) continue;
    if (index > 0 && !isBlankLine(lines[index - 1])) continue;
    starts.push(lines[index].start);
  }
  const hinted = new Set<number>();
  const hintPattern = /"version"\s*:\s*"languagegui\/v1"/g;
  for (const match of markdown.matchAll(hintPattern)) {
    const position = match.index;
    if (position === undefined) continue;
    const ownerIndex = upperBound(starts, position) - 1;
    if (ownerIndex >= 0) hinted.add(starts[ownerIndex]);
  }
  return hinted;
}

/**
 * Wraps bare top-level LanguageGUI v1 documents in the fence understood by the
 * existing Markdown renderer. The scanner deliberately requires a line-start
 * object and never rewrites content already inside a Markdown code fence.
 */
export function normalizeBareLanguageGuiDocuments(markdown: string, streaming = false): string {
  if (!markdown) return markdown;
  const lines = splitLines(markdown);
  const inFence = fencedLines(lines);
  const fenceStarts = findFenceStarts(lines);
  const protocolStarts = protocolCandidateStarts(markdown, lines, inFence);
  const replacements: Replacement[] = [];
  let coveredUntil = -1;
  let scanBudget = markdown.length * 2 + MAX_DOCUMENT_LENGTH;

  for (let lineIndex = 0; lineIndex < lines.length; lineIndex += 1) {
    const line = lines[lineIndex];
    if (line.start < coveredUntil || inFence[lineIndex]) continue;
    if (!line.text.startsWith('{')) continue;
    if (lineIndex > 0 && !isBlankLine(lines[lineIndex - 1])) continue;
    if (!protocolStarts.has(line.start)) continue;

    const scanAllowance = Math.min(MAX_DOCUMENT_LENGTH, scanBudget);
    if (scanAllowance <= 0) break;
    const scan = scanJson(markdown, line.start, fenceStarts, scanAllowance);
    scanBudget -= scan.scanned;
    if (scan.kind === 'over-limit' || scan.kind === 'budget-exhausted') continue;
    if (scan.kind === 'balanced' && scan.end !== undefined) {
      const balancedEnd = isStandaloneCompleteCandidate(markdown, lines, scan.end);
      if (balancedEnd === undefined) continue;
      const raw = markdown.slice(line.start, balancedEnd);
      if (!raw || raw.length > MAX_DOCUMENT_LENGTH || !declaresLanguageGuiV1(raw)) continue;
      const replacement = fencedDocument('languagegui', raw, lineEnding(markdown, line), true);
      replacements.push({ start: line.start, end: balancedEnd, text: replacement });
      coveredUntil = balancedEnd;
      continue;
    }

    const candidateEnd = candidateRawEnd(lines, lineIndex, markdown.length, fenceStarts, line.start);
    const raw = markdown.slice(line.start, candidateEnd);
    if (!raw || raw.length > MAX_DOCUMENT_LENGTH || !declaresLanguageGuiV1(raw)) continue;

    const newline = lineEnding(markdown, line);
    const hasFollowingContent = /\S/.test(markdown.slice(candidateEnd));
    const closed = !streaming || scan.kind !== 'incomplete' || hasFollowingContent;
    // An explicitly declared but malformed document stays on the same
    // LanguageGUI renderer path so its source remains visible when parsing
    // cannot produce widgets. A stream tail stays open only at end of input.
    const replacement = fencedDocument('languagegui', raw, newline, closed);
    replacements.push({ start: line.start, end: candidateEnd, text: replacement });
    coveredUntil = candidateEnd;
  }

  if (!replacements.length) return markdown;
  let result = '';
  let cursor = 0;
  for (const replacement of replacements) {
    result += markdown.slice(cursor, replacement.start) + replacement.text;
    cursor = replacement.end;
  }
  return result + markdown.slice(cursor);
}
