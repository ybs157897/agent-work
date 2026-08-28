import { describe, expect, it } from 'vitest';
import type { ApprovalRequest } from '../api/types';
import type { ChatMessage } from '../stores/chat.store';
import { mergeApprovalSegments, transcriptSegmentKey } from './approval-transcript';
import type { TranscriptSegment } from './chronological-transcript';

const msg = (key: string, kind: ChatMessage['kind'], text: string): TranscriptSegment => ({
  kind: kind === 'user' ? 'user' : 'assistant',
  msg: { key, runId: 'r1', kind, text, at: 't1' },
});

const approval = (id: string, status: ApprovalRequest['status'] = 'pending'): ApprovalRequest => ({
  id,
  run_id: 'r1',
  work_item_id: 'wi1',
  kind: 'command',
  risk: 'high',
  status,
  summary: 'go test',
});

describe('mergeApprovalSegments', () => {
  it('inserts approval after anchor so later segments stay below', () => {
    const before = [msg('u1', 'user', 'hi'), msg('tool1', 'assistant', 'tool phase')];
    const after = [
      msg('u1', 'user', 'hi'),
      msg('tool1', 'assistant', 'tool phase'),
      msg('think1', 'assistant', 'after approval'),
    ];
    const anchors = { ap1: 'tool1' };

    const mergedBefore = mergeApprovalSegments(before, [approval('ap1')], anchors);
    expect(mergedBefore.map((s) => s.kind)).toEqual(['user', 'assistant', 'approval']);

    const mergedAfter = mergeApprovalSegments(after, [approval('ap1')], anchors);
    expect(mergedAfter.map((s) => s.kind)).toEqual(['user', 'assistant', 'approval', 'assistant']);
  });

  it('同一工具批次追加新调用时保持稳定 key，后续批次使用新的首工具 key', () => {
    const first: TranscriptSegment = {
      kind: 'activity',
      runId: 'r1',
      items: [{ key: 'tool-a', runId: 'r1', kind: 'tool', text: 'a', at: 't1', toolStatus: 'running' }],
    };
    const expanded: TranscriptSegment = {
      kind: 'activity',
      runId: 'r1',
      items: [
        ...first.items,
        { key: 'tool-b', runId: 'r1', kind: 'tool', text: 'b', at: 't2', toolStatus: 'running' },
      ],
    };
    const later: TranscriptSegment = {
      kind: 'activity',
      runId: 'r1',
      items: [{ key: 'tool-c', runId: 'r1', kind: 'tool', text: 'c', at: 't3', toolStatus: 'running' }],
    };
    expect(transcriptSegmentKey(first)).toBe(transcriptSegmentKey(expanded));
    expect(transcriptSegmentKey(later)).not.toBe(transcriptSegmentKey(first));
  });
});
