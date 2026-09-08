import { lazy, Suspense, useCallback, useEffect, useMemo, useRef, useState, type FormEvent } from 'react'
import { fetchEmbeddedBootstrap, type EmbeddedBootstrap } from './api/bootstrap'
import { GatewayClient, type ProjectInfo, type Workspace } from './api/client'
import type { RevealTarget, SymbolClickRequest } from './components/CodePane'
const CodePane = lazy(() => import('./components/CodePane').then((module) => ({ default: module.CodePane })))
import { QuickOpen } from './components/QuickOpen'
import { TreeIcon, classifyEntry } from './components/icons'
import { FileTree } from './components/FileTree'
import { FindInFiles } from './components/FindInFiles'
import { ReferencesPeek, type PeekMode } from './components/ReferencesPeek'
import { decideGtdu, type NavTarget } from './lsp/gtdu'
import { NavHistory, placeFromClick, type NavPlace } from './lsp/navHistory'
import { JavaLspSession, clientDocUri, type LspStatus } from './lsp/session'
import { buildUsageHits, type UsageHit } from './lsp/usageHits'
import type { EmbeddedTheme } from './theme/embeddedTheme'

type Session = {
  client: GatewayClient
  workspaceId: string
  rootLabel: string
  gatewayUrl: string
  token: string
  lspEnabled: boolean
  readOnly: boolean
  embedded: boolean
}

const defaultGateway = import.meta.env.VITE_GATEWAY_URL ?? 'http://127.0.0.1:8080'
const defaultToken = import.meta.env.VITE_DEV_TOKEN ?? 'dev-token-change-me'
const defaultRoot =
  import.meta.env.VITE_DEFAULT_ROOT ?? ''
const embeddedWindow = typeof window !== 'undefined' && window.parent !== window

export default function App() {
  const [gatewayUrl, setGatewayUrl] = useState(defaultGateway)
  const [token, setToken] = useState(defaultToken)
  const [root, setRoot] = useState(defaultRoot)
  const [session, setSession] = useState<Session | null>(null)
  const [projectInfo, setProjectInfo] = useState<ProjectInfo | null>(null)
  const [busy, setBusy] = useState(false)
  const [formError, setFormError] = useState<string | null>(null)
  const [embeddedState, setEmbeddedState] = useState<'standalone' | 'loading' | 'ready' | 'error'>(embeddedWindow ? 'loading' : 'standalone')
  const [embeddedError, setEmbeddedError] = useState<string | null>(null)
  const [editorTheme, setEditorTheme] = useState<EmbeddedTheme>(() =>
    typeof document !== 'undefined' && document.documentElement.dataset.theme === 'dark' ? 'dark' : 'light',
  )

  const [activePath, setActivePath] = useState<string | null>(null)
  const [fileContent, setFileContent] = useState<string | null>(null)
  /** Bumps when Monaco must adopt buffer (open / disk reload) — not on local typing. */
  const [contentRevision, setContentRevision] = useState(0)
  const [dirty, setDirty] = useState(false)
  const [saving, setSaving] = useState(false)
  const [fileError, setFileError] = useState<string | null>(null)
  const [fileLoading, setFileLoading] = useState(false)
  const [reveal, setReveal] = useState<RevealTarget | null>(null)
  const [mdScrollToId, setMdScrollToId] = useState<string | null>(null)
  const [revealDir, setRevealDir] = useState<string | null>(null)
  const [findOpen, setFindOpen] = useState(false)
  const [quickMode, setQuickMode] = useState<'files' | 'recent' | null>(null)
  const [tabs, setTabs] = useState<string[]>([])
  const [recent, setRecent] = useState<string[]>([])
  const [previewPath, setPreviewPath] = useState<string | null>(null)
  const [projectVisible, setProjectVisible] = useState(true)
  const [fontSize, setFontSize] = useState(14)
  const [wordWrap, setWordWrap] = useState(false)
  const [focusRequest, setFocusRequest] = useState(0)
  const [caret, setCaret] = useState({ line: 1, column: 1 })
  const caretRef = useRef(caret)
  const closeQuick = useCallback(() => setQuickMode(null), [])
  const previewRef = useRef<string | null>(null)
  previewRef.current = previewPath
  const savePromiseRef = useRef<Promise<boolean> | null>(null)
  const loadingRef = useRef(false)
  const navigationActionRef = useRef(false)
  const sessionRef = useRef(session)
  sessionRef.current = session


  const [lspStatus, setLspStatus] = useState<LspStatus>('off')
  const [lspError, setLspError] = useState<string | null>(null)
  const lspRef = useRef<JavaLspSession | null>(null)
  const didChangeTimer = useRef<ReturnType<typeof setTimeout> | null>(null)
  const autoSaveTimer = useRef<ReturnType<typeof setTimeout> | null>(null)
  const fileContentRef = useRef<string | null>(null)
  const dirtyRef = useRef(false)
  const savingRef = useRef(false)
  const activePathRef = useRef<string | null>(null)
  const diskFpRef = useRef<{ mtime?: string; size?: number } | null>(null)
  const ignoreWatchUntilRef = useRef(0)
  const navHistoryRef = useRef(new NavHistory())
  const openSeqRef = useRef(0)
  const [, setNavEpoch] = useState(0)
  const embeddedBootstrapRef = useRef<Promise<EmbeddedBootstrap> | null>(null)
  const embeddedAppliedRef = useRef(false)

  fileContentRef.current = fileContent
  dirtyRef.current = dirty
  savingRef.current = saving
  activePathRef.current = activePath
 // bump to refresh canBack/canForward in UI

  const peekSeqRef = useRef(0)
  const closePeek = useCallback(() => { ++peekSeqRef.current; setPeekOpen(false) }, [])
  const [peekOpen, setPeekOpen] = useState(false)
  const [peekMode, setPeekMode] = useState<PeekMode>('usages')
  const [peekSymbol, setPeekSymbol] = useState('')
  const [peekLoading, setPeekLoading] = useState(false)
  const [peekProgress, setPeekProgress] = useState<string | null>(null)
  const [peekError, setPeekError] = useState<string | null>(null)
  const [peekHits, setPeekHits] = useState<UsageHit[]>([])
  const [peekAnchor, setPeekAnchor] = useState({ x: 0, y: 0 })

  const client = useMemo(
    () => (session ? session.client : new GatewayClient(gatewayUrl, token)),
    [session, gatewayUrl, token],
  )

  function applyWorkspaceSession(
    ws: Workspace,
    nextClient: GatewayClient,
    options: { gatewayUrl: string; token: string; rootLabel: string; readOnly: boolean; embedded: boolean },
  ): void {
    const current = sessionRef.current
    if (
      current &&
      current.workspaceId === ws.id &&
      current.gatewayUrl === options.gatewayUrl &&
      current.readOnly === options.readOnly &&
      current.embedded === options.embedded
    ) return

    lspRef.current?.dispose()
    lspRef.current = null
    if (didChangeTimer.current) clearTimeout(didChangeTimer.current)
    if (autoSaveTimer.current) clearTimeout(autoSaveTimer.current)
    setLspStatus('off')
    setLspError(null)

    const next: Session = {
      client: nextClient,
      workspaceId: ws.id,
      rootLabel: options.rootLabel,
      gatewayUrl: options.gatewayUrl,
      token: options.token,
      lspEnabled: Boolean(ws.lsp_enabled),
      readOnly: options.readOnly,
      embedded: options.embedded,
    }
    sessionRef.current = next
    setSession(next)
    setProjectInfo(null)
    void next.client.project(next.workspaceId).then((info) => {
      if (sessionRef.current === next) setProjectInfo(info)
    }).catch(() => {
      if (sessionRef.current === next) setProjectInfo(null)
    })
    setTabs([])
    setRecent([])
    setPreviewPath(null)
    setActivePath(null)
    setFileContent(null)
    activePathRef.current = null
    fileContentRef.current = null
    diskFpRef.current = null
    setContentRevision((n) => n + 1)
    setDirty(false)
    dirtyRef.current = false
    setSaving(false)
    savingRef.current = false
    setFileError(null)
    setReveal(null)
    setPeekOpen(false)
    setQuickMode(null)
    navHistoryRef.current.clear()
    setNavEpoch((n) => n + 1)

    if (ws.lsp_enabled) {
      const lsp = new JavaLspSession(next.gatewayUrl, next.token, next.workspaceId, next.readOnly)
      lsp.onStatus = (status, error) => {
        if (sessionRef.current !== next) return
        setLspStatus(status)
        setLspError(error ?? null)
      }
      lspRef.current = lsp
    } else {
      setLspError('jdtls not configured on gateway')
    }
  }

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (!session) return
      const mod = e.metaKey || e.ctrlKey
      if (mod && e.shiftKey && e.key.toLowerCase() === 'f') {
        e.preventDefault()
        setFindOpen(true)
        return
      }
      if (quickMode || findOpen) return
      if (mod && !e.altKey && ((e.shiftKey && e.key.toLowerCase() === 'o') || (!e.shiftKey && e.key.toLowerCase() === 'p'))) {
        e.preventDefault(); setQuickMode('files'); return
      }
      if (mod && !e.shiftKey && e.key.toLowerCase() === 'e') {
        e.preventDefault(); setQuickMode('recent'); return
      }
      if (e.altKey && e.key === '1') { e.preventDefault(); setProjectVisible((v) => !v); return }
      if (e.key === 'Escape' && !peekOpen) { setFocusRequest((n) => n + 1); return }
      if (mod && e.key.toLowerCase() === 'w' && activePath) { e.preventDefault(); void closeTab(activePath); return }
      // IDEA Navigate Back / Forward:
      // Win/Linux: Ctrl+Alt+Left/Right ; macOS: ⌘[ / ⌘] (also Ctrl+Alt+←/→)
      const back =
        ((e.ctrlKey || e.metaKey) && e.altKey && e.key === 'ArrowLeft') ||
        (e.metaKey && e.key === '[')
      const forward =
        ((e.ctrlKey || e.metaKey) && e.altKey && e.key === 'ArrowRight') ||
        (e.metaKey && e.key === ']')
      if (back) {
        e.preventDefault()
        goNavBack()
        return
      }
      if (forward) {
        e.preventDefault()
        goNavForward()
        return
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  })

  useEffect(() => {
    const onTheme = (event: Event) => {
      const theme = (event as CustomEvent<EmbeddedTheme>).detail
      if (theme === 'light' || theme === 'dark') setEditorTheme(theme)
    }
    window.addEventListener('atw-code-theme', onTheme)
    return () => window.removeEventListener('atw-code-theme', onTheme)
  }, [])

  function loadEmbeddedWorkspace(): void {
    if (!embeddedWindow) return
    setEmbeddedState('loading')
    setEmbeddedError(null)
    const pending = embeddedBootstrapRef.current ?? (embeddedBootstrapRef.current = fetchEmbeddedBootstrap())
    void pending.then((bootstrap) => {
      if (!embeddedAppliedRef.current || sessionRef.current?.workspaceId !== bootstrap.workspace.id) {
        embeddedAppliedRef.current = true
        const nextClient = new GatewayClient(bootstrap.gateway_base_url, '')
        applyWorkspaceSession(bootstrap.workspace, nextClient, {
          gatewayUrl: bootstrap.gateway_base_url,
          token: '',
          rootLabel: bootstrap.workspace.root_display || 'Java workspace',
          readOnly: true,
          embedded: true,
        })
      }
      setEmbeddedState('ready')
    }).catch((error: unknown) => {
      embeddedBootstrapRef.current = null
      setEmbeddedState('error')
      setEmbeddedError(error instanceof Error ? error.message : 'embedded code workspace bootstrap failed')
    })
  }

  useEffect(() => {
    if (embeddedWindow) loadEmbeddedWorkspace()
    // The promise is deliberately shared through embeddedBootstrapRef so
    // React StrictMode cannot create two bootstrap sessions or LSP owners.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  useEffect(() => {
    return () => {
      lspRef.current?.dispose()
      lspRef.current = null
      if (didChangeTimer.current) clearTimeout(didChangeTimer.current)
      if (autoSaveTimer.current) clearTimeout(autoSaveTimer.current)
    }
  }, [])

  useEffect(() => {
    if (!session || !activePath?.endsWith('.java') || fileLoading) return
    const lsp = lspRef.current
    if (!lsp) return
    if (lsp.status === 'off') void lsp.start().catch(() => {})
    const text = fileContentRef.current
    if (text != null) void lsp.didOpen(activePath, text, 'java')
  }, [session, activePath, contentRevision, lspStatus, fileLoading])

  useEffect(() => {
    const warn = (e: BeforeUnloadEvent) => {
      if (dirtyRef.current || savingRef.current) { e.preventDefault(); e.returnValue = '' }
    }
    window.addEventListener('beforeunload', warn)
    return () => window.removeEventListener('beforeunload', warn)
  }, [])

  async function openWorkspace(e: FormEvent) {
    e.preventDefault()
    setFormError(null)
    setBusy(true)
    try {
      const c = new GatewayClient(gatewayUrl.trim(), token.trim())
      const ws = await c.createWorkspace(root.trim())
      applyWorkspaceSession(ws, c, {
        gatewayUrl: gatewayUrl.trim(),
        token: token.trim(),
        rootLabel: ws.root_display || root.trim(),
        readOnly: Boolean(ws.read_only),
        embedded: false,
      })
    } catch (err) {
      setFormError(err instanceof Error ? err.message : 'failed')
      sessionRef.current = null
      setSession(null)
    } finally {
      setBusy(false)
    }
  }

  function currentPlace(): NavPlace | null {
    const path = activePathRef.current
    if (!path) return null
    const position = caretRef.current
    return { path, reveal: { ...position, endLine: position.line, endColumn: position.column } }
  }

  async function captureDiskFingerprint(path: string) {
    const current = sessionRef.current
    if (!current) return
    try {
      const st = await current.client.stat(current.workspaceId, path)
      if (activePathRef.current === path && sessionRef.current === current) diskFpRef.current = { mtime: st.modified_at, size: st.size }
    } catch {
      if (activePathRef.current === path) diskFpRef.current = null
    }
  }

  async function saveFile(): Promise<boolean> {
    if (savePromiseRef.current) {
      if (!await savePromiseRef.current) return false
      return dirtyRef.current ? saveFile() : true
    }
    const current = sessionRef.current
    const path = activePathRef.current
    const content = fileContentRef.current
    if (!current || current.readOnly || !path || content == null || !dirtyRef.current) return true
    if (autoSaveTimer.current) clearTimeout(autoSaveTimer.current)
    setSaving(true)
    savingRef.current = true
    const work = (async () => {
      try {
        await current.client.writeFile(current.workspaceId, path, content)
        if (fileContentRef.current === content && activePathRef.current === path) {
          setDirty(false)
          dirtyRef.current = false
        }
        setFileError(null)
        ignoreWatchUntilRef.current = Date.now() + 1200
        await captureDiskFingerprint(path)
        if (path.endsWith('.java')) await lspRef.current?.didSave(path, content)
        return true
      } catch (err) {
        setFileError(`Could not save ${path}: ${err instanceof Error ? err.message : 'save failed'}. Your edits are still open.`)
        return false
      } finally {
        setSaving(false)
        savingRef.current = false
        savePromiseRef.current = null
      }
    })()
    savePromiseRef.current = work
    const ok = await work
    return ok && dirtyRef.current ? saveFile() : ok
  }

  async function openFile(
    path: string, line?: number, column?: number, endColumn?: number,
    opts?: { fromHistory?: boolean; recordFrom?: NavPlace | null; keepMdAnchor?: boolean; pin?: boolean; internal?: boolean },
  ): Promise<boolean> {
    const current = sessionRef.current
    if (!current || (navigationActionRef.current && !opts?.internal)) return false
    if (path === activePathRef.current && !line && !loadingRef.current) {
      if (opts?.pin) setPreviewPath((p) => p === path ? null : p)
      setFocusRequest((n) => n + 1)
      return true
    }
    const seq = ++openSeqRef.current
    ++peekSeqRef.current
    setPeekOpen(false)
    const nextReveal: RevealTarget | null = line && line > 0
      ? { line, column: column && column > 0 ? column : 1, endLine: line, endColumn: endColumn ?? column ?? 1 }
      : null
    const from = opts?.recordFrom !== undefined ? opts.recordFrom : currentPlace()
    loadingRef.current = true
    setFileLoading(true)
    if (autoSaveTimer.current) clearTimeout(autoSaveTimer.current)
    try {
      if (!await saveFile()) return false
      if (seq !== openSeqRef.current || sessionRef.current !== current) return false
      let text = fileContentRef.current
      let fingerprint = diskFpRef.current
      if (path !== activePathRef.current || text == null) {
        const stat = await current.client.stat(current.workspaceId, path)
        text = await current.client.readFile(current.workspaceId, path)
        fingerprint = { mtime: stat.modified_at, size: stat.size }
      }
      if (seq !== openSeqRef.current || sessionRef.current !== current) return false
      if (!opts?.fromHistory && from && !samePlaceQuick(from, { path, reveal: nextReveal })) {
        navHistoryRef.current.recordLeave(from)
        setNavEpoch((n) => n + 1)
      }
      const previousPath = activePathRef.current
      if (didChangeTimer.current) clearTimeout(didChangeTimer.current)
      if (previousPath?.endsWith('.java') && previousPath !== path) lspRef.current?.didClose(previousPath)
      const previousPreview = previewRef.current
      setTabs((list) => {
        if (list.includes(path)) return list
        const retained = previousPreview ? list.filter((p) => p !== previousPreview) : list
        return [...retained, path].slice(-30)
      })
      setPreviewPath((previous) => tabs.includes(path) ? (opts?.pin && previous === path ? null : previous) : (opts?.pin ? null : path))
      setRecent((list) => [path, ...list.filter((p) => p !== path)].slice(0, 30))
      activePathRef.current = path
      fileContentRef.current = text
      diskFpRef.current = fingerprint
      dirtyRef.current = false
      setActivePath(path)
      setFileContent(text)
      setContentRevision((n) => n + 1)
      setDirty(false)
      setFileError(null)
      setReveal(nextReveal)
      if (!opts?.keepMdAnchor) setMdScrollToId(null)
      return true
    } catch (err) {
      if (seq === openSeqRef.current) setFileError(`Could not open ${path}: ${err instanceof Error ? err.message : 'failed'}`)
      return false
    } finally {
      if (seq === openSeqRef.current) { loadingRef.current = false; setFileLoading(false) }
    }
  }

  async function withNavigationAction(action: () => Promise<void>) {
    if (loadingRef.current || navigationActionRef.current) return
    navigationActionRef.current = true
    loadingRef.current = true
    setFileLoading(true)
    try { await action() }
    finally {
      navigationActionRef.current = false
      loadingRef.current = false
      setFileLoading(false)
    }
  }

  async function closeTab(path: string) {
    await withNavigationAction(async () => {
      if (path === activePathRef.current) {
        if (!await saveFile()) return
        if (didChangeTimer.current) clearTimeout(didChangeTimer.current)
        if (autoSaveTimer.current) clearTimeout(autoSaveTimer.current)
        const remaining = tabs.filter((p) => p !== path)
        if (remaining.length) {
          const next = remaining[Math.min(tabs.indexOf(path), remaining.length - 1)]!
          if (!await openFile(next, undefined, undefined, undefined, { pin: true, internal: true })) return
        } else {
          if (path.endsWith('.java')) lspRef.current?.didClose(path)
          activePathRef.current = null
          fileContentRef.current = null
          setActivePath(null); setFileContent(null); setReveal(null)
        }
      }
      setTabs((list) => list.filter((p) => p !== path))
      setPreviewPath((p) => p === path ? null : p)
    })
  }

  // Poll active file mtime/size; reload when changed on disk (other editor / agent).
  useEffect(() => {
    if (!session || !activePath) return
    let cancelled = false
    let inFlight = false
    const tick = async () => {
      if (cancelled || inFlight || document.hidden || loadingRef.current) return
      if (Date.now() < ignoreWatchUntilRef.current) return
      if (dirtyRef.current || savingRef.current) return
      const path = activePathRef.current
      if (!path) return
      inFlight = true
      try {
        const st = await session.client.stat(session.workspaceId, path)
        const fp = diskFpRef.current
        if (fp && fp.mtime === st.modified_at && fp.size === st.size) return
        const text = await session.client.readFile(session.workspaceId, path)
        if (cancelled || loadingRef.current || dirtyRef.current || savingRef.current) return
        if (activePathRef.current !== path) return
        setFileContent(text)
        fileContentRef.current = text
        setContentRevision((n) => n + 1)
        setDirty(false)
        dirtyRef.current = false
        diskFpRef.current = { mtime: st.modified_at, size: st.size }
        const lsp = lspRef.current
        if (lsp && path.endsWith('.java')) {
          void lsp.didChange(path, text)
        }
      } catch {
        /* ignore transient watch errors */
      } finally { inFlight = false }
    }
    const id = window.setInterval(() => void tick(), 2000)
    void tick()
    return () => {
      cancelled = true
      window.clearInterval(id)
    }
  }, [session, activePath])

  async function openFromMarkdown(
    path: string,
    line?: number,
    anchorId?: string,
    dirHint?: boolean,
  ) {
    if (!session) return
    let isDir = !!dirHint
    if (!isDir) {
      try {
        const st = await session.client.stat(session.workspaceId, path)
        isDir = st.type === 'dir'
      } catch {
        // fall through — open as file (not found still surfaces via readFile)
      }
    }
    if (isDir) {
      setRevealDir(path)
      const candidates = ['README.md', 'README_ZH.md', 'readme.md', 'Readme.md']
      for (const name of candidates) {
        const readme = `${path}/${name}`
        try {
          const st = await session.client.stat(session.workspaceId, readme)
          if (st.type === 'file') {
            setMdScrollToId(null)
            void openFile(readme)
            return
          }
        } catch {
          /* try next */
        }
      }
      // Folder only: expand tree, keep current editor (no "not a file" error).
      return
    }
    setRevealDir(null)
    setMdScrollToId(anchorId ?? null)
    void openFile(path, line, undefined, undefined, { keepMdAnchor: !!anchorId })
  }

  function samePlaceQuick(a: NavPlace, b: NavPlace): boolean {
    if (a.path !== b.path) return false
    const ar = a.reveal
    const br = b.reveal
    if (!ar && !br) return true
    if (!ar || !br) return false
    return ar.line === br.line && ar.column === br.column
  }

  async function goNavBack() {
    await withNavigationAction(async () => {
      if (!await saveFile()) return
      const prev = navHistoryRef.current.goBack(currentPlace())
      setNavEpoch((n) => n + 1)
      if (!prev) return
      const opened = await openFile(prev.path, prev.reveal?.line, prev.reveal?.column, prev.reveal?.endColumn, { fromHistory: true, internal: true })
      if (!opened) { navHistoryRef.current.goForward(prev); setNavEpoch((n) => n + 1) }
    })
  }

  async function goNavForward() {
    await withNavigationAction(async () => {
      if (!await saveFile()) return
      const next = navHistoryRef.current.goForward(currentPlace())
      setNavEpoch((n) => n + 1)
      if (!next) return
      const opened = await openFile(next.path, next.reveal?.line, next.reveal?.column, next.reveal?.endColumn, { fromHistory: true, internal: true })
      if (!opened) { navHistoryRef.current.goBack(next); setNavEpoch((n) => n + 1) }
    })
  }

  function navigateTo(target: NavTarget, recordFrom?: NavPlace | null) {
    void openFile(
      target.path,
      target.line + 1,
      target.character + 1,
      Math.max(target.character + 1, target.endCharacter + 1),
      { recordFrom },
    )
  }

	async function closeWorkspace() {
		if (loadingRef.current || navigationActionRef.current) return
		if (sessionRef.current?.embedded) return
    navigationActionRef.current = true
    loadingRef.current = true
    setFileLoading(true)
    try {
      if (!await saveFile()) return
      const current = sessionRef.current
      if (current) {
        try { await current.client.deleteWorkspace(current.workspaceId) }
        catch (err) { setFileError(`Could not close project: ${err instanceof Error ? err.message : 'failed'}`); return }
      }
      ++openSeqRef.current
      ++peekSeqRef.current
      if (didChangeTimer.current) clearTimeout(didChangeTimer.current)
      if (autoSaveTimer.current) clearTimeout(autoSaveTimer.current)
      setTabs([]); setRecent([]); setPreviewPath(null); setQuickMode(null)
      activePathRef.current = null
      fileContentRef.current = null
      sessionRef.current = null
      lspRef.current?.dispose()
      lspRef.current = null
      setLspStatus('off')
      setLspError(null)
      setSession(null)
      setProjectInfo(null)
      setActivePath(null)
      setFileContent(null)
      setContentRevision((n) => n + 1)
      setDirty(false)
      setSaving(false)
      setFileError(null)
      setReveal(null)
      setMdScrollToId(null)
      setRevealDir(null)
      setFindOpen(false)
      setPeekOpen(false)
      navHistoryRef.current.clear()
      setNavEpoch((n) => n + 1)
    } finally {
      navigationActionRef.current = false
      loadingRef.current = false
      setFileLoading(false)
    }
  }

  async function showUsagesPopup(
    req: SymbolClickRequest,
    opts?: { skipSingleJump?: boolean; recordFrom?: NavPlace | null },
  ) {
    const seq = ++peekSeqRef.current
    const lsp = lspRef.current
    if (!session) return
    if (!lsp || lsp.status === 'off' || lsp.status === 'failed') {
      setPeekOpen(true); setPeekSymbol(req.word); setPeekAnchor(req.anchor); setPeekHits([]); setPeekLoading(false)
      setPeekError('Java navigation is unavailable. You can still use Go to File and Find in Files.')
      return
    }
    setPeekMode('usages')
    setPeekOpen(true)
    setPeekSymbol(req.word)
    setPeekAnchor(req.anchor)
    setPeekHits([])
    setPeekError(null)
    setPeekLoading(true)
    setPeekProgress(
      lsp.progress || (lsp.status === 'connecting' ? 'Waiting for jdtls index…' : 'Searching…'),
    )
    const tick = window.setInterval(() => {
      if (seq !== peekSeqRef.current) return
      setPeekProgress(
        lsp.progress || (lsp.status === 'connecting' ? 'Waiting for jdtls index…' : 'Searching…'),
      )
    }, 400)
    try {
      if (fileContent != null && req.path === activePath) {
        await lsp.didOpen(req.path, fileContent, 'java')
      }
      const locs = await lsp.references(req.path, {
        line: req.line,
        character: req.character,
      })
      if (seq !== peekSeqRef.current) return
      setPeekProgress('Loading previews…')
      const hits = await buildUsageHits(session.client, session.workspaceId, locs, {
        path: req.path,
        line: req.line,
        character: req.character,
      })

      if (seq !== peekSeqRef.current) return
      // IDEA: navigateToSingleUsageImmediately — one non-origin hit → jump, no popup
      const others = hits.filter((h) => !h.isOrigin && h.path)
      if (!opts?.skipSingleJump && others.length === 1) {
        const only = others[0]!
        setPeekOpen(false)
        void openFile(
          only.path!,
          only.line,
          only.column,
          only.endColumn,
          { recordFrom: opts?.recordFrom ?? placeFromClick(req.path, req.line, req.character) },
        )
        return
      }

      setPeekHits(hits)
      setPeekProgress(null)
    } catch (err) {
      if (seq !== peekSeqRef.current) return
      setPeekError(err instanceof Error ? err.message : 'references failed')
      setPeekProgress(null)
    } finally {
      window.clearInterval(tick)
      if (seq === peekSeqRef.current) setPeekLoading(false)
    }
  }

  async function showDeclarationsChooser(req: SymbolClickRequest, targets: NavTarget[]) {
    if (!session) return
    const seq = ++peekSeqRef.current
    setPeekMode('declarations')
    setPeekOpen(true)
    setPeekSymbol(req.word)
    setPeekAnchor(req.anchor)
    setPeekError(null)
    setPeekLoading(true)
    setPeekProgress('Resolving declarations…')
    try {
      // Build fake locations for preview enrichment
      const locs = targets.map((t) => ({
        uri: clientDocUri(session.workspaceId, t.path),
        range: {
          start: { line: t.line, character: t.character },
          end: { line: t.endLine, character: t.endCharacter },
        },
      }))
      const hits = await buildUsageHits(session.client, session.workspaceId, locs, {
        path: req.path,
        line: req.line,
        character: req.character,
      })
      if (seq !== peekSeqRef.current) return
      setPeekHits(hits.map((h) => ({ ...h, isOrigin: false })))
      setPeekProgress(null)
    } catch (err) {
      if (seq !== peekSeqRef.current) return
      setPeekError(err instanceof Error ? err.message : 'declarations failed')
      setPeekProgress(null)
    } finally {
      if (seq === peekSeqRef.current) setPeekLoading(false)
    }
  }

  /**
   * IDEA Ctrl+Click → GotoDeclarationOrUsageHandler2:
   * - on declaration → Show Usages
   * - on reference → Go to Declaration (1 target) or chooser (many)
   */
  async function onSymbolClick(req: SymbolClickRequest) {
    const seq = ++peekSeqRef.current
    const lsp = lspRef.current
    setPeekAnchor(req.anchor)
    setPeekSymbol(req.word)
    if (!session || !lsp || lsp.status === 'off' || lsp.status === 'failed') {
      setPeekOpen(true)
      setPeekMode('usages')
      setPeekHits([])
      setPeekLoading(false)
      setPeekError(lspError || 'Java language server unavailable (set WEBIDEA_JDTLS_LAUNCH)')
      return
    }
    setPeekOpen(true)
    setPeekMode('declarations')
    setPeekHits([])
    setPeekLoading(true)
    setPeekError(null)
    setPeekProgress('Resolving declaration…')
    try {
      if (fileContent != null && req.path === activePath) {
        await lsp.didOpen(req.path, fileContent, 'java')
      }
      const defs = await lsp.definition(req.path, {
        line: req.line,
        character: req.character,
      })
      if (seq !== peekSeqRef.current) return
      const decision = decideGtdu(session.workspaceId, req.path, req.line, req.character, defs)

      if (decision.kind === 'nowhere') {
        setPeekOpen(true)
        setPeekMode('declarations')
        setPeekHits([])
        setPeekLoading(false)
        setPeekError('Cannot find declaration to go to')
        return
      }
      const from = placeFromClick(req.path, req.line, req.character)
      if (decision.kind === 'goto') {
        if (decision.targets.length === 1) {
          setPeekOpen(false)
          navigateTo(decision.targets[0]!, from)
          return
        }
        await showDeclarationsChooser(req, decision.targets)
        return
      }
      // showUsages
      await showUsagesPopup(req, { recordFrom: from })
    } catch (err) {
      if (seq !== peekSeqRef.current) return
      setPeekOpen(true)
      setPeekMode('usages')
      setPeekLoading(false)
      setPeekError(err instanceof Error ? err.message : 'navigation failed')
    }
  }

  function lspStatusLabel(): string {
    switch (lspStatus) {
      case 'ready':
        return 'Java ready'
      case 'connecting':
        return 'Java indexing…'
      case 'failed':
        return 'Java unavailable'
      default:
        return session?.lspEnabled ? 'Java on demand' : 'Text navigation'
    }
  }

  if (embeddedWindow && embeddedState !== 'ready') {
    return (
      <main className="ij-embedded-state" role="status" aria-live="polite">
        <div className="ij-embedded-card">
          <strong>{embeddedState === 'loading' ? 'Opening Java workspace…' : 'Could not open Java workspace'}</strong>
          {embeddedError ? <span>{embeddedError}</span> : null}
          {embeddedState === 'error' ? <button type="button" className="ij-primary" onClick={loadEmbeddedWorkspace}>Retry</button> : null}
        </div>
      </main>
    )
  }

  return (
    <div className="ij-app">
      <header className="ij-menubar">
        <span className="ij-brand-mark" aria-hidden="true">ij</span>
        <span className="ij-app-name">web-idea</span>
        <span className="ij-app-purpose">Code reading workspace</span>
        <div className="ij-menubar-spacer" />
        {session ? <>
          <button className="ij-command-search" onClick={() => setQuickMode('files')}><span>Go to File…</span><kbd>⌘⇧O / Ctrl+P</kbd></button>
          {!session.embedded ? <button className="ij-ghost-btn" onClick={() => void closeWorkspace()} disabled={fileLoading || saving}>Close Project</button> : null}
        </> : <span className="ij-toolbar-muted">Java · Source navigation</span>}
      </header>
      <div className="ij-toolbar">
        {session ? <>
          <button className="ij-icon-btn" onClick={() => setProjectVisible((v) => !v)} aria-label="Toggle Project" aria-pressed={projectVisible} title="Project · Alt+1">☷</button>
          <span className="ij-toolbar-divider" />
          <button className="ij-icon-btn" disabled={!navHistoryRef.current.canBack() || fileLoading} onClick={goNavBack} aria-label="Navigate Back" title="Back · ⌘[ / Ctrl+Alt+←">←</button>
          <button className="ij-icon-btn" disabled={!navHistoryRef.current.canForward() || fileLoading} onClick={goNavForward} aria-label="Navigate Forward" title="Forward · ⌘] / Ctrl+Alt+→">→</button>
          <span className="ij-toolbar-label" title={root}>{projectInfo?.root_module?.artifact_id || session.rootLabel}</span>
          <span className="ij-toolbar-divider" />
          <button className="ij-toolbar-action" onClick={() => setQuickMode('recent')} title="Recent Files · ⌘/Ctrl+E">Recent Files</button>
          <button className="ij-toolbar-action" onClick={() => setFindOpen(true)} title="Find in Files · ⌘/Ctrl+Shift+F">Find in Files</button>
          <div className="ij-toolbar-spacer" />
          <div className="ij-reading-controls" aria-label="Reading appearance">
            <button className="ij-icon-btn" aria-label="Decrease font size" disabled={fontSize <= 11} onClick={() => setFontSize((n) => n - 1)}>A−</button>
            <span>{fontSize}</span>
            <button className="ij-icon-btn" aria-label="Increase font size" disabled={fontSize >= 22} onClick={() => setFontSize((n) => n + 1)}>A+</button>
            <button className="ij-toolbar-action" aria-pressed={wordWrap} onClick={() => setWordWrap((v) => !v)}>Soft Wrap</button>
          </div>
        </> : <span className="ij-toolbar-muted">Open a project. Find your way through the code.</span>}
      </div>

      {!session ? (
        <main className="ij-welcome">
          <form className="ij-welcome-card" onSubmit={(e) => void openWorkspace(e)}>
            <div className="ij-welcome-brand">
              <div className="ij-logo" aria-hidden />
              <div>
                <h1>web-idea</h1>
                <p>Familiar navigation. More room for your code.</p>
              </div>
            </div>
            <label>
              Gateway URL
              <input value={gatewayUrl} onChange={(e) => setGatewayUrl(e.target.value)} required />
            </label>
            <label>
              Dev token
              <input
                type="password"
                value={token}
                onChange={(e) => setToken(e.target.value)}
                required
                autoComplete="off"
              />
            </label>
            <label>
              Workspace root
              <input value={root} onChange={(e) => setRoot(e.target.value)} placeholder="Absolute project path on the Gateway host" required />
            </label>
            {formError ? <div className="ij-form-error">{formError}</div> : null}
            <button type="submit" className="ij-primary" disabled={busy}>
              {busy ? 'Opening…' : 'Open'}
            </button>
          </form>
        </main>
      ) : (
        <main className={`ij-workspace${projectVisible ? '' : ' project-hidden'}`}>
          <aside className="ij-toolwindow" hidden={!projectVisible}>
            <div className="ij-toolwindow-header">
              <span>Project</span>
              <button className="ij-icon-btn" title="Locate current file in Project" aria-label="Locate current file" disabled={!activePath} onClick={() => { setRevealDir(activePath?.split('/').slice(0, -1).join('/') ?? ''); setProjectVisible(true) }}>⌖</button>
            </div>
            <FileTree
              key={session.workspaceId}
              client={client}
              workspaceId={session.workspaceId}
              onOpenFile={(p) => void openFile(p)}
              onPinFile={(p) => { setPreviewPath((v) => v === p ? null : v); void openFile(p, undefined, undefined, undefined, { pin: true }) }}
              onQuickOpen={() => setQuickMode('files')}
              activePath={activePath}
              revealDir={revealDir}
              project={projectInfo}
            />
          </aside>
          <section className="ij-editor-host" aria-label="Source editor">
            {tabs.length ? <div className="ij-file-tabs" role="tablist" aria-label="Open files">
              {tabs.map((path) => {
                const name = path.slice(path.lastIndexOf('/') + 1)
                return <div className={`ij-file-tab${path === activePath ? ' active' : ''}${path === previewPath ? ' preview' : ''}`} key={path}>
                  <button role="tab" aria-selected={path === activePath} title={path} onClick={() => void openFile(path)}
                    onDoubleClick={() => setPreviewPath((v) => v === path ? null : v)}
                    onKeyDown={(e) => {
                      if (e.key === 'ArrowRight' || e.key === 'ArrowLeft') {
                        e.preventDefault()
                        const i = tabs.indexOf(path)
                        const next = tabs[(i + (e.key === 'ArrowRight' ? 1 : tabs.length - 1)) % tabs.length]
                        if (next) void openFile(next)
                      }
                    }}>
                    <TreeIcon kind={classifyEntry(name, 'file', { path })} /><span>{name}</span>{path === activePath && dirty ? <span aria-label="Modified">•</span> : null}
                  </button>
                  <button className="ij-tab-close" aria-label={`Close ${name}`} title="Close file · ⌘/Ctrl+W" onClick={() => void closeTab(path)}>×</button>
                </div>
              })}
              {previewPath ? <button className="ij-pin-tab" onClick={() => setPreviewPath(null)} title="Keep the preview tab open. You can also double-click it.">Keep open</button> : null}
            </div> : <div className="ij-empty-tabs">Editor</div>}
            {fileError ? <div className="ij-file-error" role="alert"><span>{fileError}</span>{dirty ? <button className="ij-ghost-btn" onClick={() => void saveFile()}>Retry save</button> : null}<button className="ij-ghost-btn" onClick={() => setFileError(null)} aria-label="Dismiss error">×</button></div> : null}
            {!activePath ? <div className="ij-reading-start">
              <div className="ij-start-symbol" aria-hidden="true">{ }</div>
              <h2>Find the code. Follow the idea.</h2>
              <p>A familiar place to understand what changed and how it works.</p>
              <button onClick={() => setQuickMode('files')}><span>Go to File</span><kbd>⌘⇧O / Ctrl+P</kbd></button>
              <button onClick={() => setQuickMode('recent')}><span>Recent Files</span><kbd>⌘ / Ctrl+E</kbd></button>
              <button onClick={() => setFindOpen(true)}><span>Find in Files</span><kbd>⌘ / Ctrl+Shift+F</kbd></button>
              <small>In Java: ⌘/Ctrl+B to follow a symbol · Alt+F7 to find usages</small>
            </div> : null}
            {activePath ? <Suspense fallback={<div className="ij-editor-empty">Loading editor…</div>}><CodePane
              path={activePath}
              fontSize={fontSize}
              wordWrap={wordWrap}
              focusRequest={focusRequest}
              onCursorChange={(position) => { caretRef.current = position; setCaret(position) }}
              onRevealInTree={(directory) => { setRevealDir(directory); setProjectVisible(true) }}
              onFindUsages={(req) => void showUsagesPopup(req, { skipSingleJump: true })}
              content={fileContent}
              contentRevision={contentRevision}
              error={fileError}
              loading={fileLoading}
              dirty={dirty}
              saving={saving}
              readOnly={session.readOnly}
              editorTheme={editorTheme === 'dark' ? 'idea-dark' : 'idea-light'}
              onChange={session.readOnly ? undefined : (text) => {
                if (loadingRef.current || !activePathRef.current) return
                setPreviewPath((p) => p === activePathRef.current ? null : p)
                setFileContent(text)
                fileContentRef.current = text
                setDirty(true)
                dirtyRef.current = true
                const path = activePathRef.current
                const lsp = lspRef.current
                if (lsp && path?.endsWith('.java')) {
                  if (didChangeTimer.current) clearTimeout(didChangeTimer.current)
                  didChangeTimer.current = setTimeout(() => {
                    void lsp.didChange(path, text)
                  }, 200)
                }
                if (autoSaveTimer.current) clearTimeout(autoSaveTimer.current)
                autoSaveTimer.current = setTimeout(() => {
                  void saveFile()
                }, 800)
              }}
              onSave={session.readOnly ? undefined : () => void saveFile()}
              getLspSession={() => lspRef.current}
              reveal={reveal}
              mdScrollToId={mdScrollToId}
              onOpenFromMarkdown={openFromMarkdown}
              onSymbolClick={(req) => void onSymbolClick(req)}
            /></Suspense> : null}
            {peekOpen ? (
              <ReferencesPeek
                mode={peekMode}
                symbol={peekSymbol}
                hits={peekHits}
                loading={peekLoading}
                loadingMessage={peekProgress}
                error={peekError}
                anchor={peekAnchor}
                onClose={closePeek}
                onOpen={(path, line, column, endColumn) => {
                  setPeekOpen(false)
                  void openFile(path, line, column, endColumn)
                }}
              />
            ) : null}
          </section>
        </main>
      )}

      {session && quickMode ? <QuickOpen client={session.client} workspaceId={session.workspaceId} recent={recent} mode={quickMode} onClose={closeQuick}
        onOpen={(path, line, column) => void openFile(path, line, column, undefined, { pin: true })} /> : null}
      {session ? (
        <FindInFiles
          open={findOpen}
          client={session.client}
          workspaceId={session.workspaceId}
          onClose={() => setFindOpen(false)}
          onOpenHit={(path, line, column) => void openFile(path, line, column, undefined, { pin: true })}
        />
      ) : null}

      <footer className="ij-statusbar">
        <span className="ij-status-path" title={activePath ?? session?.rootLabel}>{activePath ?? (session ? session.rootLabel : 'No project open')}</span>
        <span className="ij-statusbar-spacer" />
        {session ? <><span className={`ij-language-status ${lspStatus}`} title={lspError ?? undefined}>{lspStatusLabel()}</span>
          <span>{session.readOnly ? 'Read only' : saving ? 'Saving…' : dirty ? 'Modified' : 'All changes saved'}</span>
          {activePath ? <span className="ij-caret">Ln {caret.line}, Col {caret.column}</span> : null}</> : null}
        <span>UTF-8</span><span>LF</span>
      </footer>
    </div>
  )
}
