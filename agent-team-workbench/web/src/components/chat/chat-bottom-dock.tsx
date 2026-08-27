import { AnimatePresence, motion, useReducedMotion } from 'motion/react';
import { ChevronDown, ChevronRight, Crosshair, FileText, ListChecks } from 'lucide-react';
import { useId, useState, type ReactNode } from 'react';
import type { ChatMessage } from '../../stores/chat.store';
import type { SessionGoalView } from '../../utils/derive-chat-dock';
import { InkBentoItem } from '../ink/ink-bento';
import { TextGenerateEffect } from '../aceternity/text-generate-effect';
import { MarkdownBody } from './markdown-body';
import { PlanCard } from './plan-card';

const dockSpring = { type: 'spring' as const, stiffness: 340, damping: 30, mass: 0.8 };

export function ChatBottomDock({
  goal,
  todoPlan,
  proposedPlan,
}: {
  goal?: SessionGoalView;
  todoPlan?: ChatMessage;
  proposedPlan?: string;
}) {
  const reduceMotion = useReducedMotion();
  if (!goal && !todoPlan && !proposedPlan) return null;
  return (
    <motion.div
      className="chat-bottom-dock mb-2 space-y-2"
      initial={reduceMotion ? false : { opacity: 0, y: 10 }}
      animate={{ opacity: 1, y: 0 }}
      transition={reduceMotion ? { duration: 0 } : dockSpring}
      layout
    >
      {goal && <DockGoal goal={goal} />}
      {todoPlan && <DockTodoPlan plan={todoPlan} />}
      {proposedPlan && <DockProposedPlan text={proposedPlan} />}
    </motion.div>
  );
}

function DockPanel({
  dock,
  header,
  open,
  body,
  bodyId,
}: {
  dock: 'goal' | 'todo-list' | 'proposed-plan';
  header: ReactNode;
  open: boolean;
  body: ReactNode;
  bodyId: string;
}) {
  const reduceMotion = useReducedMotion();
  return (
    <motion.div layout transition={reduceMotion ? { duration: 0 } : dockSpring} data-dock={dock}>
      <InkBentoItem
        className="!min-h-0 !gap-0 !space-y-0 !rounded-card !border-border-subtle !bg-surface-raised/90 !p-0 shadow-card"
        contentClassName="!text-caption"
        header={header}
        description={(
          <AnimatePresence initial={false} mode="sync">
            {open && (
              <motion.div
                id={bodyId}
                key="dock-body"
                className="chat-dock-body"
                initial={reduceMotion ? false : { opacity: 0, height: 0, y: -4 }}
                animate={{ opacity: 1, height: 'auto', y: 0 }}
                exit={reduceMotion ? { opacity: 0 } : { opacity: 0, height: 0, y: -4 }}
                transition={reduceMotion ? { duration: 0 } : { duration: 0.24, ease: [0.16, 1, 0.3, 1] }}
                layout
              >
                {body}
              </motion.div>
            )}
          </AnimatePresence>
        )}
      />
    </motion.div>
  );
}

function DockHeader({
  open,
  onToggle,
  icon,
  label,
  meta,
  bodyId,
}: {
  open: boolean;
  onToggle: () => void;
  icon: ReactNode;
  label: string;
  meta: ReactNode;
  bodyId: string;
}) {
  return (
    <button
      type="button"
      className="chat-dock-head group relative flex w-full items-center gap-2 text-left"
      onClick={onToggle}
      aria-expanded={open}
      aria-controls={bodyId}
    >
      <span className="text-text-tertiary transition-transform duration-ink group-hover:translate-x-0.5" aria-hidden>
        {open ? <ChevronDown className="h-3.5 w-3.5" /> : <ChevronRight className="h-3.5 w-3.5" />}
      </span>
      <span className="text-text-tertiary">{icon}</span>
      <span className="font-medium text-text-primary">{label}</span>
      <span className="ml-auto text-caption text-text-tertiary">{meta}</span>
      <span
        className="absolute bottom-0 left-8 right-3 h-px origin-left scale-x-50 bg-border-subtle/70 transition-transform duration-ink group-hover:scale-x-100"
        aria-hidden
      />
    </button>
  );
}

function DockTodoPlan({ plan }: { plan: ChatMessage }) {
  // 默认展开仅当有进行中步骤（有活动才值得占位）；静止清单默认折叠成一行。
  const [open, setOpen] = useState(() => (plan.steps ?? []).some((step) => step.status === 'in_progress'));
  const bodyId = useId();
  const steps = plan.steps ?? [];
  const inProgress = steps.filter((step) => step.status === 'in_progress').length;
  const pending = steps.filter((step) => step.status === 'pending').length;
  const done = steps.filter((step) => step.status === 'completed').length;
  return (
    <DockPanel
      dock="todo-list"
      open={open}
      bodyId={bodyId}
      header={(
        <DockHeader
          open={open}
          onToggle={() => setOpen((value) => !value)}
          bodyId={bodyId}
          icon={<ListChecks className="h-3.5 w-3.5" />}
          label="任务"
          meta={(
            <>
              {inProgress > 0 && `${inProgress} 进行中`}
              {inProgress > 0 && pending > 0 && ' · '}
              {pending > 0 && `${pending} 待处理`}
              {inProgress === 0 && pending === 0 && done > 0 && `${done}/${steps.length} 已完成`}
            </>
          )}
        />
      )}
      body={<PlanCard msg={plan} compact />}
    />
  );
}

function DockProposedPlan({ text }: { text: string }) {
  const [open, setOpen] = useState(true);
  const bodyId = useId();
  return (
    <DockPanel
      dock="proposed-plan"
      open={open}
      bodyId={bodyId}
      header={(
        <DockHeader
          open={open}
          onToggle={() => setOpen((value) => !value)}
          bodyId={bodyId}
          icon={<FileText className="h-3.5 w-3.5" />}
          label="计划"
          meta="Plan 模式"
        />
      )}
      body={<div className="chat-prose"><MarkdownBody text={text} /></div>}
    />
  );
}

function DockGoal({ goal }: { goal: SessionGoalView }) {
  // 目标一行即可扫读，默认折叠；状态标签（TextGenerateEffect）在头上已承载活性。
  const [open, setOpen] = useState(false);
  const bodyId = useId();
  const reduceMotion = useReducedMotion();
  const statusLabel = goal.status === 'complete' ? '已完成' : goal.status === 'paused' ? '已暂停' : goal.status === 'blocked' ? '受阻' : '进行中';
  return (
    <DockPanel
      dock="goal"
      open={open}
      bodyId={bodyId}
      header={(
        <DockHeader
          open={open}
          onToggle={() => setOpen((value) => !value)}
          bodyId={bodyId}
          icon={<Crosshair className="h-3.5 w-3.5" />}
          label="目标"
          meta={(
            <TextGenerateEffect
              as="span"
              words={statusLabel}
              className="text-caption text-text-tertiary"
              duration={reduceMotion ? 0 : 0.35}
            />
          )}
        />
      )}
      body={(
        <p className="whitespace-pre-wrap break-words text-caption leading-5 text-text-secondary">
          {goal.objective}
        </p>
      )}
    />
  );
}
