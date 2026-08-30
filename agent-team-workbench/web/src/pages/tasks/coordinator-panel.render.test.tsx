import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it } from 'vitest';
import type { CoordinatorSnapshot, WorkItem } from '../../api/types';
import { CoordinatorAttempts, CoordinatorPanel, CoordinatorTimeline, coordinatorStageText, coordinatorStatusText } from './coordinator-panel';

const task: WorkItem = {
  id: 'wi_root',
  workspace_id: 'ws_1',
  record_kind: 'task',
  title: '发布版本',
  description: '完成发布流程',
  status: 'in_progress',
  phase: 'execution',
  priority: 'high',
  due_date: null,
  runs_count: 2,
  version: 4,
  created_at: '2026-08-30T01:00:00Z',
  updated_at: '2026-08-30T01:02:00Z',
};

const snapshot: CoordinatorSnapshot = {
  root_work_item_id: 'wi_root',
  work_item_id: 'wi_root',
  status: 'waiting_retry',
  stage: 'retry',
  progress: 0.5,
  completed_steps: 1,
  total_steps: 2,
  current_agent: { id: 'agent_atlas', name: 'Atlas', role: '开发' },
  next_action: '退避后重试当前步骤',
  next_action_at: '2026-08-30T01:03:00Z',
  attempts: [
    {
      id: 'attempt_2',
      run_id: 'run_2',
      agent: { id: 'agent_atlas', name: 'Atlas' },
      status: 'waiting_retry',
      attempt: 2,
      max_attempts: 4,
      started_at: '2026-08-30T01:02:00Z',
      failure: { code: 'timeout', message: 'Worker 响应超时', retryable: true },
      next_action: '5 秒后自动重试',
    },
  ],
  timeline: [
    {
      id: 'event_2',
      stage: 'failure',
      title: 'Worker 执行失败',
      message: 'Worker 响应超时',
      occurred_at: '2026-08-30T01:02:00Z',
      attempt: 2,
      failure: { code: 'timeout', message: 'Worker 响应超时', retryable: true },
    },
    {
      id: 'event_1',
      stage: 'plan',
      title: 'Coordinator 生成计划',
      message: '拆分为 2 个可验证步骤',
      occurred_at: '2026-08-30T01:01:00Z',
    },
  ],
  updated_at: '2026-08-30T01:02:00Z',
  version: 2,
};

describe('CoordinatorPanel', () => {
  it('首屏展示状态、文字进度、当前 Agent、下一动作和恢复原因', () => {
    const html = renderToStaticMarkup(<CoordinatorPanel task={task} snapshot={snapshot} onRetry={() => undefined} />);
    expect(html).toContain('任务统筹');
    expect(html).toContain('等待自动重试');
    expect(html).toContain('1/2 步');
    expect(html).toContain('Atlas');
    expect(html).toContain('退避后重试当前步骤');
    expect(html).toContain('计划于');
    expect(html).toContain('Worker 响应超时');
    expect(html).toContain('aria-valuenow="50"');
    expect(html).toContain('任务主时间线');
    expect(html).toContain('尝试 2 / 4');
    expect(html).toContain('将自动恢复');
  });

  it('缺少快照时明确显示接取中和等待首个计划，不误报 0%', () => {
    const html = renderToStaticMarkup(<CoordinatorPanel task={{ ...task, status: 'todo' }} onRetry={() => undefined} />);
    expect(html).toContain('接取中');
    expect(html).toContain('Coordinator 正在接取任务并生成首个计划');
    expect(html).toContain('等待首个计划');
    expect(html).toContain('aria-valuetext="等待首个计划"');
    expect(html).not.toContain('aria-valuenow="0"');
  });
});

describe('Coordinator display helpers', () => {
  it('maps the durable state and stage vocabulary to readable Chinese labels', () => {
    expect(coordinatorStatusText('waiting_user')).toBe('等待你处理');
    expect(coordinatorStageText('reassign')).toBe('重新分配');
    expect(coordinatorStatusText('unknown')).toBe('unknown');
  });

  it('orders timeline causally and renders empty attempts with a next action', () => {
    const timeline = renderToStaticMarkup(<CoordinatorTimeline entries={snapshot.timeline} />);
    expect(timeline.indexOf('Coordinator 生成计划')).toBeLessThan(timeline.indexOf('Worker 执行失败'));
    const attempts = renderToStaticMarkup(<CoordinatorAttempts attempts={[]} />);
    expect(attempts).toContain('首个 Worker 尚未开始');
  });
});
