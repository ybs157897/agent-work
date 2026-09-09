import { apiFetch } from './client';

export interface WorkspaceLocation {
  id: string;
  execution_host_id: string;
  mount_alias: string;
  mount_generation: string;
  repository_identity: string;
  is_default: boolean;
  status: string;
  version: number;
}

export interface HostMount {
  execution_host_id?: string;
  alias: string;
  repository_identity: string;
  registry_generation: string;
  display_label?: string;
  default_branch?: string;
  supported_ref_kinds?: string[];
  checkouts?: Array<Record<string, unknown>>;
  status: string;
}

export const listWorkspaceLocations = (workspaceId: string) =>
  apiFetch<{ items: WorkspaceLocation[] }>(`/workspaces/${encodeURIComponent(workspaceId)}/locations`);

export const listHostMounts = (hostId: string) =>
  apiFetch<{ items: HostMount[] }>(`/execution-hosts/${encodeURIComponent(hostId)}/mounts`);

export function workspaceConnectionState(location: WorkspaceLocation, mount?: HostMount): 'unavailable' | 'changed' | 'ready' {
  if (!mount || mount.status !== 'ready' || mount.alias !== location.mount_alias || mount.repository_identity !== location.repository_identity) return 'unavailable';
  return location.mount_generation === mount.registry_generation ? 'ready' : 'changed';
}

export function reconnectWorkspaceLocation(location: WorkspaceLocation, mount: HostMount) {
  if (workspaceConnectionState(location, mount) !== 'changed') {
    return Promise.reject(new Error('当前目录身份不匹配或连接无需更新，请刷新后检查。'));
  }
  return apiFetch<WorkspaceLocation>(`/workspace-locations/${encodeURIComponent(location.id)}`, {
    method: 'PATCH',
    body: {
      repository_identity: location.repository_identity,
      mount_generation: mount.registry_generation,
      expected_version: location.version,
    },
  });
}

export const probeWorkspaceLocation = (locationId: string) =>
  apiFetch<{ status: string; detail?: string }>(`/workspace-locations/${encodeURIComponent(locationId)}/commands/probe`, { method: 'POST', body: {} });
