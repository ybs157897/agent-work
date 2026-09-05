import type { ReactNode } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it, vi } from 'vitest';
import type { WorkItem } from '../../api/types';

vi.mock('../../components/modal', () => ({
  Modal: ({
    open,
    title,
    children,
    footer,
    skin,
  }: {
    open: boolean;
    title: string;
    children: ReactNode;
    footer?: ReactNode;
    skin?: string;
  }) => open ? (
    <div role="dialog" data-skin={skin}>
      <h1>{title}</h1>
      <div>{children}</div>
      <footer>{footer}</footer>
    </div>
  ) : null,
}));

import { createTaskDraftCache, ReturnTaskModal } from './return-modal';

const task: WorkItem = {
  id: 'wi_root',
  workspace_id: 'ws_1',
  record_kind: 'task',
  title: '修复任务',
  description: '',
  status: 'in_progress',
  phase: 'acceptance',
  priority: 'medium',
  due_date: null,
  runs_count: 1,
  version: 1,
  created_at: '2026-09-05T00:00:00Z',
  updated_at: '2026-09-05T00:00:00Z',
};

describe('ReturnTaskModal', () => {
  it('uses the task skin and explains the return flow in task language', () => {
    const html = renderToStaticMarkup(
      <ReturnTaskModal task={task} onClose={() => undefined} />,
    );

    expect(html).toContain('data-skin="task"');
    expect(html).toContain('说明需要调整的内容，提交后任务返回执行流程');
    expect(html).toContain('调整说明（必填）');
    expect(html).toContain('placeholder="请输入需要调整的内容"');
    expect(html).toContain('<footer>');
    expect(html).toContain('>打回重做</button>');
  });

  it('keeps drafts isolated by task id and clears only the completed task', () => {
    const drafts = createTaskDraftCache();
    drafts.write('task-a', '补充失败原因');
    drafts.write('task-b', '更新验收截图');

    expect(drafts.read('task-a')).toBe('补充失败原因');
    expect(drafts.read('task-b')).toBe('更新验收截图');

    drafts.clear('task-a');
    expect(drafts.read('task-a')).toBe('');
    expect(drafts.read('task-b')).toBe('更新验收截图');
  });
});
