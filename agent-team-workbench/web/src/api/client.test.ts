import { afterEach, describe, expect, it, vi } from 'vitest';
import { ApiError, apiFetch, apiUpload } from './client';

const jsonResponse = (status: number, body: unknown, contentType = 'application/json') =>
  new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': contentType },
  });

afterEach(() => {
  vi.unstubAllGlobals();
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

  it('multipart 上传不手动设置 Content-Type，并携带请求 ID 与幂等键', async () => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse(201, { path: '/tmp/image.png' }));
    vi.stubGlobal('fetch', fetchMock);
    const body = new FormData();
    body.append('file', new Blob(['png'], { type: 'image/png' }), 'image.png');

    await apiUpload('/workspaces/ws_1/uploads/images', body, 'upload-key');

    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    const headers = init.headers as Record<string, string>;
    expect(url).toBe('/api/v1/workspaces/ws_1/uploads/images');
    expect(init.body).toBe(body);
    expect(headers['Content-Type']).toBeUndefined();
    expect(headers['Idempotency-Key']).toBe('upload-key');
    expect(headers['X-Request-Id']).toMatch(/^req_/);
  });
});
