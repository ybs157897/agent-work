import { AlertCircle, CheckCircle2, Info, X } from 'lucide-react';
import { useEffect } from 'react';
import { useToastStore, type Toast } from '../stores/toast.store';

export function Toaster() {
  const toasts = useToastStore((s) => s.toasts);
  return (
    <div className="fixed bottom-6 right-6 z-[100] flex flex-col gap-2 w-80">
      {toasts.map((t) => (
        <ToastCard key={t.id} toast={t} />
      ))}
    </div>
  );
}

function ToastCard({ toast }: { toast: Toast }) {
  const dismiss = useToastStore((s) => s.dismiss);
  useEffect(() => {
    const timer = setTimeout(() => dismiss(toast.id), toast.kind === 'error' ? 6000 : 3500);
    return () => clearTimeout(timer);
  }, [toast.id, toast.kind, dismiss]);

  const icon =
    toast.kind === 'error' ? (
      <AlertCircle className="w-4 h-4 text-status-error shrink-0" />
    ) : toast.kind === 'success' ? (
      <CheckCircle2 className="w-4 h-4 text-status-success shrink-0" />
    ) : (
      <Info className="w-4 h-4 text-brand-primary shrink-0" />
    );

  return (
    <div className="bg-surface-raised rounded-card shadow-level-3 border border-border-subtle p-3 flex items-start gap-2">
      {icon}
      <p className="text-body text-text-primary flex-1 break-words">{toast.message}</p>
      <button
        onClick={() => dismiss(toast.id)}
        className="text-text-tertiary hover:text-text-primary shrink-0"
      >
        <X className="w-4 h-4" />
      </button>
    </div>
  );
}
