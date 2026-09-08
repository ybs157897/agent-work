import { AnimatePresence, motion, useReducedMotion } from 'motion/react';
import { X } from 'lucide-react';
import React, { useEffect, useId, useState } from 'react';
import { createPortal } from 'react-dom';
import { closeDialogOnEscape, useDialogInteraction } from './dialog-interaction';

export function Modal({
  open,
  onClose,
  title,
  children,
  footer,
  skin = 'default',
  width = 440,
}: {
  open: boolean;
  onClose: () => void;
  title: string;
  children: React.ReactNode;
  footer?: React.ReactNode;
  skin?: 'default' | 'task';
  width?: number;
}) {
  const titleId = useId();
  const reducedMotion = useReducedMotion() === true;
  const [dialogOpen, setDialogOpen] = useState(open);
  useEffect(() => {
    if (open) setDialogOpen(true);
  }, [open]);

  const handleExitComplete = () => {
    if (open) return;
    setDialogOpen(false);
  };

  const { panelRef, closeButtonRef } = useDialogInteraction(dialogOpen, onClose);
  const taskSkin = skin === 'task' ? 'plane-board min-h-0 bg-surface-raised' : '';
  return createPortal(
    open || dialogOpen ? (
      <div data-dialog-layer="modal">
        <AnimatePresence onExitComplete={handleExitComplete}>
          {open && (
            <>
              <motion.div
                initial={{ opacity: 0 }}
                animate={{ opacity: 1 }}
                exit={{ opacity: 0 }}
                transition={{ duration: reducedMotion ? 0 : 0.15, ease: 'easeOut' }}
                onClick={onClose}
                aria-hidden="true"
                className="fixed inset-0 z-[60] bg-sidebar/40 backdrop-blur-sm"
              />
              <div className="fixed inset-0 z-[70] flex items-center justify-center p-base pointer-events-none">
                <motion.div
                  ref={panelRef}
                  role="dialog"
                  aria-modal="true"
                  aria-labelledby={titleId}
                  tabIndex={-1}
                  onKeyDown={(event) => closeDialogOnEscape(event, onClose)}
                  initial={reducedMotion ? false : { opacity: 0, y: 12, scale: 0.98 }}
                  animate={{ opacity: 1, y: 0, scale: 1 }}
                  exit={reducedMotion ? { opacity: 1, y: 0, scale: 1 } : { opacity: 0, y: 12, scale: 0.98 }}
                  transition={{ duration: reducedMotion ? 0 : 0.15, ease: 'easeOut' }}
                  className={`workbench-panel pointer-events-auto flex max-h-[calc(100dvh-32px)] w-full min-h-0 flex-col overflow-hidden rounded-card p-comfortable shadow-level-4 ${taskSkin}`}
                  style={{ maxWidth: width }}
                >
                  <div className="mb-comfortable flex shrink-0 items-center justify-between gap-4">
                    <h3 id={titleId} className="text-h3 text-text-primary">{title}</h3>
                    <button
                      ref={closeButtonRef}
                      type="button"
                      onClick={onClose}
                      className="inline-flex h-8 w-8 items-center justify-center rounded-button text-text-tertiary transition-all duration-150 hover:bg-surface-sunken hover:text-text-primary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-brand-primary/40 focus-visible:ring-offset-2 motion-reduce:transition-none"
                      aria-label="关闭"
                    >
                      <X className="w-5 h-5" aria-hidden />
                    </button>
                  </div>
                  <div className="min-h-0 flex-1 overflow-y-auto">{children}</div>
                  {footer ? <div className="mt-snug shrink-0 border-t border-border-subtle pt-snug">{footer}</div> : null}
                </motion.div>
              </div>
            </>
          )}
        </AnimatePresence>
      </div>
    ) : null,
    document.body,
  );
}
