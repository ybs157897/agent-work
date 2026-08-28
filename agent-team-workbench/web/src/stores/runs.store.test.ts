import { beforeEach, describe, expect, it, vi } from 'vitest';
import type { CanonicalEvent } from '../api/types';
import { buildMessages } from './chat.store';
import { useRunsStore } from './runs.store';

const runEvent = (seq: number, type: string, data?: Record<string, unknown>): CanonicalEvent => ({
  contract_version: 'events/v1',
  event_id: `evt_${seq}`,
  workspace_id: 'ws_1',
  stream_seq: seq,
  aggregate: { type: 'execution_run', id: 'run_1', version: seq },
  run_seq: seq,
  type,
  occurred_at: '2026-08-21T00:00:00Z',
  data,
});

describe('runs.store applyEvent', () => {
  beforeEach(() => {
    useRunsStore.setState({ runs: {}, timelines: {}, approvals: {}, artifacts: {}, watching: {}, historyLoaded: {} });
  });

  it('追加时间线并就地更新已缓存 run 的状态/进度', () => {
    useRunsStore.setState((s) => ({
      runs: {
        ...s.runs,
        run_1: {
          id: 'run_1',
          work_item_id: 'wi_1',
          status: 'queued',
          version: 1,
          created_at: '',
          updated_at: '',
        },
      },
    }));
    const { applyEvent } = useRunsStore.getState();

    expect(applyEvent(runEvent(1, 'run.status_changed', { from: 'queued', status: 'running' }))).toBe(true);
    expect(applyEvent(runEvent(2, 'run.progress_updated', { progress: 0.55 }))).toBe(true);
    expect(applyEvent(runEvent(3, 'message.delta', { text: '正在生成实现方案' }))).toBe(true);

    const s = useRunsStore.getState();
    expect(s.runs.run_1.status).toBe('running');
    expect(s.runs.run_1.progress).toBe(0.55);
    expect(s.timelines.run_1).toHaveLength(3);
    expect(s.timelines.run_1[2].text).toBe('正在生成实现方案');
    expect(s.timelines.run_1[2].run_seq).toBe(3);
  });

  it('usage.updated 就地更新已缓存 run 的用量四字段，且不被后续状态事件清除', () => {
    useRunsStore.setState((s) => ({
      runs: {
        ...s.runs,
        run_1: {
          id: 'run_1',
          work_item_id: 'wi_1',
          status: 'running',
          version: 1,
          created_at: '',
          updated_at: '',
          usage_in: 100,
        },
      },
    }));
    const { applyEvent } = useRunsStore.getState();

    expect(
      applyEvent(runEvent(4, 'usage.updated', { usage_in: 1200, usage_out: 300, usage_cached: 400, usage_basis: 'per_run' })),
    ).toBe(true);
    let run = useRunsStore.getState().runs.run_1;
    expect(run.usage_in).toBe(1200);
    expect(run.usage_out).toBe(300);
    expect(run.usage_cached).toBe(400);
    expect(run.usage_basis).toBe('per_run');

    // 后续仅状态的 SSE 事件（如 succeeding）不得清掉已知用量。
    expect(applyEvent(runEvent(5, 'run.status_changed', { from: 'running', status: 'succeeding' }))).toBe(true);
    run = useRunsStore.getState().runs.run_1;
    expect(run.status).toBe('succeeding');
    expect(run.usage_in).toBe(1200);
    expect(run.usage_basis).toBe('per_run');
  });

  it('SSE 重放同 run_seq 事件时按 run_seq 去重，不重复追加', () => {
    const { applyEvent } = useRunsStore.getState();
    applyEvent(runEvent(10, 'message.completed', { role: 'assistant', text: '你好' }));
    applyEvent(runEvent(10, 'message.completed', { role: 'assistant', text: '你好' }));
    expect(useRunsStore.getState().timelines.run_1).toHaveLength(1);
  });

  it('高频事件超过时间线容量后仍保留最新 Plan 与 Goal 状态快照', () => {
    const { applyEvent } = useRunsStore.getState();
    applyEvent(runEvent(1, 'run.plan_updated', {
      steps: [{ step: '保留计划', status: 'in_progress' }],
    }));
    applyEvent(runEvent(2, 'goal.updated', { objective: '保留目标', status: 'active' }));
    for (let seq = 3; seq <= 505; seq += 1) {
      applyEvent(runEvent(seq, 'message.delta', { text: `delta-${seq}` }));
    }

    const timeline = useRunsStore.getState().timelines.run_1;
    expect(timeline).toHaveLength(500);
    expect(timeline.find((event) => event.type === 'run.plan_updated')?.data).toEqual({
      steps: [{ step: '保留计划', status: 'in_progress' }],
    });
    expect(timeline.find((event) => event.type === 'goal.updated')?.data).toEqual({
      objective: '保留目标',
      status: 'active',
    });
  });

  it('146 个工具调用被尾部 delta 挤压时仍保留 started/terminal 成对生命周期', () => {
    const { applyEvent } = useRunsStore.getState();
    applyEvent(runEvent(1, 'run.created', { status: 'running' }));
    applyEvent(runEvent(2, 'message.completed', { role: 'assistant', text: '开始执行' }));
    let seq = 3;
    for (let index = 1; index <= 146; index += 1) {
      const callId = `call-${index}`;
      applyEvent(runEvent(seq, 'tool.started', { tool: 'Bash', call_id: callId, args_summary: `step-${index}` }));
      seq += 1;
      applyEvent(runEvent(seq, 'tool.completed', { call_id: callId, output: `ok-${index}`, exit_code: 0 }));
      seq += 1;
    }
    for (let index = 0; index < 400; index += 1) {
      applyEvent(runEvent(seq, 'message.delta', { text: `tail-${index}` }));
      seq += 1;
    }

    const timeline = useRunsStore.getState().timelines.run_1;
    expect(timeline).toHaveLength(500);
    const starts = timeline.filter((entry) => entry.type === 'tool.started');
    const terminals = timeline.filter((entry) => entry.type === 'tool.completed' || entry.type === 'tool.failed');
    expect(starts).toHaveLength(146);
    expect(terminals).toHaveLength(146);
    expect(new Set(starts.map((entry) => entry.data?.call_id))).toEqual(
      new Set(terminals.map((entry) => entry.data?.call_id)),
    );

    const messages = buildMessages(['run_1'], { run_1: timeline });
    const toolMessages = messages.filter((message) => message.kind === 'tool');
    expect(toolMessages).toHaveLength(146);
    expect(toolMessages.every((message) => message.tool === 'Bash')).toBe(true);
  });

  it('容量紧张时运行中的 call 保留 started 与最新 progress，并丢弃孤立 terminal', () => {
    const { applyEvent } = useRunsStore.getState();
    applyEvent(runEvent(1, 'tool.started', { tool: 'Read', call_id: 'live', args_summary: 'a.go' }));
    applyEvent(runEvent(2, 'tool.progress', { call_id: 'live', text: 'old' }));
    applyEvent(runEvent(3, 'tool.progress', { call_id: 'live', text: 'latest' }));
    applyEvent(runEvent(4, 'tool.completed', { call_id: 'orphan', output: 'must not render' }));
    for (let seq = 5; seq <= 505; seq += 1) {
      applyEvent(runEvent(seq, 'message.delta', { text: `tail-${seq}` }));
    }

    const timeline = useRunsStore.getState().timelines.run_1;
    expect(timeline.some((entry) => entry.type === 'tool.completed' && entry.data?.call_id === 'orphan')).toBe(false);
    expect(timeline.filter((entry) => entry.data?.call_id === 'live').map((entry) => entry.type)).toEqual([
      'tool.started',
      'tool.progress',
    ]);
    expect(timeline.find((entry) => entry.data?.call_id === 'live' && entry.type === 'tool.progress')?.data?.text).toBe('latest');
  });

  it('未知聚合类型不消费', () => {
    const ev: CanonicalEvent = {
      ...runEvent(1, 'workspace.updated'),
      aggregate: { type: 'workspace', id: 'ws_1', version: 1 },
    };
    expect(useRunsStore.getState().applyEvent(ev)).toBe(false);
  });
});

describe('runs.store 推理折叠（超帽时间线）', () => {
  beforeEach(() => {
    useRunsStore.setState({ runs: {}, timelines: {}, approvals: {}, artifacts: {}, watching: {}, historyLoaded: {} });
  });

  const reasoningDelta = (seq: number, text: string): CanonicalEvent =>
    runEvent(seq, 'message.delta', { raw: { chunk: { text, type: 'reasoning-delta' } }, role: 'assistant' });

  it('推理帧前段被逐出后，completed 锚点携带尾部预算截断的折叠全量推理', () => {
    const { applyEvent } = useRunsStore.getState();
    applyEvent(runEvent(1, 'run.created', { status: 'running' }));
    // 600 条推理 delta（约 6.6KB，超 4000 截断预算）；尾部填充只留最新约 498 条，
    // 最早期帧被逐出 → 触发折叠。
    for (let seq = 2; seq <= 601; seq += 1) applyEvent(reasoningDelta(seq, `推理片段-${seq}-校验;`));
    applyEvent(runEvent(602, 'message.completed', { role: 'assistant', text: '答复' }));

    const timeline = useRunsStore.getState().timelines.run_1;
    expect(timeline.length).toBeLessThanOrEqual(500);
    // 尾部填充留下的是最新推理帧；最早期的 seq=2 已被逐出。
    expect(timeline.some((e) => e.run_seq === 2)).toBe(false);

    const anchor = timeline.find((e) => e.type === 'message.completed');
    const folded = anchor?.data?.reasoning_folded;
    expect(typeof folded).toBe('string');
    expect(anchor?.data?.reasoning_folded_truncated).toBe(true);
    expect((folded as string).length).toBe(4000);
    expect((folded as string).endsWith('推理片段-601-校验;')).toBe(true);

    // 端到端：折叠锚点经 buildMessages 产出思考卡（带省略前缀），不再整层消失。
    const messages = buildMessages(['run_1'], { run_1: timeline });
    const thinking = messages.filter((m) => m.kind === 'thinking');
    expect(thinking).toHaveLength(1);
    expect(thinking[0]?.text).toContain('早期推理已省略');
    expect(thinking[0]?.text).toContain('推理片段-601-校验;');
  });

  it('增量路径：折叠钉住锚点到达时的存活窗口，后续淘汰不再侵蚀（粘滞）', () => {
    const { applyEvent } = useRunsStore.getState();
    applyEvent(runEvent(1, 'run.created', { status: 'running' }));
    // 600 条短推理 delta 逐帧到达：超帽后前段在锚点到达前已被淘汰，
    // 锚点到达时窗口前沿约 d103 ——折叠覆盖「当时仍存活」的全量窗口。
    for (let seq = 2; seq <= 601; seq += 1) applyEvent(reasoningDelta(seq, `d${seq};`));
    applyEvent(runEvent(602, 'message.completed', { role: 'assistant', text: '答复' }));
    // 锚点定稿后再来 100 条正文帧：继续淘汰更老的推理帧，但折叠必须粘滞不动。
    for (let seq = 603; seq <= 702; seq += 1) applyEvent(runEvent(seq, 'message.delta', { text: `tail-${seq}` }));

    const timeline = useRunsStore.getState().timelines.run_1;
    expect(timeline.length).toBeLessThanOrEqual(500);
    // 时间线里 seq≤150 的推理帧已被后续正文帧挤出（对照组）。
    expect(timeline.some((e) => e.run_seq === 150)).toBe(false);
    const anchor = timeline.find((e) => e.type === 'message.completed');
    const folded = anchor?.data?.reasoning_folded;
    expect(typeof folded).toBe('string');
    // 折叠保住锚点到达时的窗口（含现已被逐出的 d103 前沿），未被后续合并重算缩短。
    expect(folded).toContain('d103;');
    expect(folded).toContain('d601;');

    const messages = buildMessages(['run_1'], { run_1: timeline });
    const thinking = messages.filter((m) => m.kind === 'thinking');
    expect(thinking).toHaveLength(1);
    expect(thinking[0]?.text).toBe(folded);
  });

  it('历史回放一次性合并：全量推理随锚点折叠存活（本 bug 的实际场景）', async () => {
    // 用户场景：打开历史会话 → loadHistory 一次拉回全量事件 → 单次截断。
    // 折叠扫描覆盖完整事件集，600 段推理（含最早 d2）全量入锚。
    const items: Array<Record<string, unknown>> = [
      { run_seq: 1, event_type: 'run.created', occurred_at: '2026-08-28T00:00:00Z', payload: { status: 'running' } },
    ];
    for (let seq = 2; seq <= 601; seq += 1) {
      items.push({
        run_seq: seq,
        event_type: 'message.delta',
        occurred_at: '2026-08-28T00:00:00Z',
        payload: { raw: { chunk: { text: `d${seq};`, type: 'reasoning-delta' } }, role: 'assistant' },
      });
    }
    items.push({ run_seq: 602, event_type: 'message.completed', occurred_at: '2026-08-28T00:00:01Z', payload: { role: 'assistant', text: '答复' } });
    vi.stubGlobal('fetch', vi.fn(() =>
      Promise.resolve(new Response(JSON.stringify({ items }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      })),
    ));

    await useRunsStore.getState().loadHistory('run_1');
    const timeline = useRunsStore.getState().timelines.run_1;
    expect(timeline.length).toBeLessThanOrEqual(500);
    expect(timeline.some((e) => e.run_seq === 2)).toBe(false);

    const anchor = timeline.find((e) => e.type === 'message.completed');
    const folded = anchor?.data?.reasoning_folded;
    expect(typeof folded).toBe('string');
    // 600 段约 2.6KB 未超 4000 预算：全量保留、无截断标记。
    expect(anchor?.data?.reasoning_folded_truncated).toBeUndefined();
    expect(folded).toContain('d2;');
    expect(folded).toContain('d601;');

    const messages = buildMessages(['run_1'], { run_1: timeline });
    const thinking = messages.filter((m) => m.kind === 'thinking');
    expect(thinking).toHaveLength(1);
    expect(thinking[0]?.text).toBe(folded);
  });

  it('未超帽时间线不折叠，纯文本/工具帧不产生折叠字段', () => {
    const { applyEvent } = useRunsStore.getState();
    applyEvent(runEvent(1, 'run.created', { status: 'running' }));
    for (let seq = 2; seq <= 40; seq += 1) applyEvent(reasoningDelta(seq, '短推理;'));
    applyEvent(runEvent(41, 'message.completed', { role: 'assistant', text: '答复' }));

    let timeline = useRunsStore.getState().timelines.run_1;
    let anchor = timeline.find((e) => e.type === 'message.completed');
    expect(anchor?.data?.reasoning_folded).toBeUndefined();
    // 原 reasoningBuf 路径不受影响。
    const thinking = buildMessages(['run_1'], { run_1: timeline }).filter((m) => m.kind === 'thinking');
    expect(thinking).toHaveLength(1);
    expect(thinking[0]?.text).toContain('短推理;');

    // 无推理帧的超帽时间线：锚点不挂折叠字段。
    useRunsStore.setState({ runs: {}, timelines: {}, approvals: {}, artifacts: {}, watching: {}, historyLoaded: {} });
    applyEvent(runEvent(1, 'run.created', { status: 'running' }));
    for (let seq = 2; seq <= 601; seq += 1) applyEvent(runEvent(seq, 'message.delta', { text: `正文-${seq}` }));
    applyEvent(runEvent(602, 'message.completed', { role: 'assistant', text: '答复' }));
    timeline = useRunsStore.getState().timelines.run_1;
    anchor = timeline.find((e) => e.type === 'message.completed');
    expect(anchor?.data?.reasoning_folded).toBeUndefined();
    expect(buildMessages(['run_1'], { run_1: timeline }).some((m) => m.kind === 'thinking')).toBe(false);
  });
});
