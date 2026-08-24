import type { ChatMessage } from '../stores/chat.store';
import type { TimelineEntry } from '../stores/runs.store';

/** Kimi transcript meta.goal / Codex thread goal 的 UI 投影（仅会话内显式启动后才有）。 */
export interface SessionGoalView {
  objective: string;
  status?: string;
}

export interface ChatDockState {
  /** /goal 或 goal.* 事件激活的目标（Kimi: meta.goal.objective）。 */
  goal?: SessionGoalView;
  /** Codex todo-list / run.plan_updated 步骤清单。 */
  todoPlan?: ChatMessage;
  /** Codex proposed-plan / plan item 正文（plan 模式产出，非 agent 配置）。 */
  proposedPlan?: string;
}

const GOAL_INLINE = /^\/goal(?:\s+([\s\S]+))?$/i;
const GOAL_EVENT_TYPES = new Set(['goal.updated', 'goal.create', 'goal.clear', 'session.goal.updated']);

/** 从用户 /goal 指令或 timeline goal 事件推导会话目标（不用 title/description）。 */
export function deriveSessionGoal(
  messages: readonly ChatMessage[],
  timelines?: Readonly<Record<string, TimelineEntry[]>>,
): SessionGoalView | undefined {
  let fromEvents: SessionGoalView | undefined;
  if (timelines) {
    for (const entries of Object.values(timelines)) {
      for (const e of entries) {
        if (!GOAL_EVENT_TYPES.has(e.type)) continue;
        if (e.type === 'goal.clear') {
          fromEvents = undefined;
          continue;
        }
        const objective =
          (typeof e.data?.objective === 'string' && e.data.objective.trim()) ||
          (typeof e.text === 'string' && e.text.trim()) ||
          '';
        if (objective) {
          fromEvents = {
            objective,
            status: typeof e.data?.status === 'string' ? e.data.status : undefined,
          };
        }
      }
    }
  }
  if (fromEvents) return fromEvents;

  let armed = false;
  let goal: SessionGoalView | undefined;
  for (const m of messages) {
    if (m.kind !== 'user') continue;
    const t = m.text.trim();
    const inline = t.match(GOAL_INLINE);
    if (inline) {
      const obj = inline[1]?.trim();
      if (obj) goal = { objective: obj, status: 'active' };
      else armed = true;
      continue;
    }
    if (armed && t) {
      goal = { objective: t, status: 'active' };
      armed = false;
    }
  }
  return goal;
}

/** 最新 run 的 todo 清单（run.plan_updated；无 steps 不渲染）。 */
export function deriveTodoPlan(messages: readonly ChatMessage[], latestRunId?: string): ChatMessage | undefined {
  if (!latestRunId) return undefined;
  let plan: ChatMessage | undefined;
  for (const m of messages) {
    if (m.runId !== latestRunId || m.kind !== 'plan') continue;
    if (m.steps?.length) plan = m;
  }
  return plan;
}

/** plan 模式 proposed-plan：message.completed item_type=plan（Codex plan item）。 */
export function deriveProposedPlan(messages: readonly ChatMessage[], latestRunId?: string): string | undefined {
  if (!latestRunId) return undefined;
  let last: string | undefined;
  for (const m of messages) {
    if (m.runId !== latestRunId || m.kind !== 'assistant') continue;
    if (m.itemType === 'plan' && m.text.trim()) last = m.text;
  }
  return last;
}

export function deriveChatDock(
  messages: readonly ChatMessage[],
  latestRunId?: string,
  timelines?: Readonly<Record<string, TimelineEntry[]>>,
): ChatDockState {
  return {
    goal: deriveSessionGoal(messages, timelines),
    todoPlan: deriveTodoPlan(messages, latestRunId),
    proposedPlan: deriveProposedPlan(messages, latestRunId),
  };
}
