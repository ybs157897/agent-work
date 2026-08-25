import type { ReactNode } from 'react';
import { cx } from './cx';

// 校验态契约（DESIGN.md 校验条）：错误文案顶替 hint；输入框侧错误描边由控件的 invalid 属性承担。
const FIELD_LABEL_CLASSES = 'block text-caption text-text-secondary';
const FIELD_HINT_CLASSES = 'mt-micro block text-caption text-text-tertiary';
const FIELD_ERROR_CLASSES = 'mt-micro block text-status-error text-caption';

/** 校验错误文案（主动语态、说清怎么改、不用感叹号）；与控件的 invalid 态配对出现。 */
export function FieldError({ children }: { children: ReactNode }) {
  return (
    <span role="alert" className={FIELD_ERROR_CLASSES}>
      {children}
    </span>
  );
}

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
      {error ? <FieldError>{error}</FieldError> : null}
      {!error && hint ? <span className={FIELD_HINT_CLASSES}>{hint}</span> : null}
    </label>
  );
}
