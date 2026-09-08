import { useEffect, useMemo, useRef } from 'react'
import { renderMarkdown, resolveMdLink, type MdLinkAction } from '../markdown/render'

export type MdNavigateRequest = {
  path: string
  line?: number
  /** Heading id after open (other .md). */
  anchorId?: string
  /** Directory link (trailing slash or known folder). */
  dirHint?: boolean
}

type Props = {
  path: string
  source: string
  /** Open another workspace file (relative path from root). */
  onNavigate?: (req: MdNavigateRequest) => void
  /** Scroll target after navigating to this md (heading id). */
  scrollToId?: string | null
}

function scrollToHeading(root: HTMLElement, id: string) {
  const el =
    root.querySelector(`#${CSS.escape(id)}`) ??
    root.querySelector(`[id="${id.replace(/"/g, '')}"]`)
  if (el instanceof HTMLElement) {
    el.scrollIntoView({ block: 'start', behavior: 'smooth' })
  }
}

export function MarkdownPreview({ path, source, onNavigate, scrollToId }: Props) {
  const html = useMemo(() => renderMarkdown(source), [source])
  const rootRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    if (!scrollToId || !rootRef.current) return
    requestAnimationFrame(() => {
      if (rootRef.current) scrollToHeading(rootRef.current, scrollToId)
    })
  }, [scrollToId, source, path])

  function dispatch(action: MdLinkAction) {
    if (action.kind === 'external') {
      window.open(action.href, '_blank', 'noopener,noreferrer')
      return
    }
    if (action.kind === 'anchor') {
      if (rootRef.current) scrollToHeading(rootRef.current, action.id)
      return
    }
    onNavigate?.({
      path: action.path,
      line: action.line,
      anchorId: action.anchorId,
      dirHint: action.dirHint,
    })
    if (action.anchorId && action.path === path && !action.line && rootRef.current) {
      scrollToHeading(rootRef.current, action.anchorId)
    }
  }

  return (
    <div
      className="ij-md-preview"
      tabIndex={0}
      ref={rootRef}
      onClick={(e) => {
        const a = (e.target as HTMLElement | null)?.closest?.('a')
        if (!a || !rootRef.current?.contains(a)) return
        const href = a.getAttribute('href')
        if (!href) return
        const action = resolveMdLink(path, href)
        if (!action) return
        e.preventDefault()
        e.stopPropagation()
        dispatch(action)
      }}
    >
      <article
        className="ij-md-body"
        // Sanitized in renderMarkdown (DOMPurify).
        dangerouslySetInnerHTML={{ __html: html }}
      />
    </div>
  )
}
