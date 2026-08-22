import type { CanonicalEvent } from '../api/types';
import { useAgentsStore } from './agents.store';
import { useDashboardStore } from './dashboard.store';
import { useLogsStore } from './logs.store';
import { useRunsStore } from './runs.store';
import { useTasksStore } from './tasks.store';

/**
 * SSE 事件路由（协议 §6.3 事件目录）：
 * - 列表类资源采用失效重取，保证与权威投影一致；
 * - run 域事件交给 runs store 就地更新（详情抽屉实时时间线）。
 */
export function routeEvent(ev: CanonicalEvent): void {
  const tasks = useTasksStore.getState();
  const dashboard = useDashboardStore.getState();
  const agents = useAgentsStore.getState();
  const logs = useLogsStore.getState();
  const runs = useRunsStore.getState();

  if (ev.type.startsWith('work_item.')) {
    void tasks.refresh();
    void dashboard.refresh();
    return;
  }
  if (ev.type.startsWith('agent_')) {
    void agents.refresh();
    void dashboard.refresh();
    return;
  }
  if (ev.type === 'dashboard.metrics.updated') {
    void dashboard.refresh();
    return;
  }
  if (ev.type === 'activity.appended') {
    logs.prependFromEvent(ev);
    void dashboard.refresh();
    return;
  }
  if (ev.type === 'run.created') {
    // work item 的 latest_run_id/runs_count 已变化，但 M1 不发 work_item.updated。
    void tasks.refresh();
  }
  if (!runs.applyEvent(ev)) {
    // system.health_changed / runtime.health_changed / runner.* 等：
    // M1 无独立健康端点，健康投影随下次 bootstrap 更新。
  }
}
