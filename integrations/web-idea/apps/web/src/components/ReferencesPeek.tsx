import { useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import type { UsageHit } from '../lsp/usageHits'

export type PeekAnchor = { x: number; y: number }

export type PeekMode = 'usages' | 'declarations'

type Props = {
  mode?: PeekMode
  symbol: string
  hits: UsageHit[]
  loading: boolean
  loadingMessage?: string | null
  error: string | null
  anchor: PeekAnchor
  onClose: () => void
  onOpen: (path: string, line: number, column: number, endColumn: number) => void
}

const GAP = 6

type FileGroup = {
  path: string | null
  fileName: string
  directory: string
  hits: UsageHit[]
}

function groupByFile(hits: UsageHit[]): FileGroup[] {
  const map = new Map<string, FileGroup>()
  const order: string[] = []
  for (const hit of hits) {
    const key = hit.path ?? hit.uri
    let g = map.get(key)
    if (!g) {
      g = {
        path: hit.path,
        fileName: hit.fileName,
        directory: hit.directory,
        hits: [],
      }
      map.set(key, g)
      order.push(key)
    }
    g.hits.push(hit)
  }
  return order.map((k) => map.get(k)!)
}

function JavaFileIcon() {
  return (
    <svg width={14} height={14} viewBox="0 0 16 16" aria-hidden className="ij-usages-java-icon">
      <path fill="#3574f0" d="M2.5 1.5h8.2L14 4.8V14.5H2.5z" opacity="0.15" />
      <path fill="none" stroke="#6c707e" strokeWidth="1" d="M2.5 1.5h8.2L14 4.8V14.5H2.5z" />
      <path fill="#6c707e" d="M10.7 1.5v3.3H14" opacity="0.55" />
      <text x="4.2" y="12" fill="#e76a00" fontSize="7.5" fontFamily="Inter,sans-serif" fontWeight="700">
        J
      </text>
    </svg>
  )
}

function PreviewText({ text, start, end }: { text: string; start: number; end: number }) {
  if (!text) return <span className="ij-usages-preview muted">—</span>
  const a = Math.max(0, Math.min(start, text.length))
  const b = Math.max(a, Math.min(end, text.length))
  return (
    <span className="ij-usages-preview">
      {text.slice(0, a)}
      <span className="ij-usages-hl">{text.slice(a, b) || text.slice(a, a + 1)}</span>
      {text.slice(b)}
    </span>
  )
}

export function ReferencesPeek({
  mode = 'usages',
  symbol,
  hits,
  loading,
  loadingMessage,
  error,
  anchor,
  onClose,
  onOpen,
}: Props) {
  const panelRef = useRef<HTMLDivElement>(null)
  const [pos, setPos] = useState({ left: anchor.x, top: anchor.y + GAP })
  const [active, setActiveState] = useState(0)
  const activeRef = useRef(0)
  function setActive(next: number | ((value: number) => number)) {
    const index = typeof next === 'function' ? next(activeRef.current) : next
    activeRef.current = index
    setActiveState(index)
  }
  const groups = useMemo(() => groupByFile(hits), [hits])
  const flat = useMemo(() => groups.flatMap((g) => g.hits), [groups])

  useEffect(() => {
    const previous = document.activeElement as HTMLElement | null
    panelRef.current?.focus()
    return () => { if (previous?.isConnected) previous.focus() }
  }, [])

  useEffect(() => {
    const originIdx = flat.findIndex((h) => h.isOrigin)
    setActive(originIdx >= 0 ? originIdx : 0)
  }, [flat])

  useLayoutEffect(() => {
    const el = panelRef.current
    if (!el) return
    const w = el.offsetWidth
    const h = el.offsetHeight
    const pad = 8
    let left = anchor.x
    let top = anchor.y + GAP
    if (left + w > window.innerWidth - pad) left = Math.max(pad, window.innerWidth - w - pad)
    if (left < pad) left = pad
    if (top + h > window.innerHeight - pad) top = Math.max(pad, anchor.y - h - GAP)
    setPos({ left, top })
  }, [anchor.x, anchor.y, loading, hits.length, error, loadingMessage, groups.length])

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        e.preventDefault()
        onClose()
        return
      }
      if (!flat.length || loading) return
      if (e.key === 'ArrowDown') {
        e.preventDefault()
        setActive((i) => Math.min(flat.length - 1, i + 1))
      } else if (e.key === 'ArrowUp') {
        e.preventDefault()
        setActive((i) => Math.max(0, i - 1))
      } else if (e.key === 'Enter') {
        e.preventDefault()
        const hit = flat[activeRef.current]
        if (hit?.path) {
          onOpen(hit.path, hit.line, hit.column, hit.endColumn)
        }
      }
    }
    const onDown = (e: MouseEvent) => {
      const el = panelRef.current
      if (el && !el.contains(e.target as Node)) onClose()
    }
    window.addEventListener('keydown', onKey)
    const t = window.setTimeout(() => window.addEventListener('mousedown', onDown), 0)
    return () => {
      window.removeEventListener('keydown', onKey)
      window.clearTimeout(t)
      window.removeEventListener('mousedown', onDown)
    }
  }, [onClose, onOpen, flat, active, loading])

  useEffect(() => {
    const row = panelRef.current?.querySelector(`[data-idx="${active}"]`)
    row?.scrollIntoView({ block: 'nearest' })
  }, [active])

  const title =
    mode === 'declarations'
      ? `Declarations of `
      : `Usages of `
  const countLabel = loading ? '' : ` (${hits.length})`

  let flatIdx = 0

  return createPortal(
    <div
      ref={panelRef}
      className="ij-usages"
      role="dialog"
      tabIndex={-1}
      aria-label={mode === 'declarations' ? 'Choose Declaration' : 'Show Usages'}
      style={{ left: pos.left, top: pos.top }}
    >
      <div className="ij-usages-header">
        <span className="ij-usages-title">
          {title}
          <span className="ij-usages-symbol">{symbol || '…'}</span>
          {countLabel}
        </span>
        <button type="button" className="ij-usages-close" onClick={onClose} title="Close (Esc)">
          ✕
        </button>
      </div>

      {loading ? <div className="ij-usages-meta">{loadingMessage || 'Searching…'}</div> : null}
      {error ? <div className="ij-usages-error">{error}</div> : null}
      {!loading && !error && hits.length === 0 ? (
        <div className="ij-usages-meta">
          {mode === 'declarations' ? 'No declaration found' : 'No usages found in project files'}
        </div>
      ) : null}

      {!loading && groups.length > 0 ? (
        <div className="ij-usages-table" role="listbox">
          {groups.map((g) => (
            <div key={g.path ?? g.fileName} className="ij-usages-group">
              <div className="ij-usages-group-header" title={g.path ?? undefined}>
                <JavaFileIcon />
                <span className="ij-usages-group-file">{g.fileName}</span>
                {g.directory ? <span className="ij-usages-group-dir">{g.directory}</span> : null}
                <span className="ij-usages-group-count">{g.hits.length}</span>
              </div>
              {g.hits.map((hit) => {
                const idx = flatIdx++
                const external = hit.path == null
                const selected = idx === active
                return (
                  <button
                    key={`${hit.uri}:${hit.line}:${hit.column}:${idx}`}
                    type="button"
                    role="option"
                    aria-selected={selected}
                    data-idx={idx}
                    className={`ij-usages-row${selected ? ' selected' : ''}${hit.isOrigin ? ' origin' : ''}`}
                    disabled={external}
                    title={external ? 'External / jdt:// not openable in P2' : hit.path ?? hit.uri}
                    onMouseEnter={() => setActive(idx)}
                    onClick={() => {
                      if (hit.path != null) {
                        onOpen(
                          hit.path,
                          hit.line,
                          hit.column,
                          hit.endColumn,
                        )
                      }
                    }}
                  >
                    <span className="ij-usages-mark" aria-hidden>
                      {hit.isOrigin ? (
                        <span className="ij-usages-current" title="Current">
                          ●
                        </span>
                      ) : null}
                    </span>
                    <span className="ij-usages-line">{hit.line}</span>
                    <span className="ij-usages-codecell">
                      <PreviewText
                        text={hit.preview}
                        start={hit.highlightStart}
                        end={hit.highlightEnd}
                      />
                      {hit.isOrigin ? <span className="ij-usages-current-tag">Current</span> : null}
                      {external ? <span className="ij-usages-ext">external</span> : null}
                    </span>
                  </button>
                )
              })}
            </div>
          ))}
        </div>
      ) : null}

      <div className="ij-usages-footer">
        <span>↑↓ Navigate</span>
        <span>⏎ Open</span>
        <span>Esc Close</span>
        {mode === 'usages' ? <span>⌘/Ctrl+click = Declaration or Usages</span> : null}
      </div>
    </div>,
    document.body,
  )
}
