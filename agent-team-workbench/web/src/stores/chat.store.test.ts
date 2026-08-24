import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { aggregateRunStream, buildForkContext, buildMessages, conversationLabel, currentRunSnapshot, extractDeltaChunk, extractExitCode, formatTokenUsage, FORK_CONTEXT_MARKER, parsePlanSteps, sessionLine, toolDuration, useChatStore, type ChatMessage } from './chat.store';
import { useRunsStore, type TimelineEntry } from './runs.store';
import { useTasksStore } from './tasks.store';
import { useWorkspaceStore } from './workspace.store';
import type { ExecutionRun, WorkItem } from '../api/types';

const entry = (
  runId: string,
  runSeq: number,
  type: string,
  data?: Record<string, unknown>,
  role?: string,
  text?: string,
  occurredAt?: string,
): TimelineEntry => ({
  event_id: `${runId}-${runSeq}`,
  stream_seq: runSeq,
  run_seq: runSeq,
  type,
  occurred_at: occurredAt ?? '2026-08-22T00:00:00Z',
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

  it('tool 事件按 call_id 折叠：args_summary 上标题，completed 输出挂 detail，failed 转 error', () => {
    const timelines = {
      run_1: [
        entry('run_1', 1, 'tool.started', { tool: 'shell', call_id: 'c1', args_summary: 'ls -la' }),
        entry('run_1', 2, 'tool.completed', { call_id: 'c1', output: 'file.go' }),
        entry('run_1', 3, 'tool.started', { tool: 'shell', call_id: 'c2', args_summary: 'rm x' }),
        entry('run_1', 4, 'tool.failed', { call_id: 'c2', output: 'permission denied' }),
        entry('run_1', 5, 'tool.completed', { call_id: 'c3', output: 'orphan' }),
        entry('run_1', 6, 'tool.completed', { call_id: 'c4' }), // 无输出不刷屏
      ],
    };
    const msgs = buildMessages(['run_1'], timelines);
    expect(msgs.map((m) => [m.kind, m.text, m.detail ?? ''])).toEqual([
      ['tool', '调用工具 shell：ls -la', 'file.go'],
      ['error', '工具失败 shell：rm x', 'permission denied'],
      ['tool', '工具输出', 'orphan'],
    ]);
  });

  it('exit_code 落 exitCode 字段（text 不再拼后缀）；非零转 error 行（红色），零保持 tool，无输出也展示', () => {
    const timelines = {
      run_1: [
        entry('run_1', 1, 'tool.started', { tool: 'shell', call_id: 'c1', args_summary: 'ls' }),
        entry('run_1', 2, 'tool.completed', { call_id: 'c1', output: 'a.go', exit_code: 0 }),
        entry('run_1', 3, 'tool.started', { tool: 'shell', call_id: 'c2', args_summary: 'npm i' }),
        entry('run_1', 4, 'tool.completed', { call_id: 'c2', output: 'err', exit_code: 134 }),
        entry('run_1', 5, 'tool.started', { tool: 'shell', call_id: 'c3', args_summary: 'false' }),
        entry('run_1', 6, 'tool.completed', { call_id: 'c3', exit_code: 1 }), // 无输出但非零：不吞
        entry('run_1', 7, 'tool.failed', { call_id: 'c4', output: 'killed', exit_code: 137 }),
      ],
    };
    const msgs = buildMessages(['run_1'], timelines);
    expect(msgs.map((m) => [m.kind, m.text, m.detail ?? '', m.exitCode])).toEqual([
      ['tool', '调用工具 shell：ls', 'a.go', 0],
      ['error', '调用工具 shell：npm i', 'err', 134],
      ['error', '调用工具 shell：false', '', 1],
      ['error', '工具调用失败', 'killed', 137],
    ]);
  });

  it('tool.started 带 args_summary/args：原值落 argsSummary/args 字段，text 仍为前缀包装形态', () => {
    const timelines = {
      run_1: [
        entry('run_1', 1, 'tool.started', {
          tool: 'shell',
          call_id: 'c1',
          args_summary: 'ls -la',
          args: '{"command":"ls -la"}',
        }),
        entry('run_1', 2, 'tool.started', { tool: 'Grep', call_id: 'c2', args_summary: 'TODO' }),
      ],
    };
    const msgs = buildMessages(['run_1'], timelines);
    expect(msgs[0].argsSummary).toBe('ls -la');
    expect(msgs[0].args).toBe('{"command":"ls -la"}');
    expect(msgs[0].text).toBe('调用工具 shell：ls -la');
    // 后端 canonical 未带 args 时字段缺席，不落空串噪音。
    expect(msgs[1].argsSummary).toBe('TODO');
    expect(msgs[1].args).toBeUndefined();
  });

  it('跨多个 run 按顺序拼接（串行轮次）', () => {
    const timelines = {
      run_1: [entry('run_1', 1, 'run.created', { instruction: '第一轮' })],
      run_2: [entry('run_2', 1, 'run.created', { instruction: '第二轮' })],
    };
    const msgs = buildMessages(['run_1', 'run_2'], timelines);
    expect(msgs.map((m) => m.text)).toEqual(['第一轮', '第二轮']);
  });

  it('reasoning-delta 在 completed 前聚合为 thinking 气泡', () => {
    const timelines = {
      run_3: [
        entry('run_3', 1, 'run.created', { instruction: '你好' }),
        entry('run_3', 2, 'message.delta', { raw: { chunk: { type: 'reasoning-delta', text: '分析中' } } }),
        entry('run_3', 3, 'message.delta', { raw: { chunk: { type: 'reasoning-delta', text: '…' } } }),
        entry('run_3', 4, 'message.completed', { role: 'assistant', text: '收到' }, 'assistant', '收到'),
      ],
    };
    const msgs = buildMessages(['run_3'], timelines);
    expect(msgs.map((m) => [m.kind, m.text])).toEqual([
      ['user', '你好'],
      ['thinking', '分析中…'],
      ['assistant', '收到'],
    ]);
  });

  it('工具调用后缺失 message.completed 时回落累积的 text-delta', () => {
    const timelines = {
      run_4: [
        entry('run_4', 1, 'run.created', { instruction: '列目录' }),
        entry('run_4', 2, 'message.completed', { role: 'assistant', text: '先看下' }, 'assistant', '先看下'),
        entry('run_4', 3, 'tool.started', { tool: 'shell', call_id: 'c1', args_summary: 'ls' }),
        entry('run_4', 4, 'tool.completed', { call_id: 'c1', output: 'web/' }),
        entry('run_4', 5, 'message.delta', { raw: { chunk: { type: 'text-delta', text: '目录如下' } } }),
      ],
    };
    const msgs = buildMessages(['run_4'], timelines);
    expect(msgs.map((m) => [m.kind, m.text])).toEqual([
      ['user', '列目录'],
      ['assistant', '先看下'],
      ['tool', '调用工具 shell：ls'],
      ['assistant', '目录如下'],
    ]);
  });

  it('工具行携带卡片数据：started→running+startedAt，completed→success+completedAt，failed→failed，非零 exit→failed', () => {
    const t0 = '2026-08-22T00:00:00.000Z';
    const timelines = {
      run_1: [
        entry('run_1', 1, 'tool.started', { tool: 'Read', call_id: 'c1', args_summary: 'a.go' }, undefined, undefined, t0),
        entry('run_1', 2, 'tool.completed', { call_id: 'c1', output: 'ok' }, undefined, undefined, '2026-08-22T00:00:02.500Z'),
        entry('run_1', 3, 'tool.started', { tool: 'Bash', call_id: 'c2', args_summary: 'npm i' }, undefined, undefined, '2026-08-22T00:00:03.000Z'),
        entry('run_1', 4, 'tool.failed', { call_id: 'c2', output: 'boom' }, undefined, undefined, '2026-08-22T00:00:09.000Z'),
        entry('run_1', 5, 'tool.completed', { call_id: 'c3', output: 'orphan' }, undefined, undefined, '2026-08-22T00:00:10.000Z'),
        entry('run_1', 6, 'tool.started', { tool: 'Bash', call_id: 'c4', args_summary: 'false' }, undefined, undefined, '2026-08-22T00:00:11.000Z'),
        entry('run_1', 7, 'tool.completed', { call_id: 'c4', exit_code: 1 }, undefined, undefined, '2026-08-22T00:00:12.000Z'),
      ],
    };
    const msgs = buildMessages(['run_1'], timelines);
    expect(
      msgs.map((m) => [m.kind, m.tool, m.toolStatus, m.startedAt, m.completedAt]),
    ).toEqual([
      ['tool', 'Read', 'success', t0, '2026-08-22T00:00:02.500Z'],
      ['error', 'Bash', 'failed', '2026-08-22T00:00:03.000Z', '2026-08-22T00:00:09.000Z'],
      ['tool', undefined, 'success', undefined, '2026-08-22T00:00:10.000Z'],
      ['error', 'Bash', 'failed', '2026-08-22T00:00:11.000Z', '2026-08-22T00:00:12.000Z'],
    ]);
  });

  it('仅有 started 的工具行保持 running（进行中卡片）', () => {
    const timelines = {
      run_1: [entry('run_1', 1, 'tool.started', { tool: 'Grep', call_id: 'c1', args_summary: 'TODO' })],
    };
    const msgs = buildMessages(['run_1'], timelines);
    expect(msgs[0].toolStatus).toBe('running');
    expect(msgs[0].startedAt).toBe('2026-08-22T00:00:00Z');
    expect(msgs[0].completedAt).toBeUndefined();
  });

  it('tool.progress 追加到同 call_id 既有工具行的 liveOutput（不新增行）', () => {
    const timelines = {
      run_1: [
        entry('run_1', 1, 'tool.started', { tool: 'shell', call_id: 'c1', args_summary: 'npm i' }),
        entry('run_1', 2, 'tool.progress', { call_id: 'c1', text: 'downloading…\n' }),
        entry('run_1', 3, 'tool.progress', { call_id: 'c1', text: 'done' }),
        entry('run_1', 4, 'tool.progress', { call_id: 'c1', text: '', percent: 50 }), // 空 text：忽略
        entry('run_1', 5, 'tool.progress', { text: 'no-call-id' }), // 无 call_id：忽略
      ],
    };
    const msgs = buildMessages(['run_1'], timelines);
    expect(msgs).toHaveLength(1);
    expect(msgs[0].liveOutput).toBe('downloading…\ndone');
    expect(msgs[0].toolStatus).toBe('running');
  });

  it('tool.progress 先于 started（回放乱序）：创建 running 占位行，started 到达后原地补全不重复', () => {
    const timelines = {
      run_1: [
        entry('run_1', 1, 'tool.progress', { tool: 'shell', call_id: 'c1', text: 'partial' }),
        entry('run_1', 2, 'tool.started', { tool: 'shell', call_id: 'c1', args_summary: 'ls' }),
      ],
    };
    const msgs = buildMessages(['run_1'], timelines);
    expect(msgs).toHaveLength(1); // 同键折叠，不产生重复行
    expect(msgs[0].key).toBe('run_1-tool-c1');
    expect(msgs[0].text).toBe('调用工具 shell：ls');
    expect(msgs[0].argsSummary).toBe('ls');
    expect(msgs[0].liveOutput).toBe('partial'); // 占位期累积的流式输出不丢
    expect(msgs[0].toolStatus).toBe('running');
  });

  it('liveOutput 累计上限 4000：超出保留尾部（最新内容最有价值）', () => {
    const timelines = {
      run_1: [
        entry('run_1', 1, 'tool.started', { tool: 'shell', call_id: 'c1', args_summary: 'ls' }),
        entry('run_1', 2, 'tool.progress', { call_id: 'c1', text: 'x'.repeat(4_500) }),
        entry('run_1', 3, 'tool.progress', { call_id: 'c1', text: 'tail' }),
      ],
    };
    const msgs = buildMessages(['run_1'], timelines);
    expect(msgs[0].liveOutput?.length).toBe(4_000);
    expect(msgs[0].liveOutput?.endsWith('xtail')).toBe(true);
  });

  it('completed 落定清除 liveOutput：无 output 时流式输出落 detail，有 output 时 output 优先', () => {
    const timelines = {
      run_1: [
        entry('run_1', 1, 'tool.started', { tool: 'shell', call_id: 'c1', args_summary: 'ls' }),
        entry('run_1', 2, 'tool.progress', { call_id: 'c1', text: 'streamed' }),
        entry('run_1', 3, 'tool.completed', { call_id: 'c1' }), // 无 output 无 exit：靠 liveOutput 落定
        entry('run_1', 4, 'tool.started', { tool: 'shell', call_id: 'c2', args_summary: 'ls' }),
        entry('run_1', 5, 'tool.progress', { call_id: 'c2', text: 'streamed2' }),
        entry('run_1', 6, 'tool.completed', { call_id: 'c2', output: 'final' }),
        entry('run_1', 7, 'tool.started', { tool: 'shell', call_id: 'c3', args_summary: 'ls' }),
        entry('run_1', 8, 'tool.progress', { call_id: 'c3', text: 'streamed3' }),
        entry('run_1', 9, 'tool.failed', { call_id: 'c3' }), // failed 无 output：流式输出也不丢
      ],
    };
    const msgs = buildMessages(['run_1'], timelines);
    expect(msgs.map((m) => [m.toolStatus, m.detail, m.liveOutput])).toEqual([
      ['success', 'streamed', undefined],
      ['success', 'final', undefined],
      ['failed', 'streamed3', undefined],
    ]);
  });

  it('run.plan_updated 同 run 新帧替换旧帧：单卡最新快照，位置保持首现处，key 稳定', () => {
    const timelines = {
      run_1: [
        entry('run_1', 1, 'run.created', { instruction: '做点什么' }),
        entry('run_1', 2, 'run.plan_updated', {
          steps: [
            { step: '调研', status: 'completed' },
            { step: '实现', status: 'in_progress' },
          ],
        }),
        entry('run_1', 3, 'tool.started', { tool: 'Bash', call_id: 'c1', args_summary: 'ls' }),
        entry('run_1', 4, 'run.plan_updated', {
          steps: [
            { step: '调研', status: 'completed' },
            { step: '实现', status: 'completed' },
            { step: '验证', status: 'pending' },
          ],
        }),
      ],
    };
    const msgs = buildMessages(['run_1'], timelines);
    expect(msgs.filter((m) => m.kind === 'plan')).toHaveLength(1);
    const plan = msgs.find((m) => m.kind === 'plan');
    expect(plan?.key).toBe('run_1-plan');
    expect(plan?.steps?.map((s) => [s.step, s.status])).toEqual([
      ['调研', 'completed'],
      ['实现', 'completed'],
      ['验证', 'pending'],
    ]);
    // 位置保持首现处（用户气泡后、工具行前），不被新帧顶到流尾。
    expect(msgs.map((m) => m.kind)).toEqual(['user', 'plan', 'tool']);
  });

  it('run.plan_updated 载荷无效时不产生卡片（防御式，不报错）', () => {
    const timelines = {
      run_1: [
        entry('run_1', 1, 'run.created', { instruction: 'go' }),
        entry('run_1', 2, 'run.plan_updated', {}),
        entry('run_1', 3, 'run.plan_updated', { steps: 'not-array' }),
        entry('run_1', 4, 'run.plan_updated', { steps: [] }),
        entry('run_1', 5, 'run.plan_updated', { steps: [{ step: '', status: 'pending' }, { step: 42 }, { step: 'x', status: 'bogus' }] }),
      ],
    };
    const msgs = buildMessages(['run_1'], timelines);
    expect(msgs.some((m) => m.kind === 'plan')).toBe(false);
  });
});

describe('parsePlanSteps', () => {
  it('仅认 step 非空字符串 + 三态 status 的条目，顺序保留', () => {
    expect(
      parsePlanSteps({
        steps: [
          { step: 'a', status: 'pending' },
          { step: 'b', status: 'in_progress' },
          { step: 'c', status: 'completed' },
        ],
      }),
    ).toEqual([
      { step: 'a', status: 'pending' },
      { step: 'b', status: 'in_progress' },
      { step: 'c', status: 'completed' },
    ]);
  });

  it('steps 缺失/非数组/无有效条目返回 null', () => {
    expect(parsePlanSteps()).toBeNull();
    expect(parsePlanSteps({})).toBeNull();
    expect(parsePlanSteps({ steps: null })).toBeNull();
    expect(parsePlanSteps({ steps: [{ step: 'a', status: 'done' }] })).toBeNull();
  });
});

describe('sessionLine', () => {
  it('tier×reason 全量映射', () => {
    expect(sessionLine({ tier: 'resume', reason: 'resume_hit' })).toBe('已续接会话');
    expect(sessionLine({ tier: 'rotation', reason: 'budget' })).toBe('已轮换新会话（预算）');
    expect(sessionLine({ tier: 'rotation', reason: 'threshold' })).toBe('已轮换新会话（阈值）');
    expect(sessionLine({ tier: 'rotation' })).toBe('已轮换新会话');
    expect(sessionLine({ tier: 'inline', reason: 'session_unknown' })).toBe('已重建会话（自愈）');
    expect(sessionLine({ tier: 'inline', reason: 'config_drift' })).toBe('已重建会话（配置漂移）');
    expect(sessionLine({ tier: 'inline', reason: 'fresh' })).toBe('已开新会话');
    expect(sessionLine({ tier: 'compacted' })).toBe('会话已压缩');
  });

  it('未知 tier/inline 未知 reason/缺输入返回 null（不渲染）', () => {
    expect(sessionLine()).toBeNull();
    expect(sessionLine({ tier: 'mystery' })).toBeNull();
    expect(sessionLine({ tier: '' })).toBeNull();
    expect(sessionLine({ tier: 'inline', reason: 'future_reason' })).toBeNull();
  });
});

describe('buildMessages 会话元信息行', () => {
  it('session.decision 渲染居中 system 行；session.compacted 渲染压缩行', () => {
    const timelines = {
      run_1: [
        entry('run_1', 1, 'run.created', { instruction: '继续' }),
        entry('run_1', 2, 'session.decision', { tier: 'resume', reason: 'resume_hit', session_ref: 'sess_9' }),
        entry('run_1', 3, 'message.completed', { role: 'assistant', text: '好的' }, 'assistant', '好的'),
        entry('run_1', 4, 'session.compacted', {}),
      ],
    };
    const msgs = buildMessages(['run_1'], timelines);
    expect(msgs.map((m) => [m.kind, m.text])).toEqual([
      ['user', '继续'],
      ['system', '已续接会话'],
      ['assistant', '好的'],
      ['system', '会话已压缩'],
    ]);
  });

  it('未知 tier 的 decision 不产生行（防御式）', () => {
    const timelines = {
      run_1: [
        entry('run_1', 1, 'session.decision', { tier: 'quantum' }),
      ],
    };
    expect(buildMessages(['run_1'], timelines)).toHaveLength(0);
  });
});

describe('aggregateRunStream', () => {
  it('聚合 reasoning 与 text 草稿', () => {
    const entries = [
      entry('run_1', 1, 'message.delta', { raw: { chunk: { type: 'reasoning-delta', text: '想' } } }),
      entry('run_1', 2, 'message.delta', { raw: { chunk: { type: 'text-delta', text: '收' } } }),
    ];
    expect(aggregateRunStream(entries)).toEqual({ reasoning: '想', answerDraft: '收' });
  });

  it('extractDeltaChunk 解析 raw.chunk', () => {
    expect(extractDeltaChunk({ raw: { chunk: { type: 'reasoning-delta', text: 'x' } } })).toEqual({
      type: 'reasoning-delta',
      text: 'x',
    });
  });
});

describe('extractExitCode', () => {
  it('仅认有限数值，其余形态忽略', () => {
    expect(extractExitCode({ exit_code: 3 })).toBe(3);
    expect(extractExitCode({ exit_code: 0 })).toBe(0);
    expect(extractExitCode({ exit_code: '3' })).toBeUndefined();
    expect(extractExitCode({ exit_code: null })).toBeUndefined();
    expect(extractExitCode({})).toBeUndefined();
    expect(extractExitCode(undefined)).toBeUndefined();
  });
});

describe('conversationLabel', () => {
  const base: WorkItem = {
    id: 'wi_1',
    workspace_id: 'ws_1',
    title: '测试',
    description: '',
    status: 'in_progress',
    priority: 'medium',
    due_date: null,
    version: 1,
    runs_count: 1,
    created_at: '',
    updated_at: '',
  };

  it('run 已成功时显示已回复', () => {
    const label = conversationLabel(
      { ...base, latest_run_id: 'run_1' },
      { run_1: { status: 'succeeded' } },
    );
    expect(label).toBe('已回复');
  });

  it('phase=review 且无 run 快照时显示已回复', () => {
    expect(conversationLabel({ ...base, phase: 'review' }, {})).toBe('已回复');
  });

  it('run 失败时显示失败', () => {
    expect(
      conversationLabel({ ...base, latest_run_id: 'run_1' }, { run_1: { status: 'failed' } }),
    ).toBe('失败');
  });
});

describe('currentRunSnapshot', () => {
  it('SSE 已更新为终态时覆盖会话列表中的旧 queued 状态', () => {
    const local = { id: 'run_1', status: 'queued' } as ExecutionRun;
    const terminal = { id: 'run_1', status: 'succeeded' } as ExecutionRun;
    expect(currentRunSnapshot(local, { run_1: terminal })?.status).toBe('succeeded');
  });
});

describe('chat.store refresh 请求序号守卫', () => {
  const json = (body: unknown) =>
    new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } });

  beforeEach(() => {
    useWorkspaceStore.setState({
      workspace: { id: 'ws_1', name: 'w', timezone: 'UTC', version: 1 },
    });
    useChatStore.setState({
      agentId: 'agent_1',
      conversationId: null,
      conversations: [],
      runs: [],
      sending: false,
    });
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('快速切换会话：慢返回的旧 refreshRuns 响应不覆盖新会话 runs', async () => {
    const runA = { id: 'run_a', work_item_id: 'wi_a', created_at: '2026-08-22T00:00:01Z' } as ExecutionRun;
    const runB = { id: 'run_b', work_item_id: 'wi_b', created_at: '2026-08-22T00:00:01Z' } as ExecutionRun;
    let resolveA: (r: Response) => void = () => {};
    const pendingA = new Promise<Response>((res) => {
      resolveA = res;
    });
    const fetchMock = vi.fn((input: RequestInfo | URL) =>
      String(input).includes('wi_a') ? pendingA : Promise.resolve(json({ items: [runB] })),
    );
    vi.stubGlobal('fetch', fetchMock);

    // 打开会话 A（响应挂起）→ 立即切到会话 B（响应先回）。
    useChatStore.getState().openConversation('wi_a');
    await vi.waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1));
    useChatStore.getState().openConversation('wi_b');
    await vi.waitFor(() =>
      expect(useChatStore.getState().runs.map((r) => r.id)).toEqual(['run_b']),
    );

    // 慢的旧响应此时才到达：必须被丢弃，不得回写会话 A 的数据。
    resolveA(json({ items: [runA] }));
    await vi.waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
    await new Promise((r) => setTimeout(r, 0));
    expect(useChatStore.getState().runs.map((r) => r.id)).toEqual(['run_b']);
  });

  it('快速切换 agent：慢返回的旧 refreshConversations 响应不覆盖新会话列表', async () => {
    const convA = {
      id: 'wi_a',
      workspace_id: 'ws_1',
      title: 'A',
      description: '',
      status: 'todo',
      priority: 'medium',
      due_date: null,
      runs_count: 0,
      version: 1,
      created_at: '',
      updated_at: '',
    } as WorkItem;
    const convB = { ...convA, id: 'wi_b', title: 'B' };
    let resolveA: (r: Response) => void = () => {};
    const pendingA = new Promise<Response>((res) => {
      resolveA = res;
    });
    const fetchMock = vi.fn((input: RequestInfo | URL) =>
      String(input).includes('agent_1') ? pendingA : Promise.resolve(json({ items: [convB], next_cursor: null })),
    );
    vi.stubGlobal('fetch', fetchMock);

    useChatStore.getState().selectAgent('agent_1');
    await vi.waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1));
    useChatStore.getState().selectAgent('agent_2');
    await vi.waitFor(() =>
      expect(useChatStore.getState().conversations.map((c) => c.id)).toEqual(['wi_b']),
    );

    resolveA(json({ items: [convA], next_cursor: null }));
    await vi.waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
    await new Promise((r) => setTimeout(r, 0));
    expect(useChatStore.getState().conversations.map((c) => c.id)).toEqual(['wi_b']);
  });
});

describe('formatTokenUsage', () => {
  it('used 缺失/非法时返回 null：UI 不渲染、不报错', () => {
    expect(formatTokenUsage()).toBeNull();
    expect(formatTokenUsage(0)).toBeNull();
    expect(formatTokenUsage(-42)).toBeNull();
    expect(formatTokenUsage(Number.NaN)).toBeNull();
    expect(formatTokenUsage(Number.POSITIVE_INFINITY)).toBeNull();
  });

  it('只有 used 时渲染 {used}k tokens：k 取整，不足 1k 记 1k', () => {
    expect(formatTokenUsage(300)).toBe('1k tokens');
    expect(formatTokenUsage(1_400)).toBe('1k tokens');
    expect(formatTokenUsage(1_500)).toBe('2k tokens');
    expect(formatTokenUsage(16_400)).toBe('16k tokens');
  });

  it('窗口可得时渲染 {used}k / {window}k；窗口非法降级为只显 used', () => {
    expect(formatTokenUsage(1_500, 128_000)).toBe('2k / 128k tokens');
    expect(formatTokenUsage(1_500, 0)).toBe('2k tokens');
    expect(formatTokenUsage(1_500, Number.NaN)).toBe('2k tokens');
  });
});

describe('toolDuration', () => {
  const at = (s: number) => new Date(s).toISOString();

  it('<1s 显示 ms；<60s 显示 1 位小数 s（整数不带小数点）', () => {
    expect(toolDuration(at(0), at(450))).toBe('450ms');
    expect(toolDuration(at(0), at(999))).toBe('999ms');
    expect(toolDuration(at(0), at(1_500))).toBe('1.5s');
    expect(toolDuration(at(0), at(2_000))).toBe('2s');
    expect(toolDuration(at(0), at(59_400))).toBe('59.4s');
  });

  it('≥60s 显示 m+s；跨分钟余数四舍五入到秒', () => {
    expect(toolDuration(at(0), at(60_000))).toBe('1m 0s');
    expect(toolDuration(at(0), at(125_000))).toBe('2m 5s');
  });

  it('缺时间/不可解析/负差值返回 null（不渲染徽章）', () => {
    expect(toolDuration()).toBeNull();
    expect(toolDuration(at(0))).toBeNull();
    expect(toolDuration('not-a-date', at(0))).toBeNull();
    expect(toolDuration(at(1_000), at(0))).toBeNull();
  });
});

describe('buildForkContext', () => {
  const msg = (key: string, kind: ChatMessage['kind'], text: string, tool?: string): ChatMessage => ({
    key,
    runId: 'run_1',
    kind,
    text,
    at: '',
    ...(tool ? { tool } : {}),
  });

  it('截取 [0..atKey]：用户/助手交替；thinking/plan/system 跳过', () => {
    const messages = [
      msg('k1', 'user', '第一问'),
      msg('k2', 'thinking', '内部推理不进包'),
      msg('k3', 'assistant', '第一答'),
      msg('k4', 'plan', ''),
      msg('k5', 'system', '已续接会话'),
      msg('k6', 'user', '第二问'),
      msg('k7', 'assistant', '第二答'),
    ];
    expect(buildForkContext(messages, 'k7')).toBe(
      `${FORK_CONTEXT_MARKER}以下是此前对话的记录（用户/助手交替）：\n\n用户：第一问\n助手：第一答\n用户：第二问\n助手：第二答`,
    );
    // 锚点之前的消息不进包（截断在锚点处）。
    expect(buildForkContext(messages, 'k3')).not.toContain('第二问');
  });

  it('atKey 不存在返回 null（调用方不分叉）', () => {
    expect(buildForkContext([msg('k1', 'user', 'hi')], 'nope')).toBeNull();
    expect(buildForkContext([], 'k1')).toBeNull();
  });

  it('tool/error 折叠为一行 [工具] 标记：正文与 detail 不进包', () => {
    const messages = [
      msg('k1', 'user', '跑一下'),
      { ...msg('k2', 'tool', '调用工具 shell：ls'), tool: 'shell', detail: 'file.go' },
      msg('k3', 'error', '工具失败 shell', 'shell'),
      msg('k4', 'assistant', '完成'),
    ];
    const out = buildForkContext(messages, 'k4');
    expect(out?.split('\n')).toEqual([
      `${FORK_CONTEXT_MARKER}以下是此前对话的记录（用户/助手交替）：`,
      '',
      '用户：跑一下',
      '[工具] shell',
      '[工具] shell',
      '助手：完成',
    ]);
  });

  it('超 4000 字符保头截断，尾部加截断标记', () => {
    const messages = [msg('k1', 'user', 'x'.repeat(5_000)), msg('k2', 'assistant', '尾部不应出现')];
    const out = buildForkContext(messages, 'k2');
    expect(out).not.toBeNull();
    expect(out!.startsWith(`${FORK_CONTEXT_MARKER}以下是此前对话的记录（用户/助手交替）：\n\n用户：`)).toBe(true);
    expect(out!.endsWith('\n…（已截断）')).toBe(true);
    expect(out!.length).toBe(4_000 + '\n…（已截断）'.length);
    expect(out).not.toContain('尾部不应出现');
  });
});

describe('send 队列语义（不再 steering）', () => {
  const json = (body: unknown) =>
    new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } });

  const run = (id: string, status: string): ExecutionRun =>
    ({ id, work_item_id: 'wi_1', status, created_at: '2026-08-22T00:00:00Z' }) as ExecutionRun;

  /** createRun 的 POST 调用（URL 以 /runs 结尾且 method=POST）。 */
  const createRunCalls = (mock: ReturnType<typeof vi.fn>) =>
    mock.mock.calls.filter(([u, init]) => {
      const url = String(u);
      return (init?.method ?? '') === 'POST' && /\/work-items\/[^/]+\/runs$/.test(url);
    });

  const inputCalls = (mock: ReturnType<typeof vi.fn>) =>
    mock.mock.calls.filter(([u, init]) => String(u).includes('/commands/input') && (init?.method ?? '') === 'POST');

  /** 通用 fetch 桩：createRun POST 返回固定 run_id，其余 GET 回空列表。 */
  const stubFetch = () => {
    const mock = vi.fn((input: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
      if ((init?.method ?? 'GET') === 'POST' && /\/work-items\/[^/]+\/runs$/.test(String(input))) {
        return Promise.resolve(
          json({ run_id: 'run_new', work_item_id: 'wi_1', status: 'queued', version: 1, capability_snapshot_id: null }),
        );
      }
      return Promise.resolve(json({ items: [], next_cursor: null }));
    });
    vi.stubGlobal('fetch', mock);
    return mock;
  };

  beforeEach(() => {
    useWorkspaceStore.setState({
      workspace: { id: 'ws_1', name: 'w', timezone: 'UTC', version: 1 },
    });
    useChatStore.setState({
      agentId: 'agent_1',
      conversationId: 'wi_1',
      conversations: [],
      runs: [],
      sending: false,
      queue: [],
    });
    // 屏蔽 watchRun/tasks refresh 的连带请求：本组只断言 send/drain 的出网面。
    useRunsStore.setState({ runs: {}, timelines: {}, watchRun: () => {} });
    useTasksStore.setState({ refresh: async () => {} });
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('最新 run 活跃：send 只入队，不调 sendRunInput 也不 createRun', async () => {
    const fetchMock = stubFetch();
    useChatStore.setState({ runs: [run('run_1', 'running')] });
    await useChatStore.getState().send('  追加指令  ');
    expect(useChatStore.getState().queue).toEqual(['追加指令']); // 入队前 trim
    expect(inputCalls(fetchMock)).toHaveLength(0); // steering 面不再触达
    expect(createRunCalls(fetchMock)).toHaveLength(0);
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it('终态 + 队列非空：先入队再出队首条 createRun（FIFO）', async () => {
    const fetchMock = stubFetch();
    useChatStore.setState({ runs: [run('run_1', 'succeeded')], queue: ['第一条', '第二条'] });
    await useChatStore.getState().send('第三条');
    const creates = createRunCalls(fetchMock);
    expect(creates).toHaveLength(1); // 只发队头，不整队连发
    expect(JSON.parse(String(creates[0][1]?.body)).input.instruction).toBe('第一条');
    expect(useChatStore.getState().queue).toEqual(['第二条', '第三条']); // 本条排到队尾
    expect(inputCalls(fetchMock)).toHaveLength(0);
  });

  it('队空 + 终态：直接 createRun 本条（现行为）', async () => {
    const fetchMock = stubFetch();
    useChatStore.setState({ runs: [run('run_1', 'succeeded')] });
    await useChatStore.getState().send('新消息');
    const creates = createRunCalls(fetchMock);
    expect(creates).toHaveLength(1);
    expect(JSON.parse(String(creates[0][1]?.body)).input.instruction).toBe('新消息');
    expect(useChatStore.getState().queue).toEqual([]);
  });

  it('removeQueued 按下标移除对应条目', () => {
    const s = useChatStore.getState();
    s.enqueue('a');
    s.enqueue('b');
    s.enqueue('c');
    s.removeQueued(1);
    expect(useChatStore.getState().queue).toEqual(['a', 'c']);
  });

  it('drainQueue：出队首条 createRun，剩余保留', async () => {
    const fetchMock = stubFetch();
    useChatStore.setState({ runs: [run('run_1', 'succeeded')], queue: ['head', 'tail'] });
    await useChatStore.getState().drainQueue();
    const creates = createRunCalls(fetchMock);
    expect(creates).toHaveLength(1);
    expect(JSON.parse(String(creates[0][1]?.body)).input.instruction).toBe('head');
    expect(useChatStore.getState().queue).toEqual(['tail']);
    expect(useChatStore.getState().sending).toBe(false); // 复位 sending 闸
  });

  it('drainQueue 空队列/无会话：不触网', async () => {
    const fetchMock = stubFetch();
    useChatStore.setState({ queue: [] });
    await useChatStore.getState().drainQueue();
    useChatStore.setState({ queue: ['x'], conversationId: null });
    await useChatStore.getState().drainQueue();
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it('分叉会话首发：description 上下文包拼进首条 instruction', async () => {
    const fetchMock = stubFetch();
    const description = `${FORK_CONTEXT_MARKER}以下是此前对话的记录（用户/助手交替）：\n\n用户：第一问\n助手：第一答`;
    const forkWi = {
      id: 'wi_fork',
      workspace_id: 'ws_1',
      title: '原会话（分叉）',
      description,
      status: 'todo',
      priority: 'medium',
      due_date: null,
      runs_count: 0,
      version: 1,
      created_at: '',
      updated_at: '',
      parent_id: 'wi_src',
    } as WorkItem;
    useChatStore.setState({ conversationId: 'wi_fork', conversations: [forkWi], runs: [], queue: [] });
    await useChatStore.getState().send('新指令');
    const creates = fetchMock.mock.calls.filter(
      ([u, init]) => /\/work-items\/wi_fork\/runs$/.test(String(u)) && (init?.method ?? '') === 'POST',
    );
    expect(creates).toHaveLength(1);
    expect(JSON.parse(String(creates[0][1]?.body)).input.instruction).toBe(`${description}\n\n【用户新指令】\n新指令`);
  });

  it('分叉会话已有 run 后续轮：不再注入上下文包', async () => {
    const fetchMock = stubFetch();
    useChatStore.setState({
      conversations: [
        {
          id: 'wi_1',
          workspace_id: 'ws_1',
          title: '分叉会话',
          description: `${FORK_CONTEXT_MARKER}旧上下文`,
          status: 'todo',
          priority: 'medium',
          due_date: null,
          runs_count: 1,
          version: 1,
          created_at: '',
          updated_at: '',
        } as WorkItem,
      ],
      runs: [run('run_1', 'succeeded')],
      queue: [],
    });
    await useChatStore.getState().send('第二轮');
    const creates = createRunCalls(fetchMock);
    expect(creates).toHaveLength(1);
    expect(JSON.parse(String(creates[0][1]?.body)).input.instruction).toBe('第二轮');
  });
});

describe('forkConversation', () => {
  const json = (body: unknown) =>
    new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } });

  beforeEach(() => {
    useWorkspaceStore.setState({
      workspace: { id: 'ws_1', name: 'w', timezone: 'UTC', version: 1 },
    });
    useChatStore.setState({
      agentId: 'agent_1',
      conversationId: 'wi_1',
      conversations: [
        {
          id: 'wi_1',
          workspace_id: 'ws_1',
          title: '原会话',
          description: '',
          status: 'todo',
          priority: 'medium',
          due_date: null,
          runs_count: 1,
          version: 1,
          created_at: '',
          updated_at: '',
        } as WorkItem,
      ],
      runs: [{ id: 'run_1', work_item_id: 'wi_1', status: 'succeeded', created_at: '2026-08-22T00:00:00Z' } as ExecutionRun],
      sending: false,
      queue: [],
    });
    useRunsStore.setState({
      runs: {},
      timelines: {
        run_1: [
          entry('run_1', 1, 'run.created', { instruction: '第一问' }),
          entry('run_1', 2, 'message.completed', { role: 'assistant', text: '第一答' }, 'assistant', '第一答'),
        ],
      },
      watchRun: () => {},
    });
    useTasksStore.setState({ refresh: async () => {} });
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('创建带 parent_id 与上下文包的分叉会话并切过去', async () => {
    const forkWi = {
      id: 'wi_fork',
      workspace_id: 'ws_1',
      title: '原会话（分叉）',
      description: '',
      status: 'todo',
      priority: 'medium',
      due_date: null,
      runs_count: 0,
      version: 1,
      created_at: '',
      updated_at: '',
      parent_id: 'wi_1',
    } as WorkItem;
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
      if ((init?.method ?? 'GET') === 'POST' && /\/workspaces\/ws_1\/work-items$/.test(String(input))) {
        return Promise.resolve(json(forkWi));
      }
      return Promise.resolve(json({ items: [], next_cursor: null }));
    });
    vi.stubGlobal('fetch', fetchMock);

    await useChatStore.getState().forkConversation('run_1-2');

    const creates = fetchMock.mock.calls.filter(
      ([u, init]) => /\/workspaces\/ws_1\/work-items$/.test(String(u)) && (init?.method ?? '') === 'POST',
    );
    expect(creates).toHaveLength(1);
    const body = JSON.parse(String(creates[0][1]?.body));
    expect(body.title).toBe('原会话（分叉）');
    expect(body.parent_id).toBe('wi_1');
    expect(body.agent_profile_id).toBe('agent_1');
    expect(body.description).toContain(FORK_CONTEXT_MARKER);
    expect(body.description).toContain('用户：第一问');
    expect(body.description).toContain('助手：第一答');
    expect(useChatStore.getState().conversationId).toBe('wi_fork');
  });

  it('锚点 key 不在投影里：不分叉、不触网', async () => {
    const fetchMock = vi.fn(() => Promise.resolve(json({ items: [], next_cursor: null })));
    vi.stubGlobal('fetch', fetchMock);
    await useChatStore.getState().forkConversation('no-such-key');
    expect(fetchMock).not.toHaveBeenCalled();
    expect(useChatStore.getState().conversationId).toBe('wi_1');
  });
});
