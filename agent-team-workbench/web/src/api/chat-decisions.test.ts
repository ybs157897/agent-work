import { afterEach, describe, expect, it, vi } from 'vitest';
import {
  listChatDecisionHistory,
  recheckChatAnalysis,
  submitChatDecision,
} from './chat-decisions';
import { useWorkspaceStore } from '../stores/workspace.store';

const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), {
  status,
  headers: { 'Content-Type': 'application/json' },
});

describe('Chat decision API', () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    useWorkspaceStore.setState({ selectedWorkspaceId: null, workspace: null });
  });

  it('submits the explicit product conclusion with the Workspace fence and client key', async () => {
    const fetchMock = vi.fn().mockResolvedValue(json({ workspace_id: 'ws_1', chat_id: 'wi_1', agent_id: 'agent_1' }));
    vi.stubGlobal('fetch', fetchMock);
    useWorkspaceStore.setState({ selectedWorkspaceId: 'ws_1' });

    await submitChatDecision('wi_1', {
      expected_version: 3,
      revision: 2,
      item_id: 'item_scope',
      outcome: 'confirmed',
      conclusion: '产品确认覆盖代码阅读。',
      basis: '开发与产品同步记录。',
      product_version: '2026-Q3',
      client_key: 'decision:wi_1:item_scope:2:key-1',
    });

    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe('/api/v1/work-items/wi_1/analysis/decisions');
    expect(init.method).toBe('POST');
    expect(JSON.parse(init.body as string)).toEqual({
      expected_version: 3,
      revision: 2,
      item_id: 'item_scope',
      outcome: 'confirmed',
      conclusion: '产品确认覆盖代码阅读。',
      basis: '开发与产品同步记录。',
      product_version: '2026-Q3',
      client_key: 'decision:wi_1:item_scope:2:key-1',
    });
    expect((init.headers as Record<string, string>)['X-Workspace-ID']).toBe('ws_1');
    expect((init.headers as Record<string, string>)['Idempotency-Key']).toBe('decision:wi_1:item_scope:2:key-1');
  });

  it('normalizes immutable revision, answer, decision and reopen history for the panel', async () => {
    const fetchMock = vi.fn().mockResolvedValue(json({
      revisions: [{ revision: 1, created_at: '2026-09-09T00:00:00Z' }],
      answers: [{ id: 'answer_1', revision: 1, question_id: 'q1', created_at: '2026-09-09T00:01:00Z', client_key: 'answer-key' }],
      decisions: [{ decision_id: 'decision_1', revision: 1, item_id: 'item_1', outcome: 'rejected', conclusion: '暂不覆盖', basis: '产品沟通', product_version: 'v1', updated_at: '2026-09-09T00:02:00Z', client_key: 'decision-key' }],
      reopens: [{ id: 'reopen_1', item_id: 'item_1', from_revision: 1, to_revision: 2, reason: 'source_changed', created_at: '2026-09-09T00:03:00Z' }],
    }));
    vi.stubGlobal('fetch', fetchMock);
    useWorkspaceStore.setState({ selectedWorkspaceId: 'ws_1' });

    const result = await listChatDecisionHistory('wi_1');

    expect(result.items.map((item) => item.kind)).toEqual(['revision', 'answer', 'decision', 'reopen']);
    expect(result.items.find((item) => item.kind === 'decision')).toMatchObject({ id: 'decision_1', client_key: 'decision-key', created_at: '2026-09-09T00:02:00Z' });
    expect(result.items.find((item) => item.kind === 'reopen')).toMatchObject({ revision: 2, reason: 'source_changed' });
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe('/api/v1/work-items/wi_1/analysis/history');
    expect(init.method).toBe('GET');
    expect((init.headers as Record<string, string>)['X-Workspace-ID']).toBe('ws_1');
  });

  it('rechecks through the same analysis boundary and sends a JSON body', async () => {
    const fetchMock = vi.fn().mockResolvedValue(json({ workspace_id: 'ws_1', chat_id: 'wi_1', agent_id: 'agent_1' }));
    vi.stubGlobal('fetch', fetchMock);
    useWorkspaceStore.setState({ selectedWorkspaceId: 'ws_1' });

    await recheckChatAnalysis('wi_1');

    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe('/api/v1/work-items/wi_1/analysis/recheck');
    expect(init.method).toBe('POST');
    expect(JSON.parse(init.body as string)).toEqual({});
    expect((init.headers as Record<string, string>)['X-Workspace-ID']).toBe('ws_1');
    expect((init.headers as Record<string, string>)['Idempotency-Key']).toMatch(/.+/);
  });
});
