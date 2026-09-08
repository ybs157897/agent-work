import type { KnowledgeItemDetails } from '../api/knowledge';

export interface KnowledgeChatRequest {
  itemId: string;
  version: string;
  returnTo: string;
}

export function knowledgeReturnPath(raw: string | null, itemId: string, version: string, workspaceId?: string): string {
  const fallback = `/knowledge?${new URLSearchParams({ ...(workspaceId ? { ws: workspaceId } : {}), item: itemId, ...(version ? { version } : {}) })}`;
  if (!raw || !raw.startsWith('/') || raw.startsWith('//') || raw.includes('\\')) return fallback;
  try {
    const url = new URL(raw, 'https://workbench.invalid');
    if (url.origin !== 'https://workbench.invalid' || !/^\/knowledge\/?$/.test(url.pathname)) return fallback;
    const params = new URLSearchParams();
    for (const key of ['ws', 'q', 'kind', 'item', 'version']) {
      const value = url.searchParams.get(key);
      if (value) params.set(key, value);
    }
    params.set('item', itemId);
    if (version) params.set('version', version);
    if (workspaceId) params.set('ws', workspaceId);
    return `/knowledge?${params}`;
  } catch {
    return fallback;
  }
}

export function readKnowledgeChatRequest(params: URLSearchParams): KnowledgeChatRequest | null {
  const itemId = params.get('knowledge');
  if (itemId === null) return null;
  const version = params.get('version') ?? '';
  if (!/^kb_[a-zA-Z0-9_-]{1,160}$/.test(itemId)
    || (version !== '' && !/^(?:[1-9]\d{0,9}|kbv_[a-zA-Z0-9_-]{1,160})$/.test(version))) {
    throw new Error('知识引用格式无效，请返回知识库重新打开条目。');
  }
  return { itemId, version, returnTo: knowledgeReturnPath(params.get('return_to'), itemId, version) };
}

export function buildKnowledgeQuestionDraft(details: KnowledgeItemDetails): string {
  const title = details.version.title.replace(/[\r\n]/g, ' ');
  const path = `/knowledge?${new URLSearchParams({ ...(details.item.workspace_id ? { ws: details.item.workspace_id } : {}), item: details.item.id, version: String(details.version.version) })}`;
  return `请基于《${title}》的第 ${details.version.version} 版回答我的问题，先读取这条知识及其来源，暂时不要修改知识。\n知识引用：${path}\n\n我的问题：`;
}
