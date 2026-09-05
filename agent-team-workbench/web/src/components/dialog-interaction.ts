import { useEffect, useRef, type KeyboardEvent as ReactKeyboardEvent } from 'react';

type DialogEntry = {
  id: symbol;
  panel: HTMLElement;
  layer: HTMLElement;
  onClose?: () => void;
};

type InertSnapshot = {
  hadInertAttribute: boolean;
  inertProperty: boolean;
  hadAriaHiddenAttribute: boolean;
  ariaHiddenValue: string | null;
};

const dialogStack: DialogEntry[] = [];
let dialogKeydownInstalled = false;
let bodyLockDepth = 0;
let bodyOverflowBeforeLock: string | null = null;
const inertSnapshots = new Map<HTMLElement, InertSnapshot>();

const FOCUSABLE_SELECTOR = [
  'a[href]',
  'area[href]',
  'button:not([disabled])',
  'input:not([disabled]):not([type="hidden"])',
  'select:not([disabled])',
  'textarea:not([disabled])',
  'audio[controls]',
  'video[controls]',
  '[contenteditable="true"]',
  '[tabindex]:not([tabindex="-1"])',
].join(',');

function focusableElements(panel: HTMLElement): HTMLElement[] {
  return Array.from(panel.querySelectorAll<HTMLElement>(FOCUSABLE_SELECTOR)).filter((element) =>
    !element.hidden
    && !element.matches(':disabled')
    && element.closest('[aria-hidden="true"]') === null
    && element.closest('[inert]') === null);
}

function dialogLayer(panel: HTMLElement): HTMLElement {
  if (typeof panel.closest === 'function') {
    return panel.closest<HTMLElement>('[data-dialog-layer]') ?? panel;
  }
  return panel;
}

function setInert(element: HTMLElement, value: boolean) {
  const inertElement = element as HTMLElement & { inert?: boolean };
  inertElement.inert = value;
  if (value) element.setAttribute('inert', '');
  else element.removeAttribute('inert');
}

function syncBackgroundInert() {
  if (typeof document === 'undefined' || !document.body) return;

  const top = dialogStack[dialogStack.length - 1];
  const bodyChildren = Array.from(document.body.children ?? []) as HTMLElement[];
  const nextInert = new Set(top ? bodyChildren.filter((child) => child !== top.layer) : []);

  for (const [element, snapshot] of inertSnapshots) {
    if (nextInert.has(element)) continue;
    setInert(element, snapshot.inertProperty);
    if (snapshot.hadInertAttribute) element.setAttribute('inert', '');
    else element.removeAttribute('inert');
    if (snapshot.hadAriaHiddenAttribute) element.setAttribute('aria-hidden', snapshot.ariaHiddenValue ?? '');
    else element.removeAttribute('aria-hidden');
    inertSnapshots.delete(element);
  }

  for (const element of nextInert) {
    if (!inertSnapshots.has(element)) {
      const inertElement = element as HTMLElement & { inert?: boolean };
      inertSnapshots.set(element, {
        hadInertAttribute: element.hasAttribute('inert'),
        inertProperty: inertElement.inert === true,
        hadAriaHiddenAttribute: element.hasAttribute('aria-hidden'),
        ariaHiddenValue: element.getAttribute('aria-hidden'),
      });
    }
    setInert(element, true);
    element.setAttribute('aria-hidden', 'true');
  }
}

function topDialog(): DialogEntry | undefined {
  const rendered = Array.from(document.querySelectorAll<HTMLElement>('[role="dialog"][aria-modal="true"]'));
  for (let index = rendered.length - 1; index >= 0; index -= 1) {
    const entry = dialogStack.find((candidate) => candidate.panel === rendered[index]);
    if (entry) return entry;
  }
  return dialogStack[dialogStack.length - 1];
}

function onDialogKeydown(event: KeyboardEvent) {
  const dialog = topDialog();
  if (!dialog) return;
  if (event.key === 'Escape' || event.key === 'Esc') {
    if (event.defaultPrevented) return;
    event.preventDefault();
    event.stopPropagation();
    dialog.onClose?.();
    return;
  }
  if (event.key !== 'Tab') return;
  const focusable = focusableElements(dialog.panel);
  if (focusable.length === 0) {
    event.preventDefault();
    dialog.panel.focus({ preventScroll: true });
    return;
  }

  const active = document.activeElement;
  const currentIndex = active instanceof HTMLElement ? focusable.indexOf(active) : -1;
  if (currentIndex < 0) {
    event.preventDefault();
    focusable[event.shiftKey ? focusable.length - 1 : 0].focus({ preventScroll: true });
    return;
  }

  const nextIndex = event.shiftKey
    ? (currentIndex - 1 + focusable.length) % focusable.length
    : (currentIndex + 1) % focusable.length;
  if ((!event.shiftKey && currentIndex === focusable.length - 1) || (event.shiftKey && currentIndex === 0)) {
    event.preventDefault();
    focusable[nextIndex].focus({ preventScroll: true });
  }
}

function setDialogKeydownListener() {
  if (dialogKeydownInstalled) return;
  window.addEventListener('keydown', onDialogKeydown, true);
  dialogKeydownInstalled = true;
}

function unsetDialogKeydownListener() {
  if (!dialogKeydownInstalled || dialogStack.length > 0) return;
  window.removeEventListener('keydown', onDialogKeydown, true);
  dialogKeydownInstalled = false;
}

function lockBodyScroll(): () => void {
  if (bodyLockDepth === 0) {
    bodyOverflowBeforeLock = document.body.style.overflow;
    document.body.style.overflow = 'hidden';
  }
  bodyLockDepth += 1;

  let released = false;
  return () => {
    if (released) return;
    released = true;
    bodyLockDepth = Math.max(0, bodyLockDepth - 1);
    if (bodyLockDepth === 0) {
      document.body.style.overflow = bodyOverflowBeforeLock ?? '';
      bodyOverflowBeforeLock = null;
    }
  };
}

export function registerDialog(panel: HTMLElement, onClose?: () => void): () => void {
  const entry: DialogEntry = { id: Symbol('dialog'), panel, layer: dialogLayer(panel), onClose };
  dialogStack.push(entry);
  setDialogKeydownListener();
  const unlockBody = lockBodyScroll();
  syncBackgroundInert();

  let registered = true;
  return () => {
    if (!registered) return;
    registered = false;
    const index = dialogStack.findIndex((candidate) => candidate.id === entry.id);
    if (index >= 0) dialogStack.splice(index, 1);
    unlockBody();
    syncBackgroundInert();
    unsetDialogKeydownListener();
  };
}

export function closeDialogOnEscape(event: ReactKeyboardEvent, onClose: () => void) {
  if (event.key !== 'Escape' && event.key !== 'Esc') return;
  if (event.defaultPrevented) return;
  const currentPanel = event.currentTarget;
  const currentTopDialog = topDialog();
  if (!currentTopDialog || currentTopDialog.panel !== currentPanel) return;
  event.preventDefault();
  event.stopPropagation();
  onClose();
}

export function useDialogInteraction(open: boolean, onClose?: () => void) {
  const panelRef = useRef<HTMLDivElement>(null);
  const closeButtonRef = useRef<HTMLButtonElement>(null);
  const restoreFocusRef = useRef<HTMLElement | null>(null);
  const capturedFocusRef = useRef(false);
  const onCloseRef = useRef<(() => void) | undefined>(undefined);

  useEffect(() => {
    onCloseRef.current = onClose;
  }, [onClose]);

  useEffect(() => {
    const restoreFocus = () => {
      if (restoreFocusRef.current?.isConnected) restoreFocusRef.current.focus({ preventScroll: true });
      restoreFocusRef.current = null;
      capturedFocusRef.current = false;
    };

    if (!open) {
      restoreFocus();
      return;
    }

    const panel = panelRef.current;
    if (!panel) return;
    if (!capturedFocusRef.current) {
      restoreFocusRef.current = document.activeElement instanceof HTMLElement ? document.activeElement : null;
      capturedFocusRef.current = true;
    }

    const unregister = registerDialog(panel, () => onCloseRef.current?.());
    (closeButtonRef.current ?? panel).focus({ preventScroll: true });
    return () => {
      unregister();
      restoreFocus();
    };
  }, [open]);

  return { panelRef, closeButtonRef };
}
