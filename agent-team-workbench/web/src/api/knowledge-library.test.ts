import { afterEach, describe, expect, it, vi } from 'vitest';
import {
  cancelTask,
  createSource,
  deleteSource,
  expandHandle,
  getDocument,
  getEvidence,
  getGraph,
  getLibrary,
  getStatus,
  getTask,
  initializeLibrary,
  listBridges,
  listDocuments,
  listEvents,
  listReleases,
  listSources,
  listTasks,
  queryLibrary,
  reindex,
  retryTask,
  submitEvent,
  updateSource,
} from './knowledge-library';

const json = (body: unknown) =>
  new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } });

const ROOT = '/api/v1/workspaces/ws_1/library';

function stub() {
  const fetch = vi.fn().mockImplementation(() => Promise.resolve(json({ items: [] })));
  vi.stubGlobal('fetch', fetch);
  return fetch;
}

afterEach(() => vi.unstubAllGlobals());

describe('资料库 HTTP 客户端', () => {
  it('总览读取工作区资料库根，不带多余查询参数', async () => {
    const fetch = vi.fn().mockResolvedValue(json({ workspace_id: 'ws_1', enabled: true }));
    vi.stubGlobal('fetch', fetch);
    await expect(getLibrary('ws_1')).resolves.toMatchObject({ workspace_id: 'ws_1' });
    expect(fetch.mock.calls[0][0]).toBe(ROOT);
    expect(fetch.mock.calls[0][1].method).toBe('GET');
  });

  it('来源列表与登记沿用同一前缀，登记体字段原样提交', async () => {
    const fetch = stub();
    await expect(listSources('ws/1')).resolves.toEqual({ items: [] });
    expect(fetch.mock.calls[0][0]).toBe('/api/v1/workspaces/ws%2F1/library/sources');
    expect(fetch.mock.calls[0][1].method).toBe('GET');

    await createSource('ws_1', {
      name: '订单服务',
      kind: 'service',
      repo_path: '/repo/order',
      default_ref: 'main',
      usages: [{ consumer: 'order-service', artifact: 'com.example:common:1.4.2', environment: 'prod', artifact_resolution: 'declared' }],
    });
    expect(fetch.mock.calls[1][0]).toBe(`${ROOT}/sources`);
    expect(fetch.mock.calls[1][1].method).toBe('POST');
    expect(JSON.parse(fetch.mock.calls[1][1].body)).toEqual({
      name: '订单服务',
      kind: 'service',
      repo_path: '/repo/order',
      default_ref: 'main',
      usages: [
        { consumer: 'order-service', artifact: 'com.example:common:1.4.2', environment: 'prod', artifact_resolution: 'declared' },
      ],
    });
    expect(fetch.mock.calls[1][1].headers['Content-Type']).toBe('application/json');
    expect(typeof fetch.mock.calls[1][1].headers['Idempotency-Key']).toBe('string');
  });

  it('来源读取路径把缺失的 usages 归一化为真数组（列表、创建、修改都过同一层）', async () => {
    const legacy = {
      id: 'src_legacy',
      name: '旧来源',
      kind: 'service',
      repo_path: '/repo/legacy',
      default_ref: 'main',
      enabled: true,
      version: 1,
      created_at: '2026-08-01T00:00:00Z',
      updated_at: '2026-08-01T00:00:00Z',
    };
    const fetch = vi
      .fn()
      .mockResolvedValueOnce(json({ items: [legacy, { ...legacy, id: 'src_2', usages: null }] }))
      .mockResolvedValueOnce(json(legacy))
      .mockResolvedValueOnce(json({ ...legacy, usages: [{ consumer: 'order-service', artifact: 'x:y:1' }] }));
    vi.stubGlobal('fetch', fetch);

    const list = await listSources('ws_1');
    expect(list.items.map((item) => item.usages)).toEqual([[], []]);
    expect((await createSource('ws_1', { name: '旧来源', kind: 'service', repo_path: '/repo/legacy' })).usages).toEqual([]);
    // 缺 artifact_resolution 的旧记录按契约回落到 declared，不能冒充 resolved。
    expect((await updateSource('ws_1', 'src_legacy', { expected_version: 1 })).usages).toEqual([
      { consumer: 'order-service', artifact: 'x:y:1', environment: '', artifact_resolution: 'declared', resolution_ref: '' },
    ]);
  });

  it('normalizeSource / normalizeSourceList 是纯函数：空值与缺字段都落成可渲染的形状', async () => {
    const { normalizeSource, normalizeSourceList, artifactResolutionOf } = await import('./knowledge-library');
    const raw = {
      id: 'src_1',
      name: 'common',
      kind: 'common',
      repo_path: '/repo/common',
      default_ref: 'main',
      usages: [{ consumer: 'order-service', artifact: 'com.example:common:1.4.2', artifact_resolution: 'resolved' }],
      enabled: true,
      version: 2,
      created_at: '',
      updated_at: '',
    } as unknown as Parameters<typeof normalizeSource>[0];

    expect(normalizeSource({ ...raw, usages: null } as unknown as typeof raw).usages).toEqual([]);
    expect(normalizeSource(raw).usages).toEqual([
      { consumer: 'order-service', artifact: 'com.example:common:1.4.2', environment: '', artifact_resolution: 'resolved', resolution_ref: '' },
    ]);
    expect(normalizeSourceList({ items: null } as unknown as { items: never[] }).items).toEqual([]);
    expect(artifactResolutionOf(undefined)).toBe('declared');
    expect(artifactResolutionOf({ artifact_resolution: 'nonsense' } as never)).toBe('declared');
    expect(artifactResolutionOf({ artifact_resolution: 'unknown' })).toBe('unknown');
  });

  it('修改来源带版本 CAS，停用走 DELETE 且不带请求体', async () => {
    const fetch = stub();
    await updateSource('ws_1', 'src_1', { name: '订单服务 v2', enabled: false, expected_version: 3 });
    expect(fetch.mock.calls[0][0]).toBe(`${ROOT}/sources/src_1`);
    expect(fetch.mock.calls[0][1].method).toBe('PATCH');
    expect(JSON.parse(fetch.mock.calls[0][1].body)).toEqual({ name: '订单服务 v2', enabled: false, expected_version: 3 });

    await deleteSource('ws_1', 'src_1');
    expect(fetch.mock.calls[1][0]).toBe(`${ROOT}/sources/src_1`);
    expect(fetch.mock.calls[1][1].method).toBe('DELETE');
    expect(fetch.mock.calls[1][1].body).toBeUndefined();
  });

  it('事件受理复用 client_key 作为幂等键，列表编码状态与条数', async () => {
    const fetch = stub();
    await submitEvent('ws_1', {
      event_type: 'code.pulled',
      source: '订单服务',
      subject: { branch: 'main' },
      content_ref: 'commit:abc',
      payload: { sha: 'abc' },
      client_key: 'ck_1',
    });
    expect(fetch.mock.calls[0][0]).toBe(`${ROOT}/events`);
    expect(fetch.mock.calls[0][1].method).toBe('POST');
    expect(fetch.mock.calls[0][1].headers['Idempotency-Key']).toBe('ck_1');
    expect(JSON.parse(fetch.mock.calls[0][1].body)).toMatchObject({ event_type: 'code.pulled', client_key: 'ck_1' });

    await initializeLibrary('ws_1', { client_key: 'ck_init', view_id: 'view_1', reason: '首次建库' });
    expect(fetch.mock.calls[1][0]).toBe(`${ROOT}/initialize`);
    expect(fetch.mock.calls[1][1].method).toBe('POST');
    expect(fetch.mock.calls[1][1].headers['Idempotency-Key']).toBe('ck_init');
    expect(JSON.parse(fetch.mock.calls[1][1].body)).toEqual({ client_key: 'ck_init', view_id: 'view_1', reason: '首次建库' });

    await listEvents('ws_1', { status: 'blocked', limit: 50 });
    expect(fetch.mock.calls[2][0]).toBe(`${ROOT}/events?status=blocked&limit=50`);
    expect(fetch.mock.calls[2][1].method).toBe('GET');

    await listEvents('ws_1');
    expect(fetch.mock.calls[3][0]).toBe(`${ROOT}/events`);
  });

  it('队列列表与任务详情、重试、取消指同一任务', async () => {
    const fetch = stub();
    await listTasks('ws_1', { status: 'blocked', limit: 20 });
    await getTask('ws_1', 'task/1');
    await retryTask('ws_1', 'task_1');
    await cancelTask('ws_1', 'task_1');
    expect(fetch.mock.calls.map((call) => call[0])).toEqual([
      `${ROOT}/tasks?status=blocked&limit=20`,
      `${ROOT}/tasks/task%2F1`,
      `${ROOT}/tasks/task_1/retry`,
      `${ROOT}/tasks/task_1/cancel`,
    ]);
    expect(fetch.mock.calls.map((call) => call[1].method)).toEqual(['GET', 'GET', 'POST', 'POST']);
  });

  it('发布、文档、证据按 release 固定读取', async () => {
    const fetch = stub();
    await listReleases('ws_1', { limit: 10 });
    await getDocument('ws_1', 'doc_1', 'rel_2');
    await getDocument('ws_1', 'doc_1');
    await getEvidence('ws_1', 'ev/1');
    expect(fetch.mock.calls.map((call) => call[0])).toEqual([
      `${ROOT}/releases?limit=10`,
      `${ROOT}/documents/doc_1?release_id=rel_2`,
      `${ROOT}/documents/doc_1`,
      `${ROOT}/evidence/ev%2F1`,
    ]);
    expect(fetch.mock.calls.every((call) => call[1].method === 'GET')).toBe(true);
  });

  it('文档列表把搜索、分类、分页与 release 一起编码', async () => {
    const fetch = stub();
    await listDocuments('ws_1', { release_id: 'rel_1', q: '退款窗口', kind: 'rule', limit: 20, cursor: 'doc_20' });
    const url = fetch.mock.calls[0][0] as string;
    const params = new URLSearchParams(url.split('?')[1]);
    expect(url.startsWith(`${ROOT}/documents?`)).toBe(true);
    expect(params.get('release_id')).toBe('rel_1');
    expect(params.get('q')).toBe('退款窗口');
    expect(params.get('kind')).toBe('rule');
    expect(params.get('limit')).toBe('20');
    expect(params.get('cursor')).toBe('doc_20');

    await listDocuments('ws_1', {});
    expect(fetch.mock.calls[1][0]).toBe(`${ROOT}/documents`);
  });

  it('图谱与桥接可固定 release，查询体与展开句柄原样编码', async () => {
    const fetch = stub();
    await getGraph('ws_1', 'rel_1');
    await getGraph('ws_1');
    await listBridges('ws_1', 'rel_1');
    expect(fetch.mock.calls.map((call) => call[0])).toEqual([
      `${ROOT}/graph?release_id=rel_1`,
      `${ROOT}/graph`,
      `${ROOT}/bridges?release_id=rel_1`,
    ]);

    await queryLibrary('ws_1', { question: '退款窗口是多久', terms: ['退款', '窗口'], release_id: 'rel_1', limit: 5 });
    expect(fetch.mock.calls[3][0]).toBe(`${ROOT}/query`);
    expect(fetch.mock.calls[3][1].method).toBe('POST');
    expect(JSON.parse(fetch.mock.calls[3][1].body)).toEqual({
      question: '退款窗口是多久',
      terms: ['退款', '窗口'],
      release_id: 'rel_1',
      limit: 5,
    });

    // Expanding inside a pinned release must keep the pin, or a historical
    // handle would open the current version of the object.
    await expandHandle('ws_1', 'evidence', 'ev 1', 'rel_hist');
    const expandUrl = fetch.mock.calls[4][0] as string;
    expect(expandUrl.startsWith(`${ROOT}/expand?`)).toBe(true);
    const expandParams = new URLSearchParams(expandUrl.split('?')[1]);
    expect(expandParams.get('kind')).toBe('evidence');
    expect(expandParams.get('id')).toBe('ev 1');
    expect(expandParams.get('release_id')).toBe('rel_hist');
    // Without a pin the parameter is absent rather than empty.
    await expandHandle('ws_1', 'assertion', 'assertion:x');
    const unpinned = new URLSearchParams((fetch.mock.calls[5][0] as string).split('?')[1]);
    expect(unpinned.get('release_id')).toBeNull();
  });

  it('观察面与索引重建走 GET /status 与 POST /reindex', async () => {
    const fetch = stub();
    await getStatus('ws_1');
    await reindex('ws_1');
    expect(fetch.mock.calls[0][0]).toBe(`${ROOT}/status`);
    expect(fetch.mock.calls[0][1].method).toBe('GET');
    expect(fetch.mock.calls[1][0]).toBe(`${ROOT}/reindex`);
    expect(fetch.mock.calls[1][1].method).toBe('POST');
  });
});
