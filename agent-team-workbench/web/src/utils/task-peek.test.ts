import { describe, expect, it } from 'vitest';
import type { Location } from 'react-router-dom';
import type { WorkItem } from '../api/types';
import { resolveTaskRootId, taskBoardTarget, taskPeekBackground, taskPeekTarget } from './task-peek';

const boardLocation: Location = {
  pathname: '/tasks',
  search: '?view=list&ws=ws_1',
  hash: '',
  state: null,
  key: 'board',
};

describe('task peek routing', () => {
  it('打开详情保留看板查询参数，并把看板位置作为可信背景', () => {
    expect(taskPeekTarget('wi_1', boardLocation.search)).toEqual({
      pathname: '/tasks/wi_1',
      search: '?view=list&ws=ws_1',
    });
    expect(taskBoardTarget(boardLocation.search)).toEqual({
      pathname: '/tasks',
      search: '?view=list&ws=ws_1',
    });
    expect(taskPeekBackground({ backgroundLocation: boardLocation })).toBe(boardLocation);
  });

  it('拒绝非任务看板背景，直接详情路由继续走全页 fallback', () => {
    expect(taskPeekBackground(null)).toBeUndefined();
    expect(taskPeekBackground({ backgroundLocation: { ...boardLocation, pathname: '/chat' } })).toBeUndefined();
  });

  it('接受规范等价的 /tasks/ 尾斜杠背景', () => {
    const trailingSlash = { ...boardLocation, pathname: '/tasks/' };
    const state = { backgroundLocation: trailingSlash };
    const resolved = taskPeekBackground(state);
    expect(resolved).toBe(trailingSlash);
    expect(taskPeekBackground(state)).toBe(resolved);
  });

  it('协调子任务优先采用服务端 root_work_item_id', async () => {
    const child = task('wi_child', 'wi_parent');
    await expect(resolveTaskRootId(child, async () => 'wi_root', async () => task('unused')))
      .resolves.toBe('wi_root');
  });

  it('历史子任务沿父链回溯，并拒绝循环父链', async () => {
    const items = new Map([
      ['wi_parent', task('wi_parent', 'wi_root')],
      ['wi_root', task('wi_root')],
    ]);
    const getItem = async (id: string) => {
      const item = items.get(id);
      if (!item) throw new Error('missing');
      return item;
    };
    await expect(resolveTaskRootId(task('wi_child', 'wi_parent'), async () => undefined, getItem))
      .resolves.toBe('wi_root');

    const cyclic = new Map([
      ['wi_a', task('wi_a', 'wi_b')],
      ['wi_b', task('wi_b', 'wi_a')],
    ]);
    await expect(resolveTaskRootId(cyclic.get('wi_a')!, async () => undefined, async (id) => cyclic.get(id)!))
      .rejects.toThrow('循环');
  });
});

function task(id: string, parentId?: string): WorkItem {
  return {
    id,
    workspace_id: 'ws_1',
    record_kind: 'task',
    title: id,
    description: '',
    status: 'in_progress',
    priority: 'medium',
    due_date: null,
    ...(parentId ? { parent_id: parentId } : {}),
    runs_count: 0,
    version: 1,
    created_at: '2026-09-04T00:00:00Z',
    updated_at: '2026-09-04T00:00:00Z',
  };
}
