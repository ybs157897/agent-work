import { apiFetch } from './client';
import type { ExecutionHost, Workspace, WorkspaceProject } from './types';
import type { HostMount } from './workspace-locations';

export interface CreateWorkspaceInput {
  name: string;
  timezone: string;
  project: Pick<WorkspaceProject, 'execution_host_id' | 'mount_alias' | 'mount_generation' | 'repository_identity'>;
  source_workspace_id?: string;
}

export interface CreateWorkspaceResponse extends Workspace {
  reused?: boolean;
  workspace?: Workspace;
}

export const listExecutionHosts = () => apiFetch<{ items: ExecutionHost[] }>('/execution-hosts');

export const listExecutionHostMounts = (hostId: string) =>
  apiFetch<{ items: HostMount[] }>(`/execution-hosts/${encodeURIComponent(hostId)}/mounts`);

export const createWorkspace = (input: CreateWorkspaceInput, idempotencyKey?: string) =>
  apiFetch<CreateWorkspaceResponse>('/workspaces', {
    method: 'POST',
    body: input,
    ...(idempotencyKey ? { idempotencyKey } : {}),
  });

export const getWorkspace = (workspaceId: string, requestWorkspaceId?: string) =>
  apiFetch<Workspace>(
    `/workspaces/${encodeURIComponent(workspaceId)}`,
    requestWorkspaceId ? { headers: { 'X-Workspace-ID': requestWorkspaceId } } : {},
  );
