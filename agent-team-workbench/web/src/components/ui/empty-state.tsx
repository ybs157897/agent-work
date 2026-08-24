import type { ReactNode } from 'react';
import { cx } from './cx';

export type EmptyStateProps = {
  /** 图标节点（lucide-react 等）；空态只用图标 + 文案，不引入插画。 */
  icon?: ReactNode;
  title: string;
  description?: string;
  /** 动作入口，通常放 <Button>。 */
  action?: ReactNode;
  className?: string;
};

export function EmptyState({ icon, title, description, action, className }: EmptyStateProps) {
  return (
    <div
      className={cx(
        'flex flex-col items-center justify-center gap-tight px-comfortable py-loose text-center',
        className,
      )}
    >
      {icon ? (
        <div className="flex h-10 w-10 items-center justify-center rounded-full bg-surface-sunken text-text-tertiary">
          {icon}
        </div>
      ) : null}
      <p className="text-body font-medium text-text-primary">{title}</p>
      {description ? <p className="text-caption text-text-secondary">{description}</p> : null}
      {action}
    </div>
  );
}
