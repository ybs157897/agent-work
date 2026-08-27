import { describe, expect, it } from 'vitest';
import { AppShellSkeleton } from './skeleton';

function classNames(node: unknown): string {
  if (!node || typeof node !== 'object') return '';
  const el = node as { props?: { className?: string; children?: unknown } };
  const self = el.props?.className ?? '';
  const children = el.props?.children;
  const nested = Array.isArray(children)
    ? children.map(classNames).join(' ')
    : classNames(children);
  return `${self} ${nested}`;
}

describe('AppShellSkeleton', () => {
  it('/chat 启动骨架挂 tx-scope，不画宣纸顶栏', () => {
    const tree = classNames(AppShellSkeleton({ chat: true }));
    expect(tree).toContain('tx-scope');
    expect(tree).not.toContain('bg-surface-raised/80');
    expect(tree).toContain('bg-sidebar');
  });

  it('非对话页启动骨架保留宣纸顶栏', () => {
    const tree = classNames(AppShellSkeleton({}));
    expect(tree).toContain('bg-surface-raised/80');
    expect(tree).not.toContain('tx-scope');
    expect(tree).toContain('bg-surface-base');
  });
});
