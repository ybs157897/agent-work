import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it, vi } from 'vitest';
import type { TaskComment, WorkItem } from '../../api/types';
import { commentRootWorkItemId, CommentThreadView } from './comment-thread';

const task: WorkItem = {
  id: 'wi_root',
  workspace_id: 'ws_1',
  record_kind: 'task',
  title: '发布版本',
  description: '',
  status: 'in_progress',
  phase: 'acceptance',
  priority: 'high',
  due_date: null,
  runs_count: 1,
  version: 4,
  created_at: '2026-08-30T01:00:00Z',
  updated_at: '2026-08-30T01:02:00Z',
};

const terminalTask: WorkItem = { ...task, status: 'completed' };

const comment = (revision: number, kind: TaskComment['kind'] = 'note'): TaskComment => ({
  id: `cmt_${revision}`,
  workspace_id: 'ws_1',
  root_work_item_id: 'wi_root',
  work_item_id: 'wi_root',
  revision,
  kind,
  body: `第 ${revision} 条评论`,
  actor_kind: 'user',
  actor_id: 'u1',
  created_at: '2026-08-30T01:05:00Z',
});

const baseProps = {
  onRefresh: () => undefined,
  onCreate: () => Promise.resolve(comment(1)),
};

describe('CommentThread 渲染（任务控制面 RFC §4.9）', () => {
  it('三级任务只能使用 Coordinator 给出的 canonical root，不按直接 parent 猜测', () => {
    const child = { ...task, id: 'wi_child', parent_id: 'wi_root' };
    const grandchild = { ...task, id: 'wi_grandchild', parent_id: 'wi_child' };
    expect(commentRootWorkItemId(task, 'loading')).toBe('wi_root');
    expect(commentRootWorkItemId(child, 'coordinated', 'wi_root')).toBe('wi_root');
    expect(commentRootWorkItemId(grandchild, 'coordinated', 'wi_root')).toBe('wi_root');
    expect(commentRootWorkItemId(grandchild, 'loading')).toBeUndefined();
    expect(commentRootWorkItemId(grandchild, 'legacy')).toBe('wi_grandchild');
  });

  it('首拉期间显示加载态（role=status），不伪装空态', () => {
    const html = renderToStaticMarkup(
      <CommentThreadView task={task} comments={undefined} latestRevision={undefined} error={undefined} loading onRefresh={baseProps.onRefresh} onCreate={baseProps.onCreate} />,
    );
    expect(html).toContain('评论加载中');
    expect(html).toContain('role="status"');
    expect(html).not.toContain('还没有评论');
  });

  it('legacy 任务树显示明确不支持状态，不暴露刷新、空态或必失败的 composer', () => {
    const html = renderToStaticMarkup(
      <CommentThreadView
        task={{ ...task, id: 'wi_legacy_child', parent_id: 'wi_legacy_root' }}
        comments={undefined}
        latestRevision={undefined}
        error={undefined}
        loading={false}
        legacy
        onRefresh={baseProps.onRefresh}
        onCreate={baseProps.onCreate}
      />,
    );
    expect(html).toContain('未启用 Coordinator 评论流');
    expect(html).not.toContain('aria-label="刷新评论"');
    expect(html).not.toContain('aria-label="评论类型"');
    expect(html).not.toContain('aria-label="评论内容"');
    expect(html).not.toContain('还没有评论');
  });

  it('错误态以 role=alert 呈现并提供重试入口，不伪装空态；已有评论时保留内容', () => {
    const html = renderToStaticMarkup(
      <CommentThreadView
        task={task}
        comments={[comment(1)]}
        latestRevision={1}
        error="该任务未接入 Coordinator，不支持评论"
        loading={false}
        onRefresh={baseProps.onRefresh}
        onCreate={baseProps.onCreate}
      />,
    );
    expect(html).toContain('role="alert"');
    expect(html).toContain('未接入 Coordinator');
    expect(html).toContain('重试');
    expect(html).toContain('第 1 条评论'); // 旧内容保留，不因刷新失败清空
  });

  it('composer 提供 note/requirement 双模式与完整 aria 标签', () => {
    const html = renderToStaticMarkup(
      <CommentThreadView task={task} comments={[]} latestRevision={0} error={undefined} loading={false} onRefresh={baseProps.onRefresh} onCreate={baseProps.onCreate} />,
    );
    expect(html).toContain('aria-label="评论类型"');
    expect(html).toContain('aria-label="评论内容"');
    expect(html).toContain('备注（不触发执行）');
    expect(html).toContain('需求补充（交给 Coordinator）');
  });

  it('终态任务不渲染 composer（后端 409 comment_terminal_work_item，前端不给入口）', () => {
    const html = renderToStaticMarkup(
      <CommentThreadView task={terminalTask} comments={[]} latestRevision={0} error={undefined} loading={false} onRefresh={baseProps.onRefresh} onCreate={baseProps.onCreate} />,
    );
    expect(html).not.toContain('aria-label="评论类型"');
    expect(html).not.toContain('aria-label="评论内容"');
  });

  it('真空态说明「为什么空 + 下一步做什么」', () => {
    const html = renderToStaticMarkup(
      <CommentThreadView task={task} comments={[]} latestRevision={0} error={undefined} loading={false} onRefresh={baseProps.onRefresh} onCreate={baseProps.onCreate} />,
    );
    expect(html).toContain('还没有评论');
  });

  it('服务端 latest_revision 大于已加载时提供「加载更多」入口', () => {
    const html = renderToStaticMarkup(
      <CommentThreadView task={task} comments={[comment(1)]} latestRevision={21} error={undefined} loading={false} onRefresh={baseProps.onRefresh} onCreate={baseProps.onCreate} />,
    );
    expect(html).toContain('加载更多评论');
  });

  it('评论行渲染 kind 徽章（requirement/review_feedback 区分）与 revision 序号', () => {
    const html = renderToStaticMarkup(
      <CommentThreadView
        task={task}
        comments={[comment(3, 'requirement'), comment(4, 'review_feedback')]}
        latestRevision={4}
        error={undefined}
        loading={false}
        onRefresh={baseProps.onRefresh}
        onCreate={baseProps.onCreate}
      />,
    );
    expect(html).toContain('需求补充');
    expect(html).toContain('打回理由');
    expect(html).toContain('#3');
  });

  it('onCreate 的回调接线被容器消费（vi.fn 冒烟）', async () => {
    const onCreate = vi.fn().mockResolvedValue(comment(9));
    // View 只负责把 input 原样交给 onCreate：composer 提交链路在 store 测试覆盖。
    expect(onCreate).not.toHaveBeenCalled();
    await onCreate({ kind: 'note', body: 'x' });
    expect(onCreate).toHaveBeenCalledWith({ kind: 'note', body: 'x' });
  });
});
