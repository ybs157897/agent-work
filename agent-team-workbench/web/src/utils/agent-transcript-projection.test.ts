import { describe, expect, it } from 'vitest';
import { buildAgentTranscriptProjection } from './agent-transcript-projection';
import type { TimelineEntry } from '../stores/runs.store';

const entry = (seq: number, type: string, data: Record<string, unknown>, agent_id: string): TimelineEntry => ({
  event_id: `${agent_id}-${seq}`, stream_seq: seq, run_seq: seq, type, occurred_at: '2026-08-29T00:00:00Z', data, agent_id,
});

describe('buildAgentTranscriptProjection', () => {
  it('隔离 child 的正文、工具与实时草稿，不混入 main', () => {
    const timeline = [
      entry(1, 'message.delta', { raw: { chunk: { type: 'reasoning-delta', text: 'child thinking' } } }, 'child-1'),
      entry(2, 'tool.started', { tool: 'Read', call_id: 'same', args_summary: 'child file' }, 'child-1'),
      entry(3, 'tool.completed', { call_id: 'same', output: 'child tool' }, 'child-1'),
      entry(4, 'message.delta', { raw: { chunk: { type: 'text-delta', text: 'child final' } } }, 'child-1'),
      entry(5, 'message.completed', { role: 'assistant' }, 'child-1'),
      entry(6, 'message.delta', { raw: { chunk: { type: 'text-delta', text: 'child live' } } }, 'child-1'),
      entry(7, 'message.delta', { raw: { chunk: { type: 'text-delta', text: 'main leak' } } }, 'main'),
    ];
    const projection = buildAgentTranscriptProjection({ runId: 'run_1', agentId: 'child-1', runStatus: 'running', timeline });
    const text = projection.messages.map((message) => `${message.kind}:${message.text}`).join('|');
    expect(text).toContain('child thinking');
    expect(text).not.toContain('main leak');
    expect(projection.messages.find((message) => message.kind === 'tool')).toMatchObject({
      tool: 'Read',
      detail: 'child tool',
      toolStatus: 'success',
    });
    expect(projection.liveStream.answerDraft).toContain('child live');
    const settled = buildAgentTranscriptProjection({ runId: 'run_1', agentId: 'child-1', runStatus: 'succeeded', timeline: timeline.filter((item) => item.run_seq !== 7) });
    expect(settled.messages.map((message) => message.text).join('|')).toContain('child final');
  });
});
