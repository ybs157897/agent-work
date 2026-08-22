import { AlertCircle, Loader2 } from 'lucide-react';

export function Loading({ label = '加载中…' }: { label?: string }) {
  return (
    <div className="flex items-center justify-center gap-2 py-16 text-text-tertiary">
      <Loader2 className="w-5 h-5 animate-spin" />
      <span className="text-body">{label}</span>
    </div>
  );
}

export function ErrorState({ message, onRetry }: { message: string; onRetry?: () => void }) {
  return (
    <div className="flex flex-col items-center justify-center gap-3 py-16 text-text-secondary">
      <AlertCircle className="w-6 h-6 text-status-error" />
      <p className="text-body">{message}</p>
      {onRetry && (
        <button
          onClick={onRetry}
          className="bg-transparent border border-brand-primary text-brand-primary rounded-button px-base py-tight font-medium transition-colors hover:bg-brand-primary/5"
        >
          重试
        </button>
      )}
    </div>
  );
}

export function EmptyState({ label }: { label: string }) {
  return <div className="text-caption text-text-tertiary py-4 text-center">{label}</div>;
}
