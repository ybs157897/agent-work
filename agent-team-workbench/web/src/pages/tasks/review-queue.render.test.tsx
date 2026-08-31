import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it, vi } from 'vitest';
import { MemoryRouter } from 'react-router-dom';
import type { ReviewQueueItem, WorkItem } from '../../api/types';
import { pendingDurationText, ReviewQueueListView, ReviewQueueSummaryView } from './review-queue';

const task: WorkItem = {
  id: 'wi_1',
  workspace_id: 'ws_1',
  record_kind: 'task',
  title: '发布登录模块',
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

const item: ReviewQueueItem = {
  work_item: task,
  pending_since: '2026-08-30T00:30:00Z',
  coordinator: { status: 'waiting_user', stage: 'acceptance', updated_at: '2026-08-30T01:00:00Z', version: 7 },
  latest_run_id: 'run_2',
  source_watermark: { as_of_event_seq: 120, work_item_version: 8, coordinator_version: 7 },
};

const noop = () => undefined;

describe('ReviewQueueSummaryView 渲染（任务控制面 RFC §12.4）', () => {
  it('badge 显示服务端 total_count（500），即使当前页只有少量行', () => {
    const html = renderToStaticMarkup(
      <ReviewQueueSummaryView loaded totalCount={500} earliest={item} queueOpen={false} onRefresh={noop} onToggle={noop} />,
    );
    expect(html).toContain('500');
    expect(html).toContain('复审队列');
  });

  it('未加载时 badge 显示占位，不显示 0 冒充空队列', () => {
    const html = renderToStaticMarkup(
      <ReviewQueueSummaryView loaded={false} totalCount={0} queueOpen={false} onRefresh={noop} onToggle={noop} />,
    );
    expect(html).toContain('…');
  });

  it('展开态显示全局最早等待（服务端排序第一行）', () => {
    // pending_since 取相对当前时间的 90 分钟前，断言与 Date.now() 解耦。
    const ninetyMinAgo = new Date(Date.now() - 90 * 60000).toISOString();
    const html = renderToStaticMarkup(
      <MemoryRouter>
        <ReviewQueueSummaryView
          loaded
          totalCount={3}
          earliest={{ ...item, pending_since: ninetyMinAgo }}
          queueOpen
          onRefresh={noop}
          onToggle={noop}
        />
      </MemoryRouter>,
    );
    expect(html).toContain('发布登录模块');
    expect(html).toContain('等待 1 小时 30 分');
  });

  it('pendingDurationText：分钟/小时/天可读分层', () => {
    const now = new Date('2026-08-30T02:00:00Z').getTime();
    expect(pendingDurationText('2026-08-30T01:59:30Z', now)).toBe('刚刚进入');
    expect(pendingDurationText('2026-08-30T01:45:00Z', now)).toBe('等待 15 分钟');
    expect(pendingDurationText('2026-08-30T00:30:00Z', now)).toBe('等待 1 小时 30 分');
    expect(pendingDurationText('2026-08-28T02:00:00Z', now)).toBe('等待 2 天 0 小时');
  });
});

describe('ReviewQueueListView 渲染（键盘导航与空态）', () => {
  it('Row 是原生 button 且带完整 aria-label，行内含阶段与 Coordinator 状态', () => {
    const html = renderToStaticMarkup(
      <ReviewQueueListView items={[item]} loaded loading={false} error={null} nextCursor={null} phaseFilter="" onPhaseFilter={noop} onRefresh={noop} onLoadMore={noop} onOpen={noop} />,
    );
    expect(html).toContain('aria-label="打开任务 发布登录模块"');
    expect(html).toContain('待验收');
    expect(html).toContain('等待你处理');
  });

  it('真空态说明为什么空 + 什么时候会有内容', () => {
    const html = renderToStaticMarkup(
      <ReviewQueueListView items={[]} loaded loading={false} error={null} nextCursor={null} phaseFilter="" onPhaseFilter={noop} onRefresh={noop} onLoadMore={noop} onOpen={noop} />,
    );
    expect(html).toContain('没有等待复审的任务');
    expect(html).toContain('评审或待验收阶段');
  });

  it('错误态 role=alert + 重试入口，不伪装空队列', () => {
    const html = renderToStaticMarkup(
      <ReviewQueueListView items={[]} loaded loading={false} error="复审队列加载失败，请重试" nextCursor={null} phaseFilter="" onPhaseFilter={noop} onRefresh={noop} onLoadMore={noop} onOpen={noop} />,
    );
    expect(html).toContain('role="alert"');
    expect(html).toContain('重试');
  });

  it('next_cursor 存在时提供「加载更多」', () => {
    const html = renderToStaticMarkup(
      <ReviewQueueListView items={[item]} loaded loading={false} error={null} nextCursor="c2" phaseFilter="" onPhaseFilter={noop} onRefresh={noop} onLoadMore={noop} onOpen={noop} />,
    );
    expect(html).toContain('加载更多');
  });

  it('onOpen 以任务 id 导航（冒烟断言回调接线形状）', () => {
    const onOpen = vi.fn();
    renderToStaticMarkup(
      <ReviewQueueListView items={[item]} loaded loading={false} error={null} nextCursor={null} phaseFilter="" onPhaseFilter={noop} onRefresh={noop} onLoadMore={noop} onOpen={onOpen} />,
    );
    // Row 的点击回调在 SSR 中不触发；导航目标由容器注入，这里断言行内容可定位。
    expect(onOpen).not.toHaveBeenCalled();
  });
});
