import type { LspLocation } from './session'
import { pathFromClientUri } from './session'

export type NavTarget = {
  path: string
  /** 0-based */
  line: number
  /** 0-based */
  character: number
  endLine: number
  endCharacter: number
}

/**
 * IDEA GotoDeclarationOrUsageHandler2 (GTDU) decision:
 * - On a declaration name → Show Usages (SU)
 * - On a reference → Go To Declaration (GTD)
 * - Ambiguous declarations → chooser
 *
 * @see platform/lang-impl/.../GotoDeclarationOrUsageHandler2.kt
 */
export type GtduDecision =
  | { kind: 'nowhere' }
  | { kind: 'goto'; targets: NavTarget[] }
  | { kind: 'showUsages' }

function locToTarget(workspaceId: string, loc: LspLocation): NavTarget | null {
  const path = pathFromClientUri(workspaceId, loc.uri)
  if (path == null || path === '') return null
  return {
    path,
    line: loc.range.start.line,
    character: loc.range.start.character,
    endLine: loc.range.end.line,
    endCharacter: loc.range.end.character,
  }
}

function containsPos(
  loc: LspLocation,
  path: string,
  line: number,
  character: number,
  workspaceId: string,
): boolean {
  const p = pathFromClientUri(workspaceId, loc.uri)
  if (p !== path) return false
  const s = loc.range.start
  const e = loc.range.end
  if (line < s.line || line > e.line) return false
  if (line === s.line && character < s.character) return false
  if (line === e.line && character > e.character) return false
  return true
}

/** True if caret is already on one of the resolved declaration ranges (→ Show Usages). */
export function isAtDeclaration(
  workspaceId: string,
  clickPath: string,
  clickLine: number,
  clickCharacter: number,
  definitions: LspLocation[],
): boolean {
  if (definitions.length === 0) return false
  return definitions.some((d) => containsPos(d, clickPath, clickLine, clickCharacter, workspaceId))
}

export function decideGtdu(
  workspaceId: string,
  clickPath: string,
  clickLine: number,
  clickCharacter: number,
  definitions: LspLocation[],
): GtduDecision {
  if (definitions.length === 0) {
    // No declaration resolved — still try Show Usages (e.g. local weirdness / partial index)
    return { kind: 'showUsages' }
  }
  if (isAtDeclaration(workspaceId, clickPath, clickLine, clickCharacter, definitions)) {
    return { kind: 'showUsages' }
  }
  const targets = definitions
    .map((d) => locToTarget(workspaceId, d))
    .filter((t): t is NavTarget => t != null)
  if (targets.length === 0) return { kind: 'nowhere' }
  return { kind: 'goto', targets }
}

export function navFromUsageHit(hit: {
  path: string | null
  line: number
  column: number
  highlightStart: number
  highlightEnd: number
}): NavTarget | null {
  if (!hit.path) return null
  const line0 = hit.line - 1
  const start = Math.max(0, hit.highlightStart)
  const end = Math.max(start, hit.highlightEnd)
  return {
    path: hit.path,
    line: line0,
    character: start,
    endLine: line0,
    endCharacter: end || start + 1,
  }
}
