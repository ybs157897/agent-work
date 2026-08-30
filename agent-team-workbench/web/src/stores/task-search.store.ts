import { create } from 'zustand';
import { searchWorkspace } from '../api/endpoints';
import type { SearchItem } from '../api/types';
import { createRequestGuard } from './request-guard';
import { useTasksStore } from './tasks.store';
import { toast } from './toast.store';
import { useWorkspaceStore } from './workspace.store';

/**
 * tasks 页 Header 的 FTS 检索（会话元模型 S4）：
 * - 输入防抖合并（窗口内按键只发最后一次请求）；空/纯空白 query 立即回退
 *   全量看板（phase=idle，不请求——后端对空 q 也返回空 items，本地直接短路）；
 * - 请求失败 toast 提示并回退本地标题过滤（source=local），面板标注降级来源。
 */

export const SEARCH_DEBOUNCE_MS = 300;

export type SearchPhase = 'idle' | 'loading' | 'done';

interface TaskSearchStore {
  /** 输入框原文（受控）；trim 后为空即回退全量。 */
  query: string;
  phase: SearchPhase;
  items: SearchItem[];
  /** 结果来源：remote=后端 FTS；local=请求失败后的本地标题匹配回退。 */
  source: 'remote' | 'local' | null;
  setQuery: (query: string) => void;
  /** 立即执行一次检索（跳过防抖窗口；测试与组件不直接消费，暴露便于重试）。 */
  searchNow: (q: string) => Promise<void>;
}

const guard = createRequestGuard();
let timer: ReturnType<typeof setTimeout> | null = null;

export const useTaskSearchStore = create<TaskSearchStore>()((set, get) => ({
  query: '',
  phase: 'idle',
  items: [],
  source: null,

  setQuery: (query) => {
    set({ query });
    if (timer) {
      clearTimeout(timer);
      timer = null;
    }
    const q = query.trim();
    if (!q) {
      // 空 query：不过滤的全量看板（协议：空 q 服务端也返回空 items，本地短路）。
      set({ phase: 'idle', items: [], source: null });
      return;
    }
    set({ phase: 'loading' });
    timer = setTimeout(() => {
      timer = null;
      void get().searchNow(q);
    }, SEARCH_DEBOUNCE_MS);
  },

  searchNow: async (q) => {
    const wsId = useWorkspaceStore.getState().workspace?.id;
    if (!wsId) return;
    const isStale = guard.begin();
    try {
      const { items } = await searchWorkspace(wsId, { q, record_kind: 'task' });
      if (isStale()) return; // 期间已有更新请求：丢弃旧响应
      set({ phase: 'done', items, source: 'remote' });
    } catch {
      if (isStale()) return;
      toast.error('检索服务暂不可用，已回退本地标题过滤');
      const needle = q.toLocaleLowerCase();
      const items = useTasksStore
        .getState()
        .items.filter((t) => t.title.toLocaleLowerCase().includes(needle))
        .map<SearchItem>((t) => ({
          kind: 'segment_summary',
          work_item_id: t.id,
          source_id: t.id,
          title: t.title,
          snippet: '',
        }));
      set({ phase: 'done', items, source: 'local' });
    }
  },
}));
