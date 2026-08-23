import type { WorkItem } from '../api/types';

export interface TaskTreeEntry {
  item: WorkItem;
  depth: number;
}

const byCreated = (a: WorkItem, b: WorkItem): number => {
  if (a.created_at !== b.created_at) return a.created_at < b.created_at ? -1 : 1;
  return a.id < b.id ? -1 : 1; // 时间相同按 id 定序，保证输出确定
};

/**
 * 任务树先序排序（列表视图 / 子任务面板 / 父任务选择器共用）：
 * 父在子前，同级按 created_at 升序。
 * 容错约定：parent_id 缺父（含自指）按根处理；互为祖先的环成员从根不可达，
 * 按 created_at 平铺在末尾（depth=0）——任何输入都不丢项、不死循环。
 */
export function sortTasksTree(items: WorkItem[]): TaskTreeEntry[] {
  const byId = new Map(items.map((t) => [t.id, t]));
  // '' 为根桶：任务 id 非空，不会与真实父 id 冲突。
  const childrenOf = new Map<string, WorkItem[]>();
  for (const t of items) {
    const parent = t.parent_id && t.parent_id !== t.id ? byId.get(t.parent_id) : undefined;
    const key = parent ? parent.id : '';
    const bucket = childrenOf.get(key);
    if (bucket) bucket.push(t);
    else childrenOf.set(key, [t]);
  }
  for (const bucket of childrenOf.values()) bucket.sort(byCreated);

  const out: TaskTreeEntry[] = [];
  const visited = new Set<string>();
  // 显式栈先序：节点在弹出时输出；子级逆序入栈保证同级按序先访。
  const stack: Array<{ item: WorkItem; depth: number }> = [];
  const roots = childrenOf.get('') ?? [];
  for (let i = roots.length - 1; i >= 0; i--) stack.push({ item: roots[i], depth: 0 });
  while (stack.length > 0) {
    const { item, depth } = stack.pop() as { item: WorkItem; depth: number };
    visited.add(item.id);
    out.push({ item, depth });
    const children = childrenOf.get(item.id) ?? [];
    for (let i = children.length - 1; i >= 0; i--) stack.push({ item: children[i], depth: depth + 1 });
  }
  // 环成员（从根不可达）兜底平铺。
  for (const orphan of items.filter((t) => !visited.has(t.id)).sort(byCreated)) {
    out.push({ item: orphan, depth: 0 });
  }
  return out;
}

/** 直接子任务数（看板角标本地推导）：parent_id → 直接子任务条数。 */
export function childCountByParent(items: WorkItem[]): Map<string, number> {
  const counts = new Map<string, number>();
  for (const t of items) {
    if (!t.parent_id) continue;
    counts.set(t.parent_id, (counts.get(t.parent_id) ?? 0) + 1);
  }
  return counts;
}
