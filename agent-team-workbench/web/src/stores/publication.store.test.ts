import { afterEach, describe, expect, it, vi } from 'vitest';
import type { ChatAnalysisProjection } from '../api/chat-analysis';
import { readChatWorkspaceState } from './chat-workspace-state';
import { useChatAnalysisStore } from './chat-analysis.store';
import { usePublicationStore } from './publication.store';
import { useWorkspaceStore } from './workspace.store';

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

const json = (body: unknown, status = 200, contentType = 'application/json') => new Response(JSON.stringify(body), {
  status,
  headers: { 'Content-Type': contentType },
});

const baseProjection = (): ChatAnalysisProjection => ({
  workspace_id: 'ws_1', chat_id: 'wi_1', agent_id: 'agent_1', version: 5, revision: 2, status: 'needs_answer',
  pending_count: 3, answered_count: 0, deferred_count: 0,
  document: {
    version: 'chat-analysis/v1', summary: '摘要',
    sources: [{ id: 's1', kind: 'conversation', ref: 'conversation:wi_1', sha256: 'a'.repeat(64), read_status: 'read' }],
    items: [
      { id: 'confirmed', kind: 'requirement', title: '已确认范围', detail: '范围', source_ids: ['s1'], basis: 'observed' },
      { id: 'rejected', kind: 'exception', title: '已否决范围', detail: '否决', source_ids: ['s1'], basis: 'observed' },
      { id: 'reconfirm', kind: 'normal', title: '需复核范围', detail: '复核', source_ids: ['s1'], basis: 'observed' },
    ], questions: [],
  },
  answers: [],
  decisions: [
    { id: 'cad_1', revision: 2, item_id: 'confirmed', item_fingerprint: 'a', outcome: 'confirmed', conclusion: '已确认结论', basis: '依据', product_version: 'v1', created_at: '2026-09-09T00:00:00Z', status: 'valid' },
    { id: 'cad_2', revision: 2, item_id: 'rejected', item_fingerprint: 'b', outcome: 'rejected', conclusion: '已否决', basis: '依据', product_version: 'v1', created_at: '2026-09-09T00:00:00Z', status: 'valid' },
    { id: 'cad_3', revision: 2, item_id: 'reconfirm', item_fingerprint: 'c', outcome: 'confirmed', conclusion: '需复核', basis: '依据', product_version: 'v1', created_at: '2026-09-09T00:00:00Z', status: 'needs_reconfirmation' },
  ],
});

const draftReceipt = {
  draft_id: 'pubdraft_1', workspace_id: 'ws_1', chat_id: 'wi_1', agent_id: 'agent_1', analysis_revision: 2,
  item_ids: ['confirmed'], confirmation_ids: ['cad_1'], title: 'Java 能力', description: '服务端生成描述', acceptance_criteria: ['服务端生成验收'],
  source_dependencies: [{ kind: 'conversation', ref: 'conversation:wi_1', sha256: 'a'.repeat(64) }],
  project_baseline: { repository_identity: 'repo', ref_kind: 'root', branch_name: 'main', checkout_ref: 'root', head: 'head', staged_digest: 's', unstaged_digest: 'u', untracked_digest: 'un', content_digest: 'c' },
  status: 'ready', task_id: null, context_snapshot_id: 'ctx_1', fingerprint: 'fp_1', client_key: 'draft:wi_1:2:key', version: 1,
};

function seed() {
  useWorkspaceStore.setState({ selectedWorkspaceId: 'ws_1', workspace: { id: 'ws_1', name: 'A', timezone: 'UTC', version: 1 } });
  useChatAnalysisStore.setState({ workspaceId: 'ws_1', agentId: 'agent_1', conversationId: 'wi_1', projection: baseProjection(), draft: null, loading: false, submitting: false, error: null });
  usePublicationStore.getState().hydrate('ws_1', 'agent_1', 'wi_1');
}

describe('publication store', () => {
  afterEach(() => {
    usePublicationStore.getState().reset();
    useChatAnalysisStore.getState().reset();
    vi.unstubAllGlobals();
    useWorkspaceStore.setState({ selectedWorkspaceId: null, workspace: null });
  });

  it('allows only valid confirmed decisions and keeps selected item ids explicit', () => {
    vi.stubGlobal('window', { localStorage: memoryStorage() });
    seed();

    usePublicationStore.getState().openFromAnalysis();
    usePublicationStore.getState().toggleItem('confirmed');
    usePublicationStore.getState().toggleItem('rejected');
    usePublicationStore.getState().toggleItem('reconfirm');
    usePublicationStore.getState().setTitle('Java 能力');

    expect(usePublicationStore.getState().draft).toMatchObject({ itemIds: ['confirmed'], title: 'Java 能力', status: 'editing' });
  });

  it('saves the server-generated description and frozen baseline without sending client body text', async () => {
    vi.stubGlobal('window', { localStorage: memoryStorage() });
    seed();
    usePublicationStore.getState().openFromAnalysis();
    usePublicationStore.getState().toggleItem('confirmed');
    usePublicationStore.getState().setTitle('Java 能力');
    const fetchMock = vi.fn().mockResolvedValue(json(draftReceipt));
    vi.stubGlobal('fetch', fetchMock);

    expect(await usePublicationStore.getState().saveDraft()).toBe(true);

    const body = JSON.parse(String(fetchMock.mock.calls[0]?.[1]?.body));
    expect(body).toEqual({ expected_version: 5, revision: 2, item_ids: ['confirmed'], title: 'Java 能力', client_key: expect.stringMatching(/^draft:wi_1:2:/) });
    expect(body).not.toHaveProperty('description');
    expect(usePublicationStore.getState().draft).toMatchObject({ status: 'ready', draftId: 'pubdraft_1', description: '服务端生成描述', acceptanceCriteria: ['服务端生成验收'] });
  });

  it('retries a lost draft response with the same client key and preserves the frozen payload', async () => {
    vi.stubGlobal('window', { localStorage: memoryStorage() });
    seed();
    usePublicationStore.getState().openFromAnalysis();
    usePublicationStore.getState().toggleItem('confirmed');
    usePublicationStore.getState().setTitle('Java 能力');
    const fetchMock = vi.fn().mockRejectedValueOnce(new Error('response lost')).mockResolvedValueOnce(json(draftReceipt));
    vi.stubGlobal('fetch', fetchMock);

    expect(await usePublicationStore.getState().saveDraft()).toBe(false);
    const firstKey = JSON.parse(String(fetchMock.mock.calls[0]?.[1]?.body)).client_key;
    expect(await usePublicationStore.getState().saveDraft()).toBe(true);
    expect(JSON.parse(String(fetchMock.mock.calls[1]?.[1]?.body)).client_key).toBe(firstKey);
    expect(usePublicationStore.getState().draft?.frozen?.draft_id).toBe('pubdraft_1');
  });

  it('keeps the publication draft through Workspace A/B/A hydration', () => {
    const storage = memoryStorage();
    vi.stubGlobal('window', { localStorage: storage });
    seed();
    usePublicationStore.getState().openFromAnalysis();
    usePublicationStore.getState().toggleItem('confirmed');
    usePublicationStore.getState().setTitle('A 的草案');
    usePublicationStore.getState().reset();
    useWorkspaceStore.setState({ selectedWorkspaceId: 'ws_2', workspace: { id: 'ws_2', name: 'B', timezone: 'UTC', version: 1 } });
    usePublicationStore.getState().hydrate('ws_2', 'agent_1', 'wi_2');
    expect(usePublicationStore.getState().draft).toBeNull();
    useWorkspaceStore.setState({ selectedWorkspaceId: 'ws_1', workspace: { id: 'ws_1', name: 'A', timezone: 'UTC', version: 1 } });
    useChatAnalysisStore.setState({ workspaceId: 'ws_1', agentId: 'agent_1', conversationId: 'wi_1', projection: baseProjection(), draft: null, loading: false, submitting: false, error: null });
    usePublicationStore.getState().hydrate('ws_1', 'agent_1', 'wi_1');
    expect(usePublicationStore.getState().draft).toMatchObject({ itemIds: ['confirmed'], title: 'A 的草案' });
    expect(readChatWorkspaceState('ws_1', 'agent_1', 'wi_1')?.publicationDraft?.title).toBe('A 的草案');
  });

  it('publishes the durable Task with a separate stable publish key', async () => {
    vi.stubGlobal('window', { localStorage: memoryStorage() });
    seed();
    usePublicationStore.getState().openFromAnalysis();
    usePublicationStore.getState().toggleItem('confirmed');
    usePublicationStore.getState().setTitle('Java 能力');
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(json(draftReceipt))
      .mockResolvedValueOnce(json({ draft: { ...draftReceipt, status: 'published', task_id: 'wi_task_1', version: 2 }, task: { id: 'wi_task_1', workspace_id: 'ws_1', record_kind: 'task', title: 'Java 能力', description: '服务端生成描述', status: 'todo', priority: 'medium', due_date: null, runs_count: 0, version: 1, created_at: '', updated_at: '' } }));
    vi.stubGlobal('fetch', fetchMock);
    await usePublicationStore.getState().saveDraft();
    expect(await usePublicationStore.getState().publish()).toBe(true);
    const publishBody = JSON.parse(String(fetchMock.mock.calls[1]?.[1]?.body));
    expect(publishBody.expected_version).toBe(1);
    expect(publishBody.client_key).toMatch(/^publish:pubdraft_1:2:/);
    expect(usePublicationStore.getState().draft).toMatchObject({ status: 'published', taskId: 'wi_task_1' });
  });

  it('starts a separate draft only from a published Task and keeps A/B buckets isolated', () => {
    const storage = memoryStorage();
    vi.stubGlobal('window', { localStorage: storage });
    seed();
    const oldKey = 'draft:old-published';
    usePublicationStore.setState({ draft: {
      expectedVersion: 1, revision: 2, itemIds: ['confirmed'], title: '已发布任务', description: '服务端描述', acceptanceCriteria: ['可验收'],
      clientKey: oldKey, draftId: 'pubdraft_1', publishClientKey: 'publish:key', taskId: 'wi_task_1', status: 'published',
    } });
    usePublicationStore.getState().startNewDraft();
    const next = usePublicationStore.getState().draft;
    expect(next).toMatchObject({ status: 'editing', itemIds: [], title: '' });
    expect(next).not.toHaveProperty('draftId');
    expect(next).not.toHaveProperty('frozen');
    expect(next).not.toHaveProperty('publishClientKey');
    expect(next?.clientKey).not.toBe(oldKey);
    usePublicationStore.getState().reset();
    useWorkspaceStore.setState({ selectedWorkspaceId: 'ws_2', workspace: { id: 'ws_2', name: 'B', timezone: 'UTC', version: 1 } });
    usePublicationStore.getState().hydrate('ws_2', 'agent_1', 'wi_2');
    expect(usePublicationStore.getState().draft).toBeNull();
    useWorkspaceStore.setState({ selectedWorkspaceId: 'ws_1', workspace: { id: 'ws_1', name: 'A', timezone: 'UTC', version: 1 } });
    useChatAnalysisStore.setState({ workspaceId: 'ws_1', agentId: 'agent_1', conversationId: 'wi_1', projection: baseProjection(), draft: null, loading: false, submitting: false, error: null });
    usePublicationStore.getState().hydrate('ws_1', 'agent_1', 'wi_1');
    expect(usePublicationStore.getState().draft).toMatchObject({ status: 'editing', clientKey: next?.clientKey });
  });

  it('retries a failed publish with the same frozen payload and publish key', async () => {
    vi.stubGlobal('window', { localStorage: memoryStorage() });
    seed();
    usePublicationStore.getState().openFromAnalysis();
    usePublicationStore.getState().toggleItem('confirmed');
    usePublicationStore.getState().setTitle('Java 能力');
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(json(draftReceipt))
      .mockResolvedValueOnce(json({ status: 500, title: 'server error', detail: 'after commit', code: 'http_error' }, 500, 'application/problem+json'))
      .mockResolvedValueOnce(json({ draft: { ...draftReceipt, status: 'published', task_id: 'wi_task_1', version: 2 }, task: { id: 'wi_task_1', workspace_id: 'ws_1', record_kind: 'task', title: 'Java 能力', description: '', status: 'todo', priority: 'medium', due_date: null, runs_count: 0, version: 1, created_at: '', updated_at: '' } }));
    vi.stubGlobal('fetch', fetchMock);

    await usePublicationStore.getState().saveDraft();
    expect(await usePublicationStore.getState().publish()).toBe(false);
    expect(usePublicationStore.getState().draft).toMatchObject({ status: 'failed', lastOperation: 'publish', publishClientKey: expect.any(String) });
    const failedKey = JSON.parse(String(fetchMock.mock.calls[1]?.[1]?.body)).client_key;
    expect(await usePublicationStore.getState().publish()).toBe(true);
    expect(JSON.parse(String(fetchMock.mock.calls[2]?.[1]?.body)).client_key).toBe(failedKey);
    expect(usePublicationStore.getState().draft).toMatchObject({ status: 'published', taskId: 'wi_task_1' });
  });

  it('keeps idempotency_in_progress retryable without marking the frozen draft stale', async () => {
    vi.stubGlobal('window', { localStorage: memoryStorage() });
    seed();
    usePublicationStore.getState().openFromAnalysis();
    usePublicationStore.getState().toggleItem('confirmed');
    usePublicationStore.getState().setTitle('Java 能力');
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(json(draftReceipt))
      .mockResolvedValueOnce(json({ status: 409, title: 'in progress', detail: 'try again', code: 'idempotency_in_progress', retryable: true }, 409, 'application/problem+json'));
    vi.stubGlobal('fetch', fetchMock);

    await usePublicationStore.getState().saveDraft();
    expect(await usePublicationStore.getState().publish()).toBe(false);
    expect(usePublicationStore.getState().draft?.status).toBe('failed');
    expect(usePublicationStore.getState().draft?.lastOperation).toBe('publish');
  });
});
