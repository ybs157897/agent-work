import { describe, expect, it } from 'vitest';
import { Card, cardClassName, cardPresetClasses } from './card';

describe('cardClassName', () => {
  it('plain 预设 = .ui-card 基础外观（raised 底 + card 圆角 + subtle 描边 + level-1 阴影）', () => {
    const cls = cardPresetClasses.plain;
    expect(cls).toContain('bg-surface-raised');
    expect(cls).toContain('rounded-card');
    expect(cls).toContain('border-border-subtle');
    expect(cls).toContain('shadow-card');
  });

  it('padded 只追加 p-comfortable', () => {
    const cls = cardPresetClasses.padded;
    for (const marker of ['bg-surface-raised', 'rounded-card', 'border-border-subtle', 'shadow-card']) {
      expect(cls).toContain(marker);
    }
    expect(cls).toContain('p-comfortable');
  });

  it('interactive 追加指针 + 200ms 过渡 + 悬停三件套（上浮/升 level-2 阴影/描边加深）', () => {
    const cls = cardPresetClasses.interactive;
    expect(cls).toContain('cursor-pointer');
    expect(cls).toContain('transition-all duration-200 ease-out');
    expect(cls).toContain('hover:-translate-y-0.5');
    expect(cls).toContain('hover:shadow-level-2');
    expect(cls).toContain('hover:border-border-strong');
  });

  it('padded 与 interactive 同开时取 interactive 预设，不叠加双份 padding', () => {
    const cls = cardClassName({ padded: true, interactive: true });
    expect(cls).toBe(cardPresetClasses.interactive);
    expect(cls.match(/p-comfortable/g)).toHaveLength(1);
  });

  it('布尔开关映射到三个预设，默认无 padding 无交互', () => {
    expect(cardClassName({})).toBe(cardPresetClasses.plain);
    expect(cardClassName({ padded: true })).toBe(cardPresetClasses.padded);
    expect(cardClassName({ interactive: true })).toBe(cardPresetClasses.interactive);
  });
});

describe('Card', () => {
  it('className 追加在预设之后，其余原生 props 原样透传', () => {
    const el = Card({ padded: true, 'aria-label': '分组', className: 'mt-base' });
    expect(el.type).toBe('div');
    expect(el.props['aria-label']).toBe('分组');
    expect(el.props.className.startsWith(cardPresetClasses.padded)).toBe(true);
    expect(el.props.className.endsWith('mt-base')).toBe(true);
  });
});
