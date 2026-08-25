import { ChevronDown } from 'lucide-react';
import type { InputHTMLAttributes, SelectHTMLAttributes, TextareaHTMLAttributes } from 'react';
import { cx } from './cx';

/** 逐字对应 index.css 遗留类 .input-field（mt-1 → 语义等价 mt-micro）。 */
const fieldBodyClasses =
  'w-full rounded-input border border-border-strong bg-surface-raised px-snug py-tight text-body outline-none transition-shadow focus:border-brand-primary/40 focus:ring-2 focus:ring-brand-primary/20';

export const inputFieldClasses = `mt-micro ${fieldBodyClasses}`;

export function Input({ className, ...props }: InputHTMLAttributes<HTMLInputElement>) {
  return <input {...props} className={cx(inputFieldClasses, className)} />;
}

export function Textarea({ className, ...props }: TextareaHTMLAttributes<HTMLTextAreaElement>) {
  return <textarea {...props} className={cx(inputFieldClasses, className)} />;
}

/**
 * 下拉选择：外观与 input-field 同系，但 appearance-none 去掉 OS 默认箭头，
 * 换自定义 chevron（DESIGN.md select 条）。布局类（className）挂外层容器。
 */
export function Select({ className, children, ...props }: SelectHTMLAttributes<HTMLSelectElement>) {
  return (
    <span className={cx('relative block mt-micro', className)}>
      <select
        {...props}
        className={cx(fieldBodyClasses, 'appearance-none pr-8 cursor-pointer')}
      >
        {children}
      </select>
      <ChevronDown className="pointer-events-none absolute right-3 top-1/2 h-4 w-4 -translate-y-1/2 text-text-tertiary" />
    </span>
  );
}
