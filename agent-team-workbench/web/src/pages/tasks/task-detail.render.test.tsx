import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it, vi } from 'vitest';
import type { WorkItem } from '../../api/types';

vi.mock('./dispatch-timeline', () => ({
  DispatchTimeline: () => (
    <section aria-label="Agent 输入与输出">
      <article>Forge · 输入 · 最终输出</article>
    </section>
  ),
}));

import { TaskPeekContent } from './task-detail';

const root: WorkItem = {
  id: 'wi_root',
  workspace_id: 'ws_1',
  record_kind: 'task',
  title: '总任务',
  description: '不在详情重复展示',
  status: 'in_progress',
  phase: 'acceptance',
  priority: 'medium',
  due_date: null,
  runs_count: 3,
  review: { ready: true, can_accept: true, can_return: true },
  version: 4,
  created_at: '2026-09-04T00:00:00Z',
  updated_at: '2026-09-04T00:00:00Z',
};

describe('TaskPeekContent', () => {
  it('根任务只展示标题、唯一总任务验收和 Agent 输入输出', () => {
    const html = renderToStaticMarkup(
      <TaskPeekContent
        task={root}
        accepting={false}
        onAccept={() => undefined}
        onReturn={() => undefined}
        onClose={() => undefined}
      />,
    );

    expect(html).toContain('总任务');
    expect(html).toContain('总任务验收通过');
    expect(html).toContain('打回总任务');
    expect(html).toContain('待验收');
    expect(html).not.toContain('进行中');
    expect(html).toContain('Forge · 输入 · 最终输出');
    expect(html).not.toContain('不在详情重复展示');
    expect(html).not.toContain('子任务');
    expect(html).not.toContain('任务属性');
    expect(html).not.toContain('编辑任务');
    expect(html).not.toContain('标记阻塞');
    expect(html).not.toContain('Task Coordinator');
  });

  it('子任务不渲染任何验收或操作，只等待跳转到总任务', () => {
    const html = renderToStaticMarkup(
      <TaskPeekContent
        task={{ ...root, id: 'wi_child', parent_id: root.id, title: 'Forge 子任务' }}
        accepting={false}
        onAccept={() => undefined}
        onReturn={() => undefined}
        onClose={() => undefined}
      />,
    );

    expect(html).toContain('正在打开总任务');
    expect(html).not.toContain('验收通过');
    expect(html).not.toContain('打回');
    expect(html).not.toContain('标记阻塞');
  });

  it('根任务未到验收阶段时不提供验收按钮', () => {
    const html = renderToStaticMarkup(
      <TaskPeekContent
        task={{ ...root, phase: 'execution', review: { ready: false, can_accept: false, can_return: false, accept_reason: '任务尚未进入待验收阶段' } }}
        accepting={false}
        onAccept={() => undefined}
        onReturn={() => undefined}
        onClose={() => undefined}
      />,
    );
    expect(html).not.toContain('总任务验收通过');
    expect(html).not.toContain('打回总任务');
  });

  it('证据未齐时保留打回但隐藏验收通过', () => {
    const html = renderToStaticMarkup(
      <TaskPeekContent
        task={{
          ...root,
          review: {
            ready: true,
            can_accept: false,
            can_return: true,
            accept_reason: '尚未获得通过的规范验证结果',
          },
        }}
        accepting={false}
        onAccept={() => undefined}
        onReturn={() => undefined}
        onClose={() => undefined}
      />,
    );
    expect(html).not.toContain('总任务验收通过');
    expect(html).toContain('打回总任务');
    expect(html).toContain('尚未获得通过的规范验证结果');
  });

  it('缺少验收读模型时只显示核验中，不暴露可点击操作', () => {
    const html = renderToStaticMarkup(
      <TaskPeekContent
        task={{ ...root, review: undefined }}
        accepting={false}
        onAccept={() => undefined}
        onReturn={() => undefined}
        onClose={() => undefined}
      />,
    );
    expect(html).not.toContain('总任务验收通过');
    expect(html).not.toContain('打回总任务');
  });

  it('非待验收任务不显示验收不可用原因', () => {
    const html = renderToStaticMarkup(
      <TaskPeekContent
        task={{
          ...root,
          status: 'completed',
          phase: '',
          review: {
            ready: false,
            can_accept: false,
            can_return: false,
            accept_reason: '任务尚未进入待验收阶段',
            return_reason: '任务尚未进入待验收阶段',
          },
        }}
        accepting={false}
        onAccept={() => undefined}
        onReturn={() => undefined}
        onClose={() => undefined}
      />,
    );
    expect(html).toContain('已完成');
    expect(html).not.toContain('任务尚未进入待验收阶段');
  });
});
