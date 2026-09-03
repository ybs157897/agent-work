import { describe, expect, it } from 'vitest';
import { parseAcceptanceCriteria } from './create-task-modal';

describe('CreateTaskModal acceptance criteria', () => {
  it('trims and removes blank lines before the root-task guard', () => {
    expect(parseAcceptanceCriteria('  first\n\n second  \n   ')).toEqual(['first', 'second']);
    expect(parseAcceptanceCriteria(' \n ')).toEqual([]);
  });
});
