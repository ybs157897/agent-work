import { describe, expect, it } from 'vitest';
import type { ChatMessage } from '../stores/chat.store';
import type { TimelineEntry } from '../stores/runs.store';
import {
  buildTranscriptSegments,
  injectPendingUsers,
  supplementUserFromTimeline,
} from './chronological-transcript';

const msg = (over: Partial<ChatMessage> & Pick<ChatMessage, 'key' | 'runId' | 'kind' | 'text' | 'at'>): ChatMessage =>
  over;

describe('buildTranscriptSegments', () => {
  it('保持时间线顺序：用户 → 工具 → 思考 → 正文', () => {
    const messages: ChatMessage[] = [
      msg({ key: 'u1', runId: 'r1', kind: 'user', text: '你好', at: 't1' }),
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
      msg({ key: 'think1', runId: 'r1', kind: 'thinking', text: '想一下', at: 't4' }),
      msg({ key: 'a1', runId: 'r1', kind: 'assistant', text: '好的', at: 't5' }),
    ];
    expect(buildTranscriptSegments(messages).map((s) => s.kind)).toEqual([
      'user',
      'activity',
      'thinking',
      'assistant',
    ]);
  });

  it('隐藏 session 运维元信息行（已开新会话等）', () => {
    const messages: ChatMessage[] = [
      msg({ key: 'u1', runId: 'r1', kind: 'user', text: 'hi', at: 't1' }),
      msg({
        key: 's1',
        runId: 'r1',
        kind: 'system',
        text: '已开新会话',
        at: 't2',
        sessionMeta: true,
      }),
      msg({ key: 'a1', runId: 'r1', kind: 'assistant', text: '好', at: 't3' }),
    ];
    expect(buildTranscriptSegments(messages).map((s) => s.kind)).toEqual(['user', 'assistant']);
  });

  it('保留全部用户消息（不只最后一条）', () => {
    const messages: ChatMessage[] = [
      msg({ key: 'u1', runId: 'r1', kind: 'user', text: '第一轮', at: 't1' }),
      msg({ key: 'a1', runId: 'r1', kind: 'assistant', text: '收到', at: 't2' }),
      msg({ key: 'u2', runId: 'r2', kind: 'user', text: '第二轮', at: 't3' }),
    ];
    const users = buildTranscriptSegments(messages).filter((s) => s.kind === 'user');
    expect(users).toHaveLength(2);
    expect(users.map((s) => (s.kind === 'user' ? s.msg.text : ''))).toEqual(['第一轮', '第二轮']);
  });

  it('隐藏 plan 与 item_type plan 的正文', () => {
    const messages: ChatMessage[] = [
      msg({ key: 'u1', runId: 'r1', kind: 'user', text: 'plan', at: 't1' }),
      msg({ key: 'p1', runId: 'r1', kind: 'plan', text: '', at: 't2', steps: [{ step: 'a', status: 'pending' }] }),
      msg({ key: 'a1', runId: 'r1', kind: 'assistant', text: '# Plan', at: 't3', itemType: 'plan' }),
      msg({ key: 'a2', runId: 'r1', kind: 'assistant', text: '正文', at: 't4' }),
    ];
    const kinds = buildTranscriptSegments(messages).map((s) => s.kind);
    expect(kinds).toEqual(['user', 'assistant']);
  });

  it('已有落定 thinking 时仍追加当前段 live reasoning（多阶段 run）', () => {
    const messages: ChatMessage[] = [
      msg({ key: 'u1', runId: 'r1', kind: 'user', text: 'hi', at: 't1' }),
      msg({ key: 'think-done', runId: 'r1', kind: 'thinking', text: '第一段思考', at: 't2' }),
    ];
    const segments = buildTranscriptSegments(messages, {
      liveRunId: 'r1',
      liveRunActive: true,
      liveStream: { reasoning: '第二段思考', answerDraft: '' },
      runStatuses: { r1: 'running' },
    });
    const thinking = segments.filter((s) => s.kind === 'thinking');
    expect(thinking).toHaveLength(2);
    expect(thinking[0].kind === 'thinking' && thinking[0].msg.text).toBe('第一段思考');
    expect(thinking[1].kind === 'thinking' && thinking[1].streaming).toBe(true);
    expect(thinking[1].kind === 'thinking' && thinking[1].msg.text).toBe('第二段思考');
  });

  it('定稿 inline 与 live 同文时不重复追加 tail', () => {
    const messages: ChatMessage[] = [
      msg({ key: 'think-done', runId: 'r1', kind: 'thinking', text: '同一段', at: 't2' }),
    ];
    const segments = buildTranscriptSegments(messages, {
      liveRunId: 'r1',
      liveRunActive: true,
      liveStream: { reasoning: '同一段', answerDraft: '' },
      runStatuses: { r1: 'running' },
    });
    expect(segments.filter((s) => s.kind === 'thinking')).toHaveLength(1);
  });
});

describe('supplementUserFromTimeline', () => {
  it('在 run 消息前插入 run.created 用户行', () => {
    const timelines: Record<string, TimelineEntry[]> = {
      r1: [
        {
          event_id: 'e1',
          stream_seq: 1,
          run_seq: 1,
          type: 'run.created',
          occurred_at: 't0',
          data: { instruction: '实现功能' },
        },
      ],
    };
    const messages: ChatMessage[] = [
      msg({ key: 'a1', runId: 'r1', kind: 'assistant', text: '好', at: 't2' }),
    ];
    const out = supplementUserFromTimeline(messages, ['r1'], timelines);
    expect(out.map((m) => m.kind)).toEqual(['user', 'assistant']);
    expect(out[0].text).toBe('实现功能');
  });
});

describe('injectPendingUsers', () => {
  it('timeline 未到时插入 optimistic 用户气泡', () => {
    const out = injectPendingUsers([], { r1: '先发出去' });
    expect(out).toEqual([
      expect.objectContaining({ runId: 'r1', kind: 'user', text: '先发出去' }),
    ]);
  });
});
