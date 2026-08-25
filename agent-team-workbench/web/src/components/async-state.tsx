import { AlertCircle, Loader2 } from 'lucide-react';
import { Button } from './ui';

export function Loading({ label = '加载中…' }: { label?: string }) {
  return (
    <div className="flex flex-col items-center justify-center gap-3 py-20">
      <div className="w-10 h-10 rounded-full border-2 border-brand-primary/20 border-t-brand-primary flex items-center justify-center">
        <Loader2 className="w-5 h-5 animate-spin text-brand-primary" />
      </div>
      <span className="text-body text-text-secondary">{label}</span>
    </div>
  );
}

export function ErrorState({ message, onRetry }: { message: string; onRetry?: () => void }) {
  return (
    <div className="flex flex-col items-center justify-center gap-4 py-20 px-6 text-center max-w-md mx-auto">
      <div className="w-12 h-12 rounded-full bg-status-error/10 flex items-center justify-center">
        <AlertCircle className="w-6 h-6 text-status-error" />
      </div>
      <p className="text-body-lg text-text-primary font-medium">{message}</p>
      {onRetry && (
        <Button variant="primary" onClick={onRetry}>
          重试
        </Button>
      )}
    </div>
  );
}

export function EmptyState({ label }: { label: string }) {
  return (
    <div className="flex flex-col items-center justify-center gap-2 py-10 text-center">
      <div className="w-10 h-10 rounded-full bg-surface-sunken flex items-center justify-center text-text-tertiary text-lg">
        —
      </div>
      <p className="text-body text-text-tertiary">{label}</p>
    </div>
  );
}
