import { motion, useReducedMotion } from 'motion/react';
import { Check, ListChecks } from 'lucide-react';
import type { ChatMessage, PlanStepView } from '../../stores/chat.store';

/** run.plan_updated 的复选清单卡：保持最新快照，状态以动效辅助而不替代文字。 */
export function PlanCard({ msg, compact = false }: { msg: ChatMessage; compact?: boolean }) {
  const steps = msg.steps ?? [];
  const done = steps.filter((s) => s.status === 'completed').length;
  const reduceMotion = useReducedMotion();
  const list = (
    <ol className="chat-plan-list space-y-1">
      {steps.map((step, index) => <PlanRow key={`${index}-${step.step}`} step={step} index={index} />)}
    </ol>
  );
  if (compact) return list;
  return (
    <motion.div className="chat-plan" layout transition={reduceMotion ? { duration: 0 } : { duration: 0.25 }}>
      <div className="chat-plan-head">
        <ListChecks className="h-4 w-4 shrink-0 text-text-tertiary" />
        <span className="font-medium">计划</span>
        <span className="ml-auto shrink-0 tabular-nums text-text-tertiary">{done}/{steps.length}</span>
      </div>
      {list}
    </motion.div>
  );
}

function PlanRow({ step, index }: { step: PlanStepView; index: number }) {
  const reduceMotion = useReducedMotion();
  const isDone = step.status === 'completed';
  const isActive = step.status === 'in_progress';
  const markerAnimation = isDone
    ? { scale: [0.82, 1.08, 1], rotate: [0, -4, 0] }
    : isActive
      ? { opacity: [0.62, 1, 0.62], scale: [0.97, 1.03, 0.97] }
      : { opacity: 1, scale: 1 };
  return (
    <motion.li
      layout
      className={`chat-plan-row ${isDone ? 'text-text-tertiary line-through' : isActive ? 'font-medium text-text-primary' : 'text-text-secondary'}`}
      initial={reduceMotion ? false : { opacity: 0, x: -6 }}
      animate={{ opacity: 1, x: 0 }}
      transition={reduceMotion ? { duration: 0 } : { delay: Math.min(index * 0.035, 0.2), duration: 0.22 }}
    >
      <motion.span
        className={`chat-plan-box ${isDone ? 'chat-plan-box-done' : isActive ? 'chat-plan-box-active' : ''}`}
        aria-hidden
        animate={reduceMotion ? undefined : markerAnimation}
        transition={reduceMotion
          ? { duration: 0 }
          : isActive
            ? { duration: 2.8, repeat: Infinity, ease: 'easeInOut' }
            : { duration: 0.34, ease: [0.16, 1, 0.3, 1] }}
      >
        {isDone && <Check className="h-3 w-3" />}
      </motion.span>
      <span className="min-w-0 flex-1">{step.step}</span>
      {isActive && <span className="shrink-0 text-caption text-status-info">进行中</span>}
    </motion.li>
  );
}
