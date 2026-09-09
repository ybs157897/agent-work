export interface ChatAttachmentRecoveryScope {
  workspaceId: string;
  agentId: string;
  conversationId: string | null;
}

export interface ChatAttachmentRecord {
  key: string;
  name: string;
  size: number;
  mime: string;
  kind: 'document' | 'image';
  lastModified: number;
  blob: Blob;
}

const DATABASE = 'agent-team-workbench-chat';
const STORE = 'attachments';
const VERSION = 1;

function recordKey(scope: ChatAttachmentRecoveryScope, attachmentKey: string): string {
  return `${encodeURIComponent(scope.workspaceId)}:${encodeURIComponent(scope.agentId)}:${encodeURIComponent(scope.conversationId ?? 'new')}:${encodeURIComponent(attachmentKey)}`;
}

function openDatabase(): Promise<IDBDatabase> {
  return new Promise((resolve, reject) => {
    if (typeof indexedDB === 'undefined') {
      reject(new Error('当前浏览器不支持本地附件恢复'));
      return;
    }
    const request = indexedDB.open(DATABASE, VERSION);
    request.onupgradeneeded = () => {
      if (!request.result.objectStoreNames.contains(STORE)) request.result.createObjectStore(STORE, { keyPath: 'id' });
    };
    request.onsuccess = () => resolve(request.result);
    request.onerror = () => reject(request.error ?? new Error('本地附件存储不可用'));
  });
}

export async function saveChatAttachments(scope: ChatAttachmentRecoveryScope, attachments: readonly ChatAttachmentRecord[]): Promise<void> {
  if (!scope.workspaceId || !scope.agentId) return;
  const db = await openDatabase();
  await new Promise<void>((resolve, reject) => {
    const tx = db.transaction(STORE, 'readwrite');
    const store = tx.objectStore(STORE);
    attachments.forEach((attachment) => store.put({ id: recordKey(scope, attachment.key), ...attachment }));
    tx.oncomplete = () => resolve();
    tx.onerror = () => reject(tx.error ?? new Error('本地附件保存失败'));
    tx.onabort = () => reject(tx.error ?? new Error('本地附件保存失败'));
  }).finally(() => db.close());
}

export async function loadChatAttachments(scope: ChatAttachmentRecoveryScope): Promise<ChatAttachmentRecord[]> {
  if (!scope.workspaceId || !scope.agentId) return [];
  const db = await openDatabase();
  return new Promise<ChatAttachmentRecord[]>((resolve, reject) => {
    const tx = db.transaction(STORE, 'readonly');
    const request = tx.objectStore(STORE).getAll();
    request.onsuccess = () => {
      const prefix = `${encodeURIComponent(scope.workspaceId)}:${encodeURIComponent(scope.agentId)}:${encodeURIComponent(scope.conversationId ?? 'new')}:`;
      const result = (request.result as Array<{ id: string } & ChatAttachmentRecord>)
        .filter((item) => item.id.startsWith(prefix))
        .map((item) => ({
          key: item.key,
          name: item.name,
          size: item.size,
          mime: item.mime,
          kind: item.kind,
          lastModified: item.lastModified,
          blob: item.blob,
        }));
      resolve(result);
    };
    request.onerror = () => reject(request.error ?? new Error('本地附件读取失败'));
    tx.oncomplete = () => db.close();
    tx.onabort = () => { db.close(); reject(tx.error ?? new Error('本地附件读取失败')); };
  });
}

export async function removeChatAttachment(scope: ChatAttachmentRecoveryScope, attachmentKey: string): Promise<void> {
  const db = await openDatabase();
  await new Promise<void>((resolve, reject) => {
    const tx = db.transaction(STORE, 'readwrite');
    tx.objectStore(STORE).delete(recordKey(scope, attachmentKey));
    tx.oncomplete = () => resolve();
    tx.onerror = () => reject(tx.error ?? new Error('本地附件删除失败'));
    tx.onabort = () => reject(tx.error ?? new Error('本地附件删除失败'));
  }).finally(() => db.close());
}

export function fileFromChatAttachment(attachment: ChatAttachmentRecord): File {
  return new File([attachment.blob], attachment.name, { type: attachment.mime, lastModified: attachment.lastModified });
}
