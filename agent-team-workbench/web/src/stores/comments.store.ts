import { create } from 'zustand';
import { ApiError } from '../api/client';
import { createTaskComment, listWorkItemComments } from '../api/endpoints';
import type { CanonicalEvent, TaskComment, TaskCommentCreateInput } from '../api/types';
import { captureScope, isCurrent, registerWorkspaceScopedReset } from './scope';

/**
 * 任务评论投影（任务控制面 RFC §4.9/§12.2）：
 * - append-only 评论流按根 Task 维度缓存（revision 正序）；GET 端点挂在任意
 *   work item 上但返回根维度流，因此统一用 root id 作投影键；
 * - task_comment.created 信封不带 body：按 root_work_item_id 失效重取，
 *   窗口内突发合并为一次请求（节奏对齐 decisions.store）；
 * - 已加载过的 root 走 after_revision=已加载最大 revision 的增量追加
 *   （append-only 保证不丢不重），未加载过才拉首页。
 */
interface CommentsStore {
  /** root_work_item_id → 评论列表（revision 正序）。 */
  byRoot: Record<string, TaskComment[]>;
  /** root_work_item_id → 服务端 latest_revision（评论 cursor，与 stream_seq 无关）。 */
  latestRevisions: Record<string, number>;
  /** root_work_item_id → 最近一次请求错误；存在时 UI 不得伪装为空态。 */
  errorByRoot: Record<string, string | undefined>;
  /** root_work_item_id → 首拉进行中（骨架依据；增量刷新不打断已有内容）。 */
  loadingByRoot: Record<string, boolean>;
  /** 详情页打开时拉评论流；已加载过的走增量。 */
  refreshFor: (rootWorkItemId: string, expectedWorkspaceId?: string) => Promise<void>;
  /** 追加评论（写命令走 apiFetch 自动 Idempotency-Key）；成功后增量刷新。 */
  create: (
    rootWorkItemId: string,
    workItemId: string,
    input: TaskCommentCreateInput,
    expectedWorkspaceId?: string,
  ) => Promise<TaskComment>;
  /** task_comment.created SSE 事件；返回是否已消费。 */
  applyEvent: (ev: CanonicalEvent) => boolean;
  /** 切换 Workspace 时清空评论投影与防抖定时器。 */
  reset: () => void;
}

/** 与 events.ts SSE_REFRESH_DEBOUNCE_MS 同值；本地定义避免 store→events 循环引用。 */
const REFETCH_DEBOUNCE_MS = 200;
/** 单页拉取上限（契约 limit ≤ 200）。 */
const PAGE_LIMIT = 200;

const timers = new Map<string, ReturnType<typeof setTimeout>>();
const requestVersions = new Map<string, number>();

/** 错误码 → 用户文案：403/404/409/422 是真实拒绝，不得伪装成空态。 */
export function describeCommentError(err: unknown): string {
  if (err instanceof ApiError) {
    switch (err.code) {
      case 'comment_coordinator_required':
        return '该任务未接入 Coordinator，不支持评论';
      case 'comment_terminal_work_item':
        return '任务已结束，不能追加评论';
      case 'comment_body_too_large':
        return '评论超过 16 KiB 上限，请缩短后重试';
      case 'comment_kind_invalid':
        return '评论类型不被允许';
      case 'comment_body_empty':
        return '评论内容不能为空';
      case 'comment_cursor_invalid':
        return '评论游标失效，请重新加载';
      case 'comment_source_run_mismatch':
        return '关联的运行不属于该任务';
      default:
        return err.message || '评论加载失败，请重试';
    }
  }
  return '评论加载失败，请重试';
}

function loadedMaxRevision(items: TaskComment[] | undefined): number | undefined {
  if (!items || items.length === 0) return undefined;
  return items[items.length - 1].revision;
}

export const useCommentsStore = create<CommentsStore>()((set, get) => ({
  byRoot: {},
  latestRevisions: {},
  errorByRoot: {},
  loadingByRoot: {},

  reset: () => {
    for (const timer of timers.values()) clearTimeout(timer);
    timers.clear();
    requestVersions.clear();
    set({ byRoot: {}, latestRevisions: {}, errorByRoot: {}, loadingByRoot: {} });
  },

  refreshFor: async (rootWorkItemId, expectedWorkspaceId) => {
    const scope = captureScope();
    if (!scope.workspaceId || (expectedWorkspaceId !== undefined && expectedWorkspaceId !== scope.workspaceId)) return;
    const requestVersion = (requestVersions.get(rootWorkItemId) ?? 0) + 1;
    requestVersions.set(rootWorkItemId, requestVersion);
    const existing = get().byRoot[rootWorkItemId];
    // append-only：已加载过的只取增量；首拉才从头开始。
    const afterRevision = loadedMaxRevision(existing);
    if (afterRevision === undefined) {
      set((s) => ({ loadingByRoot: { ...s.loadingByRoot, [rootWorkItemId]: true } }));
    }
    try {
      const page = await listWorkItemComments(rootWorkItemId, {
        ...(afterRevision !== undefined ? { afterRevision } : {}),
        limit: PAGE_LIMIT,
      });
      if (requestVersions.get(rootWorkItemId) !== requestVersion || !isCurrent(scope)) return;
      // 资源端点是裸 work-item id；即使 generation 没变，也不能把别的
      // Workspace 的评论流写进当前投影。
      if (page.items.some((item) => item.workspace_id !== scope.workspaceId)) return;
      set((s) => {
        const errorByRoot = { ...s.errorByRoot };
        delete errorByRoot[rootWorkItemId];
        const loadingByRoot = { ...s.loadingByRoot };
        delete loadingByRoot[rootWorkItemId];
        return {
          byRoot: {
            ...s.byRoot,
            [rootWorkItemId]: [...(s.byRoot[rootWorkItemId] ?? []), ...page.items],
          },
          latestRevisions: { ...s.latestRevisions, [rootWorkItemId]: page.latest_revision },
          errorByRoot,
          loadingByRoot,
        };
      });
    } catch (err) {
      if (requestVersions.get(rootWorkItemId) !== requestVersion || !isCurrent(scope)) return;
      // 保留错误与已加载内容给当前详情；重试按钮复用同一权威 GET。
      set((s) => ({
        errorByRoot: { ...s.errorByRoot, [rootWorkItemId]: describeCommentError(err) },
        loadingByRoot: { ...s.loadingByRoot, [rootWorkItemId]: false },
      }));
    }
  },

  create: async (rootWorkItemId, workItemId, input, expectedWorkspaceId) => {
    const scope = captureScope();
    if (!scope.workspaceId || (expectedWorkspaceId !== undefined && expectedWorkspaceId !== scope.workspaceId)) {
      throw new Error('任务不属于当前工作区');
    }
    const comment = await createTaskComment(workItemId, input);
    if (isCurrent(scope) && comment.workspace_id === scope.workspaceId) {
      void get().refreshFor(rootWorkItemId, expectedWorkspaceId);
    }
    return comment;
  },

  applyEvent: (ev) => {
    if (ev.type !== 'task_comment.created') return false;
    // SSE 不推 body：按根 Task 失效重取；child 落点也归并到 root 投影。
    const root =
      typeof ev.data?.root_work_item_id === 'string' && ev.data.root_work_item_id.length > 0
        ? ev.data.root_work_item_id
        : undefined;
    if (!root) return true; // 已消费但定位不到 root，丢弃
    const pending = timers.get(root);
    if (pending) clearTimeout(pending);
    timers.set(
      root,
      setTimeout(() => {
        timers.delete(root);
        void useCommentsStore.getState().refreshFor(root, ev.workspace_id);
      }, REFETCH_DEBOUNCE_MS),
    );
    return true;
  },
}));

registerWorkspaceScopedReset(() => useCommentsStore.getState().reset());
