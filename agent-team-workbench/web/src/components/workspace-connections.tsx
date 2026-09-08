import { useCallback, useEffect, useRef, useState } from 'react';
import { listHostMounts, listWorkspaceLocations, probeWorkspaceLocation, reconnectWorkspaceLocation, workspaceConnectionState, type HostMount, type WorkspaceLocation } from '../api/workspace-locations';
import { Button, Card } from './ui';

type Connection = { location: WorkspaceLocation; mount?: HostMount };

export function WorkspaceConnections({ workspaceId }: { workspaceId?: string }) {
  const [connections, setConnections] = useState<Connection[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const generation = useRef(0);

  const load = useCallback(async () => {
    const request = ++generation.current;
    setError(null);
    setConnections(null);
    if (!workspaceId) return;
    try {
      const { items } = await listWorkspaceLocations(workspaceId);
      const hosts = [...new Set(items.map((item) => item.execution_host_id))];
      const mounts = new Map(await Promise.all(hosts.map(async (id) => [id, (await listHostMounts(id)).items] as const)));
      if (generation.current !== request) return;
      setConnections(items.map((location) => ({ location, mount: mounts.get(location.execution_host_id)?.find((mount) => mount.alias === location.mount_alias) })));
    } catch (cause) {
      if (generation.current === request) setError(cause instanceof Error ? cause.message : '工作目录连接读取失败，请重试。');
    }
  }, [workspaceId]);

  useEffect(() => {
    setBusy(null);
    setNotice(null);
    void load();
    return () => { generation.current += 1; };
  }, [load]);

  const check = async ({ location, mount }: Connection) => {
    if (busy) return;
    const request = generation.current;
    setBusy(location.id);
    setError(null);
    setNotice(null);
    try {
      if (mount && workspaceConnectionState(location, mount) === 'changed') await reconnectWorkspaceLocation(location, mount);
      const result = await probeWorkspaceLocation(location.id);
      if (generation.current !== request) return;
      if (result.status !== 'ready') throw new Error(result.detail || '工作目录仍不可用，请检查文件夹是否存在。');
      setNotice('工作目录已连接，可以返回对话继续发送。');
      setBusy(null);
      await load();
    } catch (cause) {
      if (generation.current === request) setError(cause instanceof Error ? cause.message : '连接检查失败');
    } finally {
      if (generation.current === request) setBusy(null);
    }
  };

  return (
    <Card padded>
      <div className="flex items-start justify-between gap-base">
        <div>
          <h3 className="text-h3 text-text-primary">工作目录连接</h3>
          <p className="mt-tight text-body text-text-secondary">智能体在这里读取和处理工作文件。移动目录后，需要重新连接才能继续对话或整理资料。</p>
        </div>
        <Button type="button" size="sm" disabled={!!busy} onClick={() => void load()}>刷新连接</Button>
      </div>
      {error && <p role="alert" className="mt-base break-words text-body text-status-error">{error}</p>}
      {notice && <p role="status" className="mt-base text-body text-status-success">{notice}</p>}
      {!connections && !error && <p className="mt-base text-body text-text-secondary">正在检查工作目录…</p>}
      {connections?.length === 0 && <p className="mt-base text-body text-text-secondary">尚未连接工作目录，请由工作区管理员配置运行位置。</p>}
      {connections?.map((connection) => {
        const { location, mount } = connection;
        const status = workspaceConnectionState(location, mount);
        return (
          <div key={location.id} className="mt-base flex flex-wrap items-center justify-between gap-base border-t border-border-subtle pt-base">
            <div>
              <p className="text-body text-text-primary">{location.mount_alias}{location.is_default ? ' · 默认工作目录' : ''}</p>
              <p className="mt-tight text-caption text-text-secondary">{status === 'ready' ? '已连接' : status === 'changed' ? '目录位置已变化，重新连接后可继续使用。' : '目录不可用或项目身份不匹配，请检查工作区配置。'}</p>
            </div>
            <Button type="button" disabled={!!busy || status === 'unavailable'} onClick={() => void check(connection)}>{busy === location.id ? '检查中…' : status === 'changed' ? '重新连接工作目录' : '检查连接'}</Button>
          </div>
        );
      })}
    </Card>
  );
}
