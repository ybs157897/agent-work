import { afterEach, describe, expect, it, vi } from 'vitest';
import { ApiError, apiFetch } from './client';
import { useWorkspaceStore } from '../stores/workspace.store';

const jsonResponse = (status: number, body: unknown, contentType = 'application/json') =>
  new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': contentType },
  });

afterEach(() => {
  vi.unstubAllGlobals();
  useWorkspaceStore.setState({ selectedWorkspaceId: null, workspace: null });
});

describe('apiFetch', () => {
  it('GET 不带 Idempotency-Key，解析 JSON 响应', async () => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse(200, { items: [] }));
    vi.stubGlobal('fetch', fetchMock);

    const result = await apiFetch<{ items: unknown[] }>('/workspaces');

    expect(result).toEqual({ items: [] });
    const [, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    const headers = init.headers as Record<string, string>;
    expect(headers['Idempotency-Key']).toBeUndefined();
    expect(headers['X-Request-Id']).toMatch(/^req_/);
  });

  it('写命令自动生成 Idempotency-Key，也支持显式传入（重试同 key）', async () => {
    const fetchMock = vi.fn().mockImplementation(() => Promise.resolve(jsonResponse(200, {})));
    vi.stubGlobal('fetch', fetchMock);

    await apiFetch('/x', { method: 'POST', body: {} });
    await apiFetch('/x', { method: 'POST', body: {}, idempotencyKey: 'fixed-key' });

    const h1 = (fetchMock.mock.calls[0] as [string, RequestInit])[1].headers as Record<string, string>;
    const h2 = (fetchMock.mock.calls[1] as [string, RequestInit])[1].headers as Record<string, string>;
    expect(h1['Idempotency-Key']).toBeTruthy();
    expect(h2['Idempotency-Key']).toBe('fixed-key');
  });

  it('problem+json 映射为 ApiError（含 code/retryable/current_version）', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        jsonResponse(
          409,
          {
            type: 'https://workbench.example/problems/version-conflict',
            title: 'Resource version conflict',
            status: 409,
            code: 'version_conflict',
            detail: '资源版本已变化',
            retryable: true,
            current_version: 8,
          },
          'application/problem+json',
        ),
      ),
    );

    const err: unknown = await apiFetch('/x', { method: 'POST', body: {} }).catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ApiError);
    if (!(err instanceof ApiError)) throw new Error('expected ApiError');
    expect(err.code).toBe('version_conflict');
    expect(err.isVersionConflict).toBe(true);
    expect(err.retryable).toBe(true);
    expect(err.currentVersion).toBe(8);
  });

  it('非 problem 错误兜底为 http_error，5xx 可重试', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response('oops', { status: 503 })));
    const err: unknown = await apiFetch('/x').catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ApiError);
    if (!(err instanceof ApiError)) throw new Error('expected ApiError');
    expect(err.code).toBe('http_error');
    expect(err.retryable).toBe(true);
  });

  it('业务请求携带当前 Workspace scope，切换到目标后使用目标 header', async () => {
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(jsonResponse(200, {}))
      .mockResolvedValueOnce(jsonResponse(200, {}));
    vi.stubGlobal('fetch', fetchMock);
    useWorkspaceStore.setState({ selectedWorkspaceId: 'ws_A', workspace: { id: 'ws_A', name: 'A', timezone: 'UTC', version: 1 } });
    await apiFetch('/work-items/wi_A');
    useWorkspaceStore.setState({ selectedWorkspaceId: 'ws_B', workspace: { id: 'ws_B', name: 'B', timezone: 'UTC', version: 1 } });
    await apiFetch('/workspaces/ws_B/bootstrap');
    expect((fetchMock.mock.calls[0][1].headers as Record<string, string>)['X-Workspace-ID']).toBe('ws_A');
    expect((fetchMock.mock.calls[1][1].headers as Record<string, string>)['X-Workspace-ID']).toBe('ws_B');
  });

  it('全局目录/宿主请求不带旧 Workspace header', async () => {
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(jsonResponse(200, {}))
      .mockResolvedValueOnce(jsonResponse(200, {}));
    vi.stubGlobal('fetch', fetchMock);
    useWorkspaceStore.setState({ selectedWorkspaceId: 'ws_A', workspace: { id: 'ws_A', name: 'A', timezone: 'UTC', version: 1 } });
    await apiFetch('/execution-hosts');
    await apiFetch('/workspaces');
    expect((fetchMock.mock.calls[0][1].headers as Record<string, string>)['X-Workspace-ID']).toBeUndefined();
    expect((fetchMock.mock.calls[1][1].headers as Record<string, string>)['X-Workspace-ID']).toBeUndefined();
  });

  it('旧 Workspace URL 配合当前 header 由服务端 scope mismatch 拒绝，客户端保留结构化错误', async () => {
    useWorkspaceStore.setState({ selectedWorkspaceId: 'ws_B', workspace: { id: 'ws_B', name: 'B', timezone: 'UTC', version: 1 } });
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse(409, {
      type: 'https://workbench.example/problems/workspace_scope_mismatch',
      title: 'Workspace scope mismatch', status: 409, code: 'workspace_scope_mismatch', detail: '资源属于其他 Workspace', retryable: false,
    }, 'application/problem+json'));
    vi.stubGlobal('fetch', fetchMock);
    const error: unknown = await apiFetch('/work-items/wi_from_a').catch((value: unknown) => value);
    expect(error).toBeInstanceOf(ApiError);
    expect((error as ApiError).code).toBe('workspace_scope_mismatch');
    expect((fetchMock.mock.calls[0][1].headers as Record<string, string>)['X-Workspace-ID']).toBe('ws_B');
  });
});
