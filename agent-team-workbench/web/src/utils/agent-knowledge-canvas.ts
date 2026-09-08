import type { AgentProfile } from '../api/types';
import { isUserManagedAgent } from './agent-scope';

export interface KnowledgeCanvasReference {
  workspaceId: string;
  agentId: string;
  itemId: string;
  version: number;
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

export function canvasPreferenceKey(workspaceId: string, agentId: string): string {
  return `knowledge-canvas:enabled:${encodeURIComponent(workspaceId)}:${encodeURIComponent(agentId)}`;
}

export function readCanvasPreference(workspaceId: string, agent: AgentProfile): boolean {
  if (!isUserManagedAgent(agent)) return false;
  try {
    const stored = typeof window === 'undefined' ? null : window.localStorage.getItem(canvasPreferenceKey(workspaceId, agent.id));
    if (stored === 'true' || stored === 'false') return stored === 'true';
  } catch { /* The in-memory view remains usable when storage is unavailable. */ }
  return agent.role === 'pm';
}

export function writeCanvasPreference(workspaceId: string, agentId: string, enabled: boolean): void {
  try { window.localStorage.setItem(canvasPreferenceKey(workspaceId, agentId), String(enabled)); } catch { /* Optional view persistence. */ }
}

export function canReadAgentKnowledge(role: string | undefined): boolean {
  return role === 'owner' || role === 'admin';
}

export function referenceBelongsTo(reference: KnowledgeCanvasReference, workspaceId: string, agentId: string): boolean {
  return reference.workspaceId === workspaceId && reference.agentId === agentId
    && !!reference.itemId && Number.isSafeInteger(reference.version) && reference.version > 0;
}

export function buildCanvasMessage(question: string, reference: KnowledgeCanvasReference | null): string {
  if (!reference) return question.trim();
  const title = reference.title.replace(/[\r\n]/g, ' ');
  const quote = reference.quote.length > 6000 ? `${reference.quote.slice(0, 6000)}\n（摘录到此为止，可按知识标识读取该版本全文。）` : reference.quote;
  return `${question.trim()}${REFERENCE_START}${JSON.stringify({ ...reference, title, quote })}${REFERENCE_END}`;
}

const REFERENCE_START = '\n\n<atw-knowledge-reference-v1>\n';
const REFERENCE_END = '\n</atw-knowledge-reference-v1>';

/** Only our explicit, versioned trailer is folded; ordinary user prose stays literal. */
export function parseCanvasMessage(text: string): { question: string; reference: KnowledgeCanvasReference } | null {
  if (!text.endsWith(REFERENCE_END)) return null;
  const index = text.lastIndexOf(REFERENCE_START);
  if (index < 1) return null;
  try {
    const value = JSON.parse(text.slice(index + REFERENCE_START.length, -REFERENCE_END.length)) as unknown;
    if (!value || typeof value !== 'object' || Array.isArray(value)) return null;
    const record = value as Record<string, unknown>;
    if (!['workspaceId', 'agentId', 'itemId', 'title', 'quote'].every((key) => typeof record[key] === 'string' && record[key] !== '')
      || typeof record.version !== 'number' || !Number.isSafeInteger(record.version) || record.version < 1
      || (record.heading !== undefined && typeof record.heading !== 'string')) return null;
    return { question: text.slice(0, index), reference: record as unknown as KnowledgeCanvasReference };
  } catch {
    return null;
  }
}
