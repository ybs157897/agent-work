import { afterEach, describe, expect, it, vi } from 'vitest';
import { useWorkspaceStore } from './workspace.store';
import { useChatAnalysisStore } from './chat-analysis.store';
import { writeChatWorkspaceState } from './chat-workspace-state';
import type { ChatAnalysisProjection } from '../api/chat-analysis';

function memoryStorage() {
  const values = new Map<string, string>();
  return {
    getItem: (key: string) => values.get(key) ?? null,
    setItem: (key: string, value: string) => values.set(key, value),
    removeItem: (key: string) => values.delete(key),
    key: (index: number) => [...values.keys()][index] ?? null,
    get length() { return values.size; },
  };
}

const projection = (overrides: Partial<ChatAnalysisProjection> = {}): ChatAnalysisProjection => ({
  workspace_id: 'ws_1', chat_id: 'wi_1', agent_id: 'agent_1', version: 3, revision: 2,
  status: 'needs_answer', pending_count: 1, answered_count: 0, deferred_count: 0,
  document: {
    version: 'chat-analysis/v1', summary: '摘要',
    sources: [{ id: 's1', kind: 'conversation', ref: 'conversation:wi_1', sha256: 'a'.repeat(64), read_status: 'read' }],
    items: [{ id: 'i1', kind: 'requirement', title: '范围', detail: '待确认', source_ids: ['s1'], basis: 'observed', impact: '影响范围', recommendation: '先确认' }],
    questions: [{ id: 'q1', prompt: '选择范围', selection: 'single', options: [{ id: 'a', label: 'A' }, { id: 'b', label: 'B' }], item_ids: ['i1'] }],
  },
  current_question: { id: 'q1', prompt: '选择范围', selection: 'single', options: [{ id: 'a', label: 'A' }, { id: 'b', label: 'B' }], item_ids: ['i1'] },
  answers: [],
  decisions: [],
  ...overrides,
});

describe('Chat analysis store', () => {
  afterEach(() => {
    useChatAnalysisStore.getState().reset();
    vi.unstubAllGlobals();
    useWorkspaceStore.setState({ selectedWorkspaceId: null, workspace: null });
  });

  it('refreshes the server projection and restores a matching local question draft', async () => {
    const storage = memoryStorage();
    vi.stubGlobal('window', { localStorage: storage });
    writeChatWorkspaceState('ws_1', 'agent_1', 'wi_1', {
      composer: { draft: '' },
      queue: [],
      analysisDraft: { version: 3, revision: 2, questionId: 'q1', selectedOptionIds: ['a'], text: '补充说明' },
    });
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(JSON.stringify(projection()), { status: 200, headers: { 'Content-Type': 'application/json' } })));
    useWorkspaceStore.setState({ selectedWorkspaceId: 'ws_1', workspace: { id: 'ws_1', name: 'A', timezone: 'UTC', version: 1 } });

    await useChatAnalysisStore.getState().refresh('ws_1', 'agent_1', 'wi_1');

    expect(useChatAnalysisStore.getState().projection?.current_question?.id).toBe('q1');
    expect(useChatAnalysisStore.getState().draft).toMatchObject({ questionId: 'q1', selectedOptionIds: ['a'], text: '补充说明' });
  });

  it('keeps a failed terminal projection and its server error visible', async () => {
    const failed = {
      workspace_id: 'ws_1', chat_id: 'wi_1', agent_id: 'agent_1', version: 2, revision: 0,
      status: 'failed', run_id: 'run_1', error: 'analysis 输出无效: invalid source version',
      pending_count: 0, answered_count: 0, deferred_count: 0, answers: [], decisions: [],
    };
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(JSON.stringify(failed), { status: 200, headers: { 'Content-Type': 'application/json' } })));
    useWorkspaceStore.setState({ selectedWorkspaceId: 'ws_1', workspace: { id: 'ws_1', name: 'A', timezone: 'UTC', version: 1 } });

    await useChatAnalysisStore.getState().refresh('ws_1', 'agent_1', 'wi_1');

    expect(useChatAnalysisStore.getState().projection?.status).toBe('failed');
    expect(useChatAnalysisStore.getState().projection?.error).toContain('invalid source version');
    expect(useChatAnalysisStore.getState().error).toBeNull();
  });

  it('keeps an old-revision draft stale and restores only its text explicitly', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(JSON.stringify(projection({ version: 4, revision: 3 })), { status: 200, headers: { 'Content-Type': 'application/json' } })));
    useWorkspaceStore.setState({ selectedWorkspaceId: 'ws_1', workspace: { id: 'ws_1', name: 'A', timezone: 'UTC', version: 1 } });
    useChatAnalysisStore.setState({ workspaceId: 'ws_1', agentId: 'agent_1', conversationId: 'wi_1', projection: projection(), draft: { version: 3, revision: 2, questionId: 'q1', selectedOptionIds: ['a'], text: '旧文字' } });

    await useChatAnalysisStore.getState().refresh('ws_1', 'agent_1', 'wi_1');
    expect(useChatAnalysisStore.getState().draft).toMatchObject({ version: 3, revision: 2, questionId: 'q1', selectedOptionIds: ['a'], text: '旧文字' });
    expect(await useChatAnalysisStore.getState().submitAnswer('answered')).toBe(false);
    useChatAnalysisStore.getState().restoreDraftText();
    expect(useChatAnalysisStore.getState().draft).toMatchObject({ version: 4, revision: 3, questionId: 'q1', selectedOptionIds: [], text: '旧文字' });
  });

  it('only clears a response-loss draft when the server answers contain its client key', async () => {
    const response = projection({ status: 'ready', current_question: undefined, pending_count: 0, answered_count: 1, answers: [{ question_id: 'q1', selected_option_ids: ['a'], text: '', disposition: 'answered', submitted_at: '2026-09-09T00:00:00Z', client_key: 'answer-lost' }] });
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(JSON.stringify(response), { status: 200, headers: { 'Content-Type': 'application/json' } })));
    useWorkspaceStore.setState({ selectedWorkspaceId: 'ws_1', workspace: { id: 'ws_1', name: 'A', timezone: 'UTC', version: 1 } });
    useChatAnalysisStore.setState({ workspaceId: 'ws_1', agentId: 'agent_1', conversationId: 'wi_1', projection: projection(), draft: { version: 3, revision: 2, questionId: 'q1', selectedOptionIds: ['a'], text: '待确认', clientKey: 'answer-lost' } });

    await useChatAnalysisStore.getState().refresh('ws_1', 'agent_1', 'wi_1');
    expect(useChatAnalysisStore.getState().draft).toBeNull();

    const unmatched = { ...response, answers: [{ ...response.answers[0], client_key: 'other-key' }] };
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(JSON.stringify(unmatched), { status: 200, headers: { 'Content-Type': 'application/json' } })));
    useChatAnalysisStore.setState({ projection: projection(), draft: { version: 3, revision: 2, questionId: 'q1', selectedOptionIds: ['a'], text: '不能丢' , clientKey: 'answer-lost' } });
    await useChatAnalysisStore.getState().refresh('ws_1', 'agent_1', 'wi_1');
    expect(useChatAnalysisStore.getState().draft?.text).toBe('不能丢');
  });

  it('saves one answer with CAS fields and advances to the returned current question', async () => {
    const next = projection({ version: 4, revision: 3, current_question: undefined, status: 'ready', pending_count: 0, answered_count: 1 });
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({ projection: next, answer: { question_id: 'q1', selected_option_ids: ['a'], text: '补充', disposition: 'answered', submitted_at: '2026-09-09T00:00:00Z' } }), { status: 200, headers: { 'Content-Type': 'application/json' } }));
    vi.stubGlobal('fetch', fetchMock);
    useWorkspaceStore.setState({ selectedWorkspaceId: 'ws_1', workspace: { id: 'ws_1', name: 'A', timezone: 'UTC', version: 1 } });
    useChatAnalysisStore.setState({ workspaceId: 'ws_1', agentId: 'agent_1', conversationId: 'wi_1', projection: projection(), draft: { version: 3, revision: 2, questionId: 'q1', selectedOptionIds: ['a'], text: '补充' } });

    expect(await useChatAnalysisStore.getState().submitAnswer('answered')).toBe(true);

    const [, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(JSON.parse(init.body as string)).toMatchObject({ expected_version: 3, revision: 2, question_id: 'q1', selected_option_ids: ['a'], text: '补充', disposition: 'answered' });
    expect(useChatAnalysisStore.getState().projection?.status).toBe('ready');
    expect(useChatAnalysisStore.getState().draft).toBeNull();
  });

  it('暂不确定明确作为 deferred 提交，并清掉误选的选项', async () => {
    const next = projection({ version: 4, revision: 2, pending_count: 0, deferred_count: 1, status: 'ready', current_question: undefined });
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({ projection: next, answer: { question_id: 'q1', selected_option_ids: [], text: '等产品确认', disposition: 'deferred', submitted_at: '2026-09-09T00:00:00Z' } }), { status: 200, headers: { 'Content-Type': 'application/json' } }));
    vi.stubGlobal('fetch', fetchMock);
    useWorkspaceStore.setState({ selectedWorkspaceId: 'ws_1', workspace: { id: 'ws_1', name: 'A', timezone: 'UTC', version: 1 } });
    useChatAnalysisStore.setState({ workspaceId: 'ws_1', agentId: 'agent_1', conversationId: 'wi_1', projection: projection(), draft: { version: 3, revision: 2, questionId: 'q1', selectedOptionIds: ['a'], text: '等产品确认' } });

    expect(await useChatAnalysisStore.getState().submitAnswer('deferred')).toBe(true);
    expect(JSON.parse(String(fetchMock.mock.calls[0]?.[1]?.body)).selected_option_ids).toEqual([]);
  });

  it('keeps the same client key when answer delivery fails and is retried', async () => {
    const fetchMock = vi.fn()
      .mockRejectedValueOnce(new Error('网络断开'))
      .mockResolvedValueOnce(new Response(JSON.stringify({ projection: projection({ version: 4, revision: 3 }), answer: { question_id: 'q1', selected_option_ids: ['a'], text: '', disposition: 'answered', submitted_at: '2026-09-09T00:00:00Z' } }), { status: 200, headers: { 'Content-Type': 'application/json' } }));
    vi.stubGlobal('fetch', fetchMock);
    useWorkspaceStore.setState({ selectedWorkspaceId: 'ws_1', workspace: { id: 'ws_1', name: 'A', timezone: 'UTC', version: 1 } });
    useChatAnalysisStore.setState({ workspaceId: 'ws_1', agentId: 'agent_1', conversationId: 'wi_1', projection: projection(), draft: { version: 3, revision: 2, questionId: 'q1', selectedOptionIds: ['a'], text: '' } });

    expect(await useChatAnalysisStore.getState().submitAnswer('answered')).toBe(false);
    const firstKey = useChatAnalysisStore.getState().draft?.clientKey;
    expect(firstKey).toBeTruthy();
    expect(await useChatAnalysisStore.getState().submitAnswer('answered')).toBe(true);
    expect(JSON.parse(String(fetchMock.mock.calls[1]?.[1]?.body)).client_key).toBe(firstKey);
  });
});
