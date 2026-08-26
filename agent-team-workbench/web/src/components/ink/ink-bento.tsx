import type React from 'react';
import { BentoGrid, BentoGridItem } from '../aceternity/bento-grid';
import { cn } from '@/lib/utils';

export function InkBentoGrid({ className, children }: { className?: string; children: React.ReactNode }) {
  return <BentoGrid className={cn('relative z-[1] max-w-none', className)}>{children}</BentoGrid>;
}

export function InkBentoItem({ className, ...props }: React.ComponentProps<typeof BentoGridItem>) {
  return <BentoGridItem {...props} className={cn('ink-paper-panel relative isolate overflow-hidden', className)} />;
}
