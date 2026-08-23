import { AnimatePresence, motion } from 'framer-motion';
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
              className="fixed inset-0 bg-sidebar/40 backdrop-blur-sm z-40"
          />
          <div className="fixed inset-0 z-50 flex items-center justify-center p-base pointer-events-none">
            <motion.div
              initial={{ opacity: 0, y: 12, scale: 0.98 }}
              animate={{ opacity: 1, y: 0, scale: 1 }}
              exit={{ opacity: 0, y: 12, scale: 0.98 }}
              transition={{ duration: 0.15, ease: 'easeOut' }}
              className="w-full max-h-[calc(100vh-32px)] overflow-hidden bg-surface-raised rounded-card shadow-level-4 border border-border-subtle p-comfortable pointer-events-auto"
              style={{ maxWidth: width }}
            >
              <div className="flex items-center justify-between mb-comfortable">
                <h3 className="text-h3 text-text-primary">{title}</h3>
                <button
                  onClick={onClose}
                  className="p-1.5 text-text-tertiary hover:text-text-primary hover:bg-surface-base rounded-lg transition-colors focus-visible:ring-2 focus-visible:ring-brand-primary/30"
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
