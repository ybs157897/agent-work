import { describe, expect, it } from 'vitest';
import { runPanelOwnershipText } from './run-panel';

describe('runPanelOwnershipText', () => {
  it('legacy 任务不显示 Coordinator 托管', () => {
    expect(runPanelOwnershipText('legacy')).toBe('历史任务 · 未启用 Coordinator 控制线');
    expect(runPanelOwnershipText('coordinated')).toBe('由 Coordinator 托管');
    expect(runPanelOwnershipText('loading')).toBe('由 Coordinator 托管');
  });
});
