import { describe, expect, it } from 'vitest';
import type { WorkItem } from '../api/types';
import { childCountByParent, sortTasksTree } from './tasks.page';

const wi = (id: string, createdAt: string, parentId?: string): WorkItem => ({
  id,
  workspace_id: 'ws_1',
  record_kind: 'task',
  title: id,
  description: '',
  status: 'todo',
  priority: 'medium',
  due_date: null,
  ...(parentId ? { parent_id: parentId } : {}),
  runs_count: 0,
  version: 1,
  created_at: createdAt,
  updated_at: createdAt,
});

const ids = (entries: ReturnType<typeof sortTasksTree>) => entries.map((e) => e.item.id);
const depths = (entries: ReturnType<typeof sortTasksTree>) => entries.map((e) => e.depth);

describe('sortTasksTree', () => {
  it('父在子前、同级按 created_at 升序、孙辈深度递增', () => {
    const entries = sortTasksTree([
      wi('A', '2026-08-23T03:00:00Z'),
      wi('B', '2026-08-23T01:00:00Z'),
      wi('A1', '2026-08-23T05:00:00Z', 'A'),
      wi('A0', '2026-08-23T04:00:00Z', 'A'),
      wi('B1', '2026-08-23T06:00:00Z', 'B'),
      wi('A1a', '2026-08-23T07:00:00Z', 'A1'),
    ]);
    expect(ids(entries)).toEqual(['B', 'B1', 'A', 'A0', 'A1', 'A1a']);
    expect(depths(entries)).toEqual([0, 1, 0, 1, 1, 2]);
  });

  it('缺父节点（父不在集合）按根处理，与真实根一起按 created_at 排序', () => {
    const entries = sortTasksTree([
      wi('B', '2026-08-23T01:00:00Z'),
      wi('X', '2026-08-23T02:00:00Z', 'ghost'),
      wi('A', '2026-08-23T03:00:00Z'),
    ]);
    expect(ids(entries)).toEqual(['B', 'X', 'A']);
    expect(depths(entries)).toEqual([0, 0, 0]);
  });

  it('环容错：互为祖先的成员不死循环、不丢项，平铺为根', () => {
    const entries = sortTasksTree([
      wi('A', '2026-08-23T03:00:00Z'),
      wi('C1', '2026-08-23T10:00:00Z', 'C2'),
      wi('C2', '2026-08-23T11:00:00Z', 'C1'),
    ]);
    expect(ids(entries)).toEqual(['A', 'C1', 'C2']);
    expect(depths(entries)).toEqual([0, 0, 0]);
  });

  it('自指 parent_id 按根处理', () => {
    const entries = sortTasksTree([wi('S', '2026-08-23T01:00:00Z', 'S')]);
    expect(ids(entries)).toEqual(['S']);
    expect(depths(entries)).toEqual([0]);
  });

  it('created_at 相同按 id 定序，输出确定', () => {
    const entries = sortTasksTree([
      wi('b', '2026-08-23T01:00:00Z'),
      wi('a', '2026-08-23T01:00:00Z'),
    ]);
    expect(ids(entries)).toEqual(['a', 'b']);
  });

  it('环成员的子树同样兜底输出（不丢项）', () => {
    const entries = sortTasksTree([
      wi('C1', '2026-08-23T10:00:00Z', 'C2'),
      wi('C2', '2026-08-23T11:00:00Z', 'C1'),
      wi('D', '2026-08-23T12:00:00Z', 'C1'),
    ]);
    expect(ids(entries).sort()).toEqual(['C1', 'C2', 'D']);
    expect(depths(entries)).toEqual([0, 0, 0]);
  });
});

describe('childCountByParent', () => {
  it('按 parent_id 统计直接子任务数，无父任务不计数', () => {
    const counts = childCountByParent([
      wi('A', '2026-08-23T01:00:00Z'),
      wi('A1', '2026-08-23T02:00:00Z', 'A'),
      wi('A2', '2026-08-23T03:00:00Z', 'A'),
      wi('A1a', '2026-08-23T04:00:00Z', 'A1'), // 孙辈只计入直接父
      wi('B', '2026-08-23T05:00:00Z'),
    ]);
    expect(counts.get('A')).toBe(2);
    expect(counts.get('A1')).toBe(1);
    expect(counts.has('B')).toBe(false);
    expect(counts.size).toBe(2);
  });

  it('空列表返回空 Map', () => {
    expect(childCountByParent([]).size).toBe(0);
  });
});
