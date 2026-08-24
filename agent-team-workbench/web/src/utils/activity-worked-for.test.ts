import { describe, expect, it } from 'vitest';
import type { ChatMessage } from '../stores/chat.store';
import { activityWorkedFor } from './activity-worked-for';

const at = (ms: number) => new Date(ms).toISOString();

function tool(partial: Partial<ChatMessage> & Pick<ChatMessage, 'key'>): ChatMessage {
  return {
    runId: 'r1',
    kind: 'tool',
    text: '',
    at: at(0),
    toolStatus: 'success',
    ...partial,
  };
}

describe('activityWorkedFor', () => {
  it('uses tool timing when present', () => {
    const items = [
      tool({ key: 'a', startedAt: at(0), completedAt: at(5_000) }),
      tool({ key: 'b', startedAt: at(1_000), completedAt: at(3_000) }),
    ];
    expect(activityWorkedFor(items)).toBe('5 秒');
  });

  it('extends end to assistant completion when tools finish early', () => {
    const items = [tool({ key: 'a', startedAt: at(0), completedAt: at(2_000) })];
    expect(activityWorkedFor(items, undefined, at(10_000))).toBe('10 秒');
  });

  it('falls back to reasoning at → assistant at when no tools', () => {
    expect(
      activityWorkedFor([], { key: 't', text: 'think', at: at(0) }, at(4_000)),
    ).toBe('4 秒');
  });
});
