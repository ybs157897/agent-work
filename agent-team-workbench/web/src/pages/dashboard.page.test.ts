import { describe, expect, it } from 'vitest';
import { activityMeta, roleLabel, taskPercent } from './dashboard.page';

describe('dashboard display helpers', () => {
  it('将常见角色转换为可读中文名称，并保留未知角色', () => {
    expect(roleLabel('developer')).toBe('开发工程师');
    expect(roleLabel('reviewer')).toBe('评审验收');
    expect(roleLabel('researcher')).toBe('researcher');
  });

  it('将活动枚举转换为面向用户的名称和视觉状态', () => {
    expect(activityMeta('run.created')).toEqual({ label: '运行已创建', tone: 'brand' });
    expect(activityMeta('run.failed')).toEqual({ label: '运行失败', tone: 'error' });
    expect(activityMeta('custom.completed')).toEqual({ label: '已完成', tone: 'success' });
    expect(activityMeta('custom.changed')).toEqual({ label: '系统活动', tone: 'neutral' });
  });

  it('任务占比按整数显示，空看板不会产生无效数值', () => {
    expect(taskPercent(2, 5)).toBe(40);
    expect(taskPercent(0, 0)).toBe(0);
  });
});
