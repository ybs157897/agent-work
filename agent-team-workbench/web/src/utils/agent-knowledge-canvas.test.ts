import { afterEach, describe, expect, it, vi } from 'vitest';
import type { AgentProfile } from '../api/types';
import { buildCanvasMessage, canReadAgentKnowledge, canvasPreferenceKey, parseCanvasMessage, readCanvasPreference, referenceBelongsTo, restoreCanvasComposer, writeCanvasPreference, type KnowledgeCanvasReference } from './agent-knowledge-canvas';

const agent = (patch: Partial<AgentProfile> = {}): AgentProfile => ({ id: 'a_pm', name: '产品', role: 'pm', skills: [], availability: 'enabled', presence: 'idle', version: 1, ...patch });
const reference: KnowledgeCanvasReference = { workspaceId: 'ws_1', agentId: 'a_pm', itemId: 'kb_1', version: 2, title: '退款\n规则', quote: '退款期限为 14 天。\n保留申请记录。', heading: '退款窗口' };

describe('Agent knowledge canvas boundaries', () => {
  afterEach(() => vi.unstubAllGlobals());

  it('uses the configured product role only as a view default and never enables system agents', () => {
    expect(readCanvasPreference('ws_1', agent())).toBe(true);
    expect(readCanvasPreference('ws_1', agent({ role: 'architect', name: '产品', skills: ['产品经理'] }))).toBe(false);
    expect(readCanvasPreference('ws_1', agent({ kind: 'knowledge_librarian', is_system: true }))).toBe(false);
    expect(canReadAgentKnowledge('viewer')).toBe(false);
    expect(canReadAgentKnowledge('operator')).toBe(false);
    expect(canReadAgentKnowledge('admin')).toBe(true);
  });

  it('keeps workspace and Agent preferences separate, including blocked storage', () => {
    const data = new Map<string, string>();
    vi.stubGlobal('window', { localStorage: { getItem: (key: string) => data.get(key), setItem: (key: string, value: string) => data.set(key, value) } });
    writeCanvasPreference('ws_1', 'a_pm', false);
    expect(readCanvasPreference('ws_1', agent())).toBe(false);
    expect(readCanvasPreference('ws_2', agent())).toBe(true);
    expect(canvasPreferenceKey('ws:a', 'b')).not.toBe(canvasPreferenceKey('ws', 'a:b'));
    vi.stubGlobal('window', { get localStorage() { throw new Error('denied'); } });
    expect(() => writeCanvasPreference('ws_1', 'a_pm', true)).not.toThrow();
    expect(readCanvasPreference('ws_1', agent())).toBe(true);
  });

  it('binds a quote to the exact workspace, Agent and version', () => {
    expect(referenceBelongsTo(reference, 'ws_1', 'a_pm')).toBe(true);
    expect(referenceBelongsTo(reference, 'ws_2', 'a_pm')).toBe(false);
    expect(referenceBelongsTo(reference, 'ws_1', 'a_other')).toBe(false);
    expect(referenceBelongsTo({ ...reference, version: 0 }, 'ws_1', 'a_pm')).toBe(false);
    const message = buildCanvasMessage('请解释这个规则。', reference);
    expect(message.startsWith('请解释这个规则。')).toBe(true);
    expect(parseCanvasMessage(message)).toEqual({ question: '请解释这个规则。', reference: { ...reference, title: '退款 规则' } });
    expect(message).not.toContain('/knowledge?');
    expect(buildCanvasMessage(' 普通问题 ', null)).toBe('普通问题');
  });

  it('bounds excerpts explicitly without dropping their identity', () => {
    const message = buildCanvasMessage('评审', { ...reference, quote: '文'.repeat(9000) });
    expect(message).toContain('摘录到此为止');
    expect(parseCanvasMessage(message)?.reference.itemId).toBe('kb_1');
    expect(message.length).toBeLessThan(6400);
  });

  it('restores the removable versioned reference on failure without overwriting a newer selection', () => {
    const failed = { draft: '请解释规则', reference };
    expect(restoreCanvasComposer({ draft: '', reference: null }, failed)).toEqual(failed);
    const newer = { ...reference, itemId: 'kb_2', title: '另一份文档', quote: '新的选区' };
    const result = restoreCanvasComposer({ draft: '还有一个问题', reference: newer }, failed);
    expect(result.reference).toBe(newer);
    expect(result.draft).toContain('"itemId":"kb_1"');
    expect(result.draft).toContain('退款期限为 14 天。');
    expect(result.draft.endsWith('还有一个问题')).toBe(true);
  });

  it('does not reinterpret ordinary prose or invalid trailers as knowledge cards', () => {
    expect(parseCanvasMessage('引用知识：《规则》 · 第 2 版')).toBeNull();
    expect(parseCanvasMessage('问题\n\n<atw-knowledge-reference-v1>\n{}\n</atw-knowledge-reference-v1>')).toBeNull();
    expect(parseCanvasMessage(buildCanvasMessage('问题', reference) + '\n用户补充')).toBeNull();
  });
});
