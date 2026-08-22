import { describe, expect, it } from 'vitest';
import { buildMessages } from './chat.store';
import type { TimelineEntry } from './runs.store';

const entry = (
  runId: string,
  runSeq: number,
  type: string,
  data?: Record<string, unknown>,
  role?: string,
  text?: string,
): TimelineEntry => ({
  event_id: `${runId}-${runSeq}`,
  stream_seq: runSeq,
  run_seq: runSeq,
  type,
  occurred_at: '2026-08-22T00:00:00Z',
  role,
  text,
  data,
});

describe('buildMessages', () => {
  it('run.created 的 instruction 渲染为用户气泡；user 输入与 assistant 完成文本分侧', () => {
    const timelines = {
      run_1: [
        entry('run_1', 1, 'run.created', { instruction: '实现 X 功能' }),
        entry('run_1', 2, 'message.delta', { role: 'assistant', text: '正在分析' }, 'assistant', '正在分析'),
        entry('run_1', 3, 'message.delta', { role: 'user', text: '补充一下' }, 'user', '补充一下'),
        entry('run_1', 4, 'message.completed', { role: 'assistant', text: '已完成' }, 'assistant', '已完成'),
        entry('run_1', 5, 'tool.started', { tool: 'bash' }),
      ],
    };
    const msgs = buildMessages(['run_1'], timelines);
    expect(msgs.map((m) => [m.kind, m.text])).toEqual([
      ['user', '实现 X 功能'],
      ['user', '补充一下'],
      ['assistant', '已完成'],
      ['tool', '调用工具 bash'],
    ]);
  });

  it('assistant 的 delta 不进气泡（completed 是权威）；run.failed 渲染错误行', () => {
    const timelines = {
      run_2: [
        entry('run_2', 1, 'message.delta', { role: 'assistant', text: 'delta' }, 'assistant', 'delta'),
        entry('run_2', 2, 'run.failed', { code: 'MISSING_CREDENTIAL', message: 'no API key' }),
      ],
    };
    const msgs = buildMessages(['run_2'], timelines);
    expect(msgs).toHaveLength(1);
    expect(msgs[0].kind).toBe('error');
    expect(msgs[0].text).toContain('MISSING_CREDENTIAL');
  });

  it('跨多个 run 按顺序拼接（串行轮次）', () => {
    const timelines = {
      run_1: [entry('run_1', 1, 'run.created', { instruction: '第一轮' })],
      run_2: [entry('run_2', 1, 'run.created', { instruction: '第二轮' })],
    };
    const msgs = buildMessages(['run_1', 'run_2'], timelines);
    expect(msgs.map((m) => m.text)).toEqual(['第一轮', '第二轮']);
  });
});
