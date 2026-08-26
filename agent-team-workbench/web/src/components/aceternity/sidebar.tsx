/**
 * Aceternity UI Sidebar, installed from the official registry and adapted to
 * the workbench's semantic tokens, desktop breakpoint, reduced motion, and
 * keyboard-accessible mobile controls.
 * Source: https://ui.aceternity.com/components/sidebar
 */
import { IconMenu2, IconX } from '@tabler/icons-react';
import { AnimatePresence, motion, useReducedMotion } from 'motion/react';
import React, { createContext, useContext, useState } from 'react';
import { cn } from '@/lib/utils';

interface Links {
  label: string;
  href: string;
  icon: React.ReactNode;
}

interface SidebarContextProps {
  open: boolean;
  setOpen: React.Dispatch<React.SetStateAction<boolean>>;
  animate: boolean;
}

const SidebarContext = createContext<SidebarContextProps | undefined>(undefined);

export function useSidebar(): SidebarContextProps {
  const context = useContext(SidebarContext);
  if (!context) throw new Error('useSidebar must be used within a SidebarProvider');
  return context;
}

export function SidebarProvider({
  children,
  open: openProp,
  setOpen: setOpenProp,
  animate = true,
}: {
  children: React.ReactNode;
  open?: boolean;
  setOpen?: React.Dispatch<React.SetStateAction<boolean>>;
  animate?: boolean;
}) {
  const [openState, setOpenState] = useState(false);
  const open = openProp ?? openState;
  const setOpen = setOpenProp ?? setOpenState;

  return <SidebarContext.Provider value={{ open, setOpen, animate }}>{children}</SidebarContext.Provider>;
}

export function Sidebar({
  children,
  open,
  setOpen,
  animate,
}: {
  children: React.ReactNode;
  open?: boolean;
  setOpen?: React.Dispatch<React.SetStateAction<boolean>>;
  animate?: boolean;
}) {
  return (
    <SidebarProvider open={open} setOpen={setOpen} animate={animate}>
      {children}
    </SidebarProvider>
  );
}

export function SidebarBody(props: React.ComponentProps<typeof motion.aside>) {
  return (
    <>
      <DesktopSidebar {...props} />
      <MobileSidebar {...(props as React.ComponentProps<'div'>)} />
    </>
  );
}

export function DesktopSidebar({
  className,
  children,
  ...props
}: React.ComponentProps<typeof motion.aside>) {
  const { open, setOpen, animate } = useSidebar();
  const reduceMotion = useReducedMotion();
  const shouldAnimate = animate && !reduceMotion;

  return (
    <motion.aside
      {...props}
      animate={{ width: shouldAnimate ? (open ? '228px' : '72px') : '228px' }}
      transition={shouldAnimate ? { type: 'spring', stiffness: 260, damping: 30 } : { duration: 0 }}
      onMouseEnter={() => setOpen(true)}
      onMouseLeave={() => setOpen(false)}
      className={cn('hidden h-full shrink-0 flex-col lg:flex', className)}
    >
      {children}
    </motion.aside>
  );
}

export function MobileSidebar({
  className,
  children,
  ...props
}: React.ComponentProps<'div'>) {
  const { open, setOpen } = useSidebar();
  const reduceMotion = useReducedMotion();

  return (
    <div
      {...props}
      className="flex h-14 w-full items-center justify-end border-b border-sidebar-border bg-sidebar px-base lg:hidden"
    >
      <button
        type="button"
        aria-label="打开主导航"
        aria-expanded={open}
        onClick={() => setOpen(true)}
        className="inline-flex h-8 w-8 items-center justify-center rounded-button text-text-on-sidebar transition-colors hover:bg-sidebar-hover hover:text-text-on-sidebar-active"
      >
        <IconMenu2 className="h-5 w-5" stroke={1.7} />
      </button>
      <AnimatePresence>
        {open ? (
          <motion.div
            initial={reduceMotion ? false : { x: '-100%', opacity: 0 }}
            animate={{ x: 0, opacity: 1 }}
            exit={reduceMotion ? { opacity: 0 } : { x: '-100%', opacity: 0 }}
            transition={{ duration: reduceMotion ? 0 : 0.22, ease: [0.16, 1, 0.3, 1] }}
            className={cn(
              'fixed inset-0 z-50 flex h-full w-full flex-col justify-between bg-sidebar p-comfortable',
              className,
            )}
          >
            <button
              type="button"
              aria-label="关闭主导航"
              onClick={() => setOpen(false)}
              className="absolute right-comfortable top-comfortable inline-flex h-8 w-8 items-center justify-center rounded-button text-text-on-sidebar transition-colors hover:bg-sidebar-hover hover:text-text-on-sidebar-active"
            >
              <IconX className="h-5 w-5" stroke={1.7} />
            </button>
            {children}
          </motion.div>
        ) : null}
      </AnimatePresence>
    </div>
  );
}

export function SidebarLink({
  link,
  className,
  ...props
}: {
  link: Links;
  className?: string;
} & Omit<React.ComponentProps<'a'>, 'href'>) {
  const { open, animate } = useSidebar();
  return (
    <a href={link.href} className={cn('group/sidebar flex items-center gap-snug py-tight', className)} {...props}>
      {link.icon}
      <motion.span
        animate={{ opacity: animate ? (open ? 1 : 0) : 1 }}
        className={cn('whitespace-nowrap text-body transition-transform group-hover/sidebar:translate-x-0.5', !open && animate && 'sr-only')}
      >
        {link.label}
      </motion.span>
    </a>
  );
}
