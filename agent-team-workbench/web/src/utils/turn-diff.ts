import { looksLikeUnifiedDiff, stripControlChars } from '../components/chat/diff-card';
import { classifyTool } from '../components/chat/tool-model';
import type { ChatMessage } from '../stores/chat.store';

/** 从同 run 的 write/edit 工具输出聚合 unified diff 文本（turn 级汇总）。 */
export function aggregateTurnDiff(messages: readonly ChatMessage[]): string | null {
  const parts: string[] = [];
  for (const m of messages) {
    if (m.toolStatus === undefined) continue;
    const family = classifyTool(m.tool);
    if (family !== 'write' && family !== 'edit') continue;
    const raw = (m.detail ?? m.liveOutput ?? '').trim();
    if (!raw) continue;
    const text = stripControlChars(raw);
    if (text !== '' && looksLikeUnifiedDiff(text)) parts.push(text);
  }
  if (!parts.length) return null;
  return parts.join('\n');
}
