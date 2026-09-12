import { afterEach, describe, expect, it, vi } from 'vitest';
import { useWorkspaceStore } from './workspace.store';
import { useChatDecisionsStore } from './chat-decisions.store';
import { useChatAnalysisStore } from './chat-analysis.store';
import { readChatWorkspaceState, writeChatWorkspaceState } from './chat-workspace-state';
import type { ChatAnalysisDecision, ChatAnalysisProjection } from '../api/chat-analysis';
import type { ChatDecisionDraft } from './chat-decisions.store';

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

const item = {
  id: 'item_scope',
  kind: 'requirement' as const,
  title: 'Java 能力范围',
  detail: '需要确认第一阶段能力。',
  source_ids: ['s1'],
  basis: 'observed' as const,
};

const otherItem = {
  ...item,
  id: 'item_other',
  title: '其他事项',
};

const decision = (overrides: Partial<ChatAnalysisDecision> = {}): ChatAnalysisDecision => ({
  id: 'decision_1',
  revision: 2,
  item_id: item.id,
  item_fingerprint: 'fingerprint-1',
  outcome: 'confirmed',
  conclusion: '确认覆盖代码阅读。',
  basis: '产品同步记录。',
  product_version: '2026-Q3',
  created_at: '2026-09-09T00:02:00Z',
  client_key: 'decision-key',
  status: 'valid',
  ...overrides,
});

const projection = (overrides: Partial<ChatAnalysisProjection> = {}): ChatAnalysisProjection => ({
  workspace_id: 'ws_1',
  chat_id: 'wi_1',
  agent_id: 'agent_1',
  version: 3,
  revision: 2,
  status: 'ready',
  pending_count: 0,
  answered_count: 1,
  deferred_count: 0,
  document: {
    version: 'chat-analysis/v1',
    summary: '摘要',
    sources: [{ id: 's1', kind: 'conversation', ref: 'conversation:wi_1', sha256: 'a'.repeat(64), read_status: 'read' }],
    items: [item],
    questions: [],
  },
  answers: [],
  decisions: [],
  ...overrides,
});

const response = (body: unknown, status = 200, contentType = 'application/json') => new Response(JSON.stringify(body), {
  status,
  headers: { 'Content-Type': contentType },
});

function projectionWithItems(overrides: Partial<ChatAnalysisProjection> = {}): ChatAnalysisProjection {
  const base = projection();
  return {
    ...base,
    document: { ...base.document!, items: [item, otherItem] },
    decisions: [decision({ item_id: otherItem.id })],
    ...overrides,
  };
}

function seed(draft: ChatDecisionDraft = {
  version: 3,
  revision: 2,
  itemId: item.id,
      outcome: 'confirmed',
  conclusion: '开发已和产品确认。',
  basis: '群聊记录。',
  productVersion: '2026-Q3',
}) {
  useWorkspaceStore.setState({
    selectedWorkspaceId: 'ws_1',
    workspace: { id: 'ws_1', name: '测试工作区', timezone: 'Asia/Shanghai', version: 1 },
  });
  useChatDecisionsStore.setState({
    workspaceId: 'ws_1',
    agentId: 'agent_1',
    conversationId: 'wi_1',
    history: [],
    draft,
    loading: false,
    historyLoading: false,
    submitting: false,
    error: null,
  });
  useChatAnalysisStore.setState({
    workspaceId: 'ws_1',
    agentId: 'agent_1',
    conversationId: 'wi_1',
    projection: projection(),
    draft: null,
    loading: false,
    submitting: false,
    error: null,
  });
}

describe('Chat decisions store', () => {
  afterEach(() => {
    useChatDecisionsStore.getState().reset();
    useChatAnalysisStore.getState().reset();
    vi.unstubAllGlobals();
    useWorkspaceStore.setState({ selectedWorkspaceId: null, workspace: null });
  });

  it('restores a matching product conclusion draft and flattens server history', async () => {
    const storage = memoryStorage();
    vi.stubGlobal('window', { localStorage: storage });
    writeChatWorkspaceState('ws_1', 'agent_1', 'wi_1', {
      composer: { draft: '' },
      queue: [],
      decisionDraft: {
        version: 3,
        revision: 2,
        itemId: item.id,
        outcome: 'needs_clarification',
        conclusion: '等待产品确认异常场景。',
        basis: '开发与产品沟通。',
        productVersion: '2026-Q3',
      },
    });
    useWorkspaceStore.setState({
      selectedWorkspaceId: 'ws_1',
      workspace: { id: 'ws_1', name: '测试工作区', timezone: 'Asia/Shanghai', version: 1 },
    });
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(response(projection()))
      .mockResolvedValueOnce(response({
        revisions: [{ revision: 2, created_at: '2026-09-09T00:00:00Z' }],
        answers: [],
        decisions: [{ id: 'decision-old', revision: 1, item_id: item.id, outcome: 'rejected', conclusion: '旧结论', basis: '旧依据', product_version: 'v0', created_at: '2026-09-09T00:01:00Z' }],
        reopens: [],
      }));
    vi.stubGlobal('fetch', fetchMock);

    await useChatDecisionsStore.getState().refresh('ws_1', 'agent_1', 'wi_1');

    expect(useChatDecisionsStore.getState().draft?.conclusion).toBe('等待产品确认异常场景。');
    expect(useChatDecisionsStore.getState().history.map((entry) => entry.kind)).toEqual(['revision', 'decision']);
  });

  it('does not carry old-revision fields into a new draft when a user starts editing', () => {
    seed({
      version: 2,
      revision: 1,
      itemId: item.id,
      outcome: 'rejected',
      conclusion: '旧 revision 结论',
      basis: '旧 revision 依据',
      productVersion: 'v0',
    });

    useChatDecisionsStore.getState().setOutcome('confirmed');

    expect(useChatDecisionsStore.getState().draft).toMatchObject({ revision: 2, outcome: 'confirmed', conclusion: '', basis: '', productVersion: '' });
  });

  it('preserves the response-loss client key while a same-revision draft is edited or rehydrated', () => {
    const storage = memoryStorage();
    vi.stubGlobal('window', { localStorage: storage });
    seed({
      version: 3,
      revision: 2,
      itemId: item.id,
      outcome: 'confirmed',
      conclusion: '已填结论',
      basis: '已填依据',
      productVersion: 'v1',
      clientKey: 'decision-key',
    });

    useChatDecisionsStore.getState().setConclusion('修改后的结论');
    useChatDecisionsStore.getState().setBasis('修改后的依据');
    useChatDecisionsStore.getState().setProductVersion('v2');

    expect(useChatDecisionsStore.getState().draft).toMatchObject({ clientKey: 'decision-key', conclusion: '修改后的结论', basis: '修改后的依据', productVersion: 'v2' });
    expect(readChatWorkspaceState('ws_1', 'agent_1', 'wi_1')?.decisionDraft?.clientKey).toBe('decision-key');
  });

  it('restores the persisted draft item instead of defaulting to the first item with a decision', async () => {
    const storage = memoryStorage();
    vi.stubGlobal('window', { localStorage: storage });
    useWorkspaceStore.setState({
      selectedWorkspaceId: 'ws_1',
      workspace: { id: 'ws_1', name: '测试工作区', timezone: 'Asia/Shanghai', version: 1 },
    });
    writeChatWorkspaceState('ws_1', 'agent_1', 'wi_1', {
      composer: { draft: '' },
      queue: [],
      decisionDraft: {
        version: 3,
        revision: 2,
        itemId: item.id,
        outcome: 'needs_clarification',
        conclusion: '恢复这个事项',
        basis: '历史沟通',
        productVersion: 'v1',
      },
    });
    useChatAnalysisStore.setState({
      workspaceId: 'ws_1', agentId: 'agent_1', conversationId: 'wi_1',
      projection: projectionWithItems(), draft: null, loading: false, submitting: false, error: null,
    });
    useChatDecisionsStore.setState({ workspaceId: 'ws_1', agentId: 'agent_1', conversationId: 'wi_1', selectedItemId: null, history: [], draft: null, loading: false, historyLoading: false, submitting: false, error: null });
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(response({ revisions: [], answers: [], decisions: [], reopens: [] })));

    await useChatDecisionsStore.getState().refresh('ws_1', 'agent_1', 'wi_1');

    expect(useChatDecisionsStore.getState().selectedItemId).toBe(item.id);
    expect(useChatDecisionsStore.getState().draft?.itemId).toBe(item.id);
  });

  it('rechecks before saving and sends the CAS fields plus a stable client key', async () => {
    seed();
    const next = projection({ version: 4, revision: 2, decisions: [decision()] });
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(response(projection()))
      .mockResolvedValueOnce(response(next));
    vi.stubGlobal('fetch', fetchMock);

    expect(await useChatDecisionsStore.getState().submit()).toBe(true);

    const recheckBody = JSON.parse(String(fetchMock.mock.calls[0]?.[1]?.body));
    const submitBody = JSON.parse(String(fetchMock.mock.calls[1]?.[1]?.body));
    expect(fetchMock.mock.calls[0]?.[0]).toBe('/api/v1/work-items/wi_1/analysis/recheck');
    expect(fetchMock.mock.calls[1]?.[0]).toBe('/api/v1/work-items/wi_1/analysis/decisions');
    expect(recheckBody).toEqual({});
    expect(submitBody).toMatchObject({ expected_version: 3, revision: 2, item_id: item.id, outcome: 'confirmed' });
    expect(submitBody.client_key).toMatch(/^chat-decision:wi_1:item_scope:2:/);
    expect(useChatDecisionsStore.getState().draft).toBeNull();
  });

  it('keeps a CAS-conflicted draft and refreshes the current projection', async () => {
    seed();
    const latest = projection({ version: 4, revision: 3 });
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(response(projection()))
      .mockResolvedValueOnce(response({ status: 409, title: 'Conflict', detail: 'version changed', code: 'version_conflict' }, 409, 'application/problem+json'))
      .mockResolvedValueOnce(response(latest))
      .mockResolvedValueOnce(response({ revisions: [], answers: [], decisions: [], reopens: [] }));
    vi.stubGlobal('fetch', fetchMock);

    expect(await useChatDecisionsStore.getState().submit()).toBe(false);

    expect(useChatDecisionsStore.getState().error).toContain('材料已变化');
    expect(useChatAnalysisStore.getState().projection?.revision).toBe(3);
    expect(useChatDecisionsStore.getState().draft?.revision).toBe(2);
    expect(useChatDecisionsStore.getState().draft?.conclusion).toBe('开发已和产品确认。');
  });

  it('retains the generated client key after response loss and clears only after a matching server receipt', async () => {
    seed();
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(response(projection()))
      .mockRejectedValueOnce(new Error('连接断开'));
    vi.stubGlobal('fetch', fetchMock);

    expect(await useChatDecisionsStore.getState().submit()).toBe(false);
    const clientKey = useChatDecisionsStore.getState().draft?.clientKey;
    expect(clientKey).toMatch(/^chat-decision:/);

    fetchMock
      .mockResolvedValueOnce(response(projection({ decisions: [decision({ client_key: clientKey })] })))
      .mockResolvedValueOnce(response({ revisions: [], answers: [], decisions: [], reopens: [] }));
    await useChatAnalysisStore.getState().refresh('ws_1', 'agent_1', 'wi_1');
    await useChatDecisionsStore.getState().refresh('ws_1', 'agent_1', 'wi_1');

    expect(useChatDecisionsStore.getState().draft).toBeNull();
    expect(useChatDecisionsStore.getState().selectedItemId).toBe(item.id);
  });

  it('ignores a late refresh that started before a matching receipt refresh', async () => {
    seed({
      version: 3,
      revision: 2,
      itemId: item.id,
      outcome: 'confirmed',
      conclusion: '开发已和产品确认。',
      basis: '群聊记录。',
      productVersion: '2026-Q3',
      clientKey: 'decision-key',
    });
    let resolveLate!: (value: Response) => void;
    const late = new Promise<Response>((resolve) => { resolveLate = resolve; });
    const fetchMock = vi.fn()
      .mockImplementationOnce(() => late)
      .mockResolvedValueOnce(response({ revisions: [], answers: [], decisions: [{ id: 'decision-receipt', revision: 2, item_id: item.id, outcome: 'confirmed', conclusion: '已保存', basis: '已沟通', product_version: 'v1', created_at: '2026-09-09T00:03:00Z', client_key: 'decision-key' }], reopens: [] }));
    vi.stubGlobal('fetch', fetchMock);

    const first = useChatDecisionsStore.getState().refresh('ws_1', 'agent_1', 'wi_1');
    const second = useChatDecisionsStore.getState().refresh('ws_1', 'agent_1', 'wi_1');
    await second;
    resolveLate(response({ revisions: [], answers: [], decisions: [], reopens: [] }));
    await first;

    expect(useChatDecisionsStore.getState().draft).toBeNull();
    expect(useChatDecisionsStore.getState().history.some((entry) => entry.client_key === 'decision-key')).toBe(true);
  });

  it('reconciles a matching receipt even when that response belongs to an older refresh', async () => {
    seed({
      version: 3,
      revision: 2,
      itemId: item.id,
      outcome: 'confirmed',
      conclusion: '开发已和产品确认。',
      basis: '群聊记录。',
      productVersion: '2026-Q3',
      clientKey: 'decision-key',
    });
    let resolveReceipt!: (value: Response) => void;
    const receipt = new Promise<Response>((resolve) => { resolveReceipt = resolve; });
    const fetchMock = vi.fn()
      .mockImplementationOnce(() => receipt)
      .mockResolvedValueOnce(response({ revisions: [], answers: [], decisions: [], reopens: [] }));
    vi.stubGlobal('fetch', fetchMock);

    const older = useChatDecisionsStore.getState().refresh('ws_1', 'agent_1', 'wi_1');
    const newer = useChatDecisionsStore.getState().refresh('ws_1', 'agent_1', 'wi_1');
    await newer;
    expect(useChatDecisionsStore.getState().draft?.clientKey).toBe('decision-key');
    resolveReceipt(response({ revisions: [], answers: [], decisions: [{ id: 'decision-receipt', revision: 2, item_id: item.id, outcome: 'confirmed', conclusion: '已保存', basis: '已沟通', product_version: 'v1', created_at: '2026-09-09T00:03:00Z', client_key: 'decision-key' }], reopens: [] }));
    await older;

    expect(useChatDecisionsStore.getState().draft).toBeNull();
  });

  it('does not let a refresh started before submit overwrite the minted client key', async () => {
    const storage = memoryStorage();
    vi.stubGlobal('window', { localStorage: storage });
    seed();
    writeChatWorkspaceState('ws_1', 'agent_1', 'wi_1', {
      composer: { draft: '' },
      queue: [],
      decisionDraft: useChatDecisionsStore.getState().draft,
    });
    let resolveOldHistory!: (value: Response) => void;
    const oldHistory = new Promise<Response>((resolve) => { resolveOldHistory = resolve; });
    const fetchMock = vi.fn()
      .mockImplementationOnce(() => oldHistory)
      .mockResolvedValueOnce(response(projection()))
      .mockRejectedValueOnce(new Error('POST response lost after commit'));
    vi.stubGlobal('fetch', fetchMock);

    const staleRefresh = useChatDecisionsStore.getState().refresh('ws_1', 'agent_1', 'wi_1');
    expect(await useChatDecisionsStore.getState().submit()).toBe(false);
    const mintedKey = useChatDecisionsStore.getState().draft?.clientKey;
    expect(mintedKey).toMatch(/^chat-decision:/);

    resolveOldHistory(response({ revisions: [], answers: [], decisions: [], reopens: [] }));
    await staleRefresh;

    expect(useChatDecisionsStore.getState().draft?.clientKey).toBe(mintedKey);
    expect(readChatWorkspaceState('ws_1', 'agent_1', 'wi_1')?.decisionDraft?.clientKey).toBe(mintedKey);
  });
});
