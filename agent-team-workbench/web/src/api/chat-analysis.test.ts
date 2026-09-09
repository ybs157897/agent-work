import { afterEach, describe, expect, it, vi } from 'vitest';
import { getChatAnalysis, parseChatAnalysisProjection, submitChatAnalysisAnswer } from './chat-analysis';
import { useWorkspaceStore } from '../stores/workspace.store';

const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), {
  status,
  headers: { 'Content-Type': 'application/json' },
});

const projection = (overrides: Record<string, unknown> = {}) => ({
  workspace_id: 'ws_1', chat_id: 'wi_1', agent_id: 'agent_1', version: 3, revision: 2,
  status: 'needs_answer', pending_count: 2, answered_count: 1, deferred_count: 0,
  document: {
    version: 'chat-analysis/v1', summary: '需求摘要',
    sources: [{ id: 's1', kind: 'attachment', ref: 'src_1', sha256: 'a'.repeat(64), read_status: 'read', locator: '第 1 页' }],
    items: [{ id: 'item_1', kind: 'requirement', title: '范围', detail: '需要确认范围', source_ids: ['s1'], basis: 'observed', impact: '影响开发范围', recommendation: '先确认' }],
    questions: [{ id: 'q1', prompt: '先覆盖哪些能力？', selection: 'multiple', options: [{ id: 'read', label: '只读' }, { id: 'edit', label: '编辑' }], item_ids: ['item_1'] }],
  },
  current_question: { id: 'q1', prompt: '先覆盖哪些能力？', selection: 'multiple', options: [{ id: 'read', label: '只读' }, { id: 'edit', label: '编辑' }], item_ids: ['item_1'] },
  answers: [],
  decisions: [],
  ...overrides,
});

describe('Chat analysis API', () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    useWorkspaceStore.setState({ selectedWorkspaceId: null, workspace: null });
  });

  it('reads the validated projection and only submits the contract answer body', async () => {
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(json(projection()))
      .mockResolvedValueOnce(json({ projection: projection({ version: 4, revision: 3, status: 'needs_answer' }), answer: { question_id: 'q1', selected_option_ids: ['read'], text: '补充', disposition: 'answered', submitted_at: '2026-09-09T00:00:00Z' } }));
    vi.stubGlobal('fetch', fetchMock);
    useWorkspaceStore.setState({ selectedWorkspaceId: 'ws_1' });

    await getChatAnalysis('wi_1');
    await submitChatAnalysisAnswer('wi_1', {
      expected_version: 3, revision: 2, question_id: 'q1', selected_option_ids: ['read'], text: '补充', disposition: 'answered', client_key: 'answer:q1:2',
    });

    expect(fetchMock.mock.calls[0][0]).toBe('/api/v1/work-items/wi_1/analysis');
    const [url, init] = fetchMock.mock.calls[1] as [string, RequestInit];
    expect(url).toBe('/api/v1/work-items/wi_1/analysis/answers');
    expect(JSON.parse(init.body as string)).toEqual({
      expected_version: 3, revision: 2, question_id: 'q1', selected_option_ids: ['read'], text: '补充', disposition: 'answered', client_key: 'answer:q1:2',
    });
    expect((init.headers as Record<string, string>)['Idempotency-Key']).toBe('answer:q1:2');
  });

  it('rejects a malformed document instead of rendering a question pile', () => {
    expect(() => parseChatAnalysisProjection(projection({ document: { version: 'chat-analysis/v1', summary: '', sources: [], items: [], questions: [] } }))).toThrow('document');
    expect(() => parseChatAnalysisProjection(projection({ current_question: { id: 'q1', prompt: 'q', selection: 'single', options: [{ id: 'a', label: 'A' }], item_ids: [] } }))).toThrow('questions.options');
  });

  it('accepts optional impact/recommendation on non-conflict analysis items', () => {
    const value = projection();
    const item = value.document.items[0] as Record<string, unknown>;
    delete item.impact;
    delete item.recommendation;
    expect(parseChatAnalysisProjection(value).document?.items[0]).not.toHaveProperty('impact');
  });

  it('accepts the backend answer timestamp field without weakening projection validation', () => {
    const value = projection({ answers: [{ question_id: 'q1', selected_option_ids: [], disposition: 'deferred', created_at: '2026-09-09T00:00:00Z', client_key: 'answer-1' }] });
    expect(parseChatAnalysisProjection(value).answers[0]?.submitted_at).toBe('2026-09-09T00:00:00Z');
    expect(parseChatAnalysisProjection(value).answers[0]?.client_key).toBe('answer-1');
  });

  it('parses the backend decisions[] projection and keeps the client-side item selection boundary', () => {
    const value = projection({
      status: 'ready',
      current_question: undefined,
      decisions: [{
        item_id: 'item_1', revision: 2, decision_id: 'decision_1', outcome: 'rejected',
        conclusion: '暂不覆盖编辑', basis: '产品沟通记录', product_version: 'v1',
        item_fingerprint: 'f'.repeat(64), status: 'needs_reconfirmation', review_reason: 'source_changed',
        updated_at: '2026-09-09T00:00:00Z',
      }],
    });
    const parsed = parseChatAnalysisProjection(value);
    expect(parsed.decisions).toMatchObject([{ id: 'decision_1', item_id: 'item_1', outcome: 'rejected', status: 'needs_reconfirmation', created_at: '2026-09-09T00:00:00Z' }]);
    expect(parsed).not.toHaveProperty('current_item_id');
  });
});
