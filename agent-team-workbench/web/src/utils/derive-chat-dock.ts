import type { ChatMessage, PlanStepView } from '../stores/chat.store';
import { TERMINAL } from '../stores/chat.store';
import type { TimelineEntry } from '../stores/runs.store';

/** Kimi transcript meta.goal / Codex thread goal 的 UI 投影（仅会话内显式启动后才有）。 */
export interface SessionGoalView {
  objective: string;
  status?: string;
}

/** LanguageGUI 视觉原语承载的 Workbench 只读工作流。 */
export interface ChatWorkflowView {
  goal?: SessionGoalView;
  steps: PlanStepView[];
  proposedPlan?: string;
}

export interface ChatDockState {
  /** Goal、执行步骤与 Plan 模式正文的统一只读投影。 */
  workflow?: ChatWorkflowView;
}

/** dock 展示态步骤：run 终态后悬空 in_progress 投影为 interrupted，止住无限旋转。 */
export type WorkflowStepStatus = PlanStepView['status'] | 'interrupted';

export interface WorkflowStep {
  step: string;
  status: WorkflowStepStatus;
}

/**
 * 展示层投影（不改数据）：run 未终态原样透传；succeeded 时模型未闭合的步骤
 * 视为完成（run 已成功，悬空中断无意义）；其余终态把 in_progress 标为
 * interrupted（中断），pending 保持待处理——用户看到的是可终扫的静态态，
 * 而不是永远旋转的 LoaderCircle。
 */
export function projectWorkflowSteps(
  steps: readonly PlanStepView[],
  runStatus?: string,
): WorkflowStep[] {
  if (!runStatus || !TERMINAL.has(runStatus)) return steps.map((s) => ({ ...s }));
  return steps.map((s) => {
    if (s.status === 'completed') return { ...s };
    if (runStatus === 'succeeded') return { ...s, status: 'completed' };
    if (s.status === 'in_progress') return { ...s, status: 'interrupted' };
    return { ...s };
  });
}

const GOAL_INLINE = /^\/goal(?:\s+([\s\S]+))?$/i;
const GOAL_EVENT_TYPES = new Set(['goal.updated', 'goal.create', 'goal.clear', 'session.goal.updated']);

/** 从用户 /goal 指令或 timeline goal 事件推导会话目标（不用 title/description）。 */
export function deriveSessionGoal(
  messages: readonly ChatMessage[],
  timelines?: Readonly<Record<string, TimelineEntry[]>>,
): SessionGoalView | undefined {
  let fromEvents: SessionGoalView | undefined;
  let sawGoalEvent = false;
  if (timelines) {
    for (const entries of Object.values(timelines)) {
      for (const e of entries) {
        if (!GOAL_EVENT_TYPES.has(e.type)) continue;
        sawGoalEvent = true;
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
  // 显式 clear 是终态，不能回退到历史 /goal 文本重新激活旧目标。
  if (sawGoalEvent) return fromEvents;

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

export function deriveChatWorkflow(
  messages: readonly ChatMessage[],
  latestRunId?: string,
  timelines?: Readonly<Record<string, TimelineEntry[]>>,
): ChatWorkflowView | undefined {
  const goal = deriveSessionGoal(messages, timelines);
  const todoPlan = deriveTodoPlan(messages, latestRunId);
  const proposedPlan = deriveProposedPlan(messages, latestRunId);
  const steps = todoPlan?.steps ?? [];
  if (!goal && steps.length === 0 && !proposedPlan) return undefined;
  return {
    ...(goal ? { goal } : {}),
    steps,
    ...(proposedPlan ? { proposedPlan } : {}),
  };
}

export function deriveChatDock(
  messages: readonly ChatMessage[],
  latestRunId?: string,
  timelines?: Readonly<Record<string, TimelineEntry[]>>,
): ChatDockState {
  const workflow = deriveChatWorkflow(messages, latestRunId, timelines);
  return workflow ? { workflow } : {};
}
