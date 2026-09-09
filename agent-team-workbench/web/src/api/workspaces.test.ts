import { afterEach, describe, expect, it, vi } from 'vitest';
import { createWorkspace, getWorkspace, listExecutionHostMounts, listExecutionHosts } from './workspaces';

const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });

describe('U01 workspace API', () => {
  afterEach(() => vi.unstubAllGlobals());

  it('只提交 opaque host/mount identity，并带 source workspace 配置复制意图', async () => {
    const fetchMock = vi.fn().mockResolvedValue(json({ id: 'ws_new', name: 'web-idea', timezone: 'Asia/Shanghai', version: 1, setup: { status: 'ready' } }, 201));
    vi.stubGlobal('fetch', fetchMock);
    const result = await createWorkspace({
      name: 'web-idea',
      timezone: 'Asia/Shanghai',
      project: { execution_host_id: 'host_local', mount_alias: 'web-idea', mount_generation: 'sha256:g1', repository_identity: 'repo_1' },
      source_workspace_id: 'ws_source',
    }, 'workspace-create-key');
    expect(result.id).toBe('ws_new');
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe('/api/v1/workspaces');
    expect(JSON.parse(init.body as string)).toEqual({
      name: 'web-idea',
      timezone: 'Asia/Shanghai',
      project: { execution_host_id: 'host_local', mount_alias: 'web-idea', mount_generation: 'sha256:g1', repository_identity: 'repo_1' },
      source_workspace_id: 'ws_source',
    });
    expect((init.headers as Record<string, string>)['Idempotency-Key']).toBe('workspace-create-key');
  });

  it('读取工作区及宿主目录时保持 opaque project/setup 投影', async () => {
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(json({ id: 'ws_1', name: 'A', timezone: 'UTC', version: 2, project: { execution_host_id: 'host_local', mount_alias: 'a', mount_generation: 'g', repository_identity: 'repo', status: 'ready' }, setup: { status: 'pending' } }))
      .mockResolvedValueOnce(json({ items: [{ id: 'host_local', name: '本机', kind: 'local', status: 'ready', version: 1 }] }))
      .mockResolvedValueOnce(json({ items: [{ alias: 'a', repository_identity: 'repo', registry_generation: 'g', status: 'ready' }] }));
    vi.stubGlobal('fetch', fetchMock);
    await expect(getWorkspace('ws/1')).resolves.toMatchObject({ id: 'ws_1', setup: { status: 'pending' } });
    await expect(listExecutionHosts()).resolves.toMatchObject({ items: [{ id: 'host_local' }] });
    await expect(listExecutionHostMounts('host/local')).resolves.toMatchObject({ items: [{ alias: 'a' }] });
    expect(fetchMock.mock.calls.map((call) => call[0])).toEqual([
      '/api/v1/workspaces/ws%2F1',
      '/api/v1/execution-hosts',
      '/api/v1/execution-hosts/host%2Flocal/mounts',
    ]);
  });
});
