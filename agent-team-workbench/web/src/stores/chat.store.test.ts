import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { aggregateRunStream, buildMessages, conversationLabel, currentRunSnapshot, extractDeltaChunk, extractExitCode, formatTokenUsage, parsePlanSteps, sessionLine, toolDuration, useChatStore } from './chat.store';
import type { TimelineEntry } from './runs.store';
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

  it('exit_code 追加 · exit {n}；非零转 error 行（红色），零保持 tool，无输出也展示', () => {
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
    expect(msgs.map((m) => [m.kind, m.text, m.detail ?? ''])).toEqual([
      ['tool', '调用工具 shell：ls · exit 0', 'a.go'],
      ['error', '调用工具 shell：npm i · exit 134', 'err'],
      ['error', '调用工具 shell：false · exit 1', ''],
      ['error', '工具调用失败 · exit 137', 'killed'],
    ]);
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
