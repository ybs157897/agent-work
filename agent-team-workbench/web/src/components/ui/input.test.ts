import { describe, expect, it } from 'vitest';
import { Input, Select, Textarea, inputFieldClasses } from './input';

describe('inputFieldClasses', () => {
  it('输入控件关键类：strong 描边 / raised 底 / body 字号 / 焦点环', () => {
    expect(inputFieldClasses).toContain('rounded-input');
    expect(inputFieldClasses).toContain('border-border-strong');
    expect(inputFieldClasses).toContain('bg-surface-raised');
    expect(inputFieldClasses).toContain('px-snug py-tight');
    expect(inputFieldClasses).toContain('text-body');
    expect(inputFieldClasses).toContain('outline-none');
    expect(inputFieldClasses).toContain('focus:border-brand-primary/40');
    expect(inputFieldClasses).toContain('focus:ring-2');
    expect(inputFieldClasses).toContain('focus:ring-brand-primary/20');
  });
});

describe('Input / Textarea / Select', () => {
  it('Input/Textarea 共用输入样式；Select 同系但去 OS 箭头', () => {
    expect(Input({}).props.className).toBe(inputFieldClasses);
    expect(Textarea({}).props.className).toBe(inputFieldClasses);

    // Select 外层只负责定位 chevron；className 必须落在真实 select 控件上
    const wrapper = Select({ className: 'extra-control' });
    expect(wrapper.type).toBe('span');
    const inner = wrapper.props.children[0];
    expect(inner.type).toBe('select');
    expect(inner.props.className).toContain('rounded-input');
    expect(inner.props.className).toContain('appearance-none');
    expect(inner.props.className).toContain('pr-8');
    expect(inner.props.className).toContain('extra-control');
    expect(wrapper.props.className).not.toContain('extra-control');
    expect(inner.props.className).not.toContain('mt-micro');
  });

  it('invalid 控件切到 error 描边并带 aria-invalid，中性描边不残留', () => {
    const input = Input({ invalid: true });
    expect(input.props['aria-invalid']).toBe(true);
    expect(input.props.className).toContain('border-status-error/60');
    expect(input.props.className).toContain('focus:ring-status-error/20');
    expect(input.props.className).not.toContain('border-border-strong');

    const wrapper = Select({ invalid: true });
    const inner = wrapper.props.children[0];
    expect(inner.props['aria-invalid']).toBe(true);
    expect(inner.props.className).toContain('border-status-error/60');
  });

  it('默认（无 invalid）走中性描边，不设 aria-invalid', () => {
    const input = Input({});
    expect(input.props['aria-invalid']).toBeUndefined();
    expect(input.props.className).toContain('border-border-strong');
  });

  it('元素标签正确且原生 props 原样透传，className 追加在预设后', () => {
    const input = Input({ placeholder: '名称', disabled: true, className: 'extra' });
    expect(input.type).toBe('input');
    expect(input.props.placeholder).toBe('名称');
    expect(input.props.disabled).toBe(true);
    expect(input.props.className.endsWith('extra')).toBe(true);

    expect(Textarea({ rows: 3 }).type).toBe('textarea');
    expect(Textarea({ rows: 3 }).props.rows).toBe(3);

    const wrapper = Select({ 'aria-label': '模型', wrapperClassName: 'layout-hook' });
    expect(wrapper.type).toBe('span');
    expect(wrapper.props.children[0].props['aria-label']).toBe('模型');
    expect(wrapper.props.className).toContain('layout-hook');
  });
});
