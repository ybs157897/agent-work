import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import {
  OUTPUT_TRACE_CAP,
  OUTPUT_TRACE_CONTENT_LIMIT,
  clearOutputTrace,
  createOutputTraceEntry,
  exportOutputTrace,
  getOutputTrace,
  outputTraceFlags,
  outputTraceHash,
  outputTraceText,
  resolveOutputTraceFlags,
  stableOutputTraceJson,
  traceOutput,
  traceOutputDeduped,
} from './output-trace';

const TRACE_KEY = 'agent-workbench:output-trace';
const CONTENT_KEY = 'agent-workbench:output-trace-content';
const CONSOLE_KEY = 'agent-workbench:output-trace-console';

function storage(values: Record<string, string> = {}) {
  return {
    getItem: (key: string) => values[key] ?? null,
    setItem: vi.fn(),
    removeItem: vi.fn(),
  };
}

function enableRuntime(search = '?outputTrace=1&outputTraceContent=1') {
  vi.stubGlobal('window', {
    location: { search },
    localStorage: storage(),
  });
}

describe('output trace flags', () => {
  it('requires DEV and supports query/storage flags', () => {
    expect(resolveOutputTraceFlags({ dev: false, search: '?outputTrace=1' })).toEqual({
      enabled: false,
      includeContent: false,
      console: false,
    });
    expect(resolveOutputTraceFlags({
      dev: true,
      search: '',
      storage: { [TRACE_KEY]: '1', [CONTENT_KEY]: '1', [CONSOLE_KEY]: '1' },
    })).toEqual({ enabled: true, includeContent: true, console: true });
  });

  it('gives query flags precedence over local storage, including explicit false', () => {
    expect(resolveOutputTraceFlags({
      dev: true,
      search: '?outputTrace=1&outputTraceContent=0&outputTraceConsole=0',
      storage: { [TRACE_KEY]: '0', [CONTENT_KEY]: '1', [CONSOLE_KEY]: '1' },
    })).toEqual({ enabled: true, includeContent: false, console: false });
    expect(resolveOutputTraceFlags({
      dev: true,
      search: '?outputTrace=0&outputTraceContent=1',
      storage: { [TRACE_KEY]: '1', [CONTENT_KEY]: '0' },
    })).toEqual({ enabled: false, includeContent: false, console: false });
  });
});

describe('output trace content fingerprints', () => {
  it('does not retain content unless explicitly enabled', () => {
    const text = outputTraceText('private answer', false);
    expect(text).toEqual({ length: 14, hash: outputTraceHash('private answer') });
    expect(text).not.toHaveProperty('content');
  });

  it('retains bounded content and marks clipping', () => {
    const value = `head-${'x'.repeat(OUTPUT_TRACE_CONTENT_LIMIT)}-tail`;
    const result = outputTraceText(value, true);
    expect(result.length).toBe(value.length);
    expect(result.hash).toBe(outputTraceHash(value));
    expect(result.truncated).toBe(true);
    expect(result.content).toContain('…[output trace clipped]…');
    expect(result.content?.startsWith('head-')).toBe(true);
    expect(result.content?.endsWith('-tail')).toBe(true);
    expect(result.content!.length).toBeLessThanOrEqual(OUTPUT_TRACE_CONTENT_LIMIT);
  });

  it('produces a stable synchronous hash and stable key-sorted JSON', () => {
    expect(outputTraceHash('')).toBe('811c9dc5');
    expect(outputTraceHash('same')).toBe(outputTraceHash('same'));
    expect(outputTraceHash('same')).not.toBe(outputTraceHash('different'));
    expect(stableOutputTraceJson({ z: 1, a: { y: true, x: 2 } })).toBe(
      '{"a":{"x":2,"y":true},"z":1}',
    );
    expect(stableOutputTraceJson({ a: Number.NaN, b: Number.POSITIVE_INFINITY })).toBe(
      '{"a":"NaN","b":"Infinity"}',
    );
    const circular: Record<string, unknown> = { a: 1 };
    circular.self = circular;
    expect(stableOutputTraceJson(circular)).toBe('{"a":1,"self":"[circular]"}');
  });
});

describe('output trace ring buffer and lifecycle', () => {
  beforeEach(() => {
    clearOutputTrace();
    enableRuntime();
  });

  afterEach(() => {
    clearOutputTrace();
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  it('does not record while runtime gate is off', () => {
    vi.stubGlobal('window', { location: { search: '' }, localStorage: storage() });
    expect(outputTraceFlags().enabled).toBe(false);
    expect(traceOutput({ stage: 'live.draft', mode: 'streaming', source: 'projection', text: 'secret' })).toBeUndefined();
    expect(getOutputTrace()).toHaveLength(0);
  });

  it('records bounded entries with identifiers and content policy', () => {
    const entry = traceOutput({
      stage: 'sse.received',
      mode: 'streaming',
      source: 'sse',
      runId: 'run-1',
      messageId: 'message-1',
      eventId: 'event-1',
      callId: 'call-1',
      streamSeq: 8,
      runSeq: 3,
      eventType: 'message.delta',
      serverOccurredAt: '2026-08-28T00:00:00Z',
      text: 'visible in opt-in trace',
    });
    expect(entry).toMatchObject({
      schema: 'languagegui/output-trace-v1',
      stage: 'sse.received',
      runId: 'run-1',
      eventId: 'event-1',
      streamSeq: 8,
      runSeq: 3,
      text: { length: 23, content: 'visible in opt-in trace', truncated: false },
    });
    expect(getOutputTrace()).toHaveLength(1);
  });

  it('keeps only the newest fixed-capacity window', () => {
    for (let index = 0; index < OUTPUT_TRACE_CAP + 7; index += 1) {
      traceOutput({
        stage: 'timeline.applied',
        mode: 'streaming',
        source: 'timeline',
        eventId: `event-${index}`,
      });
    }
    const entries = getOutputTrace();
    expect(entries).toHaveLength(OUTPUT_TRACE_CAP);
    expect(entries[0]?.eventId).toBe('event-7');
    expect(entries.at(-1)?.eventId).toBe(`event-${OUTPUT_TRACE_CAP + 6}`);
  });

  it('exports the schema and clears records and sequence', () => {
    traceOutput({ stage: 'assistant.committed', mode: 'final', source: 'react-commit', runId: 'run-1' });
    const exported = JSON.parse(exportOutputTrace()) as { schema: string; exportedAt: string; entries: unknown[] };
    expect(exported.schema).toBe('languagegui/output-trace-v1');
    expect(exported.exportedAt).toEqual(expect.any(String));
    expect(exported.entries).toHaveLength(1);

    clearOutputTrace();
    expect(getOutputTrace()).toHaveLength(0);
    const next = traceOutput({ stage: 'markdown.committed', mode: 'final', source: 'react-commit' });
    expect(next?.sequence).toBe(1);
  });

  it('deduplicates a StrictMode-style commit replay without merging distinct messages', () => {
    const input = {
      stage: 'assistant.committed' as const,
      mode: 'final' as const,
      source: 'react-commit' as const,
      text: 'same answer',
    };
    expect(traceOutputDeduped('assistant:run-1:message-1:final:hash', input)).toBeDefined();
    expect(traceOutputDeduped('assistant:run-1:message-1:final:hash', input)).toBeUndefined();
    expect(traceOutputDeduped('assistant:run-2:message-2:final:hash', {
      ...input,
      runId: 'run-2',
      messageId: 'message-2',
    })).toBeDefined();
    expect(getOutputTrace()).toHaveLength(2);
  });

  it('creates deterministic entries when timestamp is supplied', () => {
    const entry = createOutputTraceEntry(
      { stage: 'content-block.committed', mode: 'final', source: 'react-commit', text: 'ok' },
      { includeContent: false },
      { iso: '2026-08-28T00:00:00.000Z', perfMs: 42 },
    );
    expect(entry).toMatchObject({ capturedAt: '2026-08-28T00:00:00.000Z', perfMs: 42, sequence: 1 });
  });
});
