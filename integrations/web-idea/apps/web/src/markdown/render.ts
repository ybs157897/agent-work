import DOMPurify from 'dompurify'
import { marked, type Tokens } from 'marked'

/** GitHub-ish heading slug for in-doc #anchors. */
export function slugifyHeading(text: string): string {
  return text
    .trim()
    .toLowerCase()
    .replace(/[^a-z0-9\u4e00-\u9fff\s-_]+/g, '')
    .replace(/\s+/g, '-')
}

marked.use({
  gfm: true,
  breaks: false,
  renderer: {
    heading({ tokens, depth }: Tokens.Heading): string {
      const text = this.parser.parseInline(tokens)
      const plain = tokens
        .map((t) => ('text' in t && typeof t.text === 'string' ? t.text : ''))
        .join('')
      const id = slugifyHeading(plain || text.replace(/<[^>]+>/g, ''))
      return `<h${depth} id="${id}">${text}</h${depth}>\n`
    },
  },
})

/** Workspace markdown → sanitized HTML for preview pane. */
export function renderMarkdown(source: string): string {
  const raw = marked.parse(source, { async: false }) as string
  return DOMPurify.sanitize(raw, {
    USE_PROFILES: { html: true },
    ADD_ATTR: ['id', 'target', 'rel'],
  })
}

export function isMarkdownPath(path: string | null): boolean {
  if (!path) return false
  const base = path.slice(path.lastIndexOf('/') + 1).toLowerCase()
  return base.endsWith('.md') || base.endsWith('.markdown') || base.endsWith('.mdx')
}

export type MdLinkAction =
  | { kind: 'external'; href: string }
  | { kind: 'anchor'; id: string }
  | {
      kind: 'workspace'
      path: string
      line?: number
      anchorId?: string
      /** Href ended with `/` (directory link, e.g. `./ServletAjax/`). */
      dirHint?: boolean
    }

function dirname(path: string): string {
  const i = path.lastIndexOf('/')
  return i >= 0 ? path.slice(0, i) : ''
}

/** Normalize `a/../b` → `b`; reject escape above workspace root. */
export function normalizeWorkspaceRel(rel: string): string | null {
  const parts: string[] = []
  for (const seg of rel.replace(/\\/g, '/').split('/')) {
    if (!seg || seg === '.') continue
    if (seg === '..') {
      if (parts.length === 0) return null
      parts.pop()
      continue
    }
    parts.push(seg)
  }
  return parts.join('/')
}

/**
 * Resolve a markdown href relative to the open file.
 * Supports `#anchor`, `path#anchor`, `path#L12`, http(s)/mailto.
 */
export function resolveMdLink(fromFile: string, href: string): MdLinkAction | null {
  const trimmed = href.trim()
  if (!trimmed || trimmed.startsWith('javascript:')) return null

  if (/^(https?:|mailto:|tel:)/i.test(trimmed)) {
    return { kind: 'external', href: trimmed }
  }

  const hashIdx = trimmed.indexOf('#')
  const pathPart = hashIdx >= 0 ? trimmed.slice(0, hashIdx) : trimmed
  const hash = hashIdx >= 0 ? trimmed.slice(hashIdx + 1) : ''

  if (!pathPart) {
    return hash ? { kind: 'anchor', id: decodeURIComponent(hash) } : null
  }

  const dirHint = pathPart.endsWith('/')
  // Workspace-absolute (leading /) or relative to current file dir
  const joined = pathPart.startsWith('/')
    ? pathPart.replace(/^\/+/, '')
    : [dirname(fromFile), pathPart].filter(Boolean).join('/')
  const path = normalizeWorkspaceRel(joined)
  if (path == null) return null

  let line: number | undefined
  let anchorId: string | undefined
  if (hash) {
    const lineMatch = /^L(\d+)(?:-L?\d+)?$/i.exec(hash)
    if (lineMatch) {
      line = Number(lineMatch[1])
    } else {
      anchorId = decodeURIComponent(hash)
    }
  }
  return { kind: 'workspace', path, line, anchorId, dirHint: dirHint || undefined }
}