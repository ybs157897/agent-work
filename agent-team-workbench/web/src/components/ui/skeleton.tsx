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

/** 列表加载骨架：匹配列表行形态。padded=false 用于已带内边距的卡片容器。 */
export function ListSkeleton({ padded = true }: { padded?: boolean }) {
  return (
    <div className={cx('space-y-2', padded && 'p-comfortable')} role="status" aria-label="列表加载中">
      {[0, 1, 2, 3, 4, 5].map((row) => (
        <Skeleton key={row} className="h-10 w-full" />
      ))}
    </div>
  );
}

/** 总览加载骨架：hero 卡 + 三统计卡 + 双栏内容卡 + 日志卡，匹配真实布局形态。 */
export function DashboardSkeleton() {
  return (
    <div className="page-shell" role="status" aria-label="总览加载中">
      <Skeleton className="h-28 w-full rounded-card" />
      <div className="grid grid-cols-1 md:grid-cols-3 gap-comfortable">
        {[0, 1, 2].map((i) => (
          <Skeleton key={i} className="h-24 rounded-card" />
        ))}
      </div>
      <div className="grid grid-cols-1 xl:grid-cols-3 gap-comfortable items-start">
        <Skeleton className="h-64 rounded-card xl:col-span-2" />
        <Skeleton className="h-64 rounded-card" />
      </div>
      <Skeleton className="h-40 rounded-card" />
    </div>
  );
}

/** 应用启动骨架：侧栏 + 顶栏 + 内容区轮廓，匹配 shell 布局形态。 */
export function AppShellSkeleton({ chat = false }: { chat?: boolean }) {
  const sidebarBlock = 'animate-pulse rounded-md bg-sidebar-hover motion-reduce:animate-none';
  return (
    <div
      className={`flex h-dvh w-full overflow-hidden ${chat ? 'bg-sidebar' : 'bg-surface-base'}`}
      role="status"
      aria-label="正在连接控制平面"
    >
      <div className="h-full w-[72px] shrink-0 space-y-3 border-r border-sidebar-border bg-sidebar p-4">
        <div className={`${sidebarBlock} h-8 w-8`} />
        {[0, 1, 2, 3, 4, 5, 6].map((i) => (
          <div key={i} className={`${sidebarBlock} h-9 w-full`} />
        ))}
      </div>
      <div className="flex min-w-0 flex-1 flex-col">
        {chat ? null : (
          <div className="flex h-14 shrink-0 items-center border-b border-border-subtle bg-surface-raised/80 px-6">
            <Skeleton delayMs={0} className="h-4 w-40" />
          </div>
        )}
        <div className={chat ? 'tx-scope min-h-0 flex-1' : 'flex-1 space-y-comfortable p-comfortable'}>
          {chat ? null : (
            <>
              <Skeleton delayMs={0} className="h-28 w-full rounded-card" />
              <div className="grid grid-cols-1 gap-comfortable md:grid-cols-3">
                {[0, 1, 2].map((i) => (
                  <Skeleton key={i} delayMs={0} className="h-24 rounded-card" />
                ))}
              </div>
            </>
          )}
        </div>
      </div>
    </div>
  );
}
