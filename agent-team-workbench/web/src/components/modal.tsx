import { AnimatePresence, motion } from 'motion/react';
import { X } from 'lucide-react';
import React from 'react';
import { createPortal } from 'react-dom';

export function Modal({
  open,
  onClose,
  title,
  children,
  width = 440,
}: {
  open: boolean;
  onClose: () => void;
  title: string;
  children: React.ReactNode;
  width?: number;
}) {
  return createPortal(
    <AnimatePresence>
      {open && (
        <>
          <motion.div
            initial={{ opacity: 0 }}
            animate={{ opacity: 1 }}
            exit={{ opacity: 0 }}
            onClick={onClose}
            className="fixed inset-0 z-40 bg-sidebar/40 backdrop-blur-sm"
          />
          <div className="fixed inset-0 z-50 flex items-center justify-center p-base pointer-events-none">
            <motion.div
              initial={{ opacity: 0, y: 12, scale: 0.98 }}
              animate={{ opacity: 1, y: 0, scale: 1 }}
              exit={{ opacity: 0, y: 12, scale: 0.98 }}
              transition={{ duration: 0.15, ease: 'easeOut' }}
              className="ink-paper-panel pointer-events-auto max-h-[calc(100dvh-32px)] w-full overflow-hidden rounded-card p-comfortable shadow-level-4"
              style={{ maxWidth: width }}
            >
              <div className="flex items-center justify-between mb-comfortable">
                <h3 className="text-h3 text-text-primary">{title}</h3>
                <button
                  onClick={onClose}
                  className="inline-flex h-8 w-8 items-center justify-center rounded-button text-text-tertiary transition-colors hover:bg-surface-sunken hover:text-text-primary focus-visible:ring-2 focus-visible:ring-brand-primary/30"
                  aria-label="关闭"
                >
                  <X className="w-5 h-5" />
                </button>
              </div>
              {children}
            </motion.div>
          </div>
        </>
      )}
    </AnimatePresence>,
    document.body,
  );
}
