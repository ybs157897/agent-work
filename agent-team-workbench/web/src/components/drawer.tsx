import { AnimatePresence, motion } from 'framer-motion';
import { X } from 'lucide-react';
import React from 'react';

/** 右侧滑入详情面板（confirmed-ia：280px，按需滑入）。 */
export function Drawer({
  open,
  onClose,
  children,
  width = 280,
}: {
  open: boolean;
  onClose: () => void;
  children: React.ReactNode;
  width?: number;
}) {
  return (
    <AnimatePresence>
      {open && (
        <>
          <motion.div
            initial={{ opacity: 0 }}
            animate={{ opacity: 1 }}
            exit={{ opacity: 0 }}
            onClick={onClose}
            className="fixed inset-0 bg-black/20 backdrop-blur-sm z-40"
          />
          <motion.div
            initial={{ x: width }}
            animate={{ x: 0 }}
            exit={{ x: width }}
            transition={{ type: 'spring', stiffness: 250, damping: 25 }}
            className="fixed top-0 right-0 bottom-0 bg-surface-raised shadow-level-4 rounded-l-2xl z-50 flex flex-col border-l border-border-subtle"
            style={{ width }}
          >
            <button
              onClick={onClose}
              className="absolute top-comfortable right-comfortable p-1 text-text-tertiary hover:text-text-primary rounded transition-colors z-10"
            >
              <X className="w-5 h-5" />
            </button>
            {children}
          </motion.div>
        </>
      )}
    </AnimatePresence>
  );
}
