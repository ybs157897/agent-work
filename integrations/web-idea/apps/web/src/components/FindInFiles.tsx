import { useEffect, useRef, useState, type FormEvent, type KeyboardEvent as ReactKeyboardEvent } from 'react'
import type { GatewayClient, SearchHit } from '../api/client'
import { useDialogFocus } from './useDialogFocus'

type Props = {
  open: boolean
  client: GatewayClient
  workspaceId: string
  onClose: () => void
  onOpenHit: (path: string, line: number, column?: number) => void
}

/** Keep the dialog subtree mounted only while open so focus hooks do not run for a hidden dialog. */
export function FindInFiles(props: Props) {
  if (!props.open) return null
  return <FindInFilesDialog {...props} />
}

function FindInFilesDialog({ client, workspaceId, onClose, onOpenHit }: Omit<Props, 'open'>) {
  const [query, setQuery] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [hits, setHits] = useState<SearchHit[]>([])
  const [truncated, setTruncated] = useState(false)
  const [selected, setSelected] = useState(0)
  const dialogRef = useRef<HTMLDivElement>(null)
  const inputRef = useRef<HTMLInputElement>(null)
  const requestGeneration = useRef(0)
  const mounted = useRef(true)

  useDialogFocus(dialogRef, inputRef, onClose)

  useEffect(() => {
    return () => {
      mounted.current = false
      // client.search has no AbortSignal parameter. Invalidate callbacks from
      // an old request when the dialog closes or is replaced.
      requestGeneration.current += 1
    }
  }, [])

  useEffect(() => {
    setSelected((current) => (hits.length ? Math.min(current, hits.length - 1) : 0))
  }, [hits.length])

  useEffect(() => {
    if (!hits[selected]) return
    const timer = window.setTimeout(() => {
      const row = document.querySelector<HTMLElement>(`[data-find-result="${selected}"]`)
      row?.scrollIntoView?.({ block: 'nearest' })
    }, 0)
    return () => window.clearTimeout(timer)
  }, [hits, selected])

  async function runSearch(event?: FormEvent) {
    event?.preventDefault()
    const q = query.trim()
    const generation = ++requestGeneration.current
    if (!q) {
      setHits([])
      setTruncated(false)
      setError(null)
      setBusy(false)
      setSelected(0)
      return
    }

    setBusy(true)
    setError(null)
    try {
      const response = await client.search(workspaceId, q)
      if (!mounted.current || generation !== requestGeneration.current) return
      setHits(Array.isArray(response.hits) ? response.hits : [])
      setTruncated(!!response.truncated)
      setSelected(0)
    } catch (err) {
      if (!mounted.current || generation !== requestGeneration.current) return
      setHits([])
      setTruncated(false)
      setError(err instanceof Error ? err.message : 'search failed')
    } finally {
      if (mounted.current && generation === requestGeneration.current) setBusy(false)
    }
  }

  function openHit(hit: SearchHit) {
    onOpenHit(hit.path, hit.line, hit.column)
    onClose()
  }

  function moveSelection(delta: number) {
    if (!hits.length) return
    setSelected((current) => (current + delta + hits.length) % hits.length)
  }

  function onInputKeyDown(event: ReactKeyboardEvent<HTMLInputElement>) {
    if (event.key === 'ArrowDown') {
      event.preventDefault()
      moveSelection(1)
    } else if (event.key === 'ArrowUp') {
      event.preventDefault()
      moveSelection(-1)
    } else if (event.key === 'Home' && hits.length) {
      event.preventDefault()
      setSelected(0)
    } else if (event.key === 'End' && hits.length) {
      event.preventDefault()
      setSelected(hits.length - 1)
    } else if (event.key === 'Enter' && hits[selected]) {
      event.preventDefault()
      openHit(hits[selected]!)
    }
  }

  function onResultKeyDown(event: ReactKeyboardEvent<HTMLButtonElement>, index: number) {
    if (event.key !== 'ArrowDown' && event.key !== 'ArrowUp') return
    event.preventDefault()
    setSelected(index + (event.key === 'ArrowDown' ? 1 : -1) < 0
      ? hits.length - 1
      : (index + (event.key === 'ArrowDown' ? 1 : -1)) % hits.length)
    inputRef.current?.focus()
  }

  return (
    <div className="ij-find-backdrop" role="presentation" onClick={onClose}>
      <div
        ref={dialogRef}
        className="ij-find-dialog"
        role="dialog"
        aria-modal="true"
        aria-labelledby="find-in-files-title"
        onClick={(event) => event.stopPropagation()}
      >
        <div className="ij-find-header">
          <span id="find-in-files-title">Find in Files</span>
          <button type="button" className="ij-ghost-btn" onClick={onClose}>
            Close
          </button>
        </div>
        <form className="ij-find-form" onSubmit={(event) => void runSearch(event)}>
          <label htmlFor="find-in-files-query">Text to find</label>
          <input
            id="find-in-files-query"
            ref={inputRef}
            value={query}
            onChange={(event) => setQuery(event.target.value)}
            onKeyDown={onInputKeyDown}
            placeholder="Search text in project…"
            spellCheck={false}
            role="combobox"
            aria-label="Text to find in project"
            aria-expanded={hits.length > 0}
            aria-controls="find-in-files-results"
            aria-autocomplete="list"
            aria-activedescendant={hits[selected] ? `find-result-${selected}` : undefined}
          />
          <button type="submit" className="ij-primary" disabled={busy}>
            {busy ? 'Searching…' : 'Find'}
          </button>
        </form>
        {error ? <div className="ij-form-error">{error}</div> : null}
        <div className="ij-find-meta" role="status" aria-live="polite">
          {hits.length === 0 && !busy && !error ? 'No results' : null}
          {hits.length > 0 ? `${hits.length} result${hits.length === 1 ? '' : 's'}` : null}
          {truncated ? ' (truncated)' : null}
        </div>
        <ul id="find-in-files-results" className="ij-find-results" role="listbox" aria-label="Search results">
          {hits.map((hit, index) => (
            <li key={`${hit.path}:${hit.line}:${hit.column}:${index}`}>
              <button
                id={`find-result-${index}`}
                type="button"
                role="option"
                aria-selected={selected === index}
                data-find-result={index}
                className={`ij-find-hit${selected === index ? ' selected' : ''}`}
                onMouseEnter={() => setSelected(index)}
                onFocus={() => setSelected(index)}
                onKeyDown={(event) => onResultKeyDown(event, index)}
                onClick={() => openHit(hit)}
              >
                <span className="ij-find-path">
                  {hit.path}
                  <span className="ij-find-loc">
                    :{hit.line}:{hit.column}
                  </span>
                </span>
                <span className="ij-find-preview">{hit.preview}</span>
              </button>
            </li>
          ))}
        </ul>
      </div>
    </div>
  )
}
