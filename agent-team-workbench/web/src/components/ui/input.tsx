import { ChevronDown } from 'lucide-react';
import type { InputHTMLAttributes, SelectHTMLAttributes, TextareaHTMLAttributes } from 'react';
import { cx } from './cx';

/** 输入控件契约（DESIGN.md 表单条）：strong 描边 / raised 底 / body 字号 / 品牌焦点环。 */
const fieldBodyClasses =
  'w-full rounded-input border bg-surface-raised px-snug py-tight text-body outline-none transition-shadow focus:ring-2';

/** 校验态边框契约（DESIGN.md 校验条）：中性 = strong 描边 + 品牌焦点环；错误 = error 描边 + error 焦点环。 */
export const fieldChromeNeutral = 'border-border-strong focus:border-brand-primary/40 focus:ring-brand-primary/20';
export const fieldChromeInvalid = 'border-status-error/60 focus:border-status-error focus:ring-status-error/20';

export const inputFieldClasses = `mt-micro ${fieldBodyClasses} ${fieldChromeNeutral}`;

export function Input({ className, invalid, ...props }: InputHTMLAttributes<HTMLInputElement> & { invalid?: boolean }) {
  return (
    <input
      {...props}
      aria-invalid={invalid || undefined}
      className={cx('mt-micro', fieldBodyClasses, invalid ? fieldChromeInvalid : fieldChromeNeutral, className)}
    />
  );
}

export function Textarea({ className, invalid, ...props }: TextareaHTMLAttributes<HTMLTextAreaElement> & { invalid?: boolean }) {
  return (
    <textarea
      {...props}
      aria-invalid={invalid || undefined}
      className={cx('mt-micro', fieldBodyClasses, invalid ? fieldChromeInvalid : fieldChromeNeutral, className)}
    />
  );
}

/**
 * 下拉选择：外观与 Input 同系，但 appearance-none 去掉 OS 默认箭头，
 * 换自定义 chevron（DESIGN.md select 条）。className 始终挂在真实 select
 * 控件上，避免把输入框 chrome 同时绘制在包装层和控件层。
 * 需要调整布局时使用 wrapperClassName，保持控件本身的一致对齐。
 */
export function Select({
  className,
  wrapperClassName,
  invalid,
  children,
  ...props
}: SelectHTMLAttributes<HTMLSelectElement> & { invalid?: boolean; wrapperClassName?: string }) {
  return (
    <span className={cx('relative block mt-micro', wrapperClassName)}>
      <select
        {...props}
        aria-invalid={invalid || undefined}
        className={cx(
          fieldBodyClasses,
          invalid ? fieldChromeInvalid : fieldChromeNeutral,
          'appearance-none pr-8 cursor-pointer',
          className,
        )}
      >
        {children}
      </select>
      <ChevronDown className="pointer-events-none absolute right-3 top-1/2 h-4 w-4 -translate-y-1/2 text-text-tertiary" />
    </span>
  );
}
