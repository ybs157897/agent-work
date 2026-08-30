import type { ApprovalRequest } from '../api/types';
import type { TranscriptSegment } from './chronological-transcript';

export function transcriptSegmentKey(seg: TranscriptSegment): string {
  switch (seg.kind) {
    case 'user':
      return seg.msg.key;
    case 'assistant':
      return seg.renderKey ?? seg.msg.key;
    case 'thinking':
      return seg.renderKey ?? seg.msg.key;
    case 'meta':
      return seg.msg.key;
    case 'activity':
      // 同一批次追加后续工具时首条不变，避免 ActivityGroup 被 remount、丢失展开态。
      return `activity:${seg.runId}:${seg.items[0]?.key ?? 'empty'}`;
    case 'swarm':
      return `swarm:${seg.runId}:${seg.msg.swarm?.id ?? seg.msg.key}`;
    case 'subagent':
      return `subagent:${seg.runId}:${seg.msg.childAgent?.id ?? seg.msg.key}`;
    case 'thinking-placeholder':
      return `placeholder:${seg.runId}`;
    case 'turn-diff':
      return `turn-diff:${seg.runId}`;
    case 'approval':
      return `approval:${seg.approval.id}`;
  }
}

/**
 * 将审批卡插入 transcript 时间线（kanna：审批 inline，不在 transcript 外另起一块）。
 * anchorKey：审批出现时最后一个 segment 的 key；之后产生的内容自然排在审批之后。
 */
export function mergeApprovalSegments(
  segments: readonly TranscriptSegment[],
  approvals: readonly ApprovalRequest[],
  anchors: Readonly<Record<string, string>>,
): TranscriptSegment[] {
  if (!approvals.length) return [...segments];

  const byAnchor = new Map<string, ApprovalRequest[]>();
  const trailing: ApprovalRequest[] = [];

  for (const approval of approvals) {
    const anchor = anchors[approval.id];
    if (anchor === undefined) {
      trailing.push(approval);
      continue;
    }
    const list = byAnchor.get(anchor) ?? [];
    list.push(approval);
    byAnchor.set(anchor, list);
  }

  const inserted = new Set<string>();
  const out: TranscriptSegment[] = [];

  for (const seg of segments) {
    out.push(seg);
    const key = transcriptSegmentKey(seg);
    const pending = byAnchor.get(key);
    if (!pending?.length) continue;
    for (const approval of pending) {
      out.push({ kind: 'approval', approval });
      inserted.add(approval.id);
    }
  }

  for (const approval of trailing) {
    if (inserted.has(approval.id)) continue;
    out.push({ kind: 'approval', approval });
    inserted.add(approval.id);
  }

  return out;
}
