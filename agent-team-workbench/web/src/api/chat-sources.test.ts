import { afterEach, describe, expect, it, vi } from 'vitest';
import { chatSourceDigest, downloadChatSource, listChatSources, uploadChatSource } from './chat-sources';
import { useWorkspaceStore } from '../stores/workspace.store';

const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), {
  status,
  headers: { 'Content-Type': 'application/json' },
});

describe('Chat source API', () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    useWorkspaceStore.setState({ selectedWorkspaceId: null, workspace: null });
  });

  it('only accepts a bare lower-case SHA-256 hex value', () => {
    expect(chatSourceDigest('a'.repeat(64))).toBe('a'.repeat(64));
    expect(() => chatSourceDigest(`sha256:${'a'.repeat(64)}`)).toThrow('附件摘要格式无效');
    expect(() => chatSourceDigest('sha256:abc')).toThrow('附件摘要格式无效');
  });

  it('uploads the original file as multipart with a stable client key', async () => {
    const fetchMock = vi.fn().mockResolvedValue(json({
      id: 'src_1', workspace_id: 'ws_1', chat_id: 'wi_1', agent_id: 'agent_1',
      filename: '需求.md', mime: 'text/markdown', size: 3, sha256: 'a'.repeat(64), status: 'saved', created_at: '',
    }, 201));
    vi.stubGlobal('fetch', fetchMock);
    useWorkspaceStore.setState({ selectedWorkspaceId: 'ws_1' });
    const file = new File(['abc'], '需求.md', { type: 'text/markdown' });

    await uploadChatSource('wi_1', file, 'chat-source:wi_1:需求.md:3:1');

    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe('/api/v1/work-items/wi_1/sources');
    expect(init.method).toBe('POST');
    const headers = init.headers as Record<string, string>;
    expect(headers['X-Workspace-ID']).toBe('ws_1');
    expect(headers['Idempotency-Key']).toBe('chat-source:wi_1:需求.md:3:1');
    expect(headers['Content-Type']).toBeUndefined();
    expect(init.body).toBeInstanceOf(FormData);
    const form = init.body as FormData;
    expect(form.get('client_key')).toBe('chat-source:wi_1:需求.md:3:1');
    expect(form.get('file')).toBeInstanceOf(File);
    expect((form.get('file') as File).name).toBe('需求.md');
  });

  it('lists sources with the current Workspace fence', async () => {
    const fetchMock = vi.fn().mockResolvedValue(json({ items: [] }));
    vi.stubGlobal('fetch', fetchMock);
    useWorkspaceStore.setState({ selectedWorkspaceId: 'ws_2' });

    await listChatSources('wi_2');

    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe('/api/v1/work-items/wi_2/sources');
    expect(init.method).toBe('GET');
    expect((init.headers as Record<string, string>)['X-Workspace-ID']).toBe('ws_2');
  });

  it('downloads through the Workspace-fenced opaque source endpoint', async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response('abc', {
      status: 200,
      headers: { 'Content-Type': 'text/plain' },
    }));
    vi.stubGlobal('fetch', fetchMock);
    useWorkspaceStore.setState({ selectedWorkspaceId: 'ws_1' });

    const blob = await downloadChatSource('wi_1', 'src_1');

    expect(await blob.text()).toBe('abc');
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe('/api/v1/work-items/wi_1/sources/src_1/download');
    expect((init.headers as Record<string, string>)['X-Workspace-ID']).toBe('ws_1');
    expect((init.headers as Record<string, string>)['X-Request-Id']).toMatch(/^req_/);
    expect((init.headers as Record<string, string>)['Idempotency-Key']).toBeUndefined();
  });
});
