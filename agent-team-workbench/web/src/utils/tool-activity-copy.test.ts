import { describe, expect, it } from 'vitest';
import { activityGroupHeadSummary, toolActivityTitle } from './tool-activity-copy';

describe('toolActivityTitle', () => {
  it('bash running 带命令摘要', () => {
    expect(
      toolActivityTitle({ family: 'bash', state: 'running', summary: 'npm test' }),
    ).toBe('Running npm test');
  });

  it('read 完成态', () => {
    expect(
      toolActivityTitle({
        family: 'read',
        state: 'ok',
        summary: '',
        filePath: 'src/foo.ts',
      }),
    ).toBe('Read src/foo.ts');
  });

  it('write 失败态', () => {
    expect(
      toolActivityTitle({ family: 'write', state: 'error', summary: 'patch', filePath: 'a.ts' }),
    ).toBe("Couldn't update file");
  });
});

describe('activityGroupHeadSummary', () => {
  it('running 时优先工具摘要', () => {
    expect(activityGroupHeadSummary(['Running npm test'], 'reason line', true)).toBe('Running npm test');
  });

  it('非 running 时用 reasoning 预览', () => {
    expect(activityGroupHeadSummary([], 'last reasoning', false)).toBe('last reasoning');
  });
});
