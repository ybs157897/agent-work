import { create } from 'zustand';
import { listWorkItemDecisions } from '../api/endpoints';
import type { CanonicalEvent, DecisionEntry } from '../api/types';
import { captureScope, isCurrent, registerWorkspaceScopedReset } from './scope';

/**
 * 决策台账投影（会话元模型 S2）：
 * - 权威来源 GET /work-items/{id}/decisions（创建时间升序），本 store 按 work item 缓存；
 * - decision.created SSE 事件信封只带 decision 聚合 id，data.work_item_id 作定位线索：
 *   按 work item 失效重取，窗口内突发合并为一次请求，节奏对齐 dispatches.store。
 */
interface DecisionsStore {
  /** work_item_id → 决策台账（升序）；未拉取过 = undefined。 */
  byWorkItem: Record<string, DecisionEntry[]>;
  /** work_item_id → 最近一次决策列表请求错误；存在时 UI 不得伪装为空态。 */
  errorByWorkItem: Record<string, string | undefined>;
  /** 详情抽屉打开时拉快照；GET 失败保留错误供就地重试。 */
  refreshFor: (workItemId: string, expectedWorkspaceId?: string) => Promise<void>;
  /** decision 域 SSE 事件（decision.*）；返回是否已消费。 */
  applyEvent: (ev: CanonicalEvent) => boolean;
  /** 切换 Workspace 时清空决策台账与防抖定时器。 */
  reset: () => void;
}

/** 与 events.ts SSE_REFRESH_DEBOUNCE_MS 同值；本地定义避免 store→events 循环引用。 */
const REFETCH_DEBOUNCE_MS = 200;

const timers = new Map<string, ReturnType<typeof setTimeout>>();
const requestVersions = new Map<string, number>();

export const useDecisionsStore = create<DecisionsStore>()((set) => ({
  byWorkItem: {},
  errorByWorkItem: {},

  refreshFor: async (workItemId, expectedWorkspaceId) => {
    const scope = captureScope();
    if (!scope.workspaceId || (expectedWorkspaceId !== undefined && expectedWorkspaceId !== scope.workspaceId)) return;
    const requestVersion = (requestVersions.get(workItemId) ?? 0) + 1;
    requestVersions.set(workItemId, requestVersion);
    try {
      const { items } = await listWorkItemDecisions(workItemId);
      if (requestVersions.get(workItemId) !== requestVersion || !isCurrent(scope)) return;
      set((s) => {
        const errorByWorkItem = { ...s.errorByWorkItem };
        delete errorByWorkItem[workItemId];
        return {
          byWorkItem: { ...s.byWorkItem, [workItemId]: items },
          errorByWorkItem,
        };
      });
    } catch {
      if (requestVersions.get(workItemId) !== requestVersion || !isCurrent(scope)) return;
      // 保留错误投影给当前详情；重试按钮会复用同一权威 GET。
      set((s) => ({
        errorByWorkItem: {
          ...s.errorByWorkItem,
          [workItemId]: '决策记录加载失败，请重试',
        },
      }));
    }
  },

  applyEvent: (ev) => {
    if (!ev.type.startsWith('decision.')) return false;
    const wi = typeof ev.data?.work_item_id === 'string' ? ev.data.work_item_id : '';
    if (!wi) return true; // 已消费但定位不到 work item，丢弃
    const pending = timers.get(wi);
    if (pending) clearTimeout(pending);
    timers.set(
      wi,
      setTimeout(() => {
        timers.delete(wi);
        void useDecisionsStore.getState().refreshFor(wi, ev.workspace_id);
      }, REFETCH_DEBOUNCE_MS),
    );
    return true;
  },

  reset: () => {
    for (const timer of timers.values()) clearTimeout(timer);
    timers.clear();
    requestVersions.clear();
    set({ byWorkItem: {}, errorByWorkItem: {} });
  },
}));

registerWorkspaceScopedReset(() => useDecisionsStore.getState().reset());
