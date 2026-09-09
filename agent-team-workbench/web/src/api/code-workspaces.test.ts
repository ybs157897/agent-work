import { afterEach, describe, expect, it, vi } from 'vitest';
import { createCodeWorkspace, deleteCodeWorkspace } from './code-workspaces';

const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), {
  status,
  headers: { 'Content-Type': 'application/json' },
});

describe('Java code workspace endpoints', () => {
  afterEach(() => vi.unstubAllGlobals());

  it('creates a workspace with only conversation/run scope fields', async () => {
    const fetchMock = vi.fn().mockResolvedValue(json({
      id: 'code_1',
      frame_url: '/api/v1/code-workspaces/code_1/view/',
      repository_identity: 'repo:demo',
      read_only: true,
      expires_at: '2026-09-08T12:00:00Z',
    }, 201));
    vi.stubGlobal('fetch', fetchMock);

    await createCodeWorkspace('ws/1', 'agent/developer', { conversation_id: 'conv_1', run_id: 'run_1' });

    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe('/api/v1/workspaces/ws%2F1/agent-profiles/agent%2Fdeveloper/code-workspaces');
    expect(init.method).toBe('POST');
    expect(JSON.parse(init.body as string)).toEqual({ conversation_id: 'conv_1', run_id: 'run_1' });
    expect((init.headers as Record<string, string>)['Idempotency-Key']).toBeTruthy();
  });

  it('deletes a session with keepalive during teardown', async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(null, { status: 204 }));
    vi.stubGlobal('fetch', fetchMock);

    await deleteCodeWorkspace('code/1', true);

    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe('/api/v1/code-workspaces/code%2F1');
    expect(init.method).toBe('DELETE');
    expect(init.keepalive).toBe(true);
  });

  it('teardown carries the session origin Workspace after the UI has switched away', async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(null, { status: 204 }));
    vi.stubGlobal('fetch', fetchMock);
    await deleteCodeWorkspace('code/old', true, 'ws_A');
    const [, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect((init.headers as Record<string, string>)['X-Workspace-ID']).toBe('ws_A');
  });
});
