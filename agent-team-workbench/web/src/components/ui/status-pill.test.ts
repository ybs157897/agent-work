import { describe, expect, it } from 'vitest';
import { StatusPill, statusPillClasses } from './status-pill';

describe('statusPillClasses', () => {
  it('胶囊形 + subtle 描边 + raised 底 + caption 次要文字', () => {
    expect(statusPillClasses).toContain('inline-flex items-center');
    expect(statusPillClasses).toContain('gap-tight');
    expect(statusPillClasses).toContain('rounded-full');
    expect(statusPillClasses).toContain('border-border-subtle');
    expect(statusPillClasses).toContain('bg-surface-raised');
    expect(statusPillClasses).toContain('px-snug py-micro');
    expect(statusPillClasses).toContain('text-caption');
    expect(statusPillClasses).toContain('text-text-secondary');
  });

  it('StatusPill 渲染为 span，className 追加在预设后，其余 props 原样透传', () => {
    const el = StatusPill({ title: '运行状态', className: 'shrink-0' });
    expect(el.type).toBe('span');
    expect(el.props.title).toBe('运行状态');
    expect(el.props.className.startsWith(statusPillClasses)).toBe(true);
    expect(el.props.className.endsWith('shrink-0')).toBe(true);
  });
});
