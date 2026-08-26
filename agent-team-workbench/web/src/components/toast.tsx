import { AnimatePresence, motion, useReducedMotion } from 'motion/react';
import { AlertCircle, Info, X } from 'lucide-react';
import { useEffect } from 'react';
import { useToastStore, type Toast } from '../stores/toast.store';

export function Toaster() {
  const toasts = useToastStore((s) => s.toasts);
  const reducedMotion = useReducedMotion();
  return (
    <div className="fixed bottom-6 right-6 z-[100] flex flex-col gap-2 w-80" role="region" aria-label="通知">
      <AnimatePresence>
        {toasts.map((t) => (
          <motion.div
            key={t.id}
            layout
            initial={{ opacity: 0, y: 8 }}
            animate={{ opacity: 1, y: 0 }}
            exit={{ opacity: 0 }}
            transition={{ duration: reducedMotion ? 0 : 0.15, ease: 'easeOut' }}
          >
            <ToastCard toast={t} />
          </motion.div>
        ))}
      </AnimatePresence>
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
      <span className="flex h-5 w-5 shrink-0 items-center justify-center rounded-sm border border-status-success/45 font-display text-caption text-status-success">
        成
      </span>
    ) : (
      <Info className="w-4 h-4 text-brand-primary shrink-0" />
    );

  return (
    <div
      className="ink-paper-panel flex items-start gap-snug rounded-card p-snug shadow-level-3"
      role={toast.kind === 'error' ? 'alert' : 'status'}
    >
      {icon}
      <p className="text-body text-text-primary flex-1 break-words">{toast.message}</p>
      <button
        type="button"
        aria-label="关闭通知"
        onClick={() => dismiss(toast.id)}
        className="inline-flex h-7 w-7 shrink-0 items-center justify-center rounded-button text-text-tertiary transition-colors hover:bg-surface-sunken hover:text-text-primary"
      >
        <X className="w-4 h-4" />
      </button>
    </div>
  );
}
