import type { Problem } from './types';
import { useWorkspaceStore } from '../stores/workspace.store';

const BASE = '/api/v1';

/** 领域错误：problem+json 的结构化表达（协议文档 §5.5）。 */
export class ApiError extends Error {
  readonly status: number;
  readonly code: string;
  readonly retryable: boolean;
  readonly currentVersion?: number;
  readonly requestId?: string;

  constructor(problem: Problem) {
    super(problem.detail || problem.title);
    this.name = 'ApiError';
    this.status = problem.status;
    this.code = problem.code ?? 'unknown';
    this.retryable = problem.retryable ?? false;
    this.currentVersion = problem.current_version;
    this.requestId = problem.request_id;
  }

  get isVersionConflict(): boolean {
    return this.status === 409 && this.code === 'version_conflict';
  }

  get isIdempotencyConflict(): boolean {
    return this.status === 409 && this.code === 'idempotency_conflict';
  }
}

export function newIdempotencyKey(): string {
  return crypto.randomUUID();
}

export function newRequestId(): string {
  return `req_${crypto.randomUUID()}`;
}

interface FetchOptions {
  method?: 'GET' | 'POST' | 'PUT' | 'PATCH' | 'DELETE';
  body?: unknown;
  /** 写命令必须携带；缺省时自动生成（协议 §5.1）。 */
  idempotencyKey?: string;
  headers?: Record<string, string>;
  /** Allow a best-effort DELETE to survive page teardown. */
  keepalive?: boolean;
}

function workspaceScopedPath(path: string): boolean {
  const pathname = path.split('?', 1)[0];
  if (pathname === '/me' || pathname === '/health' || pathname === '/workspaces' || pathname === '/models' || pathname.startsWith('/models/') || pathname === '/execution-hosts' || pathname.startsWith('/execution-hosts/') || pathname === '/runtimes' || pathname.startsWith('/runtimes/')) return false;
  return pathname.startsWith('/workspaces/')
    || pathname.startsWith('/workspace-locations/')
    || pathname.startsWith('/work-items/')
    || pathname.startsWith('/agent-profiles/')
    || pathname.startsWith('/runs/')
    || pathname.startsWith('/code-workspaces/');
}

/**
 * 统一 fetch 封装：JSON 编解码、problem+json → ApiError。
 * 网络层错误（TypeError）原样抛出，调用方可用同一 Idempotency-Key 安全重试。
 */
export async function apiFetch<T>(path: string, opts: FetchOptions = {}): Promise<T> {
  const headers: Record<string, string> = {
    'X-Request-Id': newRequestId(),
    ...opts.headers,
  };
  const currentWorkspaceId = useWorkspaceStore.getState().selectedWorkspaceId ?? useWorkspaceStore.getState().workspace?.id ?? '';
  if (workspaceScopedPath(path) && currentWorkspaceId && !headers['X-Workspace-ID']) headers['X-Workspace-ID'] = currentWorkspaceId;
  let body: BodyInit | undefined;
  if (opts.body !== undefined) {
    if (typeof FormData !== 'undefined' && opts.body instanceof FormData) {
      body = opts.body;
    } else {
      headers['Content-Type'] = 'application/json';
      body = JSON.stringify(opts.body);
    }
  }
  const method = opts.method ?? 'GET';
  if (method !== 'GET') {
    headers['Idempotency-Key'] = opts.idempotencyKey ?? newIdempotencyKey();
  }

  const resp = await fetch(`${BASE}${path}`, { method, headers, body, ...(opts.keepalive ? { keepalive: true } : {}) });

  if (resp.ok) {
    if (resp.status === 204) return undefined as T;
    return (await resp.json()) as T;
  }

  const contentType = resp.headers.get('Content-Type') ?? '';
  if (contentType.includes('application/problem+json')) {
    throw new ApiError((await resp.json()) as Problem);
  }
  throw new ApiError({
    type: 'about:blank',
    title: resp.statusText || 'Request failed',
    status: resp.status,
    code: 'http_error',
    retryable: resp.status >= 500 || resp.status === 429,
  });
}
