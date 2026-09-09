import { afterEach, describe, expect, it, vi } from 'vitest';
import { createPublicationDraft, publishPublicationDraft } from './task-publications';
import { useWorkspaceStore } from '../stores/workspace.store';

const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), {
  status,
  headers: { 'Content-Type': 'application/json' },
});

describe('publication API', () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    useWorkspaceStore.setState({ selectedWorkspaceId: null, workspace: null });
  });

  it('sends only the explicit server-reference draft body with a Workspace fence', async () => {
    const fetchMock = vi.fn().mockResolvedValue(json({
      draft_id: 'pubdraft_1', workspace_id: 'ws_1', chat_id: 'wi_1', agent_id: 'agent_1', analysis_revision: 2,
      item_ids: ['exc-jdk', 'exc-deps'], confirmation_ids: ['cad_1'], title: 'web-idea Java 能力', description: '说明', acceptance_criteria: ['可验收'], source_dependencies: [], project_baseline: {
        repository_identity: 'repo-1', ref_kind: 'root', branch_name: 'main', checkout_ref: 'root', head: 'abc', staged_digest: 'a', unstaged_digest: 'b', untracked_digest: 'c', content_digest: 'd',
      }, status: 'ready', task_id: null, context_snapshot_id: 'ctx_1', fingerprint: 'fp_1', client_key: 'draft:chat:2:key', version: 1,
    }));
    vi.stubGlobal('fetch', fetchMock);
    useWorkspaceStore.setState({ selectedWorkspaceId: 'ws_1' });

    await createPublicationDraft('wi/1', {
      expected_version: 5, revision: 2, item_ids: ['exc-jdk', 'exc-deps'], title: 'web-idea Java 能力', client_key: 'draft:chat:2:key',
    });

    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe('/api/v1/work-items/wi%2F1/analysis/drafts');
    expect(init.method).toBe('POST');
    expect(JSON.parse(init.body as string)).toEqual({ expected_version: 5, revision: 2, item_ids: ['exc-jdk', 'exc-deps'], title: 'web-idea Java 能力', client_key: 'draft:chat:2:key' });
    expect((init.headers as Record<string, string>)['X-Workspace-ID']).toBe('ws_1');
    expect((init.headers as Record<string, string>)['Idempotency-Key']).toBe('draft:chat:2:key');
  });

  it('publishes a durable draft with the same retry key and no browser baseline fields', async () => {
    const fetchMock = vi.fn().mockResolvedValue(json({ draft: {
      draft_id: 'pubdraft_1', workspace_id: 'ws_1', chat_id: 'wi_1', agent_id: 'agent_1', analysis_revision: 2,
      item_ids: ['exc-jdk'], confirmation_ids: ['cad_1'], title: '任务', description: '说明', acceptance_criteria: ['可验收'], source_dependencies: [],
      project_baseline: { repository_identity: 'repo', ref_kind: 'root', branch_name: 'main', checkout_ref: 'root', head: 'abc', staged_digest: 'a', unstaged_digest: 'b', untracked_digest: 'c', content_digest: 'd' },
      status: 'published', task_id: 'wi_task_1', context_snapshot_id: 'ctx_1', fingerprint: 'fp_1', client_key: 'draft:key', version: 2,
    }, task: { id: 'wi_task_1', workspace_id: 'ws_1', record_kind: 'task', title: '任务', description: '', status: 'todo', priority: 'medium', due_date: null, runs_count: 0, version: 1, created_at: '', updated_at: '' } }));
    vi.stubGlobal('fetch', fetchMock);
    useWorkspaceStore.setState({ selectedWorkspaceId: 'ws_1' });

    await publishPublicationDraft('wi_1', 'pubdraft/1', { expected_version: 6, client_key: 'publish:pubdraft_1:key' });

    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe('/api/v1/work-items/wi_1/analysis/drafts/pubdraft%2F1/publish');
    expect(JSON.parse(init.body as string)).toEqual({ expected_version: 6, client_key: 'publish:pubdraft_1:key' });
    expect((init.headers as Record<string, string>)['Idempotency-Key']).toBe('publish:pubdraft_1:key');
  });
});
