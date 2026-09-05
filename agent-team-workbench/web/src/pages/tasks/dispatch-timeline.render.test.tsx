import { renderToStaticMarkup } from 'react-dom/server';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import type { DispatchCard } from '../../api/types';
import type { ContentBlockDocument } from '../../utils/content-blocks';

const dispatchStoreState = vi.hoisted(() => ({
  byWorkItem: {} as Record<string, DispatchCard[]>,
  errorByWorkItem: {} as Record<string, string | undefined>,
  refreshFor: vi.fn(),
}));
const runStoreState = vi.hoisted(() => ({
  runs: {} as Record<string, unknown>,
  timelines: {} as Record<string, unknown[]>,
  historyErrors: {} as Record<string, string | undefined>,
  historyLoading: {} as Record<string, boolean>,
  loadHistory: vi.fn(),
  fetchRun: vi.fn(),
  watchRun: vi.fn(),
  unwatchRun: vi.fn(),
}));

vi.mock('../../stores/dispatches.store', () => ({
  useDispatchesStore: (selector: (state: typeof dispatchStoreState) => unknown) => selector(dispatchStoreState),
}));
vi.mock('../../stores/runs.store', () => ({
  useRunsStore: (selector: (state: typeof runStoreState) => unknown) => selector(runStoreState),
}));

import { DispatchTimeline, TaskAgentOutputCard, taskAgentRuns } from './dispatch-timeline';
import { projectTaskRunIO, TaskRunOutput } from './task-run-output';

const card = (overrides: Partial<DispatchCard> = {}): DispatchCard => ({
  id: 'disp_1',
  work_item_id: 'wi_1',
  trigger: 'user_message',
  status: 'running',
  runs: [
    {
      id: 'run_1',
      work_item_id: 'wi_1',
      agent_profile_id: 'agent_1',
      agent_name: 'Task Coordinator',
      role: 'coordinator',
      status: 'succeeded',
      summary: '系统正在汇总',
    },
    {
      id: 'run_2',
      work_item_id: 'wi_2',
      agent_profile_id: 'agent_2',
      agent_name: '阿评',
      role: 'worker',
      status: 'running',
      summary: '评审任务',
    },
  ],
  created_at: '2026-08-30T01:02:03Z',
  ...overrides,
});

const runCreated = (instruction: string) => ({
  event_id: 'e1', stream_seq: 1, run_seq: 1, type: 'run.created',
  occurred_at: '2026-08-30T01:00:00Z', data: { instruction },
});

const completed = (
  id: string,
  seq: number,
  text: string,
  contentBlocks?: ContentBlockDocument,
  agentId = 'main',
) => ({
  event_id: id, stream_seq: seq, run_seq: seq, type: 'message.completed', role: 'assistant',
  agent_id: agentId,
  occurred_at: `2026-08-30T01:0${seq}:00Z`, text,
  data: { role: 'assistant', ...(contentBlocks ? { content_blocks: contentBlocks } : {}) },
});

describe('DispatchTimeline', () => {
  beforeEach(() => {
    dispatchStoreState.byWorkItem = {};
    dispatchStoreState.errorByWorkItem = {};
    dispatchStoreState.refreshFor.mockClear();
    runStoreState.runs = {};
    runStoreState.timelines = {};
    runStoreState.historyErrors = {};
    runStoreState.historyLoading = {};
    runStoreState.fetchRun.mockClear();
    runStoreState.watchRun.mockClear();
    runStoreState.unwatchRun.mockClear();
  });

  it('直接展示 Worker 输入输出，隐藏 Coordinator、派发批次与展开交互', () => {
    dispatchStoreState.byWorkItem = {
      wi_1: [card({ trigger_message: { run_id: 'run_0', excerpt: '帮我出一版方案' } })],
    };
    const html = renderToStaticMarkup(<DispatchTimeline taskId="wi_1" workspaceId="ws_1" />);
    expect(html).toContain('Agent 输入与输出');
    expect(html).toContain('阿评');
    expect(html).toContain('输入');
    expect(html).toContain('最终输出');
    expect(html).not.toContain('Task Coordinator');
    expect(html).not.toContain('aria-expanded');
    expect(html).not.toContain('派发时间线');
    expect(html).not.toContain('帮我出一版方案');
    expect(html).not.toContain('2 个会话');
  });

  it('跨批次按子任务去重，保留新批次内最新 Worker 尝试', () => {
    const older = card({
      id: 'disp_old',
      created_at: '2026-08-29T00:00:00Z',
      runs: [{ ...card().runs[1], id: 'run_old' }],
    });
    const newer = card({
      id: 'disp_new',
      created_at: '2026-08-30T00:00:00Z',
      runs: [
        card().runs[0],
        { ...card().runs[1], id: 'run_first' },
        { ...card().runs[1], id: 'run_retry', status: 'succeeded' },
        { ...card().runs[0], id: 'run_eval', role: 'evaluation' },
      ],
    });
    expect(taskAgentRuns([newer, older], 'wi_1').map((item) => item.run.id)).toEqual(['run_retry']);
  });

  it('未拉取、空列表与失败分别呈现明确状态', () => {
    expect(renderToStaticMarkup(<DispatchTimeline taskId="wi_1" workspaceId="ws_1" />)).toContain('执行记录加载中…');

    dispatchStoreState.byWorkItem = { wi_1: [] };
    const empty = renderToStaticMarkup(<DispatchTimeline taskId="wi_1" workspaceId="ws_1" />);
    expect(empty).toContain('等待执行 Agent 输出');

    dispatchStoreState.errorByWorkItem = { wi_1: '派发记录加载失败，请重试' };
    const failed = renderToStaticMarkup(<DispatchTimeline taskId="wi_1" workspaceId="ws_1" />);
    expect(failed).toContain('role="alert"');
    expect(failed).toContain('派发记录加载失败，请重试');
    expect(failed).not.toContain('等待执行 Agent 输出');
  });

  it('Agent 卡直接渲染身份、状态、输入和最终输出', () => {
    const run = { ...card().runs[1], status: 'succeeded' as const };
    runStoreState.timelines = {
      [run.id]: [runCreated('执行 worker 任务'), completed('e2', 2, '最终结果')],
    };
    const html = renderToStaticMarkup(<TaskAgentOutputCard run={run} createdAt="2026-08-30T01:02:03Z" />);
    expect(html).toContain('阿评');
    expect(html).toContain('已成功');
    expect(html).toContain('执行 worker 任务');
    expect(html).toContain('最终结果');
    expect(html).not.toContain('aria-expanded');
  });
});

describe('TaskRunOutput', () => {
  beforeEach(() => {
    runStoreState.runs = {};
    runStoreState.timelines = {};
    runStoreState.historyErrors = {};
    runStoreState.historyLoading = {};
  });

  it('历史请求失败显示错误和当前 Agent 重试入口，不伪装为加载中', () => {
    runStoreState.historyErrors = { run_1: '输入与结果加载失败，请重试' };
    const html = renderToStaticMarkup(<TaskRunOutput run={card().runs[0]} agentName="Nova" />);
    expect(html).toContain('role="alert"');
    expect(html).toContain('输入与结果加载失败，请重试');
    expect(html).toContain('重试加载 Nova 的输入与输出');
    expect(html).not.toContain('加载中');
    expect(html).not.toContain('未返回可展示的最终结果');
  });

  it('成功请求但没有正文时显示空结果，区别于网络错误', () => {
    runStoreState.timelines = { run_1: [] };
    const html = renderToStaticMarkup(<TaskRunOutput run={card().runs[0]} agentName="Nova" />);
    expect(html).toContain('本次执行未返回可展示的最终结果');
    expect(html).not.toContain('重试加载');
  });

  it('成功 run 只展示输入和最后一条 assistant completed', () => {
    runStoreState.runs = {
      run_1: {
        id: 'run_1', work_item_id: 'wi_1', agent_profile_id: 'agent_1', status: 'succeeded',
        version: 1, created_at: '2026-08-30T01:00:00Z', updated_at: '2026-08-30T01:03:00Z',
      },
    };
    runStoreState.timelines = {
      run_1: [runCreated('执行 worker 任务'), completed('e2', 2, '阶段性说明'), completed('e3', 3, '最终交付结果')],
    };

    const html = renderToStaticMarkup(<TaskRunOutput run={card().runs[0]} agentName="小明" />);
    expect(html).toContain('输入');
    expect(html).toContain('执行 worker 任务');
    expect(html).toContain('最终输出');
    expect(html).toContain('最终交付结果');
    expect(html).not.toContain('阶段性说明');
    expect(html).not.toContain('思考过程');
    expect(html).not.toContain('工具调用');
  });

  it('保留最终输出的 canonical ContentBlocks', () => {
    const document: ContentBlockDocument = {
      version: 'languagegui/v1',
      blocks: [{ type: 'metric', title: 'Worker 指标', items: [{ label: '质量', value: '通过', tone: 'neutral' }] }],
    };
    runStoreState.runs = {
      run_1: {
        id: 'run_1', work_item_id: 'wi_1', status: 'succeeded', version: 1,
        created_at: '2026-08-30T01:00:00Z', updated_at: '2026-08-30T01:03:00Z',
      },
    };
    runStoreState.timelines = { run_1: [runCreated('输入'), completed('e2', 2, '最终正文', document)] };
    const html = renderToStaticMarkup(<TaskRunOutput run={card().runs[0]} agentName="小明" />);
    expect(html).toContain('最终正文');
    expect(html).toContain('data-content-block="metric"');
    expect(html).toContain('Worker 指标');
  });

  it('run 未成功时不把 completed 阶段说明冒充最终结果', () => {
    const timeline = [runCreated('检查实现'), completed('e2', 2, '我先检查文件')];
    expect(projectTaskRunIO(timeline, 'running')).toEqual({ input: '检查实现', output: '' });

    runStoreState.runs = {
      run_1: {
        id: 'run_1', work_item_id: 'wi_1', status: 'running', version: 1,
        created_at: '2026-08-30T01:00:00Z', updated_at: '2026-08-30T01:03:00Z',
      },
    };
    runStoreState.timelines = { run_1: timeline };
    const html = renderToStaticMarkup(<TaskRunOutput run={card().runs[0]} agentName="小明" />);
    expect(html).toContain('Agent 完成执行后在这里显示最终结果');
    expect(html).not.toContain('我先检查文件');
  });

  it('忽略同一 Run 中较晚的子 Agent completed，只取 main 最终结果', () => {
    const projection = projectTaskRunIO([
      runCreated('汇总团队结果'),
      completed('e2', 2, '主 Agent 最终交付'),
      completed('e3', 3, '子 Agent 后到结果', undefined, 'subagent_1'),
    ], 'succeeded');
    expect(projection.output).toBe('主 Agent 最终交付');
  });
});
