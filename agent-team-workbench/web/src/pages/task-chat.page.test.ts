import { describe, expect, it } from 'vitest';
import type { ModelEntry } from '../api/types';
import {
  followTaskIntakeScroll,
  intakeCompatibleModels,
  isTaskIntakeNearBottom,
  parseAcceptanceCriteria,
} from './task-chat.page';

describe('TaskChatPage helpers', () => {
  it('trims and removes blank acceptance criteria', () => {
    expect(parseAcceptanceCriteria('  first\n\n second  \n   ')).toEqual(['first', 'second']);
    expect(parseAcceptanceCriteria(' \n ')).toEqual([]);
  });

  it('only follows near-bottom content using immediate scrollTop', () => {
    expect(isTaskIntakeNearBottom({ scrollHeight: 1000, scrollTop: 820, clientHeight: 160 })).toBe(true);
    expect(isTaskIntakeNearBottom({ scrollHeight: 1000, scrollTop: 500, clientHeight: 160 })).toBe(false);
    const element = { scrollHeight: 1000, scrollTop: 500 };
    followTaskIntakeScroll(element, false);
    expect(element.scrollTop).toBe(500);
    followTaskIntakeScroll(element, true);
    expect(element.scrollTop).toBe(1000);
  });

  it('filters analysis models to registered OpenAI endpoints', () => {
    const model = (id: string, api: ModelEntry['api'], baseUrl?: string): ModelEntry => ({
      id,
      display_name: id,
      provider_id: 'provider',
      provider: 'provider',
      api,
      model: id,
      ...(baseUrl === undefined ? {} : { base_url: baseUrl }),
    });
    expect(intakeCompatibleModels([
      model('chat', 'openai-completions', 'https://api.example.com/v1'),
      model('responses', 'openai-responses', 'https://api.example.com/v1'),
      model('anthropic', 'anthropic-messages', 'https://api.example.com/v1'),
      model('missing-url', 'openai-completions'),
    ]).map((item) => item.id)).toEqual(['chat', 'responses']);
  });
});
