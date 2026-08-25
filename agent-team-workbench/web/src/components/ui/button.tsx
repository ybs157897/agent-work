import type { ButtonHTMLAttributes } from 'react';
import { cx } from './cx';

export type ButtonVariant = 'primary' | 'secondary' | 'ghost' | 'success' | 'danger' | 'danger-outline' | 'warning-outline';
export type ButtonSize = 'sm' | 'md';

/**
 * 类名逐字对应 index.css 遗留类 .btn / .btn-*（@apply 展开）。
 * 内边距不进基类：sm/md 各自带全量 padding，避免同属性工具类在产物 CSS 里比拼顺序。
 */
const BUTTON_BASE_CLASSES =
  'inline-flex items-center justify-center gap-tight rounded-button text-body font-medium transition-all duration-150 focus-visible:ring-2 focus-visible:ring-brand-primary/40 focus-visible:ring-offset-2 disabled:opacity-50 disabled:cursor-not-allowed disabled:pointer-events-none';

export const buttonSizeClasses: Record<ButtonSize, string> = {
  sm: 'px-snug py-micro',
  md: 'px-base py-tight',
};

export const buttonVariantClasses: Record<ButtonVariant, string> = {
  primary: 'bg-brand-primary text-text-inverse shadow-sm hover:bg-brand-accent active:scale-[0.98]',
  secondary:
    'border border-border-strong bg-surface-raised text-text-secondary hover:bg-surface-base hover:text-text-primary active:scale-[0.98]',
  ghost:
    'border border-brand-primary/30 bg-transparent text-brand-primary hover:bg-brand-primary/5 active:scale-[0.98]',
  success: 'bg-status-success text-text-inverse hover:bg-status-success/90 active:scale-[0.98]',
  danger: 'bg-status-error text-text-inverse hover:bg-status-error/90 active:scale-[0.98]',
  'danger-outline':
    'border border-status-error/35 bg-surface-base text-status-error hover:bg-status-error/5 active:scale-[0.98]',
  'warning-outline':
    'border border-status-warning/40 bg-surface-base text-status-warning hover:bg-status-warning/5 active:scale-[0.98]',
};

export function buttonClassName(variant: ButtonVariant, size: ButtonSize): string {
  return cx(BUTTON_BASE_CLASSES, buttonSizeClasses[size], buttonVariantClasses[variant]);
}

export type ButtonProps = ButtonHTMLAttributes<HTMLButtonElement> & {
  /** 默认 secondary：DESIGN.md 要求每区域至多一个 primary，默认档不做强调色。 */
  variant?: ButtonVariant;
  size?: ButtonSize;
};

export function Button({ variant = 'secondary', size = 'md', className, ...props }: ButtonProps) {
  return <button {...props} className={cx(buttonClassName(variant, size), className)} />;
}
