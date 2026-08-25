import { describe, expect, it } from 'vitest';
import { Field, FieldError } from './field';

/** Field 的 children 是 JSX 元素；测试环境下取 props 做断言，窄化到可判别的形状。 */
function propsOf(node: unknown): Record<string, unknown> | null {
  if (node && typeof node === 'object' && 'props' in node) {
    return (node as { props: Record<string, unknown> }).props;
  }
  return null;
}

describe('FieldError', () => {
  it('role=alert + error 文案色，供 Field 与配置页表单共用', () => {
    const el = FieldError({ children: 'Base URL 需以 http:// 或 https:// 开头' });
    expect(el.type).toBe('span');
    expect(el.props.role).toBe('alert');
    expect(el.props.className).toContain('text-status-error');
    expect(el.props.className).toContain('text-caption');
  });
});

describe('Field', () => {
  // .ts 测试无 JSX，用字符串占位控件（ReactNode 合法，断言只关心身份与顺序）
  const children = '[control]';

  it('无 error 时展示 hint', () => {
    const el = Field({ label: 'Base URL', hint: '留空走默认端点', children });
    const nodes = (el.props.children as unknown[]).filter(Boolean);
    expect(nodes.some((n) => String(propsOf(n)?.className ?? '').includes('text-text-tertiary'))).toBe(true);
  });

  it('error 出现时顶替 hint，错误槽位是 FieldError（role=alert 由 FieldError 契约保证）', () => {
    const el = Field({ label: 'Base URL', hint: '留空走默认端点', error: '需以 http:// 开头', children });
    const nodes = (el.props.children as unknown[]).filter(Boolean);
    const errorEl = nodes.find((n) => typeof n === 'object' && n !== null && 'type' in n && n.type === FieldError);
    expect(errorEl).toBeTruthy();
    expect(propsOf(errorEl)?.children).toBe('需以 http:// 开头');
    const hint = nodes.find((n) => String(propsOf(n)?.className ?? '').includes('text-text-tertiary'));
    expect(hint).toBeUndefined();
  });

  it('外层是 label（点击文案即聚焦控件），文案与控件顺序固定', () => {
    const el = Field({ label: '名称', children });
    expect(el.type).toBe('label');
    const [labelSpan, control] = el.props.children as unknown[];
    expect(propsOf(labelSpan)?.children).toBe('名称');
    expect(control).toBe(children);
  });
});
