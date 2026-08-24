import type { InputHTMLAttributes, SelectHTMLAttributes, TextareaHTMLAttributes } from 'react';
import { cx } from './cx';

/** 逐字对应 index.css 遗留类 .input-field（mt-1 → 语义等价 mt-micro）。 */
export const inputFieldClasses =
  'mt-micro w-full rounded-input border border-border-strong bg-surface-raised px-snug py-tight text-body outline-none transition-shadow focus:border-brand-primary/40 focus:ring-2 focus:ring-brand-primary/20';

export function Input({ className, ...props }: InputHTMLAttributes<HTMLInputElement>) {
  return <input {...props} className={cx(inputFieldClasses, className)} />;
}

export function Textarea({ className, ...props }: TextareaHTMLAttributes<HTMLTextAreaElement>) {
  return <textarea {...props} className={cx(inputFieldClasses, className)} />;
}

export function Select({ className, ...props }: SelectHTMLAttributes<HTMLSelectElement>) {
  return <select {...props} className={cx(inputFieldClasses, className)} />;
}
