import { describe, expect, it } from 'vitest';
import type { ChatMessage } from '../stores/chat.store';
import type { TimelineEntry } from '../stores/runs.store';
import { deriveChatDock, deriveProposedPlan, deriveSessionGoal, deriveTodoPlan } from './derive-chat-dock';

const user = (text: string, key = text): ChatMessage => ({
  key,
  runId: 'r1',
  kind: 'user',
  text,
  at: '2026-01-01T00:00:00Z',
});

const assistant = (text: string, itemType?: string): ChatMessage => ({
  key: `a-${text.slice(0, 8)}`,
  runId: 'r1',
  kind: 'assistant',
  text,
  at: '2026-01-01T00:00:01Z',
  itemType,
});

describe('deriveSessionGoal', () => {
  it('returns undefined without /goal or goal events', () => {
    expect(deriveSessionGoal([user('hello')])).toBeUndefined();
    expect(deriveSessionGoal([user('fix bug')], {})).toBeUndefined();
  });

  it('parses inline /goal objective', () => {
    expect(deriveSessionGoal([user('/goal ship v1')])).toEqual({
      objective: 'ship v1',
      status: 'active',
    });
  });

  it('parses armed /goal then next user message', () => {
    expect(deriveSessionGoal([user('/goal'), user('refactor auth module')])).toEqual({
      objective: 'refactor auth module',
      status: 'active',
    });
  });

  it('prefers timeline goal events over slash parsing', () => {
    const timelines: Record<string, TimelineEntry[]> = {
      r1: [
        {
          event_id: 'e1',
          stream_seq: 1,
          type: 'goal.updated',
          occurred_at: '2026-01-01T00:00:02Z',
          data: { objective: 'from event', status: 'active' },
        },
      ],
    };
    expect(deriveSessionGoal([user('/goal old')], timelines)).toEqual({
      objective: 'from event',
      status: 'active',
    });
  });

  it('goal.clear removes goal from events', () => {
    const timelines: Record<string, TimelineEntry[]> = {
      r1: [
        {
          event_id: 'e1',
          stream_seq: 1,
          type: 'goal.updated',
          occurred_at: '2026-01-01T00:00:01Z',
          data: { objective: 'temp' },
        },
        {
          event_id: 'e2',
          stream_seq: 2,
          type: 'goal.clear',
          occurred_at: '2026-01-01T00:00:02Z',
        },
      ],
    };
    expect(deriveSessionGoal([], timelines)).toBeUndefined();
  });
});

describe('deriveTodoPlan', () => {
  it('requires non-empty steps on latest run', () => {
    const messages: ChatMessage[] = [
      {
        key: 'p',
        runId: 'r1',
        kind: 'plan',
        text: '',
        at: '',
        steps: [],
      },
    ];
    expect(deriveTodoPlan(messages, 'r1')).toBeUndefined();

    messages[0].steps = [{ step: 'a', status: 'pending' }];
    expect(deriveTodoPlan(messages, 'r1')?.steps).toHaveLength(1);
  });
});

describe('deriveProposedPlan', () => {
  it('only picks assistant item_type plan on latest run', () => {
    expect(
      deriveProposedPlan(
        [assistant('normal reply'), assistant('# Plan\n- step 1', 'plan')],
        'r1',
      ),
    ).toBe('# Plan\n- step 1');
    expect(deriveProposedPlan([assistant('normal reply')], 'r1')).toBeUndefined();
  });
});

describe('deriveChatDock', () => {
  it('returns empty dock when nothing active', () => {
    expect(deriveChatDock([user('hi')], 'r1', {})).toEqual({});
  });
});
