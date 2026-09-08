import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type CSSProperties,
  type KeyboardEvent as ReactKeyboardEvent,
} from 'react'
import type { DirEntry, GatewayClient, ProjectInfo } from '../api/client'
import { ChevronIcon, TreeIcon, classifyEntry } from './icons'

type Props = {
  client: GatewayClient
  workspaceId: string
  onOpenFile: (path: string) => void
  activePath: string | null
  /** Expand ancestors and open this directory (markdown folder links). */
  revealDir?: string | null
  /** Maven/JDK project snapshot from Gateway. */
  project?: ProjectInfo | null
  /** Open the project-wide quick file search, when the host provides it. */
  onQuickOpen?: () => void
  /** Pin a file opened from the tree (IDEA double-click behavior). */
  onPinFile?: (path: string) => void
}

type NodeState = {
  loading?: boolean
  children?: DirEntry[]
  error?: string
  open?: boolean
}

type TreeRow = {
  path: string
  type: DirEntry['type']
  open?: boolean
}

type RowRefs = {
  current: Record<string, HTMLButtonElement | null>
}

type PendingTreeLoad = {
  generation: number
  pathGeneration: number
  promise: Promise<void>
}

function joinPath(parent: string, name: string): string {
  return parent ? `${parent}/${name}` : name
}

function normalizePath(path: string): string {
  return path.replaceAll('\\', '/').replace(/^\/+/, '').replace(/\/+$/, '')
}

function parentPath(path: string): string | null {
  const slash = path.lastIndexOf('/')
  return slash < 0 ? null : path.slice(0, slash)
}

function rowId(path: string): string {
  return `ij-tree-item-${encodeURIComponent(path || 'root')}`
}

function sortEntries(entries: DirEntry[]): DirEntry[] {
  return [...entries].sort((a, b) => {
    if (a.type !== b.type) return a.type === 'dir' ? -1 : 1
    return a.name.localeCompare(b.name)
  })
}

function flattenVisible(
  entries: DirEntry[],
  parent: string,
  nodes: Record<string, NodeState>,
  rows: TreeRow[],
): void {
  for (const entry of sortEntries(entries)) {
    const path = joinPath(parent, entry.name)
    const state = nodes[path]
    const open = entry.type === 'dir' && !!state?.open
    rows.push({ path, type: entry.type, open })
    if (entry.type === 'dir' && open && state?.children) {
      flattenVisible(state.children, path, nodes, rows)
    }
  }
}

function shortJdkLabel(version?: string): string {
  if (!version) return 'JDK'
  // 1.8.0_xxx → 8 ; 17.0.2 → 17
  const m17 = /^(\d+)/.exec(version)
  if (m17 && Number(m17[1]) >= 9) return `JDK ${m17[1]}`
  const m8 = /^1\.(\d+)/.exec(version)
  if (m8) return `JDK ${m8[1]}`
  return `JDK ${version}`
}

export function FileTree({
  client,
  workspaceId,
  onOpenFile,
  activePath,
  revealDir,
  project,
  onQuickOpen,
  onPinFile,
}: Props) {
  const [rootEntries, setRootEntries] = useState<DirEntry[]>([])
  const [nodes, setNodes] = useState<Record<string, NodeState>>({})
  const [rootError, setRootError] = useState<string | null>(null)
  const [truncated, setTruncated] = useState(false)
  const [filter, setFilter] = useState('')
  const [libsOpen, setLibsOpen] = useState(false)
  const [jdkOpen, setJdkOpen] = useState(false)
  const [focusPath, setFocusPath] = useState<string | null>(null)
  const nodesRef = useRef(nodes)
  nodesRef.current = nodes

  // The gateway client does not expose AbortSignal yet. These generations make
  // an old response harmless when the workspace changes or a row is collapsed
  // while its children are loading.
  const treeGenerationRef = useRef(0)
  const pathGenerationRef = useRef(new Map<string, number>())
  const pendingLoadsRef = useRef(new Map<string, PendingTreeLoad>())
  const rowRefs = useRef<Record<string, HTMLButtonElement | null>>({})

  const modulePaths = useMemo(() => {
    const paths = new Set<string>()
    for (const module of project?.modules ?? []) {
      if (module.path) paths.add(normalizePath(module.path))
    }
    return paths
  }, [project])

  useEffect(() => {
    const generation = ++treeGenerationRef.current
    let cancelled = false
    setRootError(null)
    setNodes({})
    setRootEntries([])
    pathGenerationRef.current.clear()
    pendingLoadsRef.current.clear()

    const load = async () => {
      try {
        const tr = await client.tree(workspaceId, '', 1)
        if (cancelled || generation !== treeGenerationRef.current) return
        const entries = Array.isArray(tr.entries) ? tr.entries : []
        setRootEntries(entries)
        setTruncated(!!tr.truncated)
        if (entries.length === 0) {
          setRootError('No files returned for this workspace root')
        }
      } catch (e) {
        if (!cancelled && generation === treeGenerationRef.current) {
          setRootError(e instanceof Error ? e.message : 'failed to load tree')
        }
      }
    }
    void load()
    return () => {
      cancelled = true
      if (generation === treeGenerationRef.current) treeGenerationRef.current += 1
      pendingLoadsRef.current.clear()
      pathGenerationRef.current.clear()
    }
  }, [client, workspaceId])

  const bumpPathGeneration = useCallback((path: string): number => {
    const next = (pathGenerationRef.current.get(path) ?? 0) + 1
    pathGenerationRef.current.set(path, next)
    return next
  }, [])

  const ensureOpen = useCallback(
    (rawPath: string): Promise<void> => {
      const path = normalizePath(rawPath)
      // The empty path is the already loaded workspace root. Treating it as a
      // successful reveal lets callers pass the root path without a special
      // case that accidentally skips later ancestors.
      if (!path) return Promise.resolve()

      const generation = treeGenerationRef.current
      const pathGeneration = pathGenerationRef.current.get(path) ?? 0
      const current = nodesRef.current[path]
      if (current?.open && current.children) return Promise.resolve()
      if (current?.children) {
        setNodes((previous) => {
          if (generation !== treeGenerationRef.current) return previous
          return { ...previous, [path]: { ...previous[path], open: true, error: undefined } }
        })
        return Promise.resolve()
      }

      const pending = pendingLoadsRef.current.get(path)
      if (
        pending &&
        pending.generation === generation &&
        pending.pathGeneration === pathGeneration
      ) {
        return pending.promise
      }

      let requestPromise: Promise<void> = Promise.resolve()
      requestPromise = (async () => {
        const isCurrent = () =>
          generation === treeGenerationRef.current &&
          pathGeneration === (pathGenerationRef.current.get(path) ?? 0)
        if (!isCurrent()) return

        setNodes((previous) => {
          if (!isCurrent()) return previous
          return {
            ...previous,
            [path]: { ...previous[path], open: true, loading: true, error: undefined },
          }
        })

        try {
          const tr = await client.tree(workspaceId, path, 1)
          if (!isCurrent()) return
          const children = Array.isArray(tr.entries) ? tr.entries : []
          setNodes((previous) => {
            if (!isCurrent()) return previous
            return { ...previous, [path]: { open: true, children, loading: false } }
          })
        } catch (e) {
          if (!isCurrent()) return
          setNodes((previous) => ({
            ...previous,
            [path]: {
              ...previous[path],
              open: true,
              loading: false,
              error: e instanceof Error ? e.message : 'failed',
            },
          }))
        } finally {
          const active = pendingLoadsRef.current.get(path)
          if (active?.promise === requestPromise) pendingLoadsRef.current.delete(path)
        }
      })()
      pendingLoadsRef.current.set(path, { generation, pathGeneration, promise: requestPromise })
      return requestPromise
    },
    [client, workspaceId],
  )

  const toggleDir = useCallback(
    (rawPath: string) => {
      const path = normalizePath(rawPath)
      if (!path) return
      const current = nodesRef.current[path]
      if (current?.open) {
        // Invalidate the outstanding load too, so collapsing a loading row
        // cannot make it spring open when its stale response arrives.
        bumpPathGeneration(path)
        setNodes((previous) => ({
          ...previous,
          [path]: { ...previous[path], open: false, loading: false },
        }))
        return
      }
      void ensureOpen(path)
    },
    [bumpPathGeneration, ensureOpen],
  )

  useEffect(() => {
    const path = normalizePath(activePath ?? '')
    setFocusPath(path || null)
    if (!path) return
    const parts = path.split('/').filter(Boolean)
    if (parts.length < 2) return
    let cancelled = false
    const run = async () => {
      let ancestor = ''
      for (let index = 0; index < parts.length - 1; index += 1) {
        ancestor = joinPath(ancestor, parts[index]!)
        if (cancelled) return
        await ensureOpen(ancestor)
      }
    }
    void run()
    return () => {
      cancelled = true
    }
  }, [activePath, ensureOpen])

  useEffect(() => {
    // Empty string and "/" intentionally reveal the root. A fresh effect run
    // also permits a repeated reveal after a previous run was cancelled.
    const target = normalizePath(revealDir ?? '')
    let cancelled = false
    const run = async () => {
      if (!target) return
      let current = ''
      for (const part of target.split('/').filter(Boolean)) {
        current = joinPath(current, part)
        if (cancelled) return
        await ensureOpen(current)
      }
    }
    void run()
    return () => {
      cancelled = true
    }
  }, [revealDir, ensureOpen])

  const q = filter.trim().toLowerCase()
  const visibleRoot = q
    ? rootEntries.filter((entry) => entry.name.toLowerCase().includes(q))
    : rootEntries

  const visibleRows = useMemo(() => {
    const rows: TreeRow[] = []
    flattenVisible(visibleRoot, '', nodes, rows)
    return rows
  }, [nodes, visibleRoot])

  const effectiveFocusPath =
    focusPath && visibleRows.some((row) => row.path === focusPath)
      ? focusPath
      : visibleRows[0]?.path ?? null

  const focusRow = useCallback((path: string | null) => {
    if (!path) return
    setFocusPath(path)
    const row = rowRefs.current[path]
    if (!row) return
    row.focus()
    row.scrollIntoView?.({ block: 'nearest' })
  }, [])

  const handleTreeKeyDown = useCallback(
    (event: ReactKeyboardEvent<HTMLButtonElement>, row: TreeRow) => {
      const index = visibleRows.findIndex((item) => item.path === row.path)
      if (index < 0) return

      if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
        event.preventDefault()
        const next = index + (event.key === 'ArrowDown' ? 1 : -1)
        if (next >= 0 && next < visibleRows.length) focusRow(visibleRows[next]!.path)
        return
      }
      if (event.key === 'Home' || event.key === 'End') {
        event.preventDefault()
        focusRow(visibleRows[event.key === 'Home' ? 0 : visibleRows.length - 1]?.path ?? null)
        return
      }
      if (event.key === 'ArrowRight') {
        if (row.type !== 'dir') return
        event.preventDefault()
        if (!row.open) {
          void ensureOpen(row.path)
          return
        }
        const firstChild = visibleRows[index + 1]
        if (firstChild?.path.startsWith(`${row.path}/`)) focusRow(firstChild.path)
        return
      }
      if (event.key === 'ArrowLeft') {
        event.preventDefault()
        if (row.type === 'dir' && row.open) {
          toggleDir(row.path)
          return
        }
        const parent = parentPath(row.path)
        if (parent) focusRow(parent)
        return
      }
      if (event.key === 'Enter' || (event.key === ' ' && row.type === 'dir')) {
        event.preventDefault()
        if (row.type === 'dir') toggleDir(row.path)
        else onOpenFile(row.path)
      }
    },
    [ensureOpen, focusRow, onOpenFile, toggleDir, visibleRows],
  )

  useEffect(() => {
    const path = normalizePath(activePath ?? '')
    if (!path) return
    const timer = window.setTimeout(() => {
      const row = rowRefs.current[path]
      row?.scrollIntoView?.({ block: 'nearest' })
    }, 0)
    return () => window.clearTimeout(timer)
  }, [activePath, filter, nodes, rootEntries])

  if (rootError) return <div className="ij-tree-error">{rootError}</div>

  const rootTitle =
    project?.root_module?.name ||
    project?.root_module?.artifact_id ||
    (project?.build === 'maven' ? 'Maven Project' : null)
  const jdkLabel = shortJdkLabel(project?.jdk.version)
  const deps = project?.dependencies ?? []
  const showProjectExtras = project && project.build !== 'none'

  return (
    <div className="ij-tree">
      {rootTitle ? (
        <div className="ij-tree-project" title={project?.root_module?.group_id}>
          <TreeIcon kind="moduleJava" />
          <span className="ij-tree-project-name">{rootTitle}</span>
          {project?.build === 'maven' ? <span className="ij-tree-project-badge">Maven</span> : null}
          {project?.build === 'gradle' ? <span className="ij-tree-project-badge">Gradle</span> : null}
        </div>
      ) : null}
      <div className="ij-tree-search">
        {onQuickOpen ? (
          <button
            type="button"
            className="ij-ghost-btn"
            style={{ width: '100%', textAlign: 'left' }}
            onClick={onQuickOpen}
          >
            Go to File…
          </button>
        ) : (
          <input
            value={filter}
            onChange={(event) => setFilter(event.target.value)}
            placeholder="Filter current project root…"
            aria-label="Filter files in current project root"
          />
        )}
      </div>
      {truncated ? <div className="ij-tree-hint">Directory truncated</div> : null}
      <div
        role="tree"
        aria-label="Project files"
        aria-activedescendant={effectiveFocusPath ? rowId(effectiveFocusPath) : undefined}
      >
        <TreeLevel
          entries={visibleRoot}
          parentPath=""
          nodes={nodes}
          activePath={activePath}
          highlightDir={revealDir ?? null}
          modulePaths={modulePaths}
          onToggle={toggleDir}
          onOpenFile={onOpenFile}
          onPinFile={onPinFile}
          onKeyDown={handleTreeKeyDown}
          onFocusPath={setFocusPath}
          focusPath={effectiveFocusPath}
          rowRefs={rowRefs}
          depth={0}
        />
      </div>
      {showProjectExtras ? (
        <div className="ij-tree-virtual">
          <button
            type="button"
            className={`ij-tree-row dir${libsOpen ? ' open' : ''}`}
            style={{ paddingLeft: 12 }}
            onClick={() => setLibsOpen((value) => !value)}
          >
            <span className="ij-chevron-slot">
              <ChevronIcon open={libsOpen} />
            </span>
            <TreeIcon kind="libraries" />
            <span className="ij-name">External Libraries</span>
          </button>
          {libsOpen ? (
            <ul className="ij-tree-list" role="group">
              {deps.length === 0 ? (
                <li className="ij-tree-hint" style={{ paddingLeft: 44 }}>
                  No declared dependencies in root pom
                </li>
              ) : (
                deps.map((dependency) => (
                  <li key={dependency.coord} className="ij-tree-virtual-item" title={dependency.scope}>
                    <span className="ij-chevron-slot spacer" />
                    <TreeIcon kind="libraryFolder" />
                    <span className="ij-name">{dependency.coord}</span>
                  </li>
                ))
              )}
              {project?.dependencies_truncated ? (
                <li className="ij-tree-hint" style={{ paddingLeft: 44 }}>
                  …truncated
                </li>
              ) : null}
            </ul>
          ) : null}

          <button
            type="button"
            className={`ij-tree-row dir${jdkOpen ? ' open' : ''}`}
            style={{ paddingLeft: 12 }}
            onClick={() => setJdkOpen((value) => !value)}
          >
            <span className="ij-chevron-slot">
              <ChevronIcon open={jdkOpen} />
            </span>
            <TreeIcon kind="jdk" />
            <span className="ij-name">{jdkLabel}</span>
          </button>
          {jdkOpen ? (
            <ul className="ij-tree-list" role="group">
              {project?.language_level ? (
                <li className="ij-tree-virtual-item">
                  <span className="ij-chevron-slot spacer" />
                  <span className="ij-name">Language level: {project.language_level}</span>
                </li>
              ) : null}
              {project?.jdk.home ? (
                <li className="ij-tree-virtual-item" title={project.jdk.home}>
                  <span className="ij-chevron-slot spacer" />
                  <span className="ij-name">{project.jdk.home}</span>
                </li>
              ) : null}
              {project?.jdk.vendor ? (
                <li className="ij-tree-virtual-item">
                  <span className="ij-chevron-slot spacer" />
                  <span className="ij-name">{project.jdk.vendor}</span>
                </li>
              ) : null}
              {!project?.jdk.version ? (
                <li className="ij-tree-hint" style={{ paddingLeft: 44 }}>
                  JAVA_HOME / java not detected on Gateway host
                </li>
              ) : null}
            </ul>
          ) : null}
        </div>
      ) : null}
    </div>
  )
}

function TreeLevel({
  entries,
  parentPath: parent,
  nodes,
  activePath,
  highlightDir,
  modulePaths,
  onToggle,
  onOpenFile,
  onPinFile,
  onKeyDown,
  onFocusPath,
  focusPath,
  rowRefs,
  depth,
}: {
  entries: DirEntry[]
  parentPath: string
  nodes: Record<string, NodeState>
  activePath: string | null
  highlightDir: string | null
  modulePaths: Set<string>
  onToggle: (path: string) => void
  onOpenFile: (path: string) => void
  onPinFile?: (path: string) => void
  onKeyDown: (event: ReactKeyboardEvent<HTMLButtonElement>, row: TreeRow) => void
  onFocusPath: (path: string) => void
  focusPath: string | null
  rowRefs: RowRefs
  depth: number
}) {
  const sorted = sortEntries(entries)

  return (
    <ul className="ij-tree-list" role="group">
      {sorted.map((entry, position) => {
        const path = joinPath(parent, entry.name)
        const pad: CSSProperties = { paddingLeft: `${12 + depth * 16}px` }

        if (entry.type === 'dir') {
          const state = nodes[path]
          const open = !!state?.open
          const row: TreeRow = { path, type: 'dir', open }
          const kind = classifyEntry(entry.name, 'dir', { open, path, modulePaths })
          const highlighted = normalizePath(highlightDir ?? '') === path
          const active = normalizePath(activePath ?? '') === path
          return (
            <li
              key={path}
              id={rowId(path)}
              role="treeitem"
              aria-expanded={open}
              aria-selected={active}
              aria-level={depth + 1}
              aria-posinset={position + 1}
              aria-setsize={sorted.length}
            >
              <button
                ref={(element) => {
                  rowRefs.current[path] = element
                }}
                type="button"
                className={`ij-tree-row dir${open ? ' open' : ''}${highlighted || active ? ' active' : ''}`}
                onClick={() => onToggle(path)}
                onKeyDown={(event) => onKeyDown(event, row)}
                onFocus={() => onFocusPath(path)}
                tabIndex={focusPath === path ? 0 : -1}
                style={pad}
                title={path || entry.name}
                aria-label={`${open ? 'Collapse' : 'Expand'} ${path || entry.name}`}
              >
                <span className="ij-chevron-slot">
                  <ChevronIcon open={open} />
                </span>
                <TreeIcon kind={kind} />
                <span className="ij-name">{entry.name}</span>
              </button>
              {open ? (
                <div className="ij-tree-children">
                  {state?.loading ? (
                    <div className="ij-tree-hint" style={{ paddingLeft: `${28 + depth * 16}px` }}>
                      Loading…
                    </div>
                  ) : null}
                  {state?.error ? <div className="ij-tree-error">{state.error}</div> : null}
                  {state?.children ? (
                    <TreeLevel
                      entries={state.children}
                      parentPath={path}
                      nodes={nodes}
                      activePath={activePath}
                      highlightDir={highlightDir}
                      modulePaths={modulePaths}
                      onToggle={onToggle}
                      onOpenFile={onOpenFile}
                      onPinFile={onPinFile}
                      onKeyDown={onKeyDown}
                      onFocusPath={onFocusPath}
                      focusPath={focusPath}
                      rowRefs={rowRefs}
                      depth={depth + 1}
                    />
                  ) : null}
                </div>
              ) : null}
            </li>
          )
        }

        const active = normalizePath(activePath ?? '') === path
        const row: TreeRow = { path, type: 'file' }
        const kind = classifyEntry(entry.name, 'file', { path })
        return (
          <li
            key={path}
            id={rowId(path)}
            role="treeitem"
            aria-selected={active}
            aria-level={depth + 1}
            aria-posinset={position + 1}
            aria-setsize={sorted.length}
          >
            <button
              ref={(element) => {
                rowRefs.current[path] = element
              }}
              type="button"
              className={`ij-tree-row file${active ? ' active' : ''}`}
              onClick={() => onOpenFile(path)}
              onDoubleClick={() => onPinFile?.(path)}
              onKeyDown={(event) => onKeyDown(event, row)}
              onFocus={() => onFocusPath(path)}
              tabIndex={focusPath === path ? 0 : -1}
              style={pad}
              title={path}
              aria-label={entry.name}
            >
              <span className="ij-chevron-slot spacer" />
              <TreeIcon kind={kind} />
              <span className="ij-name">{entry.name}</span>
            </button>
          </li>
        )
      })}
    </ul>
  )
}
