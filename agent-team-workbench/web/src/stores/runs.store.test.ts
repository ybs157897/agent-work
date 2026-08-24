import { beforeEach, describe, expect, it } from 'vitest';
import type { CanonicalEvent } from '../api/types';
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
    useRunsStore.setState({ runs: {}, timelines: {}, approvals: {}, artifacts: {}, watching: {} });
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

  it('未知聚合类型不消费', () => {
    const ev: CanonicalEvent = {
      ...runEvent(1, 'workspace.updated'),
      aggregate: { type: 'workspace', id: 'ws_1', version: 1 },
    };
    expect(useRunsStore.getState().applyEvent(ev)).toBe(false);
  });
});
