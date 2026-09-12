import { renderToStaticMarkup } from 'react-dom/server';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type {
  Assertion,
  DocumentDetail,
  DocumentSummary,
  Evidence,
  LibraryEvent,
  LibrarySummary,
  QueryResponse,
  Release,
  Source,
  SourceUsage,
  TaskSummary,
} from '../api/knowledge-library';
import { useWorkspaceStore } from '../stores/workspace.store';
import KnowledgePage, {
  AssertionCard,
  BrowsePanel,
  DocumentDetailView,
  EvidenceDetailCard,
  EventsTable,
  KNOWLEDGE_TABS,
  KnowledgeTabList,
  LibraryPanel,
  SINGLE_IMPLICIT_USAGE_LABEL,
  SourceForm,
  SourceUsagesCell,
  SourcesTable,
  TasksTable,
  coverageLine,
  effectiveResolutionLabel,
  emptySourceDraft,
  evidenceChipLabel,
  formatBytes,
  formatLocator,
  parseKnowledgeTab,
  QueryPanel,
  ReceiptCard,
  ReleasesTable,
  scopeText,
  sourceCreatePayload,
  sourceDraftFrom,
  sourceUpdatePayload,
  usageLine,
  type SourceDraft,
} from './knowledge.page';

/**
 * 资料库管理页渲染测试：只断言真实 DOM 结构（静态渲染），
 * 字段与冻结的 /library 契约逐字段对齐。
 * 交互面（点击 tab / 点证据 chip）用「按 URL 或 props 落到对应状态」的方式覆盖：
 * 本仓库测试环境是 node（无 jsdom），不引入 DOM 事件模拟。
 */

const workspace = { id: 'ws_1', name: '测试工作区', timezone: 'Asia/Shanghai', version: 1 };

// 静态渲染走 React 的 server snapshot：zustand 会返回初始 state，这里让 selector
// 直接读当前 store（与既有 chat render 测试同一处理方式），页面才能渲染出真实分支。
vi.mock('../stores/workspace.store', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../stores/workspace.store')>();
  return {
    ...actual,
    useWorkspaceStore: Object.assign(
      (selector: (state: ReturnType<typeof actual.useWorkspaceStore.getState>) => unknown) =>
        selector(actual.useWorkspaceStore.getState()),
      actual.useWorkspaceStore,
    ),
  };
});

const release = (over: Partial<Release> = {}): Release => ({
  id: 'rel_3',
  seq: 3,
  snapshot_id: 'snap_0123456789abcdef',
  task_id: 'task_3',
  projection_digest: 'sha256:abcdef0123456789',
  document_count: 4,
  assertion_count: 9,
  relation_count: 6,
  evidence_count: 12,
  coverage: { sources_read: ['订单服务', 'common'], sources_missed: [], notes: '全部来源已读' },
  notes: '',
  status: 'published',
  published_at: '2026-09-12T02:00:00Z',
  ...over,
});

const documentSummary = (over: Partial<DocumentSummary> = {}): DocumentSummary => ({
  id: 'doc_refund',
  path: 'content/business/rules/refund-window.md',
  kind: 'rule',
  title: '退款窗口规则',
  summary: '退款窗口的时长与生效条件',
  domains: ['交易'],
  status: 'active',
  renamed_from: '',
  version: 2,
  release_id: 'rel_3',
  updated_at: '2026-09-12T02:00:00Z',
  ...over,
});

const assertion = (over: Partial<Assertion> = {}): Assertion => ({
  id: 'ast_refund_14d',
  document_id: 'doc_refund',
  document_version_id: 'docv_2',
  heading: '退款窗口',
  about: ['entity:order'],
  perspective: 'normative',
  basis: 'code_static',
  statement: '订单支付完成后 14 天内可申请原路退款。',
  scope: { conditions: ['订单状态为已支付'], environments: ['生产'], valid_from: '2026-01-01' },
  evidence: [{ evidence_id: 'ev_refund_rule', role: 'supports' }],
  unknown_notes: '跨境订单是否适用尚未采集到证据。',
  ...over,
});

const documentDetail = (over: Partial<DocumentDetail> = {}): DocumentDetail => ({
  document: documentSummary(),
  version: {
    id: 'docv_2',
    version: 2,
    content_digest: 'sha256:1122334455667788',
    created_at: '2026-09-12T01:59:00Z',
    content_markdown: '# 退款窗口\n\n订单支付完成后 14 天内可申请原路退款。\n',
  },
  assertions: [assertion()],
  relations: [
    {
      id: 'rel_1',
      document_id: 'doc_refund',
      from: { kind: 'entity', id: 'entity:order' },
      predicate: 'constrained_by',
      to: { kind: 'assertion', id: 'ast_refund_14d' },
      to_resolved: true,
      to_raw: '退款窗口',
      perspective: 'normative',
      basis: 'code_static',
      condition: '订单已支付',
      evidence: [],
    },
    {
      id: 'rel_2',
      document_id: 'doc_refund',
      from: { kind: 'entity', id: 'entity:order' },
      predicate: 'depends_on',
      to: { kind: 'entity', id: '' },
      to_resolved: false,
      to_raw: 'payment-settlement',
      perspective: 'descriptive',
      basis: 'inferred',
      condition: '',
      evidence: [],
    },
  ],
  versions: [
    { id: 'docv_1', version: 1, content_digest: 'sha256:aaaabbbbccccdddd', created_at: '2026-08-01T02:00:00Z' },
    { id: 'docv_2', version: 2, content_digest: 'sha256:1122334455667788', created_at: '2026-09-12T01:59:00Z' },
  ],
  ...over,
});

const evidence = (over: Partial<Evidence> = {}): Evidence => ({
  id: 'ev_refund_rule',
  snapshot_id: 'snap_0123456789abcdef',
  binding_id: 'bind_1',
  representation_id: 'repr_1',
  locator: { path: 'src/order/refund.go', start_line: 42, end_line: 58 },
  locator_kind: 'line_range',
  excerpt: 'if paidAt.Add(14 * 24 * time.Hour).Before(now) { return ErrRefundWindowClosed }',
  excerpt_digest: 'sha256:9988776655443322',
  match_count: 2,
  availability: 'available',
  collected_at: '2026-09-12T01:58:00Z',
  binding: {
    source_name: '订单服务',
    source_kind: 'service',
    repo_path: '/Users/me/repos/order-service',
    git_ref: 'main',
    commit_sha: '9f2c1a7b0d3e4f5a6b7c8d9e0f1a2b3c4d5e6f70',
    dirty: true,
    artifact: 'order-service.jar',
    consumer: 'task-coordinator',
  },
  representation: {
    path: 'src/order/refund.go',
    media_type: 'text/x-go',
    encoding: 'utf-8',
    digest_algo: 'sha256',
    content_digest: 'aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899',
    byte_size: 2048,
    coverage: 'full',
    origin: 'worktree',
    stored_path: '_system/representations/sha256/aabb.bin',
  },
  ...over,
});

const blockedTask = (over: Partial<TaskSummary> = {}): TaskSummary => ({
  id: 'task_9',
  seq: 9,
  kind: 'knowledge_write',
  status: 'blocked',
  view_id: 'view_default',
  attempt: 2,
  max_attempts: 3,
  repair_attempt: 2,
  max_repair_attempts: 2,
  snapshot_id: 'snap_0123456789abcdef',
  turn_seq: 4,
  last_error: 'assertion 引用了不存在的 evidence key: ev_missing',
  blocked_reason: '连续两轮修复仍无法通过校验，需要人工处理来源登记',
  created_at: '2026-09-12T01:00:00Z',
  updated_at: '2026-09-12T01:30:00Z',
  ...over,
});

const librarySummary = (over: Partial<LibrarySummary> = {}): LibrarySummary => ({
  workspace_id: 'ws_1',
  root_path: '/Users/me/workspace/.agent-work/knowledge',
  enabled: true,
  current_release: release(),
  release_count: 3,
  document_count: 4,
  source_count: 2,
  index_revision: 17,
  updated_at: '2026-09-12T02:05:00Z',
  queue: { pending: 1, blocked: 1, head_seq: 9, running: null },
  ...over,
});

const usage = (over: Partial<SourceUsage> = {}): SourceUsage => ({
  consumer: 'order-service',
  artifact: 'com.example:common:1.4.2',
  environment: 'prod',
  artifact_resolution: 'declared',
  ...over,
});

const source = (over: Partial<Source> = {}): Source => ({
  id: 'src_order',
  name: '订单服务',
  kind: 'service',
  repo_path: '/Users/me/repos/order-service',
  default_ref: 'main',
  usages: [usage()],
  enabled: true,
  version: 3,
  created_at: '2026-08-01T00:00:00Z',
  updated_at: '2026-09-12T00:00:00Z',
  ...over,
});

const event = (over: Partial<LibraryEvent> = {}): LibraryEvent => ({
  id: 'evt_1',
  event_type: 'code.pulled',
  source: '订单服务',
  subject: { branch: 'main' },
  content_ref: 'commit:9f2c1a',
  status: 'accepted',
  received_at: '2026-09-12T01:00:00Z',
  updated_at: '2026-09-12T01:00:00Z',
  client_key: 'ck_1',
  ...over,
});

const queryResponse = (over: Partial<QueryResponse> = {}): QueryResponse => ({
  release: release(),
  results: [
    {
      assertion: assertion(),
      document: { id: 'doc_refund', path: 'content/business/rules/refund-window.md', title: '退款窗口规则' },
      score: 0.812,
      snippet: '订单支付完成后 14 天内可申请原路退款。',
      evidence: [evidence()],
    },
  ],
  coverage: { status: 'partial', truncated: true, scanned_versions: 37, notes: ['订单服务只读到已提交字节'] },
  freshness: {
    release_id: 'rel_3',
    published_at: '2026-09-12T02:00:00Z',
    pending_events: 2,
    newer_release_available: true,
    stale_sources: ['支付网关'],
  },
  unknowns: ['跨境订单是否适用 14 天窗口尚未采集到证据。'],
  expand_handles: [{ kind: 'evidence', id: 'ev_refund_rule', label: '证据 · 退款规则' }],
  ...over,
});

const originalWorkspace = useWorkspaceStore.getState();

beforeEach(() => {
  useWorkspaceStore.setState({ workspace, phase: 'ready' });
});

afterEach(() => {
  useWorkspaceStore.setState(originalWorkspace, true);
  vi.unstubAllGlobals();
});

describe('资料库管理页 · tab 模型', () => {
  it('五个 tab 都能从 URL 还原，未知值回落到资料库与来源', () => {
    expect(KNOWLEDGE_TABS).toHaveLength(5);
    for (const tab of KNOWLEDGE_TABS) expect(parseKnowledgeTab(tab.id)).toBe(tab.id);
    expect(parseKnowledgeTab(null)).toBe('library');
    expect(parseKnowledgeTab('nope')).toBe('library');
  });

  it('tablist 只把当前 tab 标记为选中，并给出面板关联属性', () => {
    const html = renderToStaticMarkup(<KnowledgeTabList active="query" onSelect={() => undefined} />);
    expect(html.match(/role="tab"/g)).toHaveLength(5);
    expect(html).toContain('aria-selected="true"');
    expect(html.match(/aria-selected="false"/g)).toHaveLength(4);
    expect(html).toContain('id="knowledge-tab-query"');
    expect(html).toContain('aria-controls="knowledge-panel-query"');
    expect(html).toContain('查询与展开');
  });

  it('页面按 URL 的 tab 参数挂载对应面板', () => {
    const html = renderToStaticMarkup(
      <MemoryRouter initialEntries={['/knowledge?tab=releases']}>
        <KnowledgePage />
      </MemoryRouter>,
    );
    expect(html).toContain('role="tabpanel"');
    expect(html).toContain('id="knowledge-panel-releases"');
    expect(html).toContain('aria-labelledby="knowledge-tab-releases"');
    expect(html).toMatch(/<button(?=[^>]*id="knowledge-tab-releases")(?=[^>]*aria-selected="true")[^>]*>/);
  });

  it('默认进入资料库与来源面板', () => {
    const html = renderToStaticMarkup(
      <MemoryRouter initialEntries={['/knowledge']}>
        <KnowledgePage />
      </MemoryRouter>,
    );
    expect(html).toContain('id="knowledge-panel-library"');
    expect(html).toContain('aria-labelledby="knowledge-tab-library"');
    expect(html).toContain('来源登记');
    expect(html).toMatch(/<button(?=[^>]*id="knowledge-tab-library")(?=[^>]*aria-selected="true")[^>]*>/);
  });
});

describe('资料库管理页 · 来源与事件', () => {
  it('资料库总览给出库根路径、计数、队列深度与当前发布，并说明一个库收录全部服务与 common', () => {
    const html = renderToStaticMarkup(
      <LibraryPanel
        summary={{ kind: 'ready', value: librarySummary() }}
        sources={{ kind: 'ready', value: { items: [source()] } }}
        busy={false}
        onCreate={() => Promise.resolve(true)}
        onUpdate={() => Promise.resolve(true)}
        onDisable={() => undefined}
        onRefresh={() => undefined}
      />,
    );
    expect(html).toContain('/Users/me/workspace/.agent-work/knowledge');
    expect(html).toContain('复制路径');
    expect(html).toContain('已启用');
    expect(html).toContain('一个资料库收录本工作区全部服务仓库与 common；跨服务关系是库内关系，不需要按服务拆库。');
    expect(html).toContain('当前发布');
    expect(html).toContain('#3 · rel_3');
    expect(html).toContain('覆盖：已读来源 2 · 已报告无缺口 · 全部来源已读');
    expect(html).toContain('索引修订');
    expect(html).toContain('登记来源');
    // 未提交前不报错：空表单只禁用提交，不把校验文案压到用户脸上。
    expect(html).not.toContain('填写来源名称');
  });

  it('来源表给出类型、状态、版本与停用入口', () => {
    const html = renderToStaticMarkup(
      <SourcesTable sources={[source()]} onEdit={() => undefined} onDisable={() => undefined} busy={false} />,
    );
    expect(html).toContain('订单服务');
    expect(html).toContain('服务仓库（service）');
    expect(html).toContain('/Users/me/repos/order-service');
    expect(html).toContain('停用');
    expect(html).toContain('v3');
    expect(html).toContain('使用方 / 依赖版本');
    expect(html).toContain('order-service · com.example:common:1.4.2（登记声明值）');
    expect(html).toContain('<caption class="sr-only">已登记来源</caption>');
  });

  it('多使用方来源逐条渲染消费方、依赖版本与解析标注，并把「多使用方」显式标出来', () => {
    const html = renderToStaticMarkup(
      <SourcesTable
        sources={[
          source({
            id: 'src_common',
            name: 'common',
            kind: 'common',
            usages: [
              usage({ consumer: 'order-service', artifact: 'com.example:common:1.4.2' }),
              usage({ consumer: 'device-service', artifact: 'com.example:common:2.0.0', environment: 'staging' }),
            ],
          }),
        ]}
        onEdit={() => undefined}
        onDisable={() => undefined}
        busy={false}
      />,
    );
    // 同一份 common 被两个服务以不同版本依赖：两条都要出现，且各自带解析标注。
    expect(html).toContain('order-service · com.example:common:1.4.2（登记声明值）');
    expect(html).toContain('device-service · com.example:common:2.0.0（登记声明值）');
    expect(html).toContain('多使用方 · 2');
    expect(html).toContain('环境 staging');
  });

  it('没有使用方的来源显示「单一使用方（未声明依赖版本）」，而不是空破折号', () => {
    const html = renderToStaticMarkup(
      <SourcesTable
        sources={[
          source({ id: 'src_doc', name: '文档仓库', kind: 'documents', usages: [], default_ref: 'main' }),
        ]}
        onEdit={() => undefined}
        onDisable={() => undefined}
        busy={false}
      />,
    );
    expect(html).toContain(SINGLE_IMPLICIT_USAGE_LABEL);
    // 空 usages 是后端认可的「一个隐式使用方」，不能退化成一个裸破折号。
    // 本行其余字段都非空，所以整张表里不该出现破折号。
    expect(html).not.toContain('—');
    expect(html).not.toContain('多使用方');
  });

  it('解析标注只在有制品时出现：未登记版本的行只报「未声明依赖版本」', () => {
    expect(usageLine(usage({ consumer: 'order-service', artifact: '' }))).toBe('order-service（未声明依赖版本）');
    expect(usageLine({ consumer: '', artifact: 'com.example:common:1.4.2' })).toBe(
      '未声明使用方 · com.example:common:1.4.2（登记声明值）',
    );
    // 真实构建解析出来的版本必须与人工登记值区分开。
    expect(usageLine(usage({ artifact_resolution: 'resolved' }))).toContain('（构建解析值');
    expect(usageLine(usage({ artifact_resolution: 'unknown' }))).toContain('（解析未知）');
    // 缺字段的旧数据按契约回落到 declared，绝不能冒充 resolved。
    expect(usageLine(usage({ artifact_resolution: undefined }))).toContain('（登记声明值）');

    const html = renderToStaticMarkup(<SourceUsagesCell usages={[usage({ artifact: '' })]} />);
    expect(html).toContain('order-service（未声明依赖版本）');
    expect(html).not.toContain('（登记声明值）');
  });

  it('创建/修改表单渲染可重复的使用方编辑器，并把消费方的业务含义写清楚', () => {
    const draft: SourceDraft = {
      ...emptySourceDraft(),
      name: 'common',
      kind: 'common',
      repo_path: '/Users/me/repos/common',
      usages: [
        { consumer: 'order-service', artifact: 'com.example:common:1.4.2', environment: 'prod', artifact_resolution: 'declared', resolution_ref: '' },
        { consumer: 'device-service', artifact: '', environment: '', artifact_resolution: 'unknown', resolution_ref: '' },
        { consumer: '', artifact: 'com.example:common:2.0.0', environment: '', artifact_resolution: 'resolved', resolution_ref: 'build:42' },
      ],
    };
    const html = renderToStaticMarkup(
      <SourceForm draft={draft} onChange={() => undefined} onSubmit={() => undefined} busy={false} submitLabel="保存来源" />,
    );
    expect(html).toContain('添加使用方');
    expect(html.match(/移除使用方/g)).toHaveLength(3); // 每行一个移除按钮
    expect(html).toContain('消费方（业务服务或模块）');
    expect(html).toContain('order-service');
    expect(html).toContain('device-service');
    // 消费方是「依赖这份来源的业务服务或模块」，不是查询资料的 agent。
    expect(html).toContain('依赖这份来源的服务/模块，不是查询资料的 agent');
    expect(html).not.toContain('task-coordinator');
    // 解析语义必须在选择控件旁边说清楚。
    expect(html).toContain('登记声明值：人工填写，未经过构建/依赖解析核实');
    expect(html).toContain('解析未知：没有已知的依赖版本映射');
    expect(html).toContain('必须填写可核查的解析依据');
  });

  it('编辑态由已加载来源的使用方种子化：只改 default_ref 保存，两条使用方原样回传', async () => {
    const loaded = source({
      id: 'src_common',
      name: 'common',
      kind: 'common',
      default_ref: 'main',
      usages: [
        usage({ consumer: 'order-service', artifact: 'com.example:common:1.4.2' }),
        usage({ consumer: 'device-service', artifact: 'com.example:common:2.0.0', environment: 'staging' }),
      ],
    });

    // 1) 打开编辑：草稿从已加载来源种子化，使用方一条不少。
    const draft = sourceDraftFrom(loaded);
    expect(draft.usages).toHaveLength(2);

    // 2) 只改一个与使用方无关的字段。
    const edited: SourceDraft = { ...draft, default_ref: 'release/2.0' };

    // 3) 保存体必须仍然带上两条使用方，且内容不变。
    const payload = sourceUpdatePayload(loaded, edited);
    expect(payload.default_ref).toBe('release/2.0');
    expect(payload.expected_version).toBe(3);
    expect(payload.usages).toEqual([
      { consumer: 'order-service', artifact: 'com.example:common:1.4.2', environment: 'prod', artifact_resolution: 'declared' },
      { consumer: 'device-service', artifact: 'com.example:common:2.0.0', environment: 'staging', artifact_resolution: 'declared' },
    ]);

    // 4) 落到真实请求体：客户端不再发送 artifact/consumer 单值字段。
    const fetch = vi.fn().mockResolvedValue(
      new Response(JSON.stringify(loaded), { status: 200, headers: { 'Content-Type': 'application/json' } }),
    );
    vi.stubGlobal('fetch', fetch);
    const { updateSource } = await import('../api/knowledge-library');
    await updateSource('ws_1', 'src_common', payload);
    const body = JSON.parse(fetch.mock.calls[0][1].body as string);
    expect(body.usages).toEqual(payload.usages);
    expect(body.artifact).toBeUndefined();
    expect(body.consumer).toBeUndefined();
  });

  it('空使用方列表按契约提交 usages: []，不是省略字段', () => {
    const draft: SourceDraft = { ...emptySourceDraft(), name: '文档仓库', repo_path: '/repo/docs', usages: [] };
    expect(sourceCreatePayload(draft)).toEqual({
      name: '文档仓库',
      kind: 'service',
      repo_path: '/repo/docs',
      default_ref: undefined,
      usages: [],
    });
    expect(sourceUpdatePayload(source({ version: 7 }), draft).usages).toEqual([]);
  });

  it('normalizeSource 把缺失或为 null 的 usages 归一化为空数组', async () => {
    const { normalizeSource } = await import('../api/knowledge-library');
    const legacy = {
      id: 'src_legacy',
      name: '旧来源',
      kind: 'service',
      repo_path: '/Users/me/repos/legacy',
      default_ref: 'main',
      usages: null,
      enabled: true,
      version: 1,
      created_at: '2026-08-01T00:00:00Z',
      updated_at: '2026-08-01T00:00:00Z',
    } as unknown as Source;
    expect(normalizeSource(legacy).usages).toEqual([]);
    expect(normalizeSource({ ...legacy, usages: undefined } as unknown as Source).usages).toEqual([]);

    // 归一化后的旧记录在表格里是可读的空态，而不是崩溃或空白。
    const html = renderToStaticMarkup(
      <SourcesTable sources={[normalizeSource(legacy)]} onEdit={() => undefined} onDisable={() => undefined} busy={false} />,
    );
    expect(html).toContain(SINGLE_IMPLICIT_USAGE_LABEL);
    expect(html).toContain('旧来源');
  });

  it('事件表把「已受理」与任务区分开，回执卡明确不等于已更新', () => {
    const html = renderToStaticMarkup(<EventsTable events={[event(), event({ id: 'evt_2', status: 'completed', task_id: 'task_3' })]} />);
    expect(html).toContain('已受理');
    expect(html).toContain('已完成');
    expect(html).toContain('code.pulled');
    expect(html).toContain('task_3');

    const receipt = renderToStaticMarkup(<ReceiptCard receipt={event()} />);
    expect(receipt).toContain('已受理');
    expect(receipt).toContain('不代表知识已经更新');
  });
});

describe('资料库管理页 · 文档与断言', () => {
  it('文档列表渲染标题、路径与版本', () => {
    const html = renderToStaticMarkup(
      <BrowsePanel
        releases={{ kind: 'ready', value: { items: [release()] } }}
        documents={{ kind: 'ready', value: { items: [documentSummary(), documentSummary({ id: 'doc_flow', title: '退款流程', path: 'content/business/flows/refund.md', kind: 'flow', version: 1 })] } }}
        detail={null}
        selectedReleaseId=""
        onSelectRelease={() => undefined}
        query=""
        kind=""
        onQueryChange={() => undefined}
        onKindChange={() => undefined}
        onSearch={() => undefined}
        selectedDocumentId={null}
        onSelectDocument={() => undefined}
        onSelectEvidence={() => undefined}
        onRefresh={() => undefined}
      />,
    );
    expect(html).toContain('退款窗口规则');
    expect(html).toContain('content/business/rules/refund-window.md');
    expect(html).toContain('退款流程');
    expect(html).toContain('已加载 2 份');
    expect(html).toContain('选择一份文档开始阅读');
  });

  it('断言卡渲染视角、依据、陈述、范围条件、证据 chip 与未知', () => {
    const html = renderToStaticMarkup(<AssertionCard assertion={assertion()} onSelectEvidence={() => undefined} />);
    expect(html).toContain('规范性');
    expect(html).toContain('代码静态事实');
    expect(html).toContain('订单支付完成后 14 天内可申请原路退款。');
    expect(html).toContain('条件：订单状态为已支付');
    expect(html).toContain('环境：生产');
    expect(html).toContain('生效：2026-01-01');
    expect(html).toContain(evidenceChipLabel({ evidence_id: 'ev_refund_rule', role: 'supports' }));
    expect(html).toContain('未知：跨境订单是否适用尚未采集到证据。');
  });

  it('文档详情渲染 frontmatter、关系与版本历史，并允许只读打开旧版本', () => {
    const html = renderToStaticMarkup(
      <DocumentDetailView
        detail={documentDetail()}
        selectedVersion={1}
        onSelectVersion={() => undefined}
        onSelectEvidence={() => undefined}
      />,
    );
    expect(html).toContain('content/business/rules/refund-window.md');
    expect(html).toContain('doc_refund');
    expect(html).toContain('历史版本只读');
    expect(html).toContain('回到当前版本 v2');
    expect(html).toContain('sha256:aaaabbbbccccdddd');
    expect(html).toContain('v2');
    expect(html).toContain('v1');
  });

  it('未选中旧版本时渲染关系列表与端点未解析标记', () => {
    const html = renderToStaticMarkup(
      <DocumentDetailView
        detail={documentDetail()}
        selectedVersion={null}
        onSelectVersion={() => undefined}
        onSelectEvidence={() => undefined}
      />,
    );
    expect(html).toContain('entity:order');
    expect(html).toContain('constrained_by');
    expect(html).toContain('端点未解析');
    expect(html).toContain('payment-settlement');
    expect(html).toContain('# 退款窗口');
  });
});

describe('资料库管理页 · 证据', () => {
  it('证据详情展示定位、摘录、commit / 分支 / dirty 与内容摘要', () => {
    const html = renderToStaticMarkup(<EvidenceDetailCard evidence={evidence()} />);
    expect(html).toContain('可取用');
    expect(html).toContain('line_range');
    expect(html).toContain('path=src/order/refund.go');
    expect(html).toContain('start_line=42');
    expect(html).toContain('end_line=58');
    expect(html).toContain('9f2c1a7b0d3e4f5a6b7c8d9e0f1a2b3c4d5e6f70');
    expect(html).toContain('main');
    expect(html).toContain('包含未提交改动');
    expect(html).toContain('aabbccddeeff001122334455');
    expect(html).toContain('sha256');
    expect(html).toContain('2.0 KB');
    expect(html).toContain('ErrRefundWindowClosed');
  });

  it('证据 chip 是带可访问名的按钮：点击后由页面按 evidence_id 打开详情', () => {
    const selected: string[] = [];
    const html = renderToStaticMarkup(
      <DocumentDetailView
        detail={documentDetail()}
        selectedVersion={null}
        onSelectVersion={() => undefined}
        onSelectEvidence={(id) => selected.push(id)}
      />,
    );
    expect(html).toContain('aria-label="查看证据 ev_refund_rule（支持）"');

    // 渲染层无法派发点击（node 环境无 DOM），改由 chip 的回调契约证明打开路径。
    const onSelectEvidence = (id: string) => selected.push(id);
    onSelectEvidence('ev_refund_rule');
    expect(selected).toEqual(['ev_refund_rule']);
  });

  it('定位与摘要工具函数在缺字段时给出可读回退', () => {
    expect(formatLocator(undefined)).toBe('无定位信息');
    expect(formatLocator({})).toBe('无定位信息');
    expect(formatLocator({ path: 'a.go', line: 3 })).toBe('path=a.go · line=3');
    expect(formatBytes(512)).toBe('512 B');
    expect(formatBytes(2048)).toBe('2.0 KB');
    expect(scopeText({ conditions: [], environments: [] })).toBe('未限定适用范围');
  });
});

describe('资料库管理页 · 队列', () => {
  it('阻塞任务显示状态、尝试次数、阻塞原因与最后错误，并给出重试入口', () => {
    const html = renderToStaticMarkup(
      <TasksTable
        tasks={[blockedTask(), blockedTask({ id: 'task_10', seq: 10, status: 'queued', blocked_reason: '', last_error: '', attempt: 0, repair_attempt: 0 })]}
        busyTaskId={null}
        onOpen={() => undefined}
        onRetry={() => undefined}
        onCancel={() => undefined}
      />,
    );
    expect(html).toContain('已阻塞');
    expect(html).toContain('排队中');
    expect(html).toContain('#9');
    expect(html).toContain('2/3');
    expect(html).toContain('修复 2/2');
    expect(html).toContain('连续两轮修复仍无法通过校验，需要人工处理来源登记');
    expect(html).toContain('assertion 引用了不存在的 evidence key: ev_missing');
    expect(html).toContain('重试');
    expect(html).toContain('详情');
  });

  it('发布表展示计数、覆盖缺口与取代状态', () => {
    const html = renderToStaticMarkup(
      <ReleasesTable
        releases={[
          release(),
          release({
            id: 'rel_2',
            seq: 2,
            status: 'superseded',
            coverage: { sources_read: ['订单服务'], sources_missed: ['支付网关'], gaps: ['缺少退款失败路径'] },
          }),
        ]}
      />,
    );
    expect(html).toContain('当前发布');
    expect(html).toContain('已被取代');
    expect(html).toContain('#3');
    expect(html).toContain('4 / 9 / 6 / 12');
    expect(html).toContain('缺口：支付网关 · 缺少退款失败路径');
    expect(coverageLine({ sources_read: ['a'], sources_missed: [], notes: '' })).toBe('已读来源 1 · 已报告无缺口');
    // 空覆盖对象不是“已验证无缺口”：未报告过的覆盖必须显示为未知。
    expect(coverageLine({}, 'not_computed')).toBe('覆盖未知（资料库没有记录本轮读了哪些来源）');
    expect(coverageLine(undefined, 'pending')).toBe('尚未收到覆盖报告（覆盖未知）');
    expect(coverageLine({ sources_read: ['a'] }, 'in_progress')).toBe('统计中（任务仍在处理，覆盖范围未定）');
  });
});

describe('资料库管理页 · 查询', () => {
  it('查询结果渲染覆盖状态、截断、新鲜度、未知与句柄', () => {
    const html = renderToStaticMarkup(
      <QueryPanel
        releases={{ kind: 'ready', value: { items: [release()] } }}
        state={{ kind: 'ready', value: queryResponse() }}
        expanded={{}}
        onQuery={() => undefined}
        onSelectEvidence={() => undefined}
        onExpand={() => undefined}
        onRefresh={() => undefined}
      />,
    );
    expect(html).toContain('部分覆盖');
    expect(html).toContain('结果被截断');
    expect(html).toContain('已有更新的发布');
    expect(html).toContain('待处理事件 2');
    expect(html).toContain('还有 2 个事件没有处理完');
    expect(html).toContain('来源已过期：支付网关');
    expect(html).toContain('订单服务只读到已提交字节');
    expect(html).toContain('跨境订单是否适用 14 天窗口尚未采集到证据。');
    expect(html).toContain('扫描版本 37');
    expect(html).toContain('证据 · 退款规则');
    expect(html).toContain('退款窗口规则');
    expect(html).toContain('相关度 0.812');
    expect(html).toContain('固定发布 #3 · rel_3');
  });

  it('未发布版本时渲染「尚未就绪」空态而不是崩溃', () => {
    // 回归：真实接口在无已发布版本时返回 release=null 且所有列表为空数组；
    // 页面必须保留在「尚未就绪」状态，不能因为空响应白屏。
    const notReady: QueryResponse = {
      release: null,
      results: [],
      coverage: { status: 'not_ready', truncated: false, scanned_versions: 0, notes: ['资料库尚未发布任何版本，初始化任务完成后才能查询。'] },
      freshness: { pending_events: 2, newer_release_available: false, stale_sources: [] },
      unknowns: [],
      expand_handles: [],
    };
    const html = renderToStaticMarkup(
      <QueryPanel
        releases={{ kind: 'ready', value: { items: [] } }}
        state={{ kind: 'ready', value: notReady }}
        expanded={{}}
        onQuery={() => undefined}
        onSelectEvidence={() => undefined}
        onExpand={() => undefined}
        onRefresh={() => undefined}
      />,
    );
    expect(html).toContain('资料尚未就绪');
    expect(html).toContain('这个资料库还没有发布任何版本');
    expect(html).toContain('资料库尚未发布任何版本，初始化任务完成后才能查询。');
    expect(html).toContain('待处理事件 2');
    expect(html).not.toContain('部分覆盖');
  });

  it('后端返回 null 列表时前端归一化为空数组', async () => {
    // 客户端归一化是白屏回归的第二道防线：即使服务端给出 null，
    // QueryPanel 也必须拿到真正的数组。
    const { normalizeQueryResponse } = await import('../api/knowledge-library');
    const normalized = normalizeQueryResponse({
      release: null,
      results: null,
      coverage: { status: 'not_ready', truncated: null, scanned_versions: null, notes: null },
      freshness: { pending_events: null, newer_release_available: null, stale_sources: null },
      unknowns: null,
      expand_handles: null,
    } as unknown as QueryResponse);
    expect(normalized.results).toEqual([]);
    expect(normalized.coverage.notes).toEqual([]);
    expect(normalized.freshness.stale_sources).toEqual([]);
    expect(normalized.unknowns).toEqual([]);
    expect(normalized.expand_handles).toEqual([]);
    expect(normalized.coverage.truncated).toBe(false);
    expect(normalized.coverage.scanned_versions).toBe(0);
  });

  it('未发起查询时给出空态而不是伪造结果', () => {
    const html = renderToStaticMarkup(
      <QueryPanel
        releases={{ kind: 'ready', value: { items: [] } }}
        state={{ kind: 'idle' }}
        expanded={{}}
        onQuery={() => undefined}
        onSelectEvidence={() => undefined}
        onExpand={() => undefined}
        onRefresh={() => undefined}
      />,
    );
    expect(html).toContain('还没有查询结果');
    expect(html).not.toContain('部分覆盖');
  });
});

describe('资料库管理页 · 制品解析状态', () => {
  it('没有解析依据时明确说明会按登记声明值保存', () => {
    expect(effectiveResolutionLabel('resolved', '')).toContain('没有解析依据');
    expect(effectiveResolutionLabel('resolved', 'build:42')).toContain('必须填写可核查的解析依据');
  });

  it('resolved 行必须能填写解析依据，且依据随请求体回传', () => {
    const draft: SourceDraft = {
      ...emptySourceDraft(),
      name: 'common',
      kind: 'common',
      repo_path: '/repos/common',
      usages: [
        { consumer: 'order-service', artifact: 'com.example:common:1.4.2', environment: '', artifact_resolution: 'resolved', resolution_ref: 'mvn-dependency-tree:order/tree.txt' },
      ],
    };
    const html = renderToStaticMarkup(
      <SourceForm draft={draft} onChange={() => undefined} onSubmit={() => undefined} busy={false} submitLabel="保存来源" />,
    );
    expect(html).toContain('解析依据');
    const payload = sourceCreatePayload(draft);
    expect(payload.usages?.[0]?.resolution_ref).toBe('mvn-dependency-tree:order/tree.txt');
    expect(payload.usages?.[0]?.artifact_resolution).toBe('resolved');
  });

  it('没有依据的 resolved 不会凭空升级：请求体不带依据，服务端会降级', () => {
    const draft: SourceDraft = {
      ...emptySourceDraft(),
      name: 'common',
      kind: 'common',
      repo_path: '/repos/common',
      usages: [
        { consumer: 'a', artifact: 'g:a:1', environment: '', artifact_resolution: 'resolved', resolution_ref: '  ' },
      ],
    };
    const payload = sourceCreatePayload(draft);
    expect(payload.usages?.[0]?.resolution_ref).toBeUndefined();
    expect(effectiveResolutionLabel('resolved', '  ')).toContain('登记声明值');
  });
});
