import { useEffect, useState } from 'react';
import { cx } from './cx';

/**
 * 骨架屏：默认 300ms 后才显现（快响应不闪烁，DESIGN.md 动效纪律）；
 * animate-pulse，reduced-motion 下降为静态色块。
 */
export function Skeleton({ className, delayMs = 300 }: { className?: string; delayMs?: number }) {
  const [show, setShow] = useState(false);
  useEffect(() => {
    const t = window.setTimeout(() => setShow(true), delayMs);
    return () => window.clearTimeout(t);
  }, [delayMs]);
  if (!show) return null;
  return (
    <div
      aria-hidden
      className={cx('animate-pulse rounded-md bg-surface-sunken motion-reduce:animate-none', className)}
    />
  );
}

/** 看板加载骨架：四列 × 卡片形状，匹配真实布局形态（审计 P1 #1/#2）。 */
export function KanbanSkeleton() {
  return (
    <div className="flex gap-4 h-full" role="status" aria-label="任务加载中">
      {[0, 1, 2, 3].map((col) => (
        <div key={col} className="flex-1 min-w-[240px] rounded-xl bg-surface-sunken/60 p-4 space-y-3">
          <Skeleton className="h-6 w-24" />
          <Skeleton className="h-24 w-full" />
          <Skeleton className="h-24 w-full" />
          <Skeleton className="h-16 w-3/4" />
        </div>
      ))}
    </div>
  );
}

/** 列表加载骨架：匹配列表行形态。 */
export function ListSkeleton() {
  return (
    <div className="space-y-2 p-comfortable" role="status" aria-label="任务加载中">
      {[0, 1, 2, 3, 4, 5].map((row) => (
        <Skeleton key={row} className="h-10 w-full" />
      ))}
    </div>
  );
}
