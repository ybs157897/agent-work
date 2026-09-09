import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { CanonicalEvent } from '../api/types';
import type { NativeQuestion } from '../api/questions';
import { useNativeQuestionsStore } from './questions.store';
import { useWorkspaceStore } from './workspace.store';

const question = { id: 'question_1', run_id: 'run_1', status: 'pending' } as NativeQuestion;
const response = { answers: { q_0: { kind: 'single' as const, option_id: 'opt_0_0' } }, method: 'click' as const };
const json = (value: unknown) => new Response(JSON.stringify(value), { headers: { 'Content-Type': 'application/json' } });
const deferred = () => {
  let finish!: (value: Response) => void;
  return { promise: new Promise<Response>((resolve) => { finish = resolve; }), resolve: (value: Response) => finish(value) };
};

beforeEach(() => {
  useNativeQuestionsStore.getState().clear();
  useWorkspaceStore.setState({ selectedWorkspaceId: 'ws_a', workspace: { id: 'ws_a', name: 'A', timezone: 'UTC', version: 1 }, generation: 1 });
});
afterEach(() => { vi.unstubAllGlobals(); });

describe('native question request ownership', () => {
  it.each(['cancelled', 'interrupted', 'succeeded'])('clears %s questions immediately and rejects late pending results', async (status) => {
    const pending = deferred();
    const fetcher = vi.fn(() => pending.promise);
    vi.stubGlobal('fetch', fetcher);
    useNativeQuestionsStore.setState({ itemsByRun: { run_1: [question] } });
    const loading = useNativeQuestionsStore.getState().refresh('run_1');
    const event: CanonicalEvent = { contract_version: 'events/v1', event_id: 'terminal_1', workspace_id: 'ws_a', stream_seq: 2, occurred_at: '2026-09-09T12:00:00Z', type: 'run.status_changed', aggregate: { type: 'execution_run', id: 'run_1', version: 2 }, data: { status } };
    useNativeQuestionsStore.getState().applyEvent(event);
    expect(useNativeQuestionsStore.getState().itemsByRun.run_1).toEqual([]);
    pending.resolve(json({ items: [question] }));
    await loading;
    expect(useNativeQuestionsStore.getState().itemsByRun.run_1).toEqual([]);
    await expect(useNativeQuestionsStore.getState().resolve('run_1', question.id, response)).rejects.toThrow('运行已结束');
    expect(fetcher).toHaveBeenCalledTimes(1);
  });

  it('does not revive a cleared request when the same Run starts a new refresh', async () => {
    const old = deferred();
    vi.stubGlobal('fetch', vi.fn().mockImplementationOnce(() => old.promise).mockResolvedValue(json({ items: [] })));
    const first = useNativeQuestionsStore.getState().refresh('run_1');
    useNativeQuestionsStore.getState().clear();
    await useNativeQuestionsStore.getState().refresh('run_1');
    old.resolve(json({ items: [question] }));
    await first;
    expect(useNativeQuestionsStore.getState().itemsByRun.run_1).toEqual([]);
  });

  it('discards an old Workspace response and does not refresh it after a late answer acknowledgement', async () => {
    const pending = deferred();
    const fetcher = vi.fn(() => pending.promise);
    vi.stubGlobal('fetch', fetcher);
    const resolving = useNativeQuestionsStore.getState().resolve('run_1', question.id, response);
    useWorkspaceStore.setState({ selectedWorkspaceId: 'ws_b', workspace: { id: 'ws_b', name: 'B', timezone: 'UTC', version: 1 }, generation: 2 });
    useNativeQuestionsStore.getState().clear();
    pending.resolve(json({ ...question, status: 'answered' }));
    await resolving;
    expect(useNativeQuestionsStore.getState().itemsByRun).toEqual({});
    expect(useNativeQuestionsStore.getState().submittingByQuestion).toEqual({});
    expect(fetcher).toHaveBeenCalledTimes(1);
  });

  it('prevents duplicate clicks while a typed answer is being submitted', async () => {
    const pending = deferred();
    const fetcher = vi.fn((_: RequestInfo | URL, init?: RequestInit) => init?.method === 'POST' ? pending.promise : Promise.resolve(json({ items: [] })));
    vi.stubGlobal('fetch', fetcher);
    const first = useNativeQuestionsStore.getState().resolve('run_1', question.id, response);
    await useNativeQuestionsStore.getState().resolve('run_1', question.id, response);
    expect(fetcher.mock.calls.filter(([, init]) => init?.method === 'POST')).toHaveLength(1);
    pending.resolve(json({ ...question, status: 'answered' }));
    await first;
    expect(useNativeQuestionsStore.getState().submittingByQuestion[question.id]).toBe(false);
  });

  it('retains a readable loading error for the retry control', async () => {
    vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new Error('问题读取连接中断')));
    await useNativeQuestionsStore.getState().refresh('run_1');
    expect(useNativeQuestionsStore.getState().errorByRun.run_1).toBe('问题读取连接中断');
    expect(useNativeQuestionsStore.getState().loadingByRun.run_1).toBe(false);
  });
});
