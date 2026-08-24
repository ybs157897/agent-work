import { describe, expect, it } from 'vitest';
import type { TimelineEntry } from '../stores/runs.store';
import { runHasVisibleOutput } from './run-timeline';

const entry = (type: string): TimelineEntry => ({
  event_id: 'e1',
  stream_seq: 1,
  type,
  occurred_at: '2026-08-24T00:00:00Z',
});

describe('runHasVisibleOutput', () => {
  it('仅 run.* / usage 不算可见进展', () => {
    expect(runHasVisibleOutput([entry('run.created'), entry('run.status_changed'), entry('usage.updated')])).toBe(false);
  });

  it('message/tool/plan 事件算可见进展', () => {
    expect(runHasVisibleOutput([entry('message.delta')])).toBe(true);
    expect(runHasVisibleOutput([entry('tool.started')])).toBe(true);
  });
});
