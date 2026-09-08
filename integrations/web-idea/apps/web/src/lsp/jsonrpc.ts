/** Minimal JSON-RPC 2.0 client over WebSocket (raw JSON text frames). */

type JsonRpcMessage = {
  jsonrpc: '2.0'
  id?: number | string
  method?: string
  params?: unknown
  result?: unknown
  error?: { code: number; message: string; data?: unknown }
}

type Pending = {
  resolve: (v: unknown) => void
  reject: (e: Error) => void
  timer: ReturnType<typeof setTimeout>
}

const DEFAULT_TIMEOUT_MS = 60_000

export class JsonRpcClient {
  private ws: WebSocket | null = null
  private nextId = 1
  private pending = new Map<number, Pending>()
  private handlers = new Map<string, Set<(params: unknown) => void>>()
  private openPromise: Promise<void> | null = null

  constructor(private readonly url: string) {}

  connect(): Promise<void> {
    if (this.openPromise) return this.openPromise
    this.openPromise = new Promise((resolve, reject) => {
      const ws = new WebSocket(this.url)
      this.ws = ws
      ws.onopen = () => resolve()
      ws.onerror = () => reject(new Error('lsp websocket error'))
      ws.onclose = () => {
        for (const [, p] of this.pending) {
          clearTimeout(p.timer)
          p.reject(new Error('lsp disconnected'))
        }
        this.pending.clear()
        this.ws = null
        this.openPromise = null
      }
      ws.onmessage = (ev) => {
        let msg: JsonRpcMessage
        try {
          msg = JSON.parse(String(ev.data)) as JsonRpcMessage
        } catch {
          return
        }
        // Response to our request
        if (msg.id != null && msg.method === undefined && (msg.result !== undefined || msg.error !== undefined)) {
          const id = Number(msg.id)
          const p = this.pending.get(id)
          if (!p) return
          this.pending.delete(id)
          clearTimeout(p.timer)
          if (msg.error) p.reject(new Error(msg.error.message))
          else p.resolve(msg.result)
          return
        }
        // Server → client request or notification
        if (msg.method) {
          const set = this.handlers.get(msg.method)
          if (set) for (const h of set) h(msg.params)
          if (msg.id != null) {
            this.replyServerRequest(msg.method, msg.id, msg.params)
          }
        }
      }
    })
    return this.openPromise
  }

  private replyServerRequest(method: string, id: number | string, params: unknown) {
    if (!this.ws || this.ws.readyState !== WebSocket.OPEN) return
    let result: unknown = null
    if (method === 'workspace/configuration') {
      const items = (params as { items?: unknown[] } | null)?.items
      result = Array.isArray(items) ? items.map(() => ({})) : [{}]
    } else if (method === 'workspace/workspaceFolders') {
      result = null
    } else if (method === 'window/workDoneProgress/create') {
      result = null
    } else if (method === 'client/registerCapability' || method === 'client/unregisterCapability') {
      result = null
    } else if (method === 'window/showMessageRequest') {
      result = null
    }
    const payload: JsonRpcMessage = { jsonrpc: '2.0', id, result }
    this.ws.send(JSON.stringify(payload))
  }

  onNotification(method: string, handler: (params: unknown) => void): () => void {
    let set = this.handlers.get(method)
    if (!set) {
      set = new Set()
      this.handlers.set(method, set)
    }
    set.add(handler)
    return () => set!.delete(handler)
  }

  request(method: string, params?: unknown, timeoutMs = DEFAULT_TIMEOUT_MS): Promise<unknown> {
    if (!this.ws || this.ws.readyState !== WebSocket.OPEN) {
      return Promise.reject(new Error('lsp not connected'))
    }
    const id = this.nextId++
    const payload: JsonRpcMessage = { jsonrpc: '2.0', id, method, params }
    return new Promise((resolve, reject) => {
      const timer = setTimeout(() => {
        this.pending.delete(id)
        reject(new Error(`${method} timed out after ${Math.round(timeoutMs / 1000)}s`))
      }, timeoutMs)
      this.pending.set(id, { resolve, reject, timer })
      this.ws!.send(JSON.stringify(payload))
    })
  }

  notify(method: string, params?: unknown): void {
    if (!this.ws || this.ws.readyState !== WebSocket.OPEN) return
    const payload: JsonRpcMessage = { jsonrpc: '2.0', method, params }
    this.ws.send(JSON.stringify(payload))
  }

  close(): void {
    for (const [, p] of this.pending) {
      clearTimeout(p.timer)
      p.reject(new Error('lsp closed'))
    }
    this.pending.clear()
    this.ws?.close()
    this.ws = null
    this.openPromise = null
  }
}
