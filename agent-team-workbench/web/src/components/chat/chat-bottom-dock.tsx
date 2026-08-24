import { ChevronDown, ChevronRight, Crosshair, FileText, ListChecks } from 'lucide-react';
import { useState } from 'react';
import type { ChatMessage } from '../../stores/chat.store';
import type { SessionGoalView } from '../../utils/derive-chat-dock';
import { MarkdownBody } from './markdown-body';
import { PlanCard } from './plan-card';

export function ChatBottomDock({
  goal,
  todoPlan,
  proposedPlan,
}: {
  goal?: SessionGoalView;
  todoPlan?: ChatMessage;
  proposedPlan?: string;
}) {
  if (!goal && !todoPlan && !proposedPlan) return null;

  return (
    <div className="chat-bottom-dock mb-2 space-y-2">
      {goal && <DockGoal goal={goal} />}
      {todoPlan && <DockTodoPlan plan={todoPlan} />}
      {proposedPlan && <DockProposedPlan text={proposedPlan} />}
    </div>
  );
}

function DockTodoPlan({ plan }: { plan: ChatMessage }) {
  const [open, setOpen] = useState(true);
  const steps = plan.steps ?? [];
  const inProgress = steps.filter((s) => s.status === 'in_progress').length;
  const pending = steps.filter((s) => s.status === 'pending').length;
  const done = steps.filter((s) => s.status === 'completed').length;

  return (
    <div className="chat-dock-panel" data-dock="todo-list">
      <button
        type="button"
        className="chat-dock-head"
        onClick={() => setOpen((v) => !v)}
        aria-expanded={open}
      >
        {open ? <ChevronDown className="h-3.5 w-3.5 shrink-0" /> : <ChevronRight className="h-3.5 w-3.5 shrink-0" />}
        <ListChecks className="h-3.5 w-3.5 shrink-0 text-text-tertiary" />
        <span className="font-medium">任务</span>
        <span className="text-text-tertiary">
          {inProgress > 0 && `${inProgress} 进行中`}
          {inProgress > 0 && pending > 0 && ' · '}
          {pending > 0 && `${pending} 待处理`}
          {inProgress === 0 && pending === 0 && done > 0 && `${done}/${steps.length} 已完成`}
        </span>
      </button>
      {open && (
        <div className="chat-dock-body">
          <PlanCard msg={plan} compact />
        </div>
      )}
    </div>
  );
}

function DockProposedPlan({ text }: { text: string }) {
  const [open, setOpen] = useState(true);

  return (
    <div className="chat-dock-panel" data-dock="proposed-plan">
      <button
        type="button"
        className="chat-dock-head"
        onClick={() => setOpen((v) => !v)}
        aria-expanded={open}
      >
        {open ? <ChevronDown className="h-3.5 w-3.5 shrink-0" /> : <ChevronRight className="h-3.5 w-3.5 shrink-0" />}
        <FileText className="h-3.5 w-3.5 shrink-0 text-text-tertiary" />
        <span className="font-medium">计划</span>
        <span className="text-text-tertiary">Plan 模式</span>
      </button>
      {open && (
        <div className="chat-dock-body chat-markdown text-caption">
          <MarkdownBody text={text} />
        </div>
      )}
    </div>
  );
}

function DockGoal({ goal }: { goal: SessionGoalView }) {
  const [open, setOpen] = useState(true);
  const statusLabel =
    goal.status === 'complete'
      ? '已完成'
      : goal.status === 'paused'
        ? '已暂停'
        : goal.status === 'blocked'
          ? '受阻'
          : '进行中';

  return (
    <div className="chat-dock-panel" data-dock="goal">
      <button
        type="button"
        className="chat-dock-head"
        onClick={() => setOpen((v) => !v)}
        aria-expanded={open}
      >
        {open ? <ChevronDown className="h-3.5 w-3.5 shrink-0" /> : <ChevronRight className="h-3.5 w-3.5 shrink-0" />}
        <Crosshair className="h-3.5 w-3.5 shrink-0 text-text-tertiary" />
        <span className="font-medium">目标</span>
        <span className="text-text-tertiary">{statusLabel}</span>
      </button>
      {open && (
        <div className="chat-dock-body">
          <p className="whitespace-pre-wrap break-words text-caption leading-5 text-text-secondary">{goal.objective}</p>
        </div>
      )}
    </div>
  );
}
