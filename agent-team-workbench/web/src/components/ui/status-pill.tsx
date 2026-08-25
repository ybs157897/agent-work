import type { HTMLAttributes } from 'react';
import { cx } from './cx';

/** 状态胶囊（顶栏 SSE 连接态等）：全圆角 + raised 底 + caption 字。 */
export const statusPillClasses =
  'inline-flex items-center gap-tight rounded-full border border-border-subtle bg-surface-raised px-snug py-micro text-caption text-text-secondary';

export function StatusPill({ className, ...props }: HTMLAttributes<HTMLSpanElement>) {
  return <span {...props} className={cx(statusPillClasses, className)} />;
}
