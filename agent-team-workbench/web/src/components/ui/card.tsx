import type { HTMLAttributes } from 'react';
import { cx } from './cx';

/** 三个预设逐字对应 index.css 遗留类 .ui-card / .ui-card-padded / .ui-card-interactive。 */
export const cardPresetClasses = {
  plain: 'bg-surface-raised rounded-card border border-border-subtle shadow-card',
  padded: 'bg-surface-raised rounded-card border border-border-subtle shadow-card p-comfortable',
  interactive:
    'bg-surface-raised rounded-card border border-border-subtle shadow-card p-comfortable cursor-pointer transition-all duration-200 ease-out hover:-translate-y-0.5 hover:shadow-level-2 hover:border-border-strong',
} as const;

export function cardClassName(options: { padded?: boolean; interactive?: boolean }): string {
  if (options.interactive) return cardPresetClasses.interactive;
  return options.padded ? cardPresetClasses.padded : cardPresetClasses.plain;
}

export type CardProps = HTMLAttributes<HTMLDivElement> & {
  padded?: boolean;
  interactive?: boolean;
};

/** interactive 预设自带 p-comfortable（遗留类语义），与 padded 同开时不重复叠加。 */
export function Card({ padded, interactive, className, ...props }: CardProps) {
  return <div {...props} className={cx(cardClassName({ padded, interactive }), className)} />;
}
