import { apiFetch } from './client';

export interface CodeWorkspaceSession {
  id: string;
  frame_url: string;
  repository_identity: string;
  branch?: string;
  worktree_ref?: string;
  read_only: boolean;
  expires_at: string;
}

export interface CreateCodeWorkspaceInput {
  conversation_id?: string;
  run_id?: string;
}

/** Create one read-only, Agent-scoped Java code viewer session. */
export const createCodeWorkspace = (
  workspaceId: string,
  agentId: string,
  input: CreateCodeWorkspaceInput = {},
) => apiFetch<CodeWorkspaceSession>(
  `/workspaces/${encodeURIComponent(workspaceId)}/agent-profiles/${encodeURIComponent(agentId)}/code-workspaces`,
  { method: 'POST', body: input },
);

/** Release a code viewer session. keepalive is used while the owner is unmounting. */
export const deleteCodeWorkspace = (sessionId: string, keepalive = false, originWorkspaceId?: string) => apiFetch<void>(
  `/code-workspaces/${encodeURIComponent(sessionId)}`,
  {
    method: 'DELETE',
    keepalive,
    ...(originWorkspaceId ? { headers: { 'X-Workspace-ID': originWorkspaceId } } : {}),
  },
);
