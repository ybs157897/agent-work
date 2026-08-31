import { useWorkspaceStore } from './workspace.store';

/**
 * Workspace 作用域（任务控制面 RFC §12.2 generation fencing）：
 * - 所有异步闭包在发请求前 capture 当前 {workspaceId, generation}；
 * - resolve/reject 写 store 前用 isCurrent 双校验，A→B 切换后旧 Workspace 的
 *   慢响应一律丢弃——reset 不能取消 Promise，resolve-time guard 才是权威防线；
 * - 每个 workspace-scoped store 在模块加载时注册 reset，切换 Workspace 时
 *   由 bootstrap 统一清空（resetWorkspaceScopedStores）。
 */
export interface Scope {
  workspaceId: string;
  generation: number;
}

/**
 * 带 workspace_id 的实体只能在其所属 Workspace 的当前 generation 内使用。
 *
 * `isCurrent` 只能证明请求发起前后仍停留在同一个 Workspace；它不能证明一个
 * 裸资源 URL（例如 /work-items/:id）本身属于该 Workspace。所有 Task/Run
 * 控件在发请求或接纳响应前都经此校验，避免保留旧 URL 后把 A 的实体投影进 B。
 */
export function isCurrentWorkspaceEntity(
  scope: Scope,
  entity: { workspace_id?: unknown } | null | undefined,
): boolean {
  return isCurrent(scope)
    && scope.workspaceId.length > 0
    && entity?.workspace_id === scope.workspaceId;
}

/** 当前选中 Workspace id（booting 期间 selectedWorkspaceId 已指向目标）。 */
export function currentWorkspaceId(): string {
  const s = useWorkspaceStore.getState();
  return s.selectedWorkspaceId ?? s.workspace?.id ?? '';
}

/** 发请求前捕获作用域快照。 */
export function captureScope(): Scope {
  return { workspaceId: currentWorkspaceId(), generation: useWorkspaceStore.getState().generation };
}

/** 写 store 前双校验：workspaceId 与 generation 必须同时匹配当前值。 */
export function isCurrent(scope: Scope): boolean {
  return currentWorkspaceId() === scope.workspaceId && useWorkspaceStore.getState().generation === scope.generation;
}

const resets = new Set<() => void>();

/** store 模块加载时注册自己的 reset（幂等；重复注册以 Set 去重）。 */
export function registerWorkspaceScopedReset(reset: () => void): void {
  resets.add(reset);
}

/** 切换 Workspace 第 3 步：清空全部 workspace-scoped store（不取消在途 Promise）。 */
export function resetWorkspaceScopedStores(): void {
  for (const reset of [...resets]) reset();
}

/** 测试隔离用：清空注册表（store 模块已加载的 reset 会随下次 import 失效）。 */
export function clearWorkspaceScopedResetsForTest(): void {
  resets.clear();
}
