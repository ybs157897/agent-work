import { describe, expect, it } from 'vitest';
import type { ChatMessage } from '../stores/chat.store';
import type { TranscriptSegment } from './chronological-transcript';
import { presentedTranscriptSegmentKey, projectWorkActivityTimeline } from './work-activity-timeline';

const message = (key: string, runId: string, kind: ChatMessage['kind'], text: string, at: string): ChatMessage => ({
  key, runId, kind, text, at,
});

const user = (key: string, runId: string, at = `${key}-at`): TranscriptSegment => ({
  kind: 'user', msg: message(key, runId, 'user', 'request', at),
});

const thinking = (key: string, runId: string, text: string, at: string, streaming = false): TranscriptSegment => ({
  kind: 'thinking', msg: message(key, runId, 'thinking', text, at), streaming,
});

const assistant = (key: string, runId: string, text: string, at: string, streaming?: boolean): TranscriptSegment => ({
  kind: 'assistant', msg: message(key, runId, 'assistant', text, at), ...(streaming === undefined ? {} : { streaming }),
});

describe('projectWorkActivityTimeline', () => {
  it('成功 run 将最后一个非 streaming assistant 提升为 final，其余内容按原序进入 timeline', () => {
    const segments: TranscriptSegment[] = [
      user('u1', 'r1', '2026-01-01T00:00:00Z'),
      thinking('t1', 'r1', '先分析', '2026-01-01T00:00:01Z'),
      assistant('a1', 'r1', '中间说明', '2026-01-01T00:00:02Z'),
      assistant('a2', 'r1', '最终答案', '2026-01-01T00:00:03Z'),
    ];
    const result = projectWorkActivityTimeline(segments, {
      runStatuses: { r1: 'succeeded' },
      timingByRun: {
        r1: {
          createdAt: '2026-01-01T00:00:00Z',
          updatedAt: '2026-01-01T00:01:25Z',
        },
      },
      metadataByRun: { r1: { agent: 'Nova' } },
    });
    expect(result.map((item) => item.kind)).toEqual(['user', 'work-timeline', 'assistant']);
    const timeline = result[1];
    expect(timeline.kind).toBe('work-timeline');
    if (timeline.kind === 'work-timeline') {
      expect(timeline.items.map((item) => item.kind)).toEqual(['thinking', 'assistant']);
      expect(timeline.items[1]).toMatchObject({ kind: 'assistant', msg: { text: '中间说明' } });
      expect(timeline.items[1]).not.toHaveProperty('streaming');
      expect(timeline.metadata).toEqual({ agent: 'Nova' });
      expect(timeline.createdAt).toBe('2026-01-01T00:00:00Z');
      expect(timeline.updatedAt).toBe('2026-01-01T00:01:25Z');
    }
    expect(result[2]).toMatchObject({ kind: 'assistant', msg: { text: '最终答案' } });
  });

  it('运行中已落定的 assistant 保留为 interim，loading 独立显示在工作卡外', () => {
    const activity: TranscriptSegment = {
      kind: 'activity', runId: 'r2', items: [message('tool', 'r2', 'tool', 'Read', '2026-01-01T00:00:02Z')],
    };
    const result = projectWorkActivityTimeline([
      user('u2', 'r2'), thinking('t2', 'r2', '思考', '2026-01-01T00:00:01Z', true), activity,
      assistant('a3', 'r2', '继续输出', '2026-01-01T00:00:03Z'),
      { kind: 'thinking-placeholder', runId: 'r2' },
    ], { runStatuses: { r2: 'running' } });
    expect(result.map((item) => item.kind)).toEqual(['user', 'work-timeline', 'thinking-placeholder']);
    const timeline = result[1];
    if (timeline.kind === 'work-timeline') {
      expect(timeline.items.map((item) => item.kind)).toEqual(['thinking', 'activity', 'assistant']);
      expect(timeline.items[2]).toMatchObject({ kind: 'assistant' });
      expect(timeline.items[2]).not.toHaveProperty('streaming');
    }
    expect(presentedTranscriptSegmentKey(result[2])).toBe('output-loading:r2');
  });

  it('运行中的 final draft 立即提升为普通正文，并让 loading 紧随其后', () => {
    const result = projectWorkActivityTimeline([
      user('u-live', 'r-live', '2026-01-01T00:00:00Z'),
      thinking('t-live', 'r-live', '先确认', '2026-01-01T00:00:01Z'),
      assistant('a-live', 'r-live', '第一段已经完成。\n\n第二段正在输出', '2026-01-01T00:00:02Z', true),
      { kind: 'thinking-placeholder', runId: 'r-live' },
    ], { runStatuses: { 'r-live': 'running' } });

    expect(result.map((item) => item.kind)).toEqual([
      'user',
      'work-timeline',
      'assistant',
      'thinking-placeholder',
    ]);
    expect(result[1]).toMatchObject({ kind: 'work-timeline', items: [{ kind: 'thinking' }] });
    expect(result[2]).toMatchObject({ kind: 'assistant', streaming: true });
    expect(presentedTranscriptSegmentKey(result[2])).toBe('a-live');
  });

  it('失败 run 的正文仍留在 timeline，审批、错误和工具不被挪到外层', () => {
    const result = projectWorkActivityTimeline([
      user('u3', 'r3'),
      { kind: 'meta', msg: message('e', 'r3', 'error', 'failed', '2026-01-01T00:00:02Z') },
      assistant('a4', 'r3', '部分结果', '2026-01-01T00:00:03Z'),
      { kind: 'approval', approval: { id: 'ap1', run_id: 'r3', status: 'pending' } as never },
    ], { runStatuses: { r3: 'failed' } });
    expect(result.map((item) => item.kind)).toEqual(['user', 'work-timeline']);
    if (result[1].kind === 'work-timeline') expect(result[1].items.map((item) => item.kind)).toEqual(['meta', 'assistant', 'approval']);
  });

  it('无 status 且只有 optimistic user 时不生成空 timeline；key 稳定', () => {
    const result = projectWorkActivityTimeline([user('u4', 'pending')]);
    expect(result).toHaveLength(1);
    expect(result[0].kind).toBe('user');

    const withWork = projectWorkActivityTimeline([
      user('u5', 'r5'), thinking('t5', 'r5', 'x', '2026-01-01T00:00:01Z'),
    ], { runStatuses: { r5: 'running' } });
    expect(withWork[1].kind).toBe('work-timeline');
    expect(presentedTranscriptSegmentKey(withWork[1])).toBe('work-timeline:r5');
    const again = projectWorkActivityTimeline([
      user('u5', 'r5'), thinking('t5', 'r5', 'xy', '2026-01-01T00:00:01Z'),
    ], { runStatuses: { r5: 'running' } });
    expect(presentedTranscriptSegmentKey(again[1])).toBe('work-timeline:r5');
  });
});
