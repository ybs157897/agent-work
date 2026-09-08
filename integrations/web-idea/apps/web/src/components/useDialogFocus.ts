import { useEffect, type RefObject } from 'react'

export function useDialogFocus(
  dialog: RefObject<HTMLElement | null>,
  initial: RefObject<HTMLElement | null>,
  onClose: () => void,
) {
  useEffect(() => {
    const previous = document.activeElement as HTMLElement | null
    initial.current?.focus()
    const onKey = (event: KeyboardEvent) => {
      if (event.key === 'Escape') { event.preventDefault(); event.stopPropagation(); onClose(); return }
      if (event.key !== 'Tab') return
      const items = Array.from(dialog.current?.querySelectorAll<HTMLElement>('button:not(:disabled), input, [tabindex="0"]') ?? [])
      const first = items[0]
      const last = items.at(-1)
      if (event.shiftKey && document.activeElement === first) { event.preventDefault(); last?.focus() }
      else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first?.focus() }
    }
    const element = dialog.current
    element?.addEventListener('keydown', onKey)
    return () => { element?.removeEventListener('keydown', onKey); if (previous?.isConnected) previous.focus() }
  }, [dialog, initial, onClose])
}
