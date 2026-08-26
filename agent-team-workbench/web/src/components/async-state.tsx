import { AlertCircle, Circle } from 'lucide-react';
import { Button } from './ui';

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
      <div className="flex h-10 w-10 items-center justify-center rounded-button bg-surface-sunken text-text-tertiary">
        <Circle className="h-4 w-4" aria-hidden="true" />
      </div>
      <p className="text-body text-text-tertiary">{label}</p>
    </div>
  );
}
