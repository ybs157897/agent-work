import { describe, expect, it } from 'vitest';
import { buildMessages, type ChatMessage } from '../stores/chat.store';
import type { TimelineEntry } from '../stores/runs.store';
import {
  buildTranscriptSegments,
  injectPendingUsers,
  supplementUserFromTimeline,
} from './chronological-transcript';
import { transcriptSegmentKey } from './approval-transcript';

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

  it('流式正文与同序号定稿正文沿用同一 render key', () => {
    const settledPrefix = [
      msg({ key: 'a1', runId: 'r1', kind: 'assistant', text: '第一段', at: 't1' }),
    ];
    const live = buildTranscriptSegments(settledPrefix, {
      liveRunId: 'r1',
      liveRunActive: true,
      liveStream: { reasoning: '', answerDraft: '第二段流式' },
      runStatuses: { r1: 'running' },
    }).filter((segment) => segment.kind === 'assistant');
    const settled = buildTranscriptSegments([
      ...settledPrefix,
      msg({ key: 'event-completed', runId: 'r1', kind: 'assistant', text: '第二段流式', at: 't2' }),
    ]).filter((segment) => segment.kind === 'assistant');

    expect(transcriptSegmentKey(live[1]!)).toBe('assistant:r1:1');
    expect(transcriptSegmentKey(settled[1]!)).toBe('assistant:r1:1');
  });

  it('不 trim 流式正文的 Markdown 空白', () => {
    const answerDraft = '  首行保留空格\n\n- item\n';
    const segments = buildTranscriptSegments([], {
      liveRunId: 'r1',
      liveRunActive: true,
      liveStream: { reasoning: '', answerDraft },
      runStatuses: { r1: 'running' },
    });
    const assistant = segments.find((segment) => segment.kind === 'assistant');
    expect(assistant?.kind === 'assistant' && assistant.msg.text).toBe(answerDraft);
  });

  it('正文切断工具批次：后续工具另起 ActivityGroup，严格保持事件顺序', () => {
    const timeline: TimelineEntry[] = [
      { event_id: 'd1', stream_seq: 1, run_seq: 1, type: 'message.delta', occurred_at: 't1', data: { raw: { chunk: { type: 'text-delta', text: '第一段正文' } } } },
      { event_id: 't1s', stream_seq: 2, run_seq: 2, type: 'tool.started', occurred_at: 't2', data: { tool: 'Read', call_id: 'c1' } },
      { event_id: 't1e', stream_seq: 3, run_seq: 3, type: 'tool.completed', occurred_at: 't3', data: { call_id: 'c1', output: 'a' } },
      { event_id: 't2s', stream_seq: 4, run_seq: 4, type: 'tool.started', occurred_at: 't4', data: { tool: 'Grep', call_id: 'c2' } },
      { event_id: 't2e', stream_seq: 5, run_seq: 5, type: 'tool.completed', occurred_at: 't5', data: { call_id: 'c2', output: 'b' } },
      { event_id: 'd2', stream_seq: 6, run_seq: 6, type: 'message.delta', occurred_at: 't6', data: { raw: { chunk: { type: 'text-delta', text: '第二段正文' } } } },
      { event_id: 't3s', stream_seq: 7, run_seq: 7, type: 'tool.started', occurred_at: 't7', data: { tool: 'Bash', call_id: 'c3' } },
      { event_id: 't3e', stream_seq: 8, run_seq: 8, type: 'tool.completed', occurred_at: 't8', data: { call_id: 'c3', output: 'c' } },
      { event_id: 'd3', stream_seq: 9, run_seq: 9, type: 'message.delta', occurred_at: 't9', data: { raw: { chunk: { type: 'text-delta', text: '第三段正文' } } } },
    ];
    const messages = buildMessages(['r1'], { r1: timeline });
    const segments = buildTranscriptSegments(messages);
    expect(segments.map((segment) => segment.kind)).toEqual([
      'assistant',
      'activity',
      'assistant',
      'activity',
      'assistant',
    ]);
    expect(segments[1]?.kind === 'activity' && segments[1].items.map((item) => item.key)).toEqual([
      'r1-tool-c1',
      'r1-tool-c2',
    ]);
    expect(segments[3]?.kind === 'activity' && segments[3].items.map((item) => item.key)).toEqual([
      'r1-tool-c3',
    ]);
  });

  it('多阶段正文不会让本轮 Diff 提前插到第一段正文后，活动 run 也不提前展示', () => {
    const messages: ChatMessage[] = [
      msg({ key: 'a1', runId: 'r1', kind: 'assistant', text: '先修改文件', at: 't1' }),
      msg({
        key: 'edit',
        runId: 'r1',
        kind: 'tool',
        text: '调用工具 edit：a.ts',
        at: 't2',
        tool: 'edit',
        toolStatus: 'success',
        detail: 'diff --git a/a.ts b/a.ts\n--- a/a.ts\n+++ b/a.ts\n@@ -1 +1 @@\n-old\n+new',
      }),
      msg({ key: 'a2', runId: 'r1', kind: 'assistant', text: '修改完成', at: 't3' }),
    ];
    expect(buildTranscriptSegments(messages).map((segment) => segment.kind)).toEqual([
      'assistant',
      'activity',
      'assistant',
      'turn-diff',
    ]);
    expect(buildTranscriptSegments(messages, {
      liveRunId: 'r1',
      liveRunActive: true,
      runStatuses: { r1: 'running' },
    }).some((segment) => segment.kind === 'turn-diff')).toBe(false);
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
