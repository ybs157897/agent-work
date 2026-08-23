import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { aggregateRunStream, buildMessages, conversationLabel, currentRunSnapshot, extractDeltaChunk, useChatStore } from './chat.store';
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
