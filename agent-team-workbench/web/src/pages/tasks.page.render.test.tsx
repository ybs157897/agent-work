import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it } from 'vitest';
import type { AgentProfile, WorkItem } from '../api/types';
import { TaskCard } from './tasks.page';

const task = (phase: WorkItem['phase']): WorkItem => ({
  id: 'review-root',
  workspace_id: 'ws_1',
  record_kind: 'task',
  title: '待验收总任务',
  description: '',
  status: 'in_progress',
  phase,
  ...(phase === 'review' || phase === 'acceptance'
    ? { review: { ready: true, can_accept: true, can_return: true } }
    : {}),
  priority: 'medium',
  due_date: null,
  runs_count: 0,
  version: 1,
  created_at: '2026-09-04T00:00:00Z',
  updated_at: '2026-09-04T00:00:00Z',
});

const agent = (id: string, name: string): AgentProfile => ({
  id,
  name,
  kind: 'user',
  role: 'worker',
  skills: [],
  availability: 'enabled',
  presence: 'idle',
  version: 1,
});

describe('TaskCard projection', () => {
  it('待验收状态进入可访问名称，头像最多四个并显示溢出数', () => {
    const participants = ['Nova', 'Pixel', 'Forge', 'Atlas', 'Sentinel'].map((name, index) =>
      agent(`agent-${index}`, name));

    const html = renderToStaticMarkup(
      <TaskCard
        task={task('review')}
        childCount={5}
        participants={participants}
        onOpen={() => undefined}
      />,
    );

    expect(html).toContain('状态 待验收');
    expect(html).toContain('参与 Agent Nova、Pixel、Forge、Atlas、Sentinel');
    expect(html).toContain('title="参与 Agent：Nova、Pixel、Forge、Atlas、Sentinel"');
    expect(html.match(/aria-label="(Nova|Pixel|Forge|Atlas)"/g)).toHaveLength(4);
    expect(html).toContain('>+1</span>');
    expect(html).not.toContain('aria-label="Sentinel"');
    expect(html).toContain('未设置截止日');
    expect(html).not.toContain('lucide-calendar');
  });

  it('普通执行阶段仍标记为进行中', () => {
    const html = renderToStaticMarkup(
      <TaskCard task={task('execution')} childCount={0} participants={[]} onOpen={() => undefined} />,
    );
    expect(html).toContain('状态 进行中');
    expect(html).not.toContain('状态 待验收');
  });

  it('phase 等待但 review 未就绪时留在进行中并说明原因', () => {
    const html = renderToStaticMarkup(
      <TaskCard
        task={{ ...task('review'), review: { ready: false, can_accept: false, can_return: false } }}
        childCount={0}
        participants={[]}
        onOpen={() => undefined}
      />,
    );
    expect(html).toContain('状态 进行中');
    expect(html).toContain('验收依据尚未就绪，任务仍在进行中');
    expect(html).not.toContain('>待验收</span>');
  });
});
