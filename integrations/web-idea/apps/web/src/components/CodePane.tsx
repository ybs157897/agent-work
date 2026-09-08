import Editor, { loader, type OnMount } from '@monaco-editor/react'
import { useEffect, useRef, useState } from 'react'
import type { editor, IDisposable } from 'monaco-editor'
import * as localMonaco from 'monaco-editor/esm/vs/editor/editor.api'
import EditorWorker from 'monaco-editor/esm/vs/editor/editor.worker?worker'
import 'monaco-editor/esm/vs/editor/contrib/find/browser/findController'
import 'monaco-editor/esm/vs/editor/contrib/suggest/browser/suggestController'
import 'monaco-editor/esm/vs/editor/contrib/folding/browser/folding'
import 'monaco-editor/esm/vs/editor/contrib/contextmenu/browser/contextmenu'
import 'monaco-editor/esm/vs/editor/standalone/browser/quickAccess/standaloneGotoLineQuickAccess'
import 'monaco-editor/esm/vs/editor/standalone/browser/quickAccess/standaloneGotoLineQuickAccess'
import 'monaco-editor/esm/vs/basic-languages/css/css.contribution'
import 'monaco-editor/esm/vs/basic-languages/dockerfile/dockerfile.contribution'
import 'monaco-editor/esm/vs/basic-languages/html/html.contribution'
import 'monaco-editor/esm/vs/basic-languages/ini/ini.contribution'
import 'monaco-editor/esm/vs/basic-languages/java/java.contribution'
import 'monaco-editor/esm/vs/basic-languages/javascript/javascript.contribution'
import 'monaco-editor/esm/vs/basic-languages/kotlin/kotlin.contribution'
import 'monaco-editor/esm/vs/basic-languages/markdown/markdown.contribution'
import 'monaco-editor/esm/vs/basic-languages/shell/shell.contribution'
import 'monaco-editor/esm/vs/basic-languages/sql/sql.contribution'
import 'monaco-editor/esm/vs/basic-languages/typescript/typescript.contribution'
import 'monaco-editor/esm/vs/basic-languages/xml/xml.contribution'
import 'monaco-editor/esm/vs/basic-languages/yaml/yaml.contribution'
import { isMarkdownPath } from '../markdown/render'
import { applyJavaIdeaDecorations, ensureJavaDecorationStyles } from '../theme/javaDecorations'
import { languageForPath, registerIdeaEditor } from '../theme/registerLanguages'
import { registerJavaLspCompletion } from '../lsp/monacoCompletion'
import type { JavaLspSession } from '../lsp/session'
import { MarkdownPreview } from './MarkdownPreview'

// Keep the editor on the local ESM runtime. A single general editor worker is
// enough for syntax highlighting and editor services; the TypeScript and JSON
// language workers are intentionally not imported for this browsing surface.
loader.config({ monaco: localMonaco })
if (typeof window !== 'undefined') {
  window.MonacoEnvironment = {
    getWorker: () => new EditorWorker(),
  }
}

/** Cmd/Ctrl+click payload (IDEA CtrlMouseHandler → Goto Declaration or Usages). */
export type SymbolClickRequest = {
  path: string
  line: number
  character: number
  word: string
  anchor: { x: number; y: number }
}

/** Reveal + select a range in the editor (1-based line/column for Monaco). */
export type RevealTarget = {
  line: number
  column: number
  endLine?: number
  endColumn?: number
}

export type CodePaneProps = {
  path: string | null
  content: string | null
  /** Bumps when `content` should replace the Monaco buffer (open / external reload). */
  contentRevision: number
  error: string | null
  loading: boolean
  reveal?: RevealTarget | null
  /** Pending heading id from markdown link navigation. */
  mdScrollToId?: string | null
  /** Open workspace path from markdown links. */
  onOpenFromMarkdown?: (path: string, line?: number, anchorId?: string, dirHint?: boolean) => void
  /** Cmd/Ctrl+click → GTDU (declaration or usages). */
  onSymbolClick?: (req: SymbolClickRequest) => void
  /** Alt+F7 / editor Find Usages action. */
  onFindUsages?: (req: SymbolClickRequest) => void
  /** Live buffer edits (dirty). */
  onChange?: (text: string) => void
  /** Persist buffer (Cmd/Ctrl+S). */
  onSave?: () => void
  /** Notify the host of the active caret position (1-based). */
  onCursorChange?: (position: { line: number; column: number }) => void
  /** Reveal a directory in the Project tool window. */
  onRevealInTree?: (directory: string) => void
  /** Editor typography and wrapping controls. */
  fontSize?: number
  wordWrap?: boolean
  /** Increment to focus the editor after an external navigation action. */
  focusRequest?: number
  dirty?: boolean
  saving?: boolean
  /** jdtls session for member completion (`obj.`). */
  getLspSession?: () => JavaLspSession | null
  /** Embedded workspaces keep the reader strictly read-only. */
  readOnly?: boolean
  /** Monaco theme selected by the host workbench. */
  editorTheme?: 'idea-light' | 'idea-dark'
}

type MdMode = 'preview' | 'source'
type MonacoEditor = Parameters<OnMount>[0]
type MonacoViewState = NonNullable<ReturnType<MonacoEditor['saveViewState']>>
type MonacoPosition = { lineNumber: number; column: number }

const MAX_VIEW_STATES = 30

function fileName(path: string): string {
  const i = path.lastIndexOf('/')
  return i >= 0 ? path.slice(i + 1) : path
}

let ideaReady: Promise<void> | null = null

function ensureIdea(): Promise<void> {
  if (!ideaReady) {
    ideaReady = loader.init().then((monaco) => {
      registerIdeaEditor(monaco)
      ensureJavaDecorationStyles()
    })
  }
  return ideaReady
}

function applyReveal(editorInstance: MonacoEditor, reveal: RevealTarget): void {
  const model = editorInstance.getModel()
  if (!model) return

  const line = Math.min(Math.max(1, reveal.line), model.getLineCount())
  const column = Math.min(Math.max(1, reveal.column), model.getLineMaxColumn(line))
  const endLine = Math.min(Math.max(line, reveal.endLine ?? line), model.getLineCount())
  const endColumn = Math.min(
    Math.max(column, reveal.endColumn ?? column),
    model.getLineMaxColumn(endLine),
  )
  editorInstance.revealPositionInCenter({ lineNumber: line, column })
  editorInstance.setPosition({ lineNumber: line, column })
  editorInstance.setSelection({
    startLineNumber: line,
    startColumn: column,
    endLineNumber: endLine,
    endColumn,
  })
  editorInstance.focus()
}

function anchorForPosition(editorInstance: MonacoEditor, position: MonacoPosition): { x: number; y: number } {
  const visible = editorInstance.getScrolledVisiblePosition(position)
  const dom = editorInstance.getDomNode()
  if (visible && dom) {
    const rect = dom.getBoundingClientRect()
    return {
      x: rect.left + visible.left,
      y: rect.top + visible.top + visible.height,
    }
  }
  return { x: 0, y: 0 }
}

function breadcrumbParts(path: string): Array<{ name: string; directory: string; isFile: boolean }> {
  const parts = path.split('/').filter(Boolean)
  let directory = ''
  return parts.map((name, index) => {
    directory = directory ? `${directory}/${name}` : name
    return { name, directory, isFile: index === parts.length - 1 }
  })
}

function Breadcrumbs({
  path,
  onRevealInTree,
  onGoToLine,
}: {
  path: string
  onRevealInTree?: (directory: string) => void
  onGoToLine?: () => void
}) {
  return (
    <div className="ij-breadcrumb" aria-label="Path" style={{ display: 'flex', alignItems: 'center' }}>
      <span style={{ minWidth: 0, overflow: 'hidden', textOverflow: 'ellipsis' }}>
        {breadcrumbParts(path).map((part, index) => (
          <span key={part.directory}>
            {index > 0 ? ' ▸ ' : null}
            {part.isFile || !onRevealInTree ? (
              <span aria-current={part.isFile ? 'page' : undefined}>{part.name}</span>
            ) : (
              <button
                type="button"
                title={`Reveal ${part.directory}`}
                onClick={() => onRevealInTree(part.directory)}
                style={{
                  padding: 0,
                  border: 0,
                  background: 'transparent',
                  color: 'inherit',
                  font: 'inherit',
                  cursor: 'pointer',
                }}
              >
                {part.name}
              </button>
            )}
          </span>
        ))}
      </span>
      {onGoToLine ? (
        <button
          type="button"
          title="Go to Line (Cmd/Ctrl+L)"
          aria-keyshortcuts="Meta+L Control+L"
          onClick={onGoToLine}
          className="ij-goto-line"

        >
          Go to Line
        </button>
      ) : null}
    </div>
  )
}

export function CodePane({
  path,
  content,
  contentRevision,
  error,
  loading,
  reveal,
  mdScrollToId,
  onOpenFromMarkdown,
  onSymbolClick,
  onFindUsages,
  onChange,
  onSave,
  onCursorChange,
  onRevealInTree,
  fontSize,
  wordWrap,
  focusRequest,
  saving,
  getLspSession,
  readOnly = false,
  editorTheme = 'idea-light',
}: CodePaneProps) {
  const editorRef = useRef<MonacoEditor | null>(null)
  const monacoRef = useRef<typeof import('monaco-editor') | null>(null)
  const modelRef = useRef<editor.ITextModel | null>(null)
  const decorationRef = useRef<IDisposable | null>(null)
  const completionDisposeRef = useRef<IDisposable | null>(null)
  const editorDisposablesRef = useRef<IDisposable[]>([])
  const viewStatesRef = useRef(new Map<string, MonacoViewState>())
  const modelPathRef = useRef<string | null>(null)
  const loadedPathRef = useRef<string | null>(null)
  const syncedRevisionRef = useRef(-1)
  const renderedPathRef = useRef(path)
  const effectPathRef = useRef(path)
  const awaitingFreshContentRef = useRef(false)
  const syncingRef = useRef(false)
  const suppressOnChangeRef = useRef(false)
  const contextPositionRef = useRef<MonacoPosition | null>(null)
  const goToLineRef = useRef<(() => void) | null>(null)
  const appliedRevealKeyRef = useRef<string | null>(null)
  const pendingFocusRef = useRef(false)
  const focusRequestRef = useRef<number | undefined>(undefined)

  const contentRef = useRef(content)
  contentRef.current = content
  const contentRevisionRef = useRef(contentRevision)
  contentRevisionRef.current = contentRevision
  const loadingRef = useRef(loading)
  loadingRef.current = loading
  const pathRef = useRef(path)
  pathRef.current = path
  const revealRef = useRef(reveal)
  revealRef.current = reveal
  const getLspRef = useRef(getLspSession)
  getLspRef.current = getLspSession
  const onChangeRef = useRef(onChange)
  onChangeRef.current = onChange
  const onSaveRef = useRef(onSave)
  onSaveRef.current = onSave
  const clickRef = useRef(onSymbolClick)
  clickRef.current = onSymbolClick
  const findUsagesRef = useRef(onFindUsages)
  findUsagesRef.current = onFindUsages
  const cursorRef = useRef(onCursorChange)
  cursorRef.current = onCursorChange

  // Mark a path switch during render so an onMount callback cannot mistake the
  // previous file's still visible content for the newly selected file.
  if (renderedPathRef.current !== path) {
    renderedPathRef.current = path
    awaitingFreshContentRef.current = path != null
    appliedRevealKeyRef.current = null
  }

  const [mdMode, setMdMode] = useState<MdMode>('preview')
  const isMd = isMarkdownPath(path)
  const showPreview = isMd && mdMode === 'preview'
  const mdModeRef = useRef(mdMode)
  mdModeRef.current = mdMode

  function storeViewState(targetPath: string, state: MonacoViewState): void {
    const states = viewStatesRef.current
    states.delete(targetPath)
    states.set(targetPath, state)
    while (states.size > MAX_VIEW_STATES) {
      const oldest = states.keys().next().value as string | undefined
      if (!oldest) break
      states.delete(oldest)
    }
  }

  function rememberViewState(targetPath = modelPathRef.current): void {
    const editorInstance = editorRef.current
    if (!editorInstance || !targetPath || syncingRef.current) return
    const state = editorInstance.saveViewState()
    if (!state) return
    storeViewState(targetPath, state)
  }

  function bindJavaDecorations(
    editorInstance: MonacoEditor,
    monaco: typeof import('monaco-editor'),
    targetPath: string,
  ): void {
    decorationRef.current?.dispose()
    decorationRef.current = null
    const model = editorInstance.getModel()
    if (model && languageForPath(targetPath) === 'java') {
      decorationRef.current = applyJavaIdeaDecorations(monaco, editorInstance, model)
    }
  }

  function emitCursorChange(editorInstance: MonacoEditor): void {
    const position = editorInstance.getPosition()
    if (position) cursorRef.current?.({ line: position.lineNumber, column: position.column })
  }

  function requestAtPosition(
    editorInstance: MonacoEditor,
    position: MonacoPosition | null,
    fallbackAnchor?: { x: number; y: number },
  ): SymbolClickRequest | null {
    const targetPath = pathRef.current
    const model = editorInstance.getModel()
    if (
      !targetPath ||
      !model ||
      !position ||
      loadingRef.current ||
      loadedPathRef.current !== targetPath ||
      syncedRevisionRef.current !== contentRevisionRef.current ||
      languageForPath(targetPath) !== 'java'
    ) {
      return null
    }
    const word = model.getWordAtPosition(position)
    return {
      path: targetPath,
      line: position.lineNumber - 1,
      character: position.column - 1,
      word: word?.word ?? '',
      anchor: fallbackAnchor ?? anchorForPosition(editorInstance, position),
    }
  }

  function dispatchSymbolClick(
    editorInstance: MonacoEditor,
    position: MonacoPosition | null,
    fallbackAnchor?: { x: number; y: number },
  ): void {
    const request = requestAtPosition(editorInstance, position, fallbackAnchor)
    if (request) clickRef.current?.(request)
  }

  function dispatchFindUsages(editorInstance: MonacoEditor, position: MonacoPosition | null): void {
    const request = requestAtPosition(editorInstance, position)
    if (!request) return
    const callback = findUsagesRef.current ?? clickRef.current
    callback?.(request)
  }

  function focusWhenReady(): void {
    const editorInstance = editorRef.current
    const targetPath = pathRef.current
    if (
      !editorInstance ||
      !targetPath ||
      contentRef.current == null ||
      loadedPathRef.current !== targetPath ||
      syncedRevisionRef.current !== contentRevisionRef.current ||
      showPreview
    ) {
      return
    }
    pendingFocusRef.current = false
    requestAnimationFrame(() => {
      if (
        pathRef.current === targetPath &&
        loadedPathRef.current === targetPath &&
        syncedRevisionRef.current === contentRevisionRef.current
      ) {
        editorRef.current?.focus()
      }
    })
  }

  function revealWhenReady(editorInstance: MonacoEditor): void {
    const targetPath = pathRef.current
    const targetReveal = revealRef.current
    const targetRevision = contentRevisionRef.current
    if (
      !targetPath ||
      !targetReveal ||
      targetReveal.line < 1 ||
      contentRef.current == null ||
      loadingRef.current ||
      loadedPathRef.current !== targetPath ||
      syncedRevisionRef.current !== targetRevision ||
      (isMarkdownPath(targetPath) && mdModeRef.current === 'preview')
    ) {
      return
    }
    const key = `${targetPath}:${targetRevision}:${targetReveal.line}:${targetReveal.column}:${targetReveal.endLine ?? ''}:${targetReveal.endColumn ?? ''}`
    if (appliedRevealKeyRef.current === key) return
    appliedRevealKeyRef.current = key
    requestAnimationFrame(() => {
      if (
        pathRef.current === targetPath &&
        contentRef.current != null &&
        loadedPathRef.current === targetPath &&
        syncedRevisionRef.current === targetRevision
      ) {
        applyReveal(editorInstance, targetReveal)
      }
    })
  }

  function synchronizeModel(): void {
    const editorInstance = editorRef.current
    const monaco = monacoRef.current
    const targetPath = pathRef.current
    const targetContent = contentRef.current
    const targetRevision = contentRevisionRef.current
    if (!editorInstance || !monaco || !targetPath) return

    const model = editorInstance.getModel()
    if (!model) return
    modelRef.current = model

    const switchingPath = modelPathRef.current !== targetPath
    // App keeps the old buffer mounted while readFile is in flight. Leave it
    // alone until loading ends so a stale document is never attached to the
    // newly selected path and no autosave can be scheduled from setValue.
    if (switchingPath && awaitingFreshContentRef.current && loadingRef.current) return

    const nextValue = targetContent ?? ''
    const valueChanged = model.getValue() !== nextValue
    const revisionChanged = syncedRevisionRef.current !== targetRevision
    if (!switchingPath && !valueChanged && !revisionChanged) {
      if (pendingFocusRef.current) focusWhenReady()
      return
    }
    const previousState = switchingPath ? null : editorInstance.saveViewState()
    if (previousState && modelPathRef.current) {
      storeViewState(modelPathRef.current, previousState)
    }

    syncingRef.current = true
    try {
      suppressOnChangeRef.current = true
      if (model.getValue() !== nextValue) editorInstance.setValue(nextValue)
      suppressOnChangeRef.current = false

      monaco.editor.setModelLanguage(model, languageForPath(targetPath))
      modelPathRef.current = targetPath
      loadedPathRef.current = targetContent == null ? null : targetPath
      syncedRevisionRef.current = targetRevision
      awaitingFreshContentRef.current = false

      const savedState = viewStatesRef.current.get(targetPath)
      if (savedState && targetContent != null) {
        editorInstance.restoreViewState(savedState)
        storeViewState(targetPath, savedState)
      } else if (switchingPath && targetContent != null) {
        editorInstance.setPosition({ lineNumber: 1, column: 1 })
        editorInstance.setScrollPosition({ scrollTop: 0, scrollLeft: 0 })
      }

      bindJavaDecorations(editorInstance, monaco, targetPath)
    } finally {
      suppressOnChangeRef.current = false
      syncingRef.current = false
    }

    if (targetContent != null) {
      emitCursorChange(editorInstance)
      revealWhenReady(editorInstance)
    }
    if (pendingFocusRef.current) focusWhenReady()
  }

  useEffect(() => {
    if (effectPathRef.current === path) return
    const previousPath = effectPathRef.current
    if (previousPath && modelPathRef.current === previousPath) rememberViewState(previousPath)
    effectPathRef.current = path
  }, [path])

  useEffect(() => {
    if (!isMarkdownPath(path)) return
    // Line deep-link (#L12) → source; heading link → preview.
    if (reveal && reveal.line > 0) setMdMode('source')
    else setMdMode('preview')
  }, [path, reveal])

  useEffect(() => {
    if (!path) return
    void ensureIdea()
  }, [path])

  useEffect(() => {
    return () => {
      decorationRef.current?.dispose()
      decorationRef.current = null
      completionDisposeRef.current?.dispose()
      completionDisposeRef.current = null
      for (const disposable of editorDisposablesRef.current) disposable.dispose()
      editorDisposablesRef.current = []
      editorRef.current = null
      modelRef.current = null
      goToLineRef.current = null
    }
  }, [])

  useEffect(() => {
    if (path !== null) return
    decorationRef.current?.dispose()
    decorationRef.current = null
    completionDisposeRef.current?.dispose()
    completionDisposeRef.current = null
    for (const disposable of editorDisposablesRef.current) disposable.dispose()
    editorDisposablesRef.current = []
    editorRef.current = null
    modelRef.current = null
    goToLineRef.current = null
    modelPathRef.current = null
    loadedPathRef.current = null
    syncedRevisionRef.current = -1
    awaitingFreshContentRef.current = false
    viewStatesRef.current.clear()
  }, [path])

  useEffect(() => {
    synchronizeModel()
  }, [path, content, contentRevision, loading])

  useEffect(() => {
    const editorInstance = editorRef.current
    if (
      !editorInstance ||
      !path ||
      content == null ||
      loading ||
      loadedPathRef.current !== path ||
      syncedRevisionRef.current !== contentRevision
    ) {
      return
    }
    if (!reveal || reveal.line < 1 || (isMarkdownPath(path) && mdMode === 'preview')) {
      appliedRevealKeyRef.current = null
      return
    }
    revealWhenReady(editorInstance)
  }, [path, content, contentRevision, loading, reveal, mdMode])

  useEffect(() => {
    if (focusRequest == null || focusRequestRef.current === focusRequest) return
    focusRequestRef.current = focusRequest
    pendingFocusRef.current = true
    focusWhenReady()
  }, [focusRequest, path, contentRevision, mdMode])

  useEffect(() => {
    if (!path || showPreview) return
    const frame = requestAnimationFrame(() => editorRef.current?.layout())
    return () => cancelAnimationFrame(frame)
  }, [path, showPreview])

  const onMount: OnMount = (editorInstance, monaco) => {
    editorRef.current = editorInstance
    monacoRef.current = monaco
    modelRef.current = editorInstance.getModel()
    registerIdeaEditor(monaco)
    ensureJavaDecorationStyles()

    if (!completionDisposeRef.current) {
      completionDisposeRef.current = registerJavaLspCompletion(
        monaco,
        () => getLspRef.current?.() ?? null,
        () => pathRef.current,
        () => loadingRef.current ? null : modelRef.current,
      )
    }

    for (const disposable of editorDisposablesRef.current) disposable.dispose()
    editorDisposablesRef.current = []

    editorDisposablesRef.current.push(
      editorInstance.onDidChangeCursorPosition(() => {
        if (syncingRef.current) return
        emitCursorChange(editorInstance)
        rememberViewState()
      }),
      editorInstance.onDidScrollChange(() => {
        if (!syncingRef.current) rememberViewState()
      }),
      editorInstance.onContextMenu((event) => {
        contextPositionRef.current = event.target.position ?? null
      }),
      editorInstance.onMouseDown((event) => {
        if (!(event.event.metaKey || event.event.ctrlKey) || !event.target.position) return
        const callback = clickRef.current
        if (!callback) return
        event.event.preventDefault()
        event.event.stopPropagation()
        dispatchSymbolClick(editorInstance, event.target.position, {
          x: event.event.posx,
          y: event.event.posy + 16,
        })
      }),
      editorInstance.addAction({
        id: 'idea.goToDeclarationOrUsages',
        label: 'Go to Declaration or Usages',
        precondition: 'editorLangId == java',
        contextMenuGroupId: 'navigation',
        contextMenuOrder: 1,
        run: () =>
          dispatchSymbolClick(editorInstance, contextPositionRef.current ?? editorInstance.getPosition()),
      }),
      editorInstance.addAction({
        id: 'idea.findUsages',
        label: 'Find Usages',
        precondition: 'editorLangId == java',
        contextMenuGroupId: 'navigation',
        contextMenuOrder: 2,
        run: () =>
          dispatchFindUsages(editorInstance, contextPositionRef.current ?? editorInstance.getPosition()),
      }),
    )

    editorInstance.addCommand(monaco.KeyMod.CtrlCmd | monaco.KeyCode.KeyS, () => {
      onSaveRef.current?.()
    })
    editorInstance.addCommand(monaco.KeyMod.CtrlCmd | monaco.KeyCode.KeyB, () => {
      dispatchSymbolClick(editorInstance, editorInstance.getPosition())
    })
    editorInstance.addCommand(monaco.KeyCode.F12, () => {
      dispatchSymbolClick(editorInstance, editorInstance.getPosition())
    })
    editorInstance.addCommand(monaco.KeyMod.Alt | monaco.KeyCode.F7, () => {
      dispatchFindUsages(editorInstance, editorInstance.getPosition())
    })
    editorInstance.addCommand(monaco.KeyMod.CtrlCmd | monaco.KeyCode.KeyL, () => {
      editorInstance.trigger('keyboard', 'editor.action.gotoLine', null)
    })
    goToLineRef.current = () => {
      setMdMode('source')
      requestAnimationFrame(() => {
        editorInstance.layout()
        editorInstance.focus()
        editorInstance.trigger('toolbar', 'editor.action.gotoLine', null)
      })
    }

    synchronizeModel()
    emitCursorChange(editorInstance)
    revealWhenReady(editorInstance)
  }

  if (!path) return null

  const stale = loading && content != null
  const showLoading = Boolean(path && loading && content == null && !error)
  const showError = Boolean(path && error && content == null)
  const effectiveFontSize = Math.max(10, fontSize ?? 14)
  const effectiveWordWrap = wordWrap ?? isMd

  return (
    <div className={`ij-editor${stale ? ' loading' : ''}`}>
      {path && isMd ? (
        <div className="ij-tabs">
          <div className="ij-md-modes" role="tablist" aria-label="Markdown view">
            <button
              type="button"
              role="tab"
              aria-selected={mdMode === 'preview'}
              className={`ij-md-mode${mdMode === 'preview' ? ' active' : ''}`}
              onClick={() => setMdMode('preview')}
            >
              Preview
            </button>
            <button
              type="button"
              role="tab"
              aria-selected={mdMode === 'source'}
              className={`ij-md-mode${mdMode === 'source' ? ' active' : ''}`}
              onClick={() => setMdMode('source')}
            >
              Source
            </button>
          </div>
        </div>
      ) : null}
      {path ? (
        <Breadcrumbs
          path={path}
          onRevealInTree={onRevealInTree}
          onGoToLine={() => goToLineRef.current?.()}
        />
      ) : null}
      <div className="ij-monaco">
        {showLoading ? (
          <div className="ij-editor-empty" style={{ height: '100%' }}>
            Loading {fileName(path)}…
          </div>
        ) : showError ? (
          <div className="ij-editor-empty error" style={{ height: '100%' }}>
            <div className="ij-empty-title">{fileName(path)}</div>
            <div>{error}</div>
          </div>
        ) : null}
        {stale ? <div className="ij-editor-busy" aria-live="polite">Loading…</div> : null}
        {saving ? <div className="ij-editor-busy" aria-live="polite">Saving…</div> : null}
        {showPreview ? (
          <MarkdownPreview
            path={path ?? ''}
            source={content ?? ''}
            scrollToId={mdScrollToId}
            onNavigate={(request) =>
              onOpenFromMarkdown?.(request.path, request.line, request.anchorId, request.dirHint)
            }
          />
        ) : null}
        {/* Keep Monaco mounted while previewing so Source switch stays cheap. */}
        <div
          className="ij-monaco-host"
          hidden={showPreview || !path}
          aria-hidden={showPreview || !path}
        >
          <Editor
            height="100%"
            defaultValue={content ?? ''}
            language={languageForPath(path)}
            theme={editorTheme}
            saveViewState={false}
            beforeMount={(monaco) => {
              registerIdeaEditor(monaco)
            }}
            onMount={onMount}
            onChange={(value) => {
              if (!suppressOnChangeRef.current && value != null) onChangeRef.current?.(value)
            }}
            options={{
              readOnly: readOnly || loading || !path || showError,
              minimap: { enabled: false },
              fontSize: effectiveFontSize,
              fontFamily: "'JetBrains Mono', 'SF Mono', Menlo, Consolas, monospace",
              lineHeight: 22,
              lineNumbers: 'on',
              guides: { indentation: true, bracketPairs: true },
              folding: true,
              showFoldingControls: 'mouseover',
              tabSize: 4,
              insertSpaces: true,
              detectIndentation: false,
              scrollBeyondLastLine: false,
              wordWrap: effectiveWordWrap ? 'on' : 'off',
              automaticLayout: true,
              renderLineHighlight: 'all',
              renderWhitespace: 'none',
              padding: { top: 6, bottom: 6 },
              bracketPairColorization: { enabled: true },
              matchBrackets: 'always',
              scrollbar: {
                verticalScrollbarSize: 10,
                horizontalScrollbarSize: 10,
              },
            }}
          />
        </div>
      </div>
    </div>
  )
}
