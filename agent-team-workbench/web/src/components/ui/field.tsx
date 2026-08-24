import type { ReactNode } from 'react';
import { cx } from './cx';

// 校验态视觉规格是 DESIGN.md 的 Known Gap：这里只做占位约定——error 出现时顶替 hint，
// 不给输入框本身发明错误样式。
const FIELD_LABEL_CLASSES = 'block text-caption text-text-secondary';
const FIELD_HINT_CLASSES = 'mt-micro block text-caption text-text-tertiary';
const FIELD_ERROR_CLASSES = 'mt-micro block text-status-error text-caption';

export type FieldProps = {
  label: string;
  hint?: string;
  error?: string;
  /** 被包裹的控件；外层用 label 元素隐式关联，点击文案即聚焦控件。 */
  children: ReactNode;
  className?: string;
};

export function Field({ label, hint, error, children, className }: FieldProps) {
  return (
    <label className={cx('block', className)}>
      <span className={FIELD_LABEL_CLASSES}>{label}</span>
      {children}
      {error ? (
        <span role="alert" className={FIELD_ERROR_CLASSES}>
          {error}
        </span>
      ) : null}
      {!error && hint ? <span className={FIELD_HINT_CLASSES}>{hint}</span> : null}
    </label>
  );
}
