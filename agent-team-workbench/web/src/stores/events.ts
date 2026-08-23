import type { CanonicalEvent } from '../api/types';
import { useAgentsStore } from './agents.store';
import { useChatStore } from './chat.store';
import { useDashboardStore } from './dashboard.store';
import { useLogsStore } from './logs.store';
import { useRunsStore } from './runs.store';
import { useTasksStore } from './tasks.store';

/** trailing-edge 合并窗口（ms）：SSE 事件风暴下同一类刷新在窗口内只执行最后一次。 */
export const SSE_REFRESH_DEBOUNCE_MS = 200;

/**
 * trailing-edge debounce（零依赖）：窗口内多次调用只保留最后一次。
 * SSE 每个事件都会触发列表失效重取，突发（如 run 批量推进）会被合并为
 * 每类 store 至多一次网络请求。
 */
export function createDebounced(fn: () => void, windowMs = SSE_REFRESH_DEBOUNCE_MS): () => void {
  let timer: ReturnType<typeof setTimeout> | null = null;
  return () => {
    if (timer !== null) clearTimeout(timer);
    timer = setTimeout(() => {
      timer = null;
      fn();
    }, windowMs);
  };
}

const refreshTasks = createDebounced(() => void useTasksStore.getState().refresh());
const refreshDashboard = createDebounced(() => void useDashboardStore.getState().refresh());
const refreshAgents = createDebounced(() => void useAgentsStore.getState().refresh());
const refreshChatConversations = createDebounced(() => void useChatStore.getState().refreshConversations());

/**
 * SSE 事件路由（协议 §6.3 事件目录）：
 * - 列表类资源采用失效重取（debounce 合并），保证与权威投影一致；
 * - run 域事件交给 runs store 就地更新（详情抽屉实时时间线）。
 */
export function routeEvent(ev: CanonicalEvent): void {
  const logs = useLogsStore.getState();
  const runs = useRunsStore.getState();

  if (ev.type.startsWith('work_item.')) {
    refreshTasks();
    refreshDashboard();
    return;
  }
  if (ev.type.startsWith('agent_')) {
    refreshAgents();
    refreshDashboard();
    return;
  }
  if (ev.type === 'dashboard.metrics.updated') {
    refreshDashboard();
    return;
  }
  if (ev.type === 'activity.appended') {
    logs.prependFromEvent(ev);
    refreshDashboard();
    return;
  }
  if (ev.type === 'run.created') {
    // work item 的 latest_run_id/runs_count 已变化，但 M1 不发 work_item.updated。
    refreshTasks();
    refreshChatConversations();
  }
  if (ev.type === 'run.completed' || ev.type === 'run.failed') {
    refreshChatConversations();
  }
  if (!runs.applyEvent(ev)) {
    // system.health_changed / runtime.health_changed / runner.* 等：
    // M1 无独立健康端点，健康投影随下次 bootstrap 更新。
  }
}
