import { renderToStaticMarkup } from 'react-dom/server';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import type { DispatchCard } from '../../api/types';
import type { ContentBlockDocument } from '../../utils/content-blocks';

// renderToStaticMarkup 走 useSyncExternalStore 的 server snapshot（zustand 初始态），
// setState 注入不可见：直接 mock store，让 fixture 可控。
const storeState = vi.hoisted(() => ({
  byWorkItem: {} as Record<string, DispatchCard[]>,
  errorByWorkItem: {} as Record<string, string | undefined>,
  refreshFor: vi.fn(),
}));
const runStoreState = vi.hoisted(() => ({
  runs: {} as Record<string, unknown>,
  timelines: {} as Record<string, unknown[]>,
  approvals: {} as Record<string, unknown[]>,
  watchRun: vi.fn(),
  unwatchRun: vi.fn(),
}));
vi.mock('../../stores/dispatches.store', () => ({
  useDispatchesStore: (selector: (s: typeof storeState) => unknown) => selector(storeState),
}));
vi.mock('../../stores/runs.store', () => ({
  useRunsStore: (selector: (s: typeof runStoreState) => unknown) => selector(runStoreState),
}));

import { AgentOutput } from '../../components/chat/agent-output';
import { AssistantTurn } from '../../components/chat/assistant-turn';
import { DispatchCardItem, DispatchRunRow, DispatchTimeline } from './dispatch-timeline';
import { TaskRunOutput } from './task-run-output';

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
    storeState.errorByWorkItem = {};
    storeState.refreshFor.mockClear();
    runStoreState.runs = {};
    runStoreState.timelines = {};
    runStoreState.approvals = {};
    runStoreState.watchRun.mockClear();
    runStoreState.unwatchRun.mockClear();
  });

  it('渲染派发卡片：触发摘录、状态胶囊、会话数与展开语义（默认折叠）', () => {
    storeState.byWorkItem = {
      wi_1: [card({ trigger_message: { run_id: 'run_0', excerpt: '帮我出一版方案' } })],
    };
    const html = renderToStaticMarkup(<DispatchTimeline taskId="wi_1" workspaceId="ws_1" />);
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
    const loading = renderToStaticMarkup(<DispatchTimeline taskId="wi_1" workspaceId="ws_1" />);
    expect(loading).toContain('派发加载中…');

    storeState.byWorkItem = { wi_1: [] };
    const empty = renderToStaticMarkup(<DispatchTimeline taskId="wi_1" workspaceId="ws_1" />);
    expect(empty).toContain('尚无派发记录');
    expect(empty).toContain('向该任务发送第一条消息后');
  });

  it('成员行渲染：名字、run 状态、一行摘要与 Task 内正文入口', () => {
    const html = renderToStaticMarkup(<DispatchRunRow run={card().runs[0]} />);
    expect(html).toContain('小明');
    expect(html).toContain('已成功');
    expect(html).toContain('完成初稿并自检');
    expect(html).toContain('aria-label="小明"'); // Avatar 可访问名
    expect(html).toContain('查看 小明 的任务正文');
    expect(html).not.toContain('/chat');
  });

  it('TaskRunOutput 复用 AgentOutput 展示 Markdown 与 canonical ContentBlocks，不带 Chat 动作', () => {
    const document: ContentBlockDocument = {
      version: 'languagegui/v1',
      blocks: [{ type: 'metric', title: 'Worker 指标', items: [{ label: '质量', value: '通过', tone: 'neutral' }] }],
    };
    runStoreState.runs = {
      run_1: {
        id: 'run_1', work_item_id: 'wi_1', agent_profile_id: 'agent_1', status: 'succeeded',
        version: 1, created_at: '2026-08-30T01:00:00Z', updated_at: '2026-08-30T01:01:00Z',
      },
    };
    runStoreState.timelines = {
      run_1: [
        { event_id: 'e1', stream_seq: 1, run_seq: 1, type: 'run.created', occurred_at: '2026-08-30T01:00:00Z', data: { instruction: '执行 worker 任务' } },
        { event_id: 'e2', stream_seq: 2, run_seq: 2, type: 'message.completed', occurred_at: '2026-08-30T01:01:00Z', text: 'Worker 正文结果', data: { role: 'assistant', content_blocks: document } },
      ],
    };

    const html = renderToStaticMarkup(
      <TaskRunOutput
        run={{ ...card().runs[0], id: 'run_1' }}
        agentName="小明"
      />,
    );

    expect(html).toContain('Worker 正文结果');
    expect(html).toContain('data-content-block="metric"');
    expect(html).toContain('Worker 指标');
    expect(html).toContain('aria-label="复制"');
    expect(html).not.toContain('分叉对话');
    expect(html).not.toContain('钉为决策');
    expect(html).not.toContain('/chat');
  });

  it('AgentOutput 与 Chat AssistantTurn 共享同一正文/结构化块渲染', () => {
    const document: ContentBlockDocument = {
      version: 'languagegui/v1',
      blocks: [{ type: 'metric', title: '共享指标', items: [{ label: '结果', value: '通过', tone: 'neutral' }] }],
    };
    const output = renderToStaticMarkup(
      <AgentOutput text="正文内容" contentBlocks={document} runId="run_1" messageId="msg_1" />,
    );
    const chat = renderToStaticMarkup(
      <AssistantTurn text="正文内容" contentBlocks={document} agentName="小明" runId="run_1" messageId="msg_1" />,
    );
    expect(output).toContain('正文内容');
    expect(chat).toContain('正文内容');
    expect(output.match(/data-content-block="metric"/g)).toHaveLength(1);
    expect(chat.match(/data-content-block="metric"/g)).toHaveLength(1);
    expect(output).toContain('共享指标');
    expect(chat).toContain('共享指标');
  });

  it('未绑定 agent 的成员行禁用对话入口', () => {
    const run = { ...card().runs[0], agent_profile_id: undefined, agent_name: undefined };
    const html = renderToStaticMarkup(<DispatchRunRow run={run} />);
    expect(html).toContain('未指派');
    expect(html).toContain('disabled=""');
    expect(html).toContain('成员未绑定 agent');
  });

  it('降级批次用 error 语义状态，触发文案按枚举回落', () => {
    const html = renderToStaticMarkup(
      <DispatchCardItem
        card={card({ status: 'degraded', trigger: 'wakeup', trigger_message: undefined })}
      />,
    );
    expect(html).toContain('已降级');
    expect(html).toContain('bg-status-error');
    expect(html).toContain('唤醒');
    // 无 trigger_message 时回落首个成员摘要。
    expect(html).toContain('完成初稿并自检');
  });

  it('请求失败显示就地错误与重试入口，不伪装为空态或加载中', () => {
    storeState.errorByWorkItem = { wi_1: '派发记录加载失败，请重试' };
    const html = renderToStaticMarkup(<DispatchTimeline taskId="wi_1" workspaceId="ws_1" />);
    expect(html).toContain('role="alert"');
    expect(html).toContain('派发记录加载失败，请重试');
    expect(html).toContain('>重试</button>');
    expect(html).not.toContain('派发加载中…');
    expect(html).not.toContain('尚无派发记录');
  });
});
