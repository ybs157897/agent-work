import type { GatewayClient } from '../api/client'
import type { LspLocation } from '../lsp/session'
import { pathFromClientUri } from '../lsp/session'

/** One row in IDEA-style Show Usages table. */
export type UsageHit = {
  uri: string
  path: string | null
  fileName: string
  directory: string
  /** 1-based */
  line: number
  /** 1-based */
  column: number
  endColumn: number
  preview: string
  /** Inclusive start / exclusive end within preview */
  highlightStart: number
  highlightEnd: number
  /** The usage that was clicked (origin) */
  isOrigin: boolean
}

export type OriginRef = {
  path: string
  /** 0-based */
  line: number
  /** 0-based */
  character: number
}

export async function buildUsageHits(
  client: GatewayClient,
  workspaceId: string,
  locations: LspLocation[],
  origin: OriginRef,
): Promise<UsageHit[]> {
  const cache = new Map<string, string>()

  async function read(path: string): Promise<string | null> {
    if (cache.has(path)) return cache.get(path)!
    try {
      const text = await client.readFile(workspaceId, path)
      cache.set(path, text)
      return text
    } catch {
      return null
    }
  }

  const hits: UsageHit[] = []
  for (const loc of locations) {
    const path = pathFromClientUri(workspaceId, loc.uri)
    const line0 = loc.range.start.line
    const col0 = loc.range.start.character
    const endCol0 =
      loc.range.end.line === line0 ? loc.range.end.character : loc.range.end.character

    let preview = ''
    let highlightStart = 0
    let highlightEnd = 0
    if (path) {
      const text = await read(path)
      if (text != null) {
        const lines = text.split(/\r?\n/)
        const raw = lines[line0] ?? ''
        preview = raw.replace(/\t/g, '    ')
        highlightStart = raw.slice(0, col0).replace(/\t/g, '    ').length
        highlightEnd = raw.slice(0, endCol0).replace(/\t/g, '    ').length
        if (highlightEnd === highlightStart && highlightStart < preview.length) {
          // Fallback: extend to identifier end
          let i = highlightStart
          while (i < preview.length && /[A-Za-z0-9_$]/.test(preview[i]!)) i++
          highlightEnd = i
        }
      }
    }

    const fileName = path ? (path.includes('/') ? path.slice(path.lastIndexOf('/') + 1) : path) : loc.uri
    const directory = path && path.includes('/') ? path.slice(0, path.lastIndexOf('/')) : ''
    const isOrigin =
      path != null &&
      path === origin.path &&
      line0 === origin.line &&
      col0 <= origin.character &&
      origin.character <= Math.max(endCol0, col0 + 1)

    hits.push({
      uri: loc.uri,
      path,
      fileName,
      directory,
      line: line0 + 1,
      column: col0 + 1,
      endColumn: Math.max(col0 + 1, endCol0 + 1),
      preview: preview.trimEnd(),
      highlightStart,
      highlightEnd,
      isOrigin,
    })
  }

  // IDEA-ish order: by path then line
  hits.sort((a, b) => {
    const pa = a.path ?? a.uri
    const pb = b.path ?? b.uri
    if (pa !== pb) return pa.localeCompare(pb)
    return a.line - b.line || a.column - b.column
  })
  return hits
}
