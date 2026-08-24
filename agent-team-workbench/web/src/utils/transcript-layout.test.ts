import { describe, expect, it } from 'vitest';
import type { ChatMessage } from '../stores/chat.store';
import { layoutTranscript, reasoningPreview } from './transcript-layout';

const msg = (over: Partial<ChatMessage> & Pick<ChatMessage, 'key' | 'runId' | 'kind' | 'text' | 'at'>): ChatMessage =>
  over;

describe('layoutTranscript', () => {
  it('plan 置后于 assistant，工具在 assistant 前为 preActivity', () => {
    const messages: ChatMessage[] = [
      msg({ key: 'u1', runId: 'r1', kind: 'user', text: 'hi', at: 't1' }),
      msg({
        key: 'tool1',
        runId: 'r1',
        kind: 'tool',
        text: 'bash',
        at: 't2',
        tool: 'bash',
        toolStatus: 'success',
        startedAt: 't2',
        completedAt: 't3',
      }),
      msg({ key: 'plan1', runId: 'r1', kind: 'plan', text: '', at: 't4', steps: [{ step: 'a', status: 'pending' }] }),
      msg({ key: 'think1', runId: 'r1', kind: 'thinking', text: 'reasoning text', at: 't5' }),
      msg({ key: 'a1', runId: 'r1', kind: 'assistant', text: 'answer', at: 't6' }),
    ];
    const turns = layoutTranscript(messages);
    expect(turns).toHaveLength(1);
    const t = turns[0];
    expect(t.user?.key).toBe('u1');
    expect(t.preActivity.map((m) => m.key)).toEqual(['tool1']);
    expect(t.reasoning?.text).toBe('reasoning text');
    expect(t.assistant?.text).toBe('answer');
    expect(t.plan?.key).toBe('plan1');
    expect(t.postActivity).toHaveLength(0);
  });

  it('assistant 落定后的工具进 postActivity', () => {
    const messages: ChatMessage[] = [
      msg({ key: 'u1', runId: 'r1', kind: 'user', text: 'hi', at: 't1' }),
      msg({ key: 'a1', runId: 'r1', kind: 'assistant', text: 'answer', at: 't5' }),
      msg({
        key: 'tool2',
        runId: 'r1',
        kind: 'tool',
        text: 'late',
        at: 't6',
        tool: 'bash',
        toolStatus: 'success',
        startedAt: 't6',
        completedAt: 't7',
      }),
    ];
    const t = layoutTranscript(messages)[0];
    expect(t.preActivity).toHaveLength(0);
    expect(t.postActivity.map((m) => m.key)).toEqual(['tool2']);
  });

  it('流式 run 注入 liveStream 与 thinking placeholder', () => {
    const turns = layoutTranscript([], {
      liveRunId: 'r-live',
      awaitingReply: true,
      liveStream: { reasoning: 'stream think', answerDraft: '' },
      runStatuses: { 'r-live': 'running' },
    });
    expect(turns).toHaveLength(1);
    expect(turns[0].reasoning?.streaming).toBe(true);
    expect(turns[0].showThinkingPlaceholder).toBe(true);
  });

  it('item_type plan 不进 assistant 槽（proposed-plan 由底部 dock 承载）', () => {
    const messages: ChatMessage[] = [
      msg({ key: 'u1', runId: 'r1', kind: 'user', text: 'plan this', at: 't1' }),
      msg({
        key: 'a1',
        runId: 'r1',
        kind: 'assistant',
        text: '# Plan\n- step',
        at: 't2',
        itemType: 'plan',
      }),
    ];
    const t = layoutTranscript(messages)[0];
    expect(t.assistant).toBeUndefined();
  });

  it('多 run 按出现顺序分组', () => {
    const messages: ChatMessage[] = [
      msg({ key: 'u1', runId: 'r1', kind: 'user', text: 'a', at: 't1' }),
      msg({ key: 'u2', runId: 'r2', kind: 'user', text: 'b', at: 't2' }),
    ];
    expect(layoutTranscript(messages).map((t) => t.runId)).toEqual(['r1', 'r2']);
  });
});

describe('reasoningPreview', () => {
  it('流式取最后一行', () => {
    expect(reasoningPreview('line1\nline2', true)).toBe('line2');
  });

  it('落定取首行', () => {
    expect(reasoningPreview('line1\nline2', false)).toBe('line1');
  });
});
