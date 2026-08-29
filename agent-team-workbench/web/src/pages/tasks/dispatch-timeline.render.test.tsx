import { renderToStaticMarkup } from 'react-dom/server';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import type { DispatchCard } from '../../api/types';

// renderToStaticMarkup 走 useSyncExternalStore 的 server snapshot（zustand 初始态），
// setState 注入不可见：直接 mock store，让 fixture 可控。
const storeState = vi.hoisted(() => ({
  byWorkItem: {} as Record<string, DispatchCard[]>,
  refreshFor: vi.fn(),
}));
vi.mock('../../stores/dispatches.store', () => ({
  useDispatchesStore: (selector: (s: typeof storeState) => unknown) => selector(storeState),
}));

import { DispatchCardItem, DispatchRunRow, DispatchTimeline } from './dispatch-timeline';

const noop = () => undefined;

const card = (over: Partial<DispatchCard> = {}): DispatchCard => ({
  id: 'disp_1',
  work_item_id: 'wi_1',
  trigger: 'user_message',
  status: 'running',
  runs: [
    {
      id: 'run_1',
      work_item_id: 'wi_1',
      agent_profile_id: 'agent_1',
      agent_name: '小明',
      status: 'succeeded',
      summary: '完成初稿并自检',
    },
    {
      id: 'run_2',
      work_item_id: 'wi_2',
      agent_profile_id: 'agent_2',
      agent_name: '阿评',
      status: 'running',
      summary: '评审中',
    },
  ],
  created_at: '2026-08-30T01:02:03Z',
  ...over,
});

describe('DispatchTimeline', () => {
  beforeEach(() => {
    storeState.byWorkItem = {};
    storeState.refreshFor.mockClear();
  });

  it('渲染派发卡片：触发摘录、状态胶囊、会话数与展开语义（默认折叠）', () => {
    storeState.byWorkItem = {
      wi_1: [card({ trigger_message: { run_id: 'run_0', excerpt: '帮我出一版方案' } })],
    };
    const html = renderToStaticMarkup(<DispatchTimeline taskId="wi_1" onOpenRunChat={noop} />);
    expect(html).toContain('派发时间线');
    expect(html).toContain('用户消息');
    expect(html).toContain('帮我出一版方案');
    expect(html).toContain('2 个会话');
    expect(html).toContain('运行中');
    expect(html).toContain('aria-expanded="false"');
    // 成员行默认折叠不渲染，折叠态不出现成员摘要与对话入口。
    expect(html).not.toContain('完成初稿并自检');
  });

  it('未拉取与空列表分别呈现加载与空态', () => {
    const loading = renderToStaticMarkup(<DispatchTimeline taskId="wi_1" onOpenRunChat={noop} />);
    expect(loading).toContain('派发加载中…');

    storeState.byWorkItem = { wi_1: [] };
    const empty = renderToStaticMarkup(<DispatchTimeline taskId="wi_1" onOpenRunChat={noop} />);
    expect(empty).toContain('尚无派发记录');
    expect(empty).toContain('向该任务发送第一条消息后');
  });

  it('成员行渲染：名字、run 状态、一行摘要与对话入口', () => {
    const html = renderToStaticMarkup(<DispatchRunRow run={card().runs[0]} onOpenRunChat={noop} />);
    expect(html).toContain('小明');
    expect(html).toContain('已成功');
    expect(html).toContain('完成初稿并自检');
    expect(html).toContain('aria-label="小明"'); // Avatar 可访问名
    expect(html).toContain('与 小明 的会话');
  });

  it('未绑定 agent 的成员行禁用对话入口', () => {
    const run = { ...card().runs[0], agent_profile_id: undefined, agent_name: undefined };
    const html = renderToStaticMarkup(<DispatchRunRow run={run} onOpenRunChat={noop} />);
    expect(html).toContain('未指派');
    expect(html).toContain('disabled=""');
    expect(html).toContain('成员未绑定 agent');
  });

  it('降级批次用 error 语义状态，触发文案按枚举回落', () => {
    const html = renderToStaticMarkup(
      <DispatchCardItem
        card={card({ status: 'degraded', trigger: 'wakeup', trigger_message: undefined })}
        onOpenRunChat={noop}
      />,
    );
    expect(html).toContain('已降级');
    expect(html).toContain('bg-status-error');
    expect(html).toContain('唤醒');
    // 无 trigger_message 时回落首个成员摘要。
    expect(html).toContain('完成初稿并自检');
  });
});
