import { AnimatePresence, motion, useReducedMotion } from 'motion/react';
import { X } from 'lucide-react';
import { useEffect, useState, type ReactNode, type RefObject } from 'react';
import { createPortal } from 'react-dom';
import { closeDialogOnEscape, useDialogInteraction } from './dialog-interaction';
import { inkMotion } from '../design/motion';

/**
 * 右侧滑入面板。带 title 时提供标准头行；自由内容形态保留独立关闭钮，
 * 由内容自行避让。破坏性确认继续使用 Modal。
 */
export function Drawer({
  open,
  onClose,
  onExitComplete,
  title,
  ariaLabel,
  skin = 'default',
  children,
  width = 280,
}: {
  open: boolean;
  onClose: () => void;
  onExitComplete?: () => void;
  title?: string;
  ariaLabel?: string;
  skin?: 'default' | 'task';
  children: ReactNode;
  width?: number;
}) {
  const reducedMotion = useReducedMotion() === true;
  const [dialogOpen, setDialogOpen] = useState(open);
  useEffect(() => {
    if (open) setDialogOpen(true);
  }, [open]);

  const handleExitComplete = () => {
    if (open) return;
    setDialogOpen(false);
    onExitComplete?.();
  };

  const { panelRef, closeButtonRef } = useDialogInteraction(dialogOpen, onClose);
  const exitTransition = reducedMotion ? { duration: 0 } : { duration: inkMotion.duration.fast, ease: inkMotion.easeOut };
  const taskSkin = skin === 'task' ? 'plane-board min-h-0 bg-surface-raised' : '';

  return createPortal(
    open || dialogOpen ? (
      <div data-dialog-layer="drawer">
        <AnimatePresence onExitComplete={handleExitComplete}>
          {open && (
            <>
              <motion.div
                initial={{ opacity: 0 }}
                animate={{ opacity: 1 }}
                exit={{ opacity: 0 }}
                transition={{ duration: reducedMotion ? 0 : inkMotion.duration.fast, ease: inkMotion.easeOut }}
                onClick={onClose}
                aria-hidden="true"
                className="fixed inset-0 z-40 bg-sidebar/30 backdrop-blur-sm"
              />
              <motion.div
                ref={panelRef}
                role="dialog"
                aria-modal="true"
                aria-label={ariaLabel ?? title ?? '详情面板'}
                tabIndex={-1}
                onKeyDown={(event) => closeDialogOnEscape(event, onClose)}
                initial={reducedMotion ? false : { x: width }}
                animate={{ x: 0 }}
                exit={reducedMotion ? { x: 0 } : { x: width, transition: exitTransition }}
                transition={reducedMotion ? { duration: 0 } : { type: 'spring', stiffness: 250, damping: 25 }}
                className={`fixed bottom-0 right-0 top-0 z-50 flex max-w-[100vw] flex-col rounded-l-xl border-l border-border-strong bg-surface-raised shadow-level-4 ${taskSkin}`}
                style={{ width, maxWidth: '100vw' }}
              >
                {title ? (
                  <div className="flex shrink-0 items-center justify-between gap-4 border-b border-border-subtle px-comfortable py-base">
                    <h3 className="min-w-0 truncate text-h3 text-text-primary">{title}</h3>
                    <CloseButton buttonRef={closeButtonRef} onClose={onClose} />
                  </div>
                ) : (
                  <CloseButton buttonRef={closeButtonRef} onClose={onClose} floating />
                )}
                <div className="min-h-0 flex-1 overflow-y-auto">{children}</div>
              </motion.div>
            </>
          )}
        </AnimatePresence>
      </div>
    ) : null,
    document.body,
  );
}

function CloseButton({
  buttonRef,
  onClose,
  floating = false,
}: {
  buttonRef: RefObject<HTMLButtonElement>;
  onClose: () => void;
  floating?: boolean;
}) {
  return (
    <button
      ref={buttonRef}
      type="button"
      onClick={onClose}
      className={`${floating ? 'absolute right-comfortable top-comfortable z-10 ' : ''}inline-flex h-8 w-8 shrink-0 items-center justify-center rounded-button text-text-tertiary transition-all duration-150 hover:bg-surface-sunken hover:text-text-primary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-brand-primary/40 focus-visible:ring-offset-2 motion-reduce:transition-none`}
      aria-label="关闭"
    >
      <X className="h-5 w-5" aria-hidden />
    </button>
  );
}
