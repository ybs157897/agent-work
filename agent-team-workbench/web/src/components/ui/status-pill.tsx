import type { HTMLAttributes } from 'react';
import { cx } from './cx';

/** 逐字对应 index.css 遗留类 .status-pill（px-3/py-1/gap-2 → 语义等价 px-snug/py-micro/gap-tight）。 */
export const statusPillClasses =
  'inline-flex items-center gap-tight rounded-full border border-border-subtle bg-surface-raised px-snug py-micro text-caption text-text-secondary';

export function StatusPill({ className, ...props }: HTMLAttributes<HTMLSpanElement>) {
  return <span {...props} className={cx(statusPillClasses, className)} />;
}
