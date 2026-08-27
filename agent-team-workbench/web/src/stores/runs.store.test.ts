import { beforeEach, describe, expect, it } from 'vitest';
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
