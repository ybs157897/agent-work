import { EVENT_NAMES, type CanonicalEvent } from './types';
import {
  isOutputTraceEnabled,
  outputTraceEventText,
  outputTraceHash,
  stableOutputTraceJson,
  traceOutput,
  type OutputTraceProjection,
} from '../utils/output-trace';

export type SseStatus = 'connecting' | 'online' | 'reconnecting';

export interface SseHandlers {
  onEvent: (ev: CanonicalEvent) => void;
  onStatus: (status: SseStatus) => void;
  /** 410 cursor_expired：调用方需重新 bootstrap 并以新 cursor 调用 start()。 */
  onCursorExpired: () => void;
}

const DEDUP_LIMIT = 1000;
function contentBlockProjection(data?: Record<string, unknown>): OutputTraceProjection | undefined {
  const document = data?.content_blocks;
  if (!document || typeof document !== 'object') return undefined;
  const blocks = (document as Record<string, unknown>).blocks;
  const list = Array.isArray(blocks) ? blocks : [];
  const serialized = stableOutputTraceJson(document);
  return {
    contentBlocks: list.length,
    blockTypes: list.map((block) => {
      if (!block || typeof block !== 'object') return 'unknown';
      const type = (block as Record<string, unknown>).type;
      return typeof type === 'string' ? type : 'unknown';
    }),
    hash: outputTraceHash(serialized),
  };
}

/**
 * Workspace SSE 订阅（协议文档 §6）：
 * - 初次连接用 ?cursor= 指定起点；断线后浏览器自动带 Last-Event-ID 重连补发；
 * - event_id / stream_seq 双重去重；
 * - 连接失败（readyState CLOSED）时探测 HTTP 状态：410 → onCursorExpired，其他 → 手动重连。
 */
export class WorkspaceEventStream {
  private readonly workspaceId: string;
  private readonly handlers: SseHandlers;
  private source: EventSource | null = null;
  private lastSeq = 0;
  private seenIds = new Set<string>();
  private seenQueue: string[] = [];
  private stopped = false;
  private retryTimer: ReturnType<typeof setTimeout> | null = null;
  private retryDelay = 3000;

  constructor(workspaceId: string, handlers: SseHandlers) {
    this.workspaceId = workspaceId;
    this.handlers = handlers;
  }

  start(cursor: number): void {
    this.stop();
    this.stopped = false;
    this.lastSeq = cursor;
    this.open();
  }

  stop(): void {
    this.stopped = true;
    if (this.retryTimer) {
      clearTimeout(this.retryTimer);
      this.retryTimer = null;
    }
    if (this.source) {
      this.source.close();
      this.source = null;
    }
  }

  private url(): string {
    return `/api/v1/workspaces/${this.workspaceId}/events?cursor=${this.lastSeq}`;
  }

  private open(): void {
    this.handlers.onStatus('connecting');
    const source = new EventSource(this.url());
    this.source = source;

    source.onopen = () => {
      this.retryDelay = 3000;
      this.handlers.onStatus('online');
    };

    for (const name of EVENT_NAMES) {
      source.addEventListener(name, (msg) => this.handleMessage(msg as MessageEvent));
    }

    source.onerror = () => {
      if (this.stopped) return;
      if (source.readyState === EventSource.CLOSED) {
        // 浏览器不会自动重连 CLOSED 连接：探测失败原因。
        source.close();
        this.source = null;
        this.handlers.onStatus('reconnecting');
        void this.probeAndRecover();
      } else {
        // CONNECTING：浏览器按 retry: 3000 自动重连（携带 Last-Event-ID）。
        this.handlers.onStatus('reconnecting');
      }
    };
  }

  private handleMessage(msg: MessageEvent): void {
    let ev: CanonicalEvent;
    try {
      ev = JSON.parse(msg.data as string) as CanonicalEvent;
    } catch {
      return; // 无法解析的事件不投影（协议 §6.4 schema drift 语义）
    }
    if (ev.contract_version !== 'events/v1') return;
    if (ev.event_id && this.seenIds.has(ev.event_id)) return;
    if (ev.event_id) {
      this.seenIds.add(ev.event_id);
      this.seenQueue.push(ev.event_id);
      if (this.seenQueue.length > DEDUP_LIMIT) {
        const oldest = this.seenQueue.shift();
        if (oldest) this.seenIds.delete(oldest);
      }
    }
    if (ev.stream_seq > this.lastSeq) this.lastSeq = ev.stream_seq;
    if (isOutputTraceEnabled()) {
      const callId = typeof ev.data?.call_id === 'string' ? ev.data.call_id : undefined;
      const messageId = typeof ev.data?.message_id === 'string'
        ? ev.data.message_id
        : typeof ev.data?.item_id === 'string'
          ? ev.data.item_id
          : undefined;
      traceOutput({
        stage: 'sse.received',
        mode: ev.type === 'message.completed' ? 'final' : 'streaming',
        source: 'sse',
        runId: ev.aggregate.type === 'execution_run' ? ev.aggregate.id : undefined,
        messageId,
        eventId: ev.event_id,
        correlationId: ev.correlation_id,
        callId,
        streamSeq: ev.stream_seq,
        runSeq: ev.run_seq,
        eventType: ev.type,
        serverOccurredAt: ev.occurred_at,
        text: outputTraceEventText(ev.type, ev.data),
        projection: contentBlockProjection(ev.data),
        status: typeof ev.data?.status === 'string' ? ev.data.status : undefined,
        metadata: {
          aggregateType: ev.aggregate.type,
          aggregateVersion: ev.aggregate.version,
          ...(ev.type === 'tool.completed' || ev.type === 'tool.failed'
            ? { lifecycle: 'tool-terminal' }
            : {}),
        },
      });
    }
    this.handlers.onEvent(ev);
  }

  /** 探测连接失败原因：410 cursor_expired 需要重新 bootstrap。 */
  private async probeAndRecover(): Promise<void> {
    const ctrl = new AbortController();
    try {
      const resp = await fetch(this.url(), {
        signal: ctrl.signal,
        headers: { Accept: 'text/event-stream' },
      });
      const status = resp.status;
      ctrl.abort();
      if (status === 410) {
        this.handlers.onCursorExpired();
        return;
      }
    } catch {
      ctrl.abort();
    }
    if (this.stopped) return;
    // 其他失败：指数退避 + 手动重连。
    this.retryTimer = setTimeout(() => {
      if (!this.stopped) this.open();
    }, this.retryDelay);
    this.retryDelay = Math.min(this.retryDelay * 2, 30000);
  }
}
