import { JsonRpcClient } from './jsonrpc'
import { javaLspSettings } from './settings'

export type LspLocation = {
  uri: string
  range: {
    start: { line: number; character: number }
    end: { line: number; character: number }
  }
}

export type LspPosition = { line: number; character: number }

// Go's url.PathEscape (used by the Gateway) follows the URL path segment
// rules rather than JavaScript's encodeURIComponent allowlist.
function pathEscape(value: string): string {
  return encodeURIComponent(value)
    .replace(/[!'()*]/g, (character) => `%${character.charCodeAt(0).toString(16).toUpperCase()}`)
    .replace(/%24/gi, '$')
    .replace(/%26/gi, '&')
    .replace(/%2B/gi, '+')
    .replace(/%3A/gi, ':')
    .replace(/%3D/gi, '=')
    .replace(/%40/gi, '@')
}

export function clientDocUri(workspaceId: string, relPath: string): string {
  const clean = relPath.replace(/^\/+/, '')
  if (!clean) return clientRootUri(workspaceId)
  const escaped = clean.split('/').map(pathEscape).join('/')
  return `webidea://ws/${pathEscape(workspaceId)}/${escaped}`
}

export function clientRootUri(workspaceId: string): string {
  return `webidea://ws/${pathEscape(workspaceId)}`
}

export function pathFromClientUri(workspaceId: string, uri: string): string | null {
  const root = `webidea://ws/${pathEscape(workspaceId)}`
  if (uri === root || uri === `${root}/`) return ''
  const prefix = `${root}/`
  if (!uri.startsWith(prefix)) return null
  try {
    return uri
      .slice(prefix.length)
      .split('/')
      .map((segment) => decodeURIComponent(segment))
      .join('/')
  } catch {
    return null
  }
}

function lspWsUrl(httpBase: string, workspaceId: string, token: string): string {
	const u = new URL(httpBase.replace(/\/$/, '') || window.location.origin, window.location.href)
	u.protocol = u.protocol === 'https:' ? 'wss:' : 'ws:'
	const basePath = u.pathname.replace(/\/+$/, '')
	u.pathname = `${basePath}/api/v1/workspaces/${encodeURIComponent(workspaceId)}/lsp`
	u.search = token ? `token=${encodeURIComponent(token)}` : ''
	u.hash = ''
	return u.toString()
}

export type LspStatus = 'off' | 'connecting' | 'ready' | 'failed'

type ReadyWaiter = {
  generation: number
  timer: ReturnType<typeof setTimeout>
  resolve: () => void
  reject: (error: Error) => void
}

export class JavaLspSession {
  private rpc: JsonRpcClient | null = null
  /** LSP initialize finished; can didOpen. */
  private started = false
  /** jdtls ServiceReady — safe for references. */
  private serviceReady = false
  private opened = new Set<string>()
  private versions = new Map<string, number>()
  private openedTexts = new Map<string, string>()
  private readyWaiters: ReadyWaiter[] = []
  /** Invalidates stale start/connect/notification continuations. */
  private generation = 0
  /** Invalidates document operations crossing dispose(). */
  private documentEpoch = 0
  private startPromise: Promise<void> | null = null
  private cancelStart: ((error: Error) => void) | null = null
  status: LspStatus = 'off'
  lastError: string | null = null
  /** Latest indexing / startup message from jdtls. */
  progress: string | null = null
  onStatus?: (s: LspStatus, err?: string | null) => void

  constructor(
    private readonly gatewayUrl: string,
    private readonly token: string,
    private readonly workspaceId: string,
    private readonly readOnly = false,
  ) {}

  private setStatus(s: LspStatus, err: string | null = null) {
    this.status = s
    this.lastError = err
    this.onStatus?.(s, err)
  }

  private setProgress(msg: string) {
    this.progress = msg
    if (this.status === 'connecting') {
      this.setStatus('connecting', msg)
    }
  }

  private isCurrent(generation: number, rpc: JsonRpcClient): boolean {
    return this.generation === generation && this.rpc === rpc
  }

  private settleReadyWaiters(error?: Error) {
    const waiters = this.readyWaiters
    this.readyWaiters = []
    for (const waiter of waiters) {
      clearTimeout(waiter.timer)
      if (error) waiter.reject(error)
      else waiter.resolve()
    }
  }

  private markServiceReady(generation: number, rpc: JsonRpcClient) {
    if (!this.isCurrent(generation, rpc)) return
    this.serviceReady = true
    this.progress = null
    this.setStatus('ready')
    this.settleReadyWaiters()
  }

  /** Wait until jdtls reports ServiceReady (or timeout). */
  waitUntilReady(timeoutMs = 180_000): Promise<void> {
    if (this.serviceReady) return Promise.resolve()
    if (this.status === 'failed' || (!this.rpc && !this.startPromise)) {
      return Promise.reject(new Error(this.lastError || 'LSP not available'))
    }
    const generation = this.generation
    return new Promise((resolve, reject) => {
      const waiter: ReadyWaiter = {
        generation,
        timer: setTimeout(() => {
          const index = this.readyWaiters.indexOf(waiter)
          if (index >= 0) this.readyWaiters.splice(index, 1)
          reject(
            new Error(
              this.progress
                ? `jdtls still indexing: ${this.progress}`
                : 'jdtls not ready (still indexing / importing)',
            ),
          )
        }, timeoutMs),
        resolve,
        reject,
      }
      this.readyWaiters.push(waiter)
    })
  }

  private async ensureStarted(): Promise<void> {
    if (this.started && this.rpc) return
    if (this.startPromise) {
      await this.startPromise
      return
    }
    if (this.status === 'failed') {
      throw new Error(this.lastError || 'LSP not available')
    }
    await this.start()
  }

  /** Start lazily on first Java operation; repeated callers share one start. */
  start(): Promise<void> {
    if (this.startPromise) return this.startPromise
    if (this.started && this.rpc) return Promise.resolve()

    const generation = ++this.generation
    this.opened.clear()
    this.versions.clear()
    this.openedTexts.clear()
    this.serviceReady = false
    this.started = false
    this.progress = null
    this.setStatus('connecting', 'Connecting…')

    const rpc = new JsonRpcClient(lspWsUrl(this.gatewayUrl, this.workspaceId, this.token))
    this.rpc = rpc

    let rejectCancelled: (error: Error) => void = () => {}
    const cancelled = new Promise<never>((_, reject) => {
      rejectCancelled = reject
    })
    this.cancelStart = rejectCancelled

    const work = (async () => {
      try {
        await rpc.connect()
        if (!this.isCurrent(generation, rpc)) throw new Error('LSP session cancelled')
        // jdtls progress (method name is language/status)
        rpc.onNotification('language/status', (params) => {
          if (!this.isCurrent(generation, rpc)) return
          const p = params as { type?: string; message?: string } | null
          const msg = String(p?.message ?? '')
          const typ = String(p?.type ?? '')
          if (/ServiceReady/i.test(msg) || /ServiceReady/i.test(typ) || typ === 'ServiceReady') {
            this.markServiceReady(generation, rpc)
            return
          }
          // "Ready" alone can fire before classpath is usable; keep waiting for ServiceReady
          if (msg) this.setProgress(msg)
        })
        rpc.onNotification('window/logMessage', () => {})

        const root = clientRootUri(this.workspaceId)
        await rpc.request(
          'initialize',
          {
            processId: null,
            clientInfo: { name: 'web-idea', version: '0.1' },
            rootUri: root,
            capabilities: {
              textDocument: {
                definition: { linkSupport: false },
                references: {},
                hover: { contentFormat: ['markdown', 'plaintext'] },
                completion: {
                  completionItem: { snippetSupport: false },
                  contextSupport: true,
                },
                synchronization: { didSave: true, willSave: false, didChange: true },
              },
              workspace: { workspaceFolders: true, configuration: true },
              window: { workDoneProgress: true },
            },
            workspaceFolders: [{ uri: root, name: 'workspace' }],
            initializationOptions: { settings: javaLspSettings(this.readOnly) },
          },
          120_000,
        )
        if (!this.isCurrent(generation, rpc)) throw new Error('LSP session cancelled')
        rpc.notify('initialized', {})
        this.started = true
        this.setStatus('connecting', 'Initialized — waiting for jdtls index…')
        // Do NOT mark ready here; wait for language/status ServiceReady
      } catch (value) {
        const error = value instanceof Error ? value : new Error('lsp start failed')
        if (this.isCurrent(generation, rpc)) {
          this.started = false
          this.serviceReady = false
          this.setStatus('failed', error.message)
          this.settleReadyWaiters(error)
          if (this.rpc === rpc) {
            rpc.close()
            this.rpc = null
          }
        }
        throw error
      }
    })()

    let result: Promise<void>
    result = Promise.race([work, cancelled]).finally(() => {
      if (this.startPromise === result) this.startPromise = null
      if (this.cancelStart === rejectCancelled) this.cancelStart = null
    })
    this.startPromise = result
    return result
  }

  async didOpen(relPath: string, text: string, languageId = 'java'): Promise<void> {
    const uri = clientDocUri(this.workspaceId, relPath)
    const documentEpoch = this.documentEpoch
    try {
      await this.ensureStarted()
    } catch {
      // Opening a source file should remain usable when jdtls is unavailable;
      // query methods still surface the connection failure to their caller.
      return
    }
    if (
      documentEpoch !== this.documentEpoch ||
      !this.rpc ||
      !this.started
    ) return
    if (this.opened.has(uri)) {
      if (this.openedTexts.get(uri) === text) return
      await this.didChange(relPath, text)
      return
    }
    const rpc = this.rpc
    this.versions.set(uri, 1)
    this.openedTexts.set(uri, text)
    rpc.notify('textDocument/didOpen', {
      textDocument: {
        uri,
        languageId,
        version: 1,
        text,
      },
    })
    this.opened.add(uri)
  }

  async didChange(relPath: string, text: string): Promise<void> {
    const uri = clientDocUri(this.workspaceId, relPath)
    const documentEpoch = this.documentEpoch
    try {
      await this.ensureStarted()
    } catch {
      return
    }
    if (
      documentEpoch !== this.documentEpoch ||
      !this.rpc ||
      !this.started
    ) return
    if (!this.opened.has(uri)) {
      await this.didOpen(relPath, text)
      return
    }
    if (this.openedTexts.get(uri) === text) return
    const next = (this.versions.get(uri) ?? 1) + 1
    this.versions.set(uri, next)
    this.openedTexts.set(uri, text)
    this.rpc.notify('textDocument/didChange', {
      textDocument: { uri, version: next },
      contentChanges: [{ text }],
    })
  }

  async didSave(relPath: string, text?: string): Promise<void> {
    const uri = clientDocUri(this.workspaceId, relPath)
    const documentEpoch = this.documentEpoch
    try {
      await this.ensureStarted()
    } catch {
      return
    }
    if (
      documentEpoch !== this.documentEpoch ||
      !this.rpc ||
      !this.started ||
      !this.opened.has(uri)
    ) return
    this.rpc.notify('textDocument/didSave', {
      textDocument: { uri },
      ...(text != null ? { text } : {}),
    })
  }

  didClose(relPath: string): void {
    const uri = clientDocUri(this.workspaceId, relPath)
    // Only one document is active; closing it invalidates pending document work.
    this.documentEpoch += 1
    const wasOpen = this.opened.delete(uri)
    this.versions.delete(uri)
    this.openedTexts.delete(uri)
    if (wasOpen && this.rpc && this.started) {
      this.rpc.notify('textDocument/didClose', { textDocument: { uri } })
    }
  }

  async completion(
    relPath: string,
    position: LspPosition,
    triggerCharacter?: string,
  ): Promise<LspCompletionItem[]> {
    await this.ensureStarted()
    const rpc = this.rpc
    if (!rpc || !this.started) throw new Error('LSP not connected')
    await this.waitUntilReady()
    if (this.rpc !== rpc || !this.started) throw new Error('LSP not connected')
    const uri = clientDocUri(this.workspaceId, relPath)
    const result = await rpc.request(
      'textDocument/completion',
      {
        textDocument: { uri },
        position,
        context: triggerCharacter
          ? { triggerKind: 2, triggerCharacter }
          : { triggerKind: 1 },
      },
      30_000,
    )
    return normalizeCompletions(result)
  }

  async references(relPath: string, position: LspPosition): Promise<LspLocation[]> {
    await this.ensureStarted()
    const rpc = this.rpc
    if (!rpc || !this.started) throw new Error('LSP not connected')
    await this.waitUntilReady()
    if (this.rpc !== rpc || !this.started) throw new Error('LSP not connected')
    const uri = clientDocUri(this.workspaceId, relPath)
    const result = await rpc.request(
      'textDocument/references',
      {
        textDocument: { uri },
        position,
        context: { includeDeclaration: true },
      },
      90_000,
    )
    return normalizeLocations(result)
  }

  async definition(relPath: string, position: LspPosition): Promise<LspLocation[]> {
    await this.ensureStarted()
    const rpc = this.rpc
    if (!rpc || !this.started) throw new Error('LSP not connected')
    await this.waitUntilReady()
    if (this.rpc !== rpc || !this.started) throw new Error('LSP not connected')
    const uri = clientDocUri(this.workspaceId, relPath)
    const result = await rpc.request(
      'textDocument/definition',
      {
        textDocument: { uri },
        position,
      },
      90_000,
    )
    return normalizeLocations(result)
  }

  dispose(): void {
    const error = new Error('LSP session disposed')
    this.generation += 1
    this.documentEpoch += 1
    this.cancelStart?.(error)
    this.cancelStart = null
    this.startPromise = null
    this.settleReadyWaiters(error)
    this.rpc?.close()
    this.rpc = null
    this.started = false
    this.serviceReady = false
    this.opened.clear()
    this.versions.clear()
    this.openedTexts.clear()
    this.progress = null
    this.setStatus('off')
  }
}

export type LspCompletionItem = {
  label: string
  kind?: number
  detail?: string
  documentation?: string
  insertText?: string
  filterText?: string
  sortText?: string
}

function normalizeCompletions(result: unknown): LspCompletionItem[] {
  if (!result) return []
  const list = Array.isArray(result)
    ? result
    : typeof result === 'object' && result && Array.isArray((result as { items?: unknown }).items)
      ? ((result as { items: unknown[] }).items)
      : []
  const out: LspCompletionItem[] = []
  for (const item of list) {
    if (!item || typeof item !== 'object') continue
    const o = item as Record<string, unknown>
    const label = o.label
    if (typeof label !== 'string' || !label) continue
    let documentation: string | undefined
    if (typeof o.documentation === 'string') documentation = o.documentation
    else if (o.documentation && typeof o.documentation === 'object') {
      const d = o.documentation as { value?: string }
      if (typeof d.value === 'string') documentation = d.value
    }
    out.push({
      label,
      kind: typeof o.kind === 'number' ? o.kind : undefined,
      detail: typeof o.detail === 'string' ? o.detail : undefined,
      documentation,
      insertText: typeof o.insertText === 'string' ? o.insertText : undefined,
      filterText: typeof o.filterText === 'string' ? o.filterText : undefined,
      sortText: typeof o.sortText === 'string' ? o.sortText : undefined,
    })
  }
  return out
}

function normalizeLocations(result: unknown): LspLocation[] {
  if (!result) return []
  const arr = Array.isArray(result) ? result : [result]
  const out: LspLocation[] = []
  for (const item of arr) {
    if (!item || typeof item !== 'object') continue
    const o = item as Record<string, unknown>
    if (o.targetUri && o.targetRange) {
      out.push({
        uri: String(o.targetUri),
        range: o.targetRange as LspLocation['range'],
      })
      continue
    }
    if (o.uri && o.range) {
      out.push({
        uri: String(o.uri),
        range: o.range as LspLocation['range'],
      })
    }
  }
  return out
}
