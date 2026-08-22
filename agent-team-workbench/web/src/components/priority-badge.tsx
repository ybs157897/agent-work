import type { Priority } from '../api/types';

const CONFIG: Record<Priority, { dot: string; label: string }> = {
  low: { dot: 'bg-brand-primary', label: '低优' },
  medium: { dot: 'bg-status-warning', label: '中优' },
  high: { dot: 'bg-status-error', label: '高优' },
  urgent: { dot: 'bg-status-error ring-2 ring-status-error/30', label: '紧急' },
};

export function PriorityBadge({ priority }: { priority: Priority }) {
  const c = CONFIG[priority] ?? CONFIG.medium;
  return (
    <div className="flex items-center gap-1.5">
      <div className={`w-2 h-2 rounded-full ${c.dot}`} />
      <span className="text-xs text-text-secondary">{c.label}</span>
    </div>
  );
}
