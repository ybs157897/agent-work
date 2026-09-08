import type { RevealTarget } from '../components/CodePane'

/** One caret/file place in IDEA-style Navigate Back/Forward history. */
export type NavPlace = {
  path: string
  reveal: RevealTarget | null
}

function sameReveal(a: RevealTarget | null, b: RevealTarget | null): boolean {
  if (a == null && b == null) return true
  if (a == null || b == null) return false
  return (
    a.line === b.line &&
    a.column === b.column &&
    (a.endLine ?? a.line) === (b.endLine ?? b.line) &&
    (a.endColumn ?? a.column) === (b.endColumn ?? b.column)
  )
}

export function samePlace(a: NavPlace, b: NavPlace): boolean {
  return a.path === b.path && sameReveal(a.reveal, b.reveal)
}

/**
 * Two-stack history like IntelliJ Navigate Back / Forward
 * (Ctrl+Alt+Left/Right on Win/Linux, ⌘[/] on macOS).
 */
export class NavHistory {
  private static readonly maxEntries = 100
  private back: NavPlace[] = []
  private forward: NavPlace[] = []

  private trim(entries: NavPlace[]): NavPlace[] {
    return entries.length > NavHistory.maxEntries
      ? entries.slice(entries.length - NavHistory.maxEntries)
      : entries
  }

  clear(): void {
    this.back = []
    this.forward = []
  }

  canBack(): boolean {
    return this.back.length > 0
  }

  canForward(): boolean {
    return this.forward.length > 0
  }

  /** Call before leaving `from` toward a new place. */
  recordLeave(from: NavPlace | null): void {
    if (!from) return
    // Any new navigation invalidates the forward branch, including a leave
    // that is a duplicate of the latest back place.
    this.forward = []
    const last = this.back[this.back.length - 1]
    if (last && samePlace(last, from)) return
    this.back = this.trim([...this.back, from])
  }

  goBack(current: NavPlace | null): NavPlace | null {
    const prev = this.back.pop()
    if (!prev) return null
    if (current) this.forward = this.trim([...this.forward, current])
    return prev
  }

  goForward(current: NavPlace | null): NavPlace | null {
    const next = this.forward.pop()
    if (!next) return null
    if (current) this.back = this.trim([...this.back, current])
    return next
  }
}

export function placeFromClick(path: string, line0: number, character0: number): NavPlace {
  const line = line0 + 1
  const column = character0 + 1
  return {
    path,
    reveal: {
      line,
      column,
      endLine: line,
      endColumn: column,
    },
  }
}
