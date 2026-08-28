export const OUTPUT_TRACE_SCHEMA = 'languagegui/output-trace-v1' as const;
export const OUTPUT_TRACE_CAP = 1_000;
export const OUTPUT_TRACE_CONTENT_LIMIT = 24_000;

export type OutputTraceStage =
  | 'sse.received'
  | 'timeline.applied'
  | 'messages.projected'
  | 'live.draft'
  | 'transcript.projected'
  | 'assistant.committed'
  | 'markdown.committed'
  | 'content-block.committed';

export type OutputTraceMode = 'streaming' | 'final';
export type OutputTraceSource = 'sse' | 'history' | 'timeline' | 'projection' | 'react-commit';

export interface OutputTraceText {
  length: number;
  hash: string;
  content?: string;
  truncated?: boolean;
}

export interface OutputTraceProjection {
  timelineEntries?: number;
  messages?: number;
  assistantMessages?: number;
  thinkingMessages?: number;
  toolMessages?: number;
  contentBlocks?: number;
  blockTypes?: string[];
  hash?: string;
  pendingChars?: number;
}

export interface OutputTraceEntry {
  schema: typeof OUTPUT_TRACE_SCHEMA;
  traceId: string;
  sequence: number;
  stage: OutputTraceStage;
  capturedAt: string;
  perfMs: number;
  runId?: string;
  messageId?: string;
  eventId?: string;
  correlationId?: string;
  callId?: string;
  streamSeq?: number;
  runSeq?: number;
  eventType?: string;
  mode: OutputTraceMode;
  source: OutputTraceSource;
  serverOccurredAt?: string;
  text?: OutputTraceText;
  projection?: OutputTraceProjection;
  status?: string;
  duplicate?: boolean;
  retained?: boolean;
  metadata?: Record<string, string | number | boolean | null>;
}

export interface OutputTraceInput {
  stage: OutputTraceStage;
  mode: OutputTraceMode;
  source: OutputTraceSource;
  text?: string;
  runId?: string;
  messageId?: string;
  eventId?: string;
  correlationId?: string;
  callId?: string;
  streamSeq?: number;
  runSeq?: number;
  eventType?: string;
  serverOccurredAt?: string;
  projection?: OutputTraceProjection;
  status?: string;
  duplicate?: boolean;
  retained?: boolean;
  metadata?: Record<string, string | number | boolean | null>;
}

export interface OutputTraceFlags {
  enabled: boolean;
  includeContent: boolean;
  console: boolean;
}

interface TraceFlagSource {
  dev: boolean;
  search?: string;
  storage?: Readonly<Record<string, string | null>>;
}

declare global {
  interface Window {
    __LANGUAGEGUI_OUTPUT_TRACE__?: {
      get: () => readonly OutputTraceEntry[];
      export: () => string;
      clear: () => void;
      flags: () => OutputTraceFlags;
    };
  }
}

const TRACE_STORAGE_KEY = 'agent-workbench:output-trace';
const TRACE_CONTENT_STORAGE_KEY = 'agent-workbench:output-trace-content';
const TRACE_CONSOLE_STORAGE_KEY = 'agent-workbench:output-trace-console';

let sequence = 0;
let entries: OutputTraceEntry[] = [];
let runtimeFlagsCache: OutputTraceFlags | undefined;
let recentDedupe = new Map<string, number>();

function queryFlag(params: URLSearchParams, key: string): boolean | undefined {
  const value = params.get(key);
  if (value === '1') return true;
  if (value === '0') return false;
  return undefined;
}

export function resolveOutputTraceFlags({ dev, search = '', storage = {} }: TraceFlagSource): OutputTraceFlags {
  if (!dev) return { enabled: false, includeContent: false, console: false };
  const params = new URLSearchParams(search);
  const enabled = queryFlag(params, 'outputTrace') ?? (storage[TRACE_STORAGE_KEY] === '1');
  if (!enabled) return { enabled: false, includeContent: false, console: false };
  return {
    enabled: true,
    includeContent: queryFlag(params, 'outputTraceContent') ?? (storage[TRACE_CONTENT_STORAGE_KEY] === '1'),
    console: queryFlag(params, 'outputTraceConsole') ?? (storage[TRACE_CONSOLE_STORAGE_KEY] === '1'),
  };
}

function runtimeStorage(): Record<string, string | null> {
  if (typeof window === 'undefined') return {};
  try {
    return {
      [TRACE_STORAGE_KEY]: window.localStorage.getItem(TRACE_STORAGE_KEY),
      [TRACE_CONTENT_STORAGE_KEY]: window.localStorage.getItem(TRACE_CONTENT_STORAGE_KEY),
      [TRACE_CONSOLE_STORAGE_KEY]: window.localStorage.getItem(TRACE_CONSOLE_STORAGE_KEY),
    };
  } catch {
    return {};
  }
}

export function outputTraceFlags(): OutputTraceFlags {
  if (typeof window === 'undefined') return { enabled: false, includeContent: false, console: false };
  if (runtimeFlagsCache) return runtimeFlagsCache;
  runtimeFlagsCache = resolveOutputTraceFlags({
    dev: import.meta.env.DEV,
    search: window.location.search,
    storage: runtimeStorage(),
  });
  return runtimeFlagsCache;
}

export function isOutputTraceEnabled(): boolean {
  return outputTraceFlags().enabled;
}

/** FNV-1a 32-bit：同步、稳定，不改变事件与渲染的时序。 */
export function outputTraceHash(value: string): string {
  let hash = 0x811c9dc5;
  for (let index = 0; index < value.length; index += 1) {
    hash ^= value.charCodeAt(index);
    hash = Math.imul(hash, 0x01000193);
  }
  return (hash >>> 0).toString(16).padStart(8, '0');
}

function clippedContent(value: string): { content: string; truncated: boolean } {
  if (value.length <= OUTPUT_TRACE_CONTENT_LIMIT) return { content: value, truncated: false };
  const edge = Math.floor((OUTPUT_TRACE_CONTENT_LIMIT - 32) / 2);
  return {
    content: value.slice(0, edge) + '\n…[output trace clipped]…\n' + value.slice(-edge),
    truncated: true,
  };
}

export function outputTraceText(value: string, includeContent: boolean): OutputTraceText {
  const base: OutputTraceText = { length: value.length, hash: outputTraceHash(value) };
  if (!includeContent) return base;
  const clipped = clippedContent(value);
  return {
    ...base,
    content: clipped.content,
    truncated: clipped.truncated,
  };
}

function recordString(data: Record<string, unknown> | undefined, key: string): string | undefined {
  const value = data?.[key];
  return typeof value === 'string' ? value : undefined;
}

/** 提取事件在 UI 中会进入正文/工具详情的文本，供各投影节点做同口径 hash 对比。 */
export function outputTraceEventText(
  eventType: string,
  data?: Record<string, unknown>,
): string | undefined {
  if (eventType === 'message.delta') {
    const raw = data?.raw;
    const chunk = raw && typeof raw === 'object'
      ? (raw as Record<string, unknown>).chunk
      : undefined;
    if (chunk && typeof chunk === 'object') {
      const text = (chunk as Record<string, unknown>).text;
      return typeof text === 'string' ? text : undefined;
    }
  }
  if (eventType === 'message.completed') return recordString(data, 'text');
  if (eventType === 'tool.started') {
    return recordString(data, 'args') ?? recordString(data, 'args_summary');
  }
  if (eventType === 'tool.progress') return recordString(data, 'text');
  if (eventType === 'tool.completed' || eventType === 'tool.failed') {
    return recordString(data, 'output');
  }
  return undefined;
}

function stableValue(value: unknown, seen: WeakSet<object>): unknown {
  if (value === null || typeof value !== 'object') {
    if (typeof value === 'number' && !Number.isFinite(value)) return String(value);
    return value;
  }
  if (seen.has(value)) return '[circular]';
  seen.add(value);
  try {
    if (Array.isArray(value)) return value.map((item) => stableValue(item, seen));
    const input = value as Record<string, unknown>;
    const output: Record<string, unknown> = {};
    for (const key of Object.keys(input).sort()) output[key] = stableValue(input[key], seen);
    return output;
  } finally {
    seen.delete(value);
  }
}

export function stableOutputTraceJson(value: unknown): string {
  return JSON.stringify(stableValue(value, new WeakSet()));
}

export function createOutputTraceEntry(
  input: OutputTraceInput,
  flags: Pick<OutputTraceFlags, 'includeContent'>,
  now: { iso: string; perfMs: number } = {
    iso: new Date().toISOString(),
    perfMs: typeof performance === 'undefined' ? 0 : performance.now(),
  },
): OutputTraceEntry {
  sequence += 1;
  return {
    schema: OUTPUT_TRACE_SCHEMA,
    traceId: `output-${sequence}`,
    sequence,
    stage: input.stage,
    capturedAt: now.iso,
    perfMs: now.perfMs,
    mode: input.mode,
    source: input.source,
    ...(input.runId ? { runId: input.runId } : {}),
    ...(input.messageId ? { messageId: input.messageId } : {}),
    ...(input.eventId ? { eventId: input.eventId } : {}),
    ...(input.correlationId ? { correlationId: input.correlationId } : {}),
    ...(input.callId ? { callId: input.callId } : {}),
    ...(input.streamSeq !== undefined ? { streamSeq: input.streamSeq } : {}),
    ...(input.runSeq !== undefined ? { runSeq: input.runSeq } : {}),
    ...(input.eventType ? { eventType: input.eventType } : {}),
    ...(input.serverOccurredAt ? { serverOccurredAt: input.serverOccurredAt } : {}),
    ...(input.text !== undefined ? { text: outputTraceText(input.text, flags.includeContent) } : {}),
    ...(input.projection ? { projection: input.projection } : {}),
    ...(input.status ? { status: input.status } : {}),
    ...(input.duplicate !== undefined ? { duplicate: input.duplicate } : {}),
    ...(input.retained !== undefined ? { retained: input.retained } : {}),
    ...(input.metadata ? { metadata: input.metadata } : {}),
  };
}

export function getOutputTrace(): readonly OutputTraceEntry[] {
  return entries;
}

export function clearOutputTrace(): void {
  entries = [];
  sequence = 0;
  runtimeFlagsCache = undefined;
  recentDedupe = new Map();
}

export function exportOutputTrace(): string {
  return JSON.stringify({
    schema: OUTPUT_TRACE_SCHEMA,
    exportedAt: new Date().toISOString(),
    entries,
  }, null, 2);
}

function installOutputTraceApi(): void {
  if (typeof window === 'undefined' || window.__LANGUAGEGUI_OUTPUT_TRACE__) return;
  window.__LANGUAGEGUI_OUTPUT_TRACE__ = {
    get: getOutputTrace,
    export: exportOutputTrace,
    clear: clearOutputTrace,
    flags: outputTraceFlags,
  };
}

export function traceOutput(input: OutputTraceInput): OutputTraceEntry | undefined {
  const flags = outputTraceFlags();
  if (!flags.enabled) return undefined;
  installOutputTraceApi();
  const entry = createOutputTraceEntry(input, flags);
  entries = [...entries, entry].slice(-OUTPUT_TRACE_CAP);
  if (flags.console) console.debug('[languagegui/output-trace]', JSON.stringify(entry));
  return entry;
}

/** React StrictMode 会在 DEV 重放首次 commit；同一消息同一内容的短窗重复只记一次。 */
export function traceOutputDeduped(
  dedupeKey: string,
  input: OutputTraceInput,
  windowMs = 50,
): OutputTraceEntry | undefined {
  if (!isOutputTraceEnabled()) return undefined;
  const now = typeof performance === 'undefined' ? Date.now() : performance.now();
  const previous = recentDedupe.get(dedupeKey);
  if (previous !== undefined && now - previous <= windowMs) return undefined;
  recentDedupe.set(dedupeKey, now);
  if (recentDedupe.size > OUTPUT_TRACE_CAP * 2) {
    for (const key of recentDedupe.keys()) {
      if (recentDedupe.size <= OUTPUT_TRACE_CAP) break;
      recentDedupe.delete(key);
    }
  }
  return traceOutput(input);
}

// DEV 始终暴露只读诊断入口，便于先检查 flags；未显式开启时 traceOutput 仍是零记录。
if (typeof window !== 'undefined' && import.meta.env.DEV) installOutputTraceApi();
