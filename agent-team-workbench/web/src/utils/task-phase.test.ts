import { describe, expect, it } from 'vitest';
import type { WorkItem } from '../api/types';
import { isAwaitingAcceptance } from './task-phase';

const task = (status: WorkItem['status'], phase?: WorkItem['phase']): Pick<WorkItem, 'status' | 'phase'> => ({
  status,
  ...(phase ? { phase } : {}),
});

describe('isAwaitingAcceptance', () => {
  it('review 与 acceptance 阶段（in_progress）都提示待验收', () => {
    expect(isAwaitingAcceptance(task('in_progress', 'review'))).toBe(true);
    expect(isAwaitingAcceptance(task('in_progress', 'acceptance'))).toBe(true);
  });

  it('execution、缺省 phase 不提示', () => {
    expect(isAwaitingAcceptance(task('in_progress', 'execution'))).toBe(false);
    expect(isAwaitingAcceptance(task('in_progress'))).toBe(false);
  });

  it('phase 只在 in_progress 期间有意义：其余看板列一律不提示', () => {
    expect(isAwaitingAcceptance(task('todo', 'acceptance'))).toBe(false);
    expect(isAwaitingAcceptance(task('completed', 'acceptance'))).toBe(false);
    expect(isAwaitingAcceptance(task('blocked', 'review'))).toBe(false);
    expect(isAwaitingAcceptance(task('cancelled', 'review'))).toBe(false);
  });
});
