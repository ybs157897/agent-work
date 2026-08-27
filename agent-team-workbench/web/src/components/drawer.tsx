import { AnimatePresence, motion } from 'motion/react';
import { X } from 'lucide-react';
import React, { useEffect } from 'react';
import { createPortal } from 'react-dom';

/**
 * 右侧滑入面板（confirmed-ia 按需滑入）。两种形态：
 * - 带 title：表单容器--头行（h3 + 关闭）+ 可滚动 body，创建/编辑表单用它；
 * - 不带 title：自由内容（如任务详情），保留绝对定位关闭钮，内容自行避让（pr-8 惯例）。
 * 破坏性确认不走 drawer，走 Modal（DESIGN.md Don'ts）。
 */
export function Drawer({
  open,
  onClose,
  title,
  children,
  width = 280,
}: {
  open: boolean;
  onClose: () => void;
  title?: string;
  children: React.ReactNode;
  width?: number;
}) {
  useEffect(() => {
    if (!open) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose();
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [open, onClose]);

  return createPortal(
    <AnimatePresence>
      {open && (
        <>
          <motion.div
            initial={{ opacity: 0 }}
            animate={{ opacity: 1 }}
            exit={{ opacity: 0 }}
            onClick={onClose}
            className="fixed inset-0 z-40 bg-sidebar/30 backdrop-blur-sm"
          />
          <motion.div
            role="dialog"
            aria-modal="true"
            aria-label={title ?? '详情面板'}
            initial={{ x: width }}
            animate={{ x: 0 }}
            exit={{ x: width }}
            transition={{ type: 'spring', stiffness: 250, damping: 25 }}
            className="fixed bottom-0 right-0 top-0 z-50 flex flex-col rounded-l-xl border-l border-border-strong bg-surface-raised shadow-level-4"
            style={{ width }}
          >
            {title ? (
              <div className="flex shrink-0 items-center justify-between gap-4 border-b border-border-subtle px-comfortable py-base">
                <h3 className="text-h3 text-text-primary min-w-0 truncate">{title}</h3>
                <button
                  onClick={onClose}
                  className="inline-flex h-8 w-8 shrink-0 items-center justify-center rounded-button text-text-tertiary transition-colors hover:bg-surface-sunken hover:text-text-primary focus-visible:ring-2 focus-visible:ring-brand-primary/30"
                  aria-label="关闭"
                >
                  <X className="w-5 h-5" />
                </button>
              </div>
            ) : (
              <button
                onClick={onClose}
                className="absolute right-comfortable top-comfortable z-10 inline-flex h-8 w-8 items-center justify-center rounded-button text-text-tertiary transition-colors hover:bg-surface-sunken hover:text-text-primary"
                aria-label="关闭"
              >
                <X className="w-5 h-5" />
              </button>
            )}
            <div className="flex-1 min-h-0 overflow-y-auto">{children}</div>
          </motion.div>
        </>
      )}
    </AnimatePresence>,
    document.body,
  );
}
