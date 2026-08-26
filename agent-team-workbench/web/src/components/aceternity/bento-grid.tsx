/** Aceternity UI Bento Grid, themed with project semantic tokens. */
import type React from 'react';
import { cn } from '@/lib/utils';

export function BentoGrid({ className, children }: { className?: string; children?: React.ReactNode }) {
  return (
    <div className={cn('mx-auto grid max-w-7xl grid-cols-1 gap-base md:auto-rows-[18rem] md:grid-cols-3', className)}>
      {children}
    </div>
  );
}

export function BentoGridItem({
  className,
  contentClassName,
  title,
  description,
  header,
  icon,
}: {
  className?: string;
  contentClassName?: string;
  title?: React.ReactNode;
  description?: React.ReactNode;
  header?: React.ReactNode;
  icon?: React.ReactNode;
}) {
  return (
    <div
      className={cn(
        'group/bento row-span-1 flex flex-col justify-between space-y-base rounded-card border border-border-subtle bg-surface-raised/90 p-base shadow-card transition duration-ink hover:-translate-y-0.5 hover:border-border-strong hover:shadow-level-2',
        className,
      )}
    >
      {header}
      <div className="relative z-[1] transition-transform duration-ink group-hover/bento:translate-x-0.5">
        {icon}
        {title != null && <div className="mb-tight mt-tight font-display text-h3 text-text-primary">{title}</div>}
        {description != null && <div className={cn('text-body text-text-secondary', contentClassName)}>{description}</div>}
      </div>
    </div>
  );
}
