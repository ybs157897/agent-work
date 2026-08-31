import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it, vi } from 'vitest';
import type { DeliveryBrief, WorkItem } from '../../api/types';
import { DeliveryBriefView } from './delivery-brief';

const workItem: WorkItem = {
  id: 'wi_root',
  workspace_id: 'ws_1',
  record_kind: 'task',
  title: '发布版本',
  description: '',
  status: 'in_progress',
  phase: 'acceptance',
  priority: 'high',
  due_date: null,
  runs_count: 2,
  version: 8,
  created_at: '2026-08-30T00:00:00Z',
  updated_at: '2026-08-30T01:00:00Z',
};

const brief: DeliveryBrief = {
  work_item: workItem,
  acceptance_criteria: ['登录成功', '错误有提示'],
  conclusion: {
    coordinator_status: 'waiting_user',
    stage: 'acceptance',
    summary: '全部步骤已执行，评估通过',
    next_action: '请验收',
    version: 7,
  },
  attempts: [
    { attempt: 1, role: 'worker', run_id: 'run_1', agent_name: 'Atlas', status: 'failed', started_at: null, finished_at: null, retry_of: null, failure: { code: 'tool_failed', message: '测试超时', retryable: true } },
    { attempt: 2, role: 'evaluation', run_id: 'run_2', agent_name: 'Eval', status: 'succeeded', started_at: null, finished_at: '2026-08-30T01:00:00Z', retry_of: 'run_1', failure: null },
  ],
  runs: [
    {
      run: { id: 'run_2', work_item_id: 'wi_root', status: 'succeeded', version: 3, created_at: '', updated_at: '' },
      summary: '评估通过',
      evidence: [
        { id: 'ev_1', source_kind: 'evaluation_verdict', source_id: 'evd_1', label: '评估结论：通过', status: 'passed', trust: 'control_plane', occurred_at: '2026-08-30T01:00:00Z' },
        { id: 'ev_2', source_kind: 'tool_result', source_id: 'evd_2', label: 'npm test', status: 'failed', trust: 'model_reported', occurred_at: '2026-08-30T00:50:00Z' },
      ],
      truncated: false,
    },
  ],
  changes: {
    run_id: 'run_2',
    files: [{ path: 'src/login.ts', added: 12, deleted: 2, status: 'modified' }],
    total_files: 1,
    total_added: 12,
    total_deleted: 2,
    truncated: false,
  },
  artifacts: [{ id: 'art_1', run_id: 'run_2', logical_path: 'reports/summary.md', mime: 'text/markdown', size: 2048, sha256: 'x', status: 'accepted', created_at: '' }],
  blocker: null,
  risks: [{ source_kind: 'review_finding', source_id: 'rf_1', code: 'coverage_gap', message: '边界用例未覆盖', severity: 'medium' }],
  comments: [
    { id: 'cmt_1', workspace_id: 'ws_1', root_work_item_id: 'wi_root', work_item_id: 'wi_root', revision: 2, kind: 'requirement', body: '补充错误提示', actor_kind: 'user', actor_id: 'u1', created_at: '2026-08-30T00:10:00Z' },
    { id: 'cmt_2', workspace_id: 'ws_1', root_work_item_id: 'wi_root', work_item_id: 'wi_root', revision: 3, kind: 'review_feedback', body: '验收标准第 2 条未达成', actor_kind: 'user', actor_id: 'u1', created_at: '2026-08-30T00:20:00Z' },
  ],
  freshness: { generated_at: '2026-08-30T01:05:00Z', as_of_event_seq: 120, state: 'current', missing_sources: [] },
  truncation: { attempts: false, runs: false, files: false, artifacts: false, comments: false },
};

const noop = () => undefined;

describe('DeliveryBriefView 渲染（任务控制面 RFC §12.5）', () => {
  it('current 态：验收标准、结论、尝试、证据、变更、产物、风险、评论齐备', () => {
    const html = renderToStaticMarkup(
      <DeliveryBriefView brief={brief} loading={false} error={undefined} stale={false} onRefresh={noop} />,
    );
    expect(html).toContain('登录成功');
    expect(html).toContain('等待你处理');
    expect(html).toContain('评估通过');
    expect(html).toContain('测试超时');
    expect(html).toContain('src/login.ts');
    expect(html).toContain('reports/summary.md');
    expect(html).toContain('边界用例未覆盖');
    expect(html).toContain('补充错误提示');
    expect(html).toContain('打回理由');
    expect(html).toContain('控制面'); // trust 三态
    expect(html).toContain('模型'); // model_reported 文案存在
  });

  it('loading 态显示加载中，不显示错误也不伪装空态', () => {
    const html = renderToStaticMarkup(
      <DeliveryBriefView brief={undefined} loading error={undefined} stale={false} onRefresh={noop} />,
    );
    expect(html).toContain('交付简报加载中');
    expect(html).toContain('role="status"');
  });

  it('error 态 role=alert + 重试入口，无旧内容时不显示空态文案', () => {
    const html = renderToStaticMarkup(
      <DeliveryBriefView brief={undefined} loading={false} error="无权限查看该任务" stale={false} onRefresh={noop} />,
    );
    expect(html).toContain('role="alert"');
    expect(html).toContain('重试');
    expect(html).not.toContain('暂无交付简报');
  });

  it('stale 态显示「可能有更新」水印并保留全部旧内容', () => {
    const html = renderToStaticMarkup(
      <DeliveryBriefView brief={brief} loading={false} error={undefined} stale onRefresh={noop} />,
    );
    expect(html).toContain('可能有更新');
    expect(html).toContain('立即刷新');
    expect(html).toContain('评估通过'); // 旧内容完整保留，不清空
  });

  it('partial 态展示缺失来源数量', () => {
    const partial: DeliveryBrief = {
      ...brief,
      freshness: { ...brief.freshness, state: 'partial', missing_sources: ['evaluation_verdict'] },
    };
    const html = renderToStaticMarkup(
      <DeliveryBriefView brief={partial} loading={false} error={undefined} stale={false} onRefresh={noop} />,
    );
    expect(html).toContain('部分来源缺失');
    expect(html).toContain('evaluation_verdict');
  });

  it('empty 只表示真的无证据（服务端聚合为空）', () => {
    const emptyBrief: DeliveryBrief = {
      ...brief,
      attempts: [], runs: [], changes: null, artifacts: [], risks: [], comments: [],
    };
    const html = renderToStaticMarkup(
      <DeliveryBriefView brief={emptyBrief} loading={false} error={undefined} stale={false} onRefresh={noop} />,
    );
    expect(html).toContain('还没有可验收的执行证据');
  });

  it('stale + 补拉失败同时存在：旧内容、更新提示与错误三通道并存', () => {
    const html = renderToStaticMarkup(
      <DeliveryBriefView brief={brief} loading={false} error="简报补拉失败，当前内容可能不是最新" stale onRefresh={noop} />,
    );
    expect(html).toContain('可能有更新');
    expect(html).toContain('简报补拉失败');
    expect(html).toContain('src/login.ts');
  });

  it('onRefresh 接线形状冒烟', async () => {
    const onRefresh = vi.fn();
    renderToStaticMarkup(
      <DeliveryBriefView brief={brief} loading={false} error={undefined} stale onRefresh={onRefresh} />,
    );
    expect(onRefresh).not.toHaveBeenCalled(); // SSR 不触发事件，仅断言渲染不抛错
  });
});
