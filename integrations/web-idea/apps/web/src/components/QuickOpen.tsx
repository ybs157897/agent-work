import { useEffect, useRef, useState } from 'react'
import type { GatewayClient } from '../api/client'
import { classifyEntry, TreeIcon } from './icons'
import { useDialogFocus } from './useDialogFocus'

type Props = {
  client: GatewayClient
  workspaceId: string
  recent: string[]
  mode: 'files' | 'recent'
  onClose: () => void
  onOpen: (path: string, line?: number, column?: number) => void
}

export function QuickOpen({ client, workspaceId, recent, mode, onClose, onOpen }: Props) {
  const [query, setQuery] = useState('')
  const [paths, setPaths] = useState<string[]>([])
  const [selected, setSelected] = useState(0)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [truncated, setTruncated] = useState(false)
  const dialog = useRef<HTMLDivElement>(null)
  const input = useRef<HTMLInputElement>(null)
  useDialogFocus(dialog, input, onClose)
  const match = /^(.*?)(?::(\d+))(?::(\d+))?$/.exec(query)
  const needle = (match ? match[1]! : query).trim()
  const line = match ? Number(match[2]) : undefined
  const column = match?.[3] ? Number(match[3]) : undefined

  useEffect(() => {
    const controller = new AbortController()
    setSelected(0)
    setError(null)
    setTruncated(false)
    setPaths([])
    if (!needle || mode === 'recent') {
      setPaths(recent.filter((p) => p.toLowerCase().includes(needle.toLowerCase())))
      setBusy(false)
      return
    }
    setBusy(true)
    const timer = window.setTimeout(() => {
      void client.files(workspaceId, needle, 60, controller.signal).then((result) => {
        if (controller.signal.aborted) return
        setPaths(result.paths)
        setTruncated(Boolean(result.truncated))
      }).catch((err: unknown) => {
        if (!controller.signal.aborted) setError(err instanceof Error ? err.message : 'File search failed')
      }).finally(() => {
        if (!controller.signal.aborted) setBusy(false)
      })
    }, 180)
    return () => { window.clearTimeout(timer); controller.abort() }
  }, [client, workspaceId, needle, mode, recent])

  useEffect(() => {
    dialog.current?.querySelector(`[data-result="${selected}"]`)?.scrollIntoView({ block: 'nearest' })
  }, [selected])

  function choose(path: string) { onOpen(path, line, column); onClose() }

  return (
    <div className="ij-find-backdrop" onMouseDown={(e) => { if (e.target === e.currentTarget) onClose() }}>
      <div ref={dialog} className="ij-quick-dialog" role="dialog" aria-modal="true" aria-label={mode === 'recent' ? 'Recent Files' : 'Go to File'}>
        <div className="ij-quick-heading"><strong>{mode === 'recent' ? 'Recent Files' : 'Go to File'}</strong><button onClick={onClose} className="ij-ghost-btn" aria-label="Close file search">Esc</button></div>
        <input ref={input} className="ij-quick-input" value={query} onChange={(e) => setQuery(e.target.value)}
          placeholder={mode === 'recent' ? 'Filter recently opened files…' : 'File name or path · append :line to jump'}
          role="combobox" aria-label="File name or path" aria-expanded="true" aria-controls="quick-file-results"
          aria-autocomplete="list" aria-activedescendant={paths[selected] ? `quick-file-${selected}` : undefined}
          spellCheck={false} onKeyDown={(e) => {
            if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
              e.preventDefault()
              setSelected((n) => Math.max(0, Math.min(paths.length - 1, n + (e.key === 'ArrowDown' ? 1 : -1))))
            } else if (e.key === 'Enter' && paths[selected]) { e.preventDefault(); choose(paths[selected]!) }
          }} />
        <div className="ij-quick-meta" role="status">{busy ? 'Searching files…' : error || (!needle ? 'Recently opened' : `${paths.length} file${paths.length === 1 ? '' : 's'}${truncated ? ' · refine your search for more' : ''}`)}</div>
        <div id="quick-file-results" className="ij-quick-results" role="listbox" aria-label="Files">
          {paths.map((path, i) => {
            const name = path.slice(path.lastIndexOf('/') + 1)
            const dir = path.includes('/') ? path.slice(0, path.lastIndexOf('/')) : 'Project root'
            return <div id={`quick-file-${i}`} data-result={i} key={path} role="option" aria-selected={selected === i}
              className={`ij-quick-result${selected === i ? ' selected' : ''}`} onMouseMove={() => setSelected(i)}
              onMouseDown={(e) => e.preventDefault()} onClick={() => choose(path)}>
              <TreeIcon kind={classifyEntry(name, 'file', { path })} /><span><strong>{name}</strong><small>{dir}</small></span>
              {selected === i ? <kbd>↵{line ? ` ${line}:${column ?? 1}` : ''}</kbd> : null}
            </div>
          })}
        </div>
        {!busy && !error && paths.length === 0 ? <div className="ij-quick-empty">{needle ? 'No matching files. Try a shorter name or part of the path.' : 'Type a file name to search the project.'}</div> : null}
        <div className="ij-quick-footer"><span><kbd>↑</kbd><kbd>↓</kbd> select</span><span><kbd>Enter</kbd> open</span><span><kbd>Esc</kbd> close</span></div>
      </div>
    </div>
  )
}
