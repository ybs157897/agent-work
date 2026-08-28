import { AnimatePresence, motion, useReducedMotion } from 'motion/react';
import {
  Check,
  ChevronDown,
  ChevronRight,
  Circle,
  CircleAlert,
  CircleCheck,
  CircleGauge,
  CirclePause,
  Crosshair,
  FileText,
  ListChecks,
  LoaderCircle,
  type LucideIcon,
} from 'lucide-react';
import { useEffect, useId, useRef, useState } from 'react';
import type { ChatWorkflowView, SessionGoalView, WorkflowStep } from '../../utils/derive-chat-dock';
import { projectWorkflowSteps } from '../../utils/derive-chat-dock';
import { MarkdownBody } from './markdown-body';

const workflowSpring = { type: 'spring' as const, stiffness: 340, damping: 30, mass: 0.8 };

export function ChatBottomDock({ workflow, runStatus }: { workflow?: ChatWorkflowView; runStatus?: string }) {
  const reduceMotion = useReducedMotion();
  if (!workflow) return null;
  // 步骤状态经 run 终态投影：succeeded 补齐完成、失败/中断标 interrupted——
  // 数据层不补写，只在展示层止住永远旋转的进行中。
  const steps = projectWorkflowSteps(workflow.steps, runStatus);
  const done = steps.filter((step) => step.status === 'completed').length;

  return (
    <motion.section
      className="chat-bottom-dock chat-workflow mb-2"
      aria-label="任务工作流"
      initial={reduceMotion ? false : { opacity: 0, y: 10 }}
      animate={{ opacity: 1, y: 0 }}
      transition={reduceMotion ? { duration: 0 } : workflowSpring}
      layout
    >
      {workflow.goal && (
        <WorkflowGoal goal={workflow.goal} done={done} total={steps.length} />
      )}
      {workflow.proposedPlan && (
        <WorkflowProposal text={workflow.proposedPlan} defaultOpen={steps.length === 0} />
      )}
      {steps.length > 0 && <WorkflowActions steps={steps} />}
    </motion.section>
  );
}

type GoalTone = 'active' | 'complete' | 'paused' | 'blocked' | 'limited' | 'unknown';

function goalStatusMeta(status?: string): { label: string; tone: GoalTone; Icon: LucideIcon } {
  switch (status) {
    case 'active':
    case 'in_progress':
      return { label: '进行中', tone: 'active', Icon: LoaderCircle };
    case 'complete':
    case 'completed':
      return { label: '已完成', tone: 'complete', Icon: CircleCheck };
    case 'paused':
      return { label: '已暂停', tone: 'paused', Icon: CirclePause };
    case 'blocked':
      return { label: '受阻', tone: 'blocked', Icon: CircleAlert };
    case 'usageLimited':
    case 'usage_limited':
      return { label: '用量受限', tone: 'limited', Icon: CircleGauge };
    case 'budgetLimited':
    case 'budget_limited':
      return { label: '预算受限', tone: 'limited', Icon: CircleGauge };
    default:
      return { label: status?.trim() || '状态未知', tone: 'unknown', Icon: Circle };
  }
}

function WorkflowGoal({ goal, done, total }: { goal: SessionGoalView; done: number; total: number }) {
  const headingId = useId();
  const status = goalStatusMeta(goal.status);
  const progress = total > 0 ? Math.round((done / total) * 100) : 0;

  return (
    <section className="chat-workflow-goal" aria-labelledby={headingId}>
      <div className="chat-workflow-goal-head">
        <span className="chat-workflow-kicker">
          <Crosshair className="h-3.5 w-3.5" aria-hidden />
          <span id={headingId}>当前目标</span>
        </span>
        <span className={`chat-workflow-status chat-workflow-status-${status.tone}`}>
          <status.Icon className="h-3.5 w-3.5" aria-hidden />
          {status.label}
        </span>
      </div>
      <p className="chat-workflow-objective">{goal.objective}</p>
      {total > 0 && (
        <div className="chat-workflow-progress-row">
          <div
            className="chat-workflow-progress"
            role="progressbar"
            aria-label="目标执行进度"
            aria-valuemin={0}
            aria-valuemax={total}
            aria-valuenow={done}
          >
            <span style={{ width: `${progress}%` }} />
          </div>
          <span className="shrink-0 tabular-nums text-caption text-text-tertiary">{done}/{total}</span>
        </div>
      )}
    </section>
  );
}

function WorkflowProposal({ text, defaultOpen }: { text: string; defaultOpen: boolean }) {
  const [open, setOpen] = useState(defaultOpen);
  const bodyId = useId();
  const reduceMotion = useReducedMotion();

  return (
    <section className="chat-workflow-proposal">
      <button
        type="button"
        className="chat-workflow-proposal-head"
        onClick={() => setOpen((value) => !value)}
        aria-expanded={open}
        aria-controls={bodyId}
      >
        <span className="text-text-tertiary" aria-hidden>
          {open ? <ChevronDown className="h-3.5 w-3.5" /> : <ChevronRight className="h-3.5 w-3.5" />}
        </span>
        <FileText className="h-3.5 w-3.5 text-text-tertiary" aria-hidden />
        <span className="font-medium text-text-primary">方案草稿</span>
        <span className="chat-workflow-badge ml-auto">Plan 模式</span>
      </button>
      <AnimatePresence initial={false} mode="sync">
        {open && (
          <motion.div
            id={bodyId}
            key="proposal-body"
            className="chat-workflow-proposal-body"
            initial={reduceMotion ? false : { opacity: 0, height: 0, y: -4 }}
            animate={{ opacity: 1, height: 'auto', y: 0 }}
            exit={reduceMotion ? { opacity: 0 } : { opacity: 0, height: 0, y: -4 }}
            transition={reduceMotion ? { duration: 0 } : { duration: 0.24, ease: [0.16, 1, 0.3, 1] }}
          >
            <div className="chat-prose"><MarkdownBody text={text} /></div>
          </motion.div>
        )}
      </AnimatePresence>
    </section>
  );
}

function WorkflowActions({ steps }: { steps: WorkflowStep[] }) {
  const headingId = useId();
  const done = steps.filter((step) => step.status === 'completed').length;
  const listRef = useRef<HTMLOListElement>(null);
  const activeStep = steps.find((step) => step.status === 'in_progress')?.step;

  useEffect(() => {
    const list = listRef.current;
    const current = list?.querySelector<HTMLElement>('[aria-current="step"]');
    if (!list || !current) return;
    const top = current.offsetTop;
    const bottom = top + current.offsetHeight;
    if (top < list.scrollTop) list.scrollTop = top;
    else if (bottom > list.scrollTop + list.clientHeight) list.scrollTop = bottom - list.clientHeight;
  }, [activeStep, steps.length]);

  return (
    <section className="chat-workflow-actions" aria-labelledby={headingId}>
      <div className="chat-workflow-actions-head">
        <span className="chat-workflow-kicker">
          <ListChecks className="h-3.5 w-3.5" aria-hidden />
          <span id={headingId}>本轮执行计划</span>
        </span>
        <span className="tabular-nums text-caption text-text-tertiary" aria-live="polite">{done}/{steps.length} 已完成</span>
      </div>
      <ol ref={listRef} className="chat-workflow-action-list" aria-label="执行步骤">
        {steps.map((step, index) => (
          <WorkflowAction key={`${index}-${step.step}`} step={step} index={index} />
        ))}
      </ol>
    </section>
  );
}

function WorkflowAction({ step, index }: { step: WorkflowStep; index: number }) {
  const reduceMotion = useReducedMotion();
  const isDone = step.status === 'completed';
  const isActive = step.status === 'in_progress';
  const isInterrupted = step.status === 'interrupted';
  const statusLabel = isDone ? '已完成' : isActive ? '进行中' : isInterrupted ? '已中断' : '待处理';
  const StatusIcon = isDone ? Check : isActive ? LoaderCircle : isInterrupted ? CirclePause : Circle;

  return (
    <motion.li
      className="chat-workflow-action-item"
      data-status={step.status}
      aria-current={isActive ? 'step' : undefined}
      initial={reduceMotion ? false : { opacity: 0, y: 6 }}
      animate={{ opacity: 1, y: 0 }}
      transition={reduceMotion ? { duration: 0 } : { delay: Math.min(index * 0.045, 0.2), duration: 0.22 }}
      layout
    >
      <article className="chat-workflow-action-card">
        <header className="chat-workflow-action-head">
          <span className="chat-workflow-badge">Action {index + 1}</span>
          <span className={`chat-workflow-step-status chat-workflow-step-status-${step.status}`}>
            <StatusIcon className="h-3.5 w-3.5" aria-hidden />
            {statusLabel}
          </span>
        </header>
        <p className="chat-workflow-action-title">{step.step}</p>
      </article>
    </motion.li>
  );
}
