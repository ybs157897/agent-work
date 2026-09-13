/**
 * 产品知识画布的对话侧协议：画布选中的一段正文以 `<atw-knowledge-reference-v1>`
 * 标记追加在问题之后，随消息一起发给 Agent；发送失败时按原样恢复到输入框。
 * 引用只携带定位事实（文档、版本、发布、标题、摘录、标题层级），不复制全文。
 */

export interface KnowledgeCanvasReference {
  workspaceId: string;
  agentId: string;
  documentId: string;
  version: number;
  releaseId: string;
  title: string;
  quote: string;
  heading?: string;
}

export interface CanvasComposerDraft {
  draft: string;
  reference: KnowledgeCanvasReference | null;
}

export function restoreCanvasComposer(current: CanvasComposerDraft, failed: CanvasComposerDraft): CanvasComposerDraft {
  const restoredText = current.reference ? buildCanvasMessage(failed.draft, failed.reference) : failed.draft;
  return {
    draft: current.draft ? `${restoredText}\n\n${current.draft}` : restoredText,
    reference: current.reference ?? failed.reference,
  };
}

const MAX_QUOTE_LENGTH = 6000;

/** 只有当前工作空间、当前 Agent、且带完整定位事实的引用才允许随消息发出。 */
export function referenceBelongsTo(reference: KnowledgeCanvasReference, workspaceId: string, agentId: string): boolean {
  return reference.workspaceId === workspaceId && reference.agentId === agentId
    && !!reference.documentId && !!reference.releaseId
    && Number.isSafeInteger(reference.version) && reference.version > 0;
}

export function buildCanvasMessage(question: string, reference: KnowledgeCanvasReference | null): string {
  if (!reference) return question.trim();
  const title = reference.title.replace(/[\r\n]/g, ' ');
  const quote = reference.quote.length > MAX_QUOTE_LENGTH
    ? `${reference.quote.slice(0, MAX_QUOTE_LENGTH)}\n（摘录到此为止，可按文档标识读取该版本全文。）`
    : reference.quote;
  return `${question.trim()}${REFERENCE_START}${JSON.stringify({ ...reference, title, quote })}${REFERENCE_END}`;
}

const REFERENCE_START = '\n\n<atw-knowledge-reference-v1>\n';
const REFERENCE_END = '\n</atw-knowledge-reference-v1>';
const REFERENCE_STRING_FIELDS = ['workspaceId', 'agentId', 'documentId', 'releaseId', 'title', 'quote'] as const;

/** Only our explicit, versioned trailer is folded; ordinary user prose stays literal. */
export function parseCanvasMessage(text: string): { question: string; reference: KnowledgeCanvasReference } | null {
  if (!text.endsWith(REFERENCE_END)) return null;
  const index = text.lastIndexOf(REFERENCE_START);
  if (index < 1) return null;
  try {
    const value = JSON.parse(text.slice(index + REFERENCE_START.length, -REFERENCE_END.length)) as unknown;
    if (!value || typeof value !== 'object' || Array.isArray(value)) return null;
    const record = value as Record<string, unknown>;
    if (!REFERENCE_STRING_FIELDS.every((key) => typeof record[key] === 'string' && record[key] !== '')
      || typeof record.version !== 'number' || !Number.isSafeInteger(record.version) || record.version < 1
      || (record.heading !== undefined && typeof record.heading !== 'string')) return null;
    return { question: text.slice(0, index), reference: record as unknown as KnowledgeCanvasReference };
  } catch {
    return null;
  }
}
