import { afterEach, describe, expect, it, vi } from 'vitest';
import { reconnectWorkspaceLocation, workspaceConnectionState, type HostMount, type WorkspaceLocation } from './workspace-locations';

const location: WorkspaceLocation = { id: 'loc', execution_host_id: 'host', mount_alias: 'project', mount_generation: 'old', repository_identity: 'repo', is_default: true, status: 'ready', version: 3 };
const mount: HostMount = { alias: 'project', registry_generation: 'current', repository_identity: 'repo', status: 'ready' };

afterEach(() => vi.unstubAllGlobals());

describe('工作目录连接恢复', () => {
  it('即使旧 location 自称 ready，代际变化仍显示需重新连接', () => {
    expect(workspaceConnectionState(location, mount)).toBe('changed');
    expect(workspaceConnectionState({ ...location, mount_generation: 'current' }, mount)).toBe('ready');
  });

  it('拒绝缺失目录、改换项目身份或不同别名，不能自动换到其他项目', async () => {
    expect(workspaceConnectionState(location)).toBe('unavailable');
    for (const wrong of [{ ...mount, repository_identity: 'other' }, { ...mount, alias: 'other' }, { ...mount, status: 'offline' }]) {
      await expect(reconnectWorkspaceLocation(location, wrong)).rejects.toThrow('身份不匹配');
    }
  });

  it('通过正式版本校验命令绑定当前广告代际，不发送路径或覆盖仓库身份', async () => {
    const fetch = vi.fn().mockResolvedValue(new Response(JSON.stringify({ ...location, mount_generation: 'current', version: 4 }), { status: 200 }));
    vi.stubGlobal('fetch', fetch);
    const result = await reconnectWorkspaceLocation(location, mount);
    expect(result.version).toBe(4);
    const [url, options] = fetch.mock.calls[0];
    expect(url).toBe('/api/v1/workspace-locations/loc');
    expect(options.method).toBe('PATCH');
    expect(JSON.parse(options.body)).toEqual({ repository_identity: 'repo', mount_generation: 'current', expected_version: 3 });
  });
});
