import { describe, expect, it } from 'vitest';
import { Button, buttonClassName, buttonSizeClasses, buttonVariantClasses } from './button';

const BASE_MARKERS = [
  'inline-flex',
  'items-center',
  'justify-center',
  'gap-tight',
  'rounded-button',
  'text-body',
  'font-medium',
  'transition-all duration-150',
];

describe('buttonClassName', () => {
  it('任意变体都带 .btn 基类与四态（hover/active/focus-visible/disabled）', () => {
    for (const variant of ['primary', 'secondary', 'ghost', 'success', 'danger', 'danger-outline', 'warning-outline'] as const) {
      const cls = buttonClassName(variant, 'md');
      for (const marker of BASE_MARKERS) expect(cls).toContain(marker);
      expect(cls).toContain('focus-visible:ring-2');
      expect(cls).toContain('focus-visible:ring-brand-primary/40');
      expect(cls).toContain('focus-visible:ring-offset-2');
      expect(cls).toContain('disabled:opacity-50');
      expect(cls).toContain('disabled:cursor-not-allowed');
      expect(cls).toContain('disabled:pointer-events-none');
    }
  });

  it('active 按压态由各变体承担（scale-[0.98]）', () => {
    for (const cls of Object.values(buttonVariantClasses)) {
      expect(cls).toContain('active:scale-[0.98]');
    }
  });

  it('primary：品牌底 + 反白文字，悬停加深到 accent', () => {
    const cls = buttonVariantClasses.primary;
    expect(cls).toContain('bg-brand-primary');
    expect(cls).toContain('text-text-inverse');
    expect(cls).toContain('hover:bg-brand-accent');
    expect(cls).not.toContain('border');
  });

  it('secondary：raised 底 + strong 描边，悬停回落画布并加深文字', () => {
    const cls = buttonVariantClasses.secondary;
    expect(cls).toContain('border-border-strong');
    expect(cls).toContain('bg-surface-raised');
    expect(cls).toContain('text-text-secondary');
    expect(cls).toContain('hover:bg-surface-base');
    expect(cls).toContain('hover:text-text-primary');
  });

  it('ghost：透明底 + 品牌描边文字，悬停浅品牌底', () => {
    const cls = buttonVariantClasses.ghost;
    expect(cls).toContain('border-brand-primary/30');
    expect(cls).toContain('bg-transparent');
    expect(cls).toContain('text-brand-primary');
    expect(cls).toContain('hover:bg-brand-primary/5');
  });

  it('success：批准语义，成功绿底 + 反白，悬停 90% 加深', () => {
    const cls = buttonVariantClasses.success;
    expect(cls).toContain('bg-status-success');
    expect(cls).toContain('text-text-inverse');
    expect(cls).toContain('hover:bg-status-success/90');
  });

  it('danger：破坏语义，错误红底 + 反白，悬停 90% 加深', () => {
    const cls = buttonVariantClasses.danger;
    expect(cls).toContain('bg-status-error');
    expect(cls).toContain('text-text-inverse');
    expect(cls).toContain('hover:bg-status-error/90');
  });

  it('danger-outline：破坏轮廓，浅底 + 红色描边文字，悬停红底 5%', () => {
    const cls = buttonVariantClasses['danger-outline'];
    expect(cls).toContain('border-status-error/35');
    expect(cls).toContain('bg-surface-base');
    expect(cls).toContain('text-status-error');
    expect(cls).toContain('hover:bg-status-error/5');
  });

  it('warning-outline：风险确认轮廓，浅底 + 橙色描边文字，悬停橙底 5%', () => {
    const cls = buttonVariantClasses['warning-outline'];
    expect(cls).toContain('border-status-warning/40');
    expect(cls).toContain('bg-surface-base');
    expect(cls).toContain('text-status-warning');
    expect(cls).toContain('hover:bg-status-warning/5');
  });

  it('尺寸档各自独占 padding，不产生同属性工具类冲突', () => {
    expect(buttonSizeClasses.md).toBe('px-base py-tight');
    expect(buttonSizeClasses.sm).toBe('px-snug py-micro');
    expect(buttonClassName('secondary', 'md')).not.toContain('px-snug');
    expect(buttonClassName('secondary', 'md')).not.toContain('py-micro');
    expect(buttonClassName('secondary', 'sm')).not.toContain('px-base');
    expect(buttonClassName('secondary', 'sm')).not.toContain('py-tight');
  });
});

describe('Button', () => {
  it('className 追加在预设之后，其余原生 props 原样透传', () => {
    const el = Button({ variant: 'ghost', size: 'sm', type: 'submit', disabled: true, className: 'w-full' });
    expect(el.type).toBe('button');
    expect(el.props.type).toBe('submit');
    expect(el.props.disabled).toBe(true);
    expect(el.props.className.startsWith(buttonClassName('ghost', 'sm'))).toBe(true);
    expect(el.props.className.endsWith('w-full')).toBe(true);
  });
});
