import { apiFetch, newRequestId } from './client';
import { useWorkspaceStore } from '../stores/workspace.store';

export type ChatSourceStatus = 'saved' | 'handed_to_agent' | 'read' | 'unready' | 'failed';

export interface ChatSource {
  id: string;
  workspace_id: string;
  chat_id: string;
  agent_id: string;
  filename: string;
  mime: string;
  size: number;
  sha256: string;
  status: ChatSourceStatus;
  available?: boolean;
  availability_error?: string;
  created_at: string;
}

export interface ChatSourceRef {
  source_id: string;
  sha256: string;
}

/** The wire contract carries a bare 64-character SHA-256 hex digest. */
export function chatSourceDigest(value: string): string {
  const digest = value.trim();
  if (!/^[0-9a-f]{64}$/.test(digest)) throw new Error('附件摘要格式无效');
  return digest;
}

const sourceRoot = (chatId: string) => `/work-items/${encodeURIComponent(chatId)}/sources`;

export function chatSourceClientKey(chatId: string, logicalFileKey: string): string {
  return `chat-source:${chatId}:${logicalFileKey}`;
}

export const uploadChatSource = (chatId: string, file: File, clientKey: string) => {
  const form = new FormData();
  form.append('file', file, file.name);
  form.append('client_key', clientKey);
  return apiFetch<ChatSource>(sourceRoot(chatId), {
    method: 'POST',
    body: form,
    idempotencyKey: clientKey,
  });
};

export const listChatSources = (chatId: string) =>
  apiFetch<{ items: ChatSource[] }>(sourceRoot(chatId));

export const getChatSource = (chatId: string, sourceId: string) =>
  apiFetch<ChatSource>(`${sourceRoot(chatId)}/${encodeURIComponent(sourceId)}`);

export function chatSourceDownloadPath(chatId: string, sourceId: string): string {
  return `/api/v1${sourceRoot(chatId)}/${encodeURIComponent(sourceId)}/download`;
}

/** Same-origin download with the current Workspace fence; callers receive the original Blob. */
export async function downloadChatSource(chatId: string, sourceId: string): Promise<Blob> {
  const workspaceId = useWorkspaceStore.getState().selectedWorkspaceId ?? useWorkspaceStore.getState().workspace?.id ?? '';
  const response = await fetch(chatSourceDownloadPath(chatId, sourceId), {
    headers: {
      'X-Request-Id': newRequestId(),
      ...(workspaceId ? { 'X-Workspace-ID': workspaceId } : {}),
    },
  });
  if (!response.ok) throw new Error(`附件下载失败（${response.status}）`);
  return response.blob();
}
