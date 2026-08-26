// 工具行展示模型：从 ChatMessage（时间线推导的工具行）派生类别族、一行摘要、
// 展开体 IN（args pretty JSON）/OUT（输出文本）与行状态。纯函数，ToolRow 渲染层
// 直接消费；参考 DSH ui-tool 的 tool-call-model，输入换成我们的消息形态。

import type { ChatMessage } from '../../stores/chat.store';
import { tailTruncate } from '../../utils/output-truncate';
import { toolActivityTitle } from '../../utils/tool-activity-copy';

export type ToolFamily = 'bash' | 'read' | 'write' | 'edit' | 'search' | 'code' | 'others';

export type ToolRowState = 'running' | 'ok' | 'error' | 'stopped';

export const FAMILY_TITLES: Record<ToolFamily, string> = {
  bash: 'Bash',
  read: 'Read',
  write: 'Write',
  edit: 'Edit',
  search: 'Search',
  code: 'Code',
  others: 'Tool call',
};

/** ToolRow 需要的一切，从一条消息一次派生。 */
export interface ToolRowModel {
  family: ToolFamily;
  title: string;
  /** 一行摘要：argsSummary 首行，否则 text 剥前缀后的首行。 */
  summary: string;
  /** read/write/edit 的文件路径（args 的 path/file_path → argsSummary 路径启发式）。 */
  filePath?: string | undefined;
  /** 展开体 IN：args pretty JSON（解析失败原文）；无 args 为 null。 */
  body: string | null;
  /** 展开体 OUT：detail 优先，运行中只有 liveOutput；均无为 null。 */
  output: string | null;
  /** error 态下 output 的首行；其余状态 null。 */
  errorSummary: string | null;
  state: ToolRowState;
  exitCode?: number | undefined;
  running: boolean;
}

/** 精确工具名表（小写化后命中）。 */
const EXACT_FAMILIES: Record<string, ToolFamily> = {
  bash: 'bash',
  shell: 'bash',
  pwsh: 'bash',
  zsh: 'bash',
  terminal: 'bash',
  read: 'read',
  cat: 'read',
  view: 'read',
  web_fetch: 'read',
  write: 'write',
  create_file: 'write',
  edit: 'edit',
  patch: 'edit',
  apply_patch: 'edit',
  grep: 'search',
  glob: 'search',
  search: 'search',
  web_search: 'search',
  find: 'search',
  run_code: 'code',
  python: 'code',
  execute_code: 'code',
};

/** 未命中精确表的正则兜底，按声明顺序取首个命中。 */
const FALLBACK_PATTERNS: readonly (readonly [RegExp, ToolFamily])[] = [
  [/(shell|bash|zsh|exec|terminal|command)/, 'bash'],
  [/(^|[^a-z])(read|view|cat)([^a-z]|$)/, 'read'],
  [/(write|create)/, 'write'],
  [/(edit|patch|save)/, 'edit'],
  [/(search|grep|glob|find|query)/, 'search'],
  [/(run_code|^code$|python)/, 'code'],
];

/** 工具名归类：大小写不敏感，精确表优先，未命中走正则兜底，其余 others。 */
export function classifyTool(toolName: string | undefined): ToolFamily {
  const name = toolName?.toLowerCase() ?? '';
  const exact = EXACT_FAMILIES[name];
  if (exact !== undefined) return exact;
  for (const [pattern, family] of FALLBACK_PATTERNS) {
    if (pattern.test(name)) return family;
  }
  return 'others';
}

export function firstLine(text: string): string {
  const nl = text.indexOf('\n');
  return nl === -1 ? text : text.slice(0, nl);
}

/** 无分隔符、无 ~ 前缀时按常见文件扩展名认路径的启发式。 */
const PATH_EXTENSION_RE =
  /\.(?:ts|tsx|js|jsx|mjs|cjs|json|go|py|pyi|rs|rb|java|kt|c|h|cpp|hpp|cs|php|swift|sh|bash|zsh|sql|css|scss|html|htm|xml|md|markdown|txt|log|csv|toml|ya?ml|ini|env|mod|sum|proto|lock)$/i;

/** 疑似文件路径：含 `/`、以 `~` 开头或带常见扩展名。 */
export function looksLikePath(s: string): boolean {
  const t = s.trim();
  if (!t) return false;
  return t.startsWith('~') || t.includes('/') || PATH_EXTENSION_RE.test(t);
}

/** args JSON 解析；空串/截断/非 JSON 一律 undefined（调用方按原文回退）。 */
function parseArgs(argsRaw: string | undefined): unknown {
  if (argsRaw === undefined || argsRaw === '') return undefined;
  try {
    return JSON.parse(argsRaw);
  } catch {
    return undefined;
  }
}

const PATH_KEYS = ['path', 'file_path'] as const;

function pickString(rec: Record<string, unknown>, keys: readonly string[]): string | undefined {
  for (const key of keys) {
    const v = rec[key];
    if (typeof v === 'string' && v !== '') return v;
  }
  return undefined;
}

/** 摘要源文本剥前缀：先按工具名精确剥「调用工具 X：」，再通用正则兜底，最后剥「工具输出」。 */
function stripToolPrefix(text: string, tool: string | undefined): string {
  let s = text;
  if (tool !== undefined && tool !== '') {
    const exact = `调用工具 ${tool}：`;
    if (s.startsWith(exact)) s = s.slice(exact.length);
  }
  s = s.replace(/^调用工具 [^：:]*[：:]\s*/, '');
  if (s.startsWith('工具输出')) s = s.slice('工具输出'.length);
  return s;
}

function deriveFilePath(family: ToolFamily, msg: ChatMessage): string | undefined {
  if (family !== 'read' && family !== 'write' && family !== 'edit') return undefined;
  const parsed = parseArgs(msg.args);
  if (typeof parsed === 'object' && parsed !== null) {
    const picked = pickString(parsed as Record<string, unknown>, PATH_KEYS);
    if (picked !== undefined) return firstLine(picked);
  }
  if (msg.argsSummary !== undefined && msg.argsSummary !== '') {
    const candidate = firstLine(msg.argsSummary);
    if (looksLikePath(candidate)) return candidate;
  }
  return undefined;
}

function deriveBody(args: string | undefined): string | null {
  if (args === undefined || args === '') return null;
  try {
    return JSON.stringify(JSON.parse(args), null, 2);
  } catch {
    return args; // 流中截断的非 JSON args：展开体按原文
  }
}

export function toolRowModel(msg: ChatMessage): ToolRowModel {
  const family = classifyTool(msg.tool);
  let state: ToolRowState;
  if (msg.toolStatus === 'running') state = 'running';
  else if (msg.toolStatus === 'failed') state = 'error';
  else if (msg.exitCode !== undefined && msg.exitCode !== 0) {
    // 终端语义：事件是 completed/success 但 exit≠0 仍是失败行。
    state = 'error';
  } else state = 'ok';

  const argsFirst = msg.argsSummary !== undefined && msg.argsSummary !== '' ? firstLine(msg.argsSummary) : '';
  const summary = argsFirst !== '' ? argsFirst : firstLine(stripToolPrefix(msg.text, msg.tool)).trim();
  const rawOutput = msg.detail?.trim() || msg.liveOutput || null;
  const output = rawOutput !== null ? tailTruncate(rawOutput).text : null;
  const filePath = deriveFilePath(family, msg);

  return {
    family,
    title: toolActivityTitle({ family, state, summary, filePath }),
    summary,
    filePath,
    body: deriveBody(msg.args),
    output,
    errorSummary: state === 'error' && output !== null ? firstLine(output) : null,
    state,
    exitCode: msg.exitCode,
    running: state === 'running',
  };
}

/** A terminal run can project a persisted running frame as stopped; recompute copy for that visual state. */
export function toolRowTitleForState(model: ToolRowModel, state: ToolRowState): string {
  if (state === model.state) return model.title;
  return toolActivityTitle({
    family: model.family,
    state,
    summary: model.summary,
    filePath: model.filePath,
  });
}
