import { Check, ListChecks } from 'lucide-react';
import type { ChatMessage, PlanStepView } from '../../stores/chat.store';

/**
 * run.plan_updated 的复选清单卡（对齐 codex-desktop 渲染对比 Q10）：
 * 同 run 新帧在 store 层已替换旧帧——这里只渲染最新快照。
 * completed 勾选灰化、in_progress 高亮、pending 普通。
 */
export function PlanCard({ msg }: { msg: ChatMessage }) {
  const steps = msg.steps ?? [];
  const done = steps.filter((s) => s.status === 'completed').length;
  return (
    <div className="chat-plan">
      <div className="chat-plan-head">
        <ListChecks className="h-4 w-4 shrink-0 text-text-tertiary" />
        <span className="font-medium">计划</span>
        <span className="ml-auto shrink-0 tabular-nums text-text-tertiary">
          {done}/{steps.length}
        </span>
      </div>
      <ol className="chat-plan-list">
        {steps.map((s, i) => (
          <PlanRow key={`${i}-${s.step}`} step={s} />
        ))}
      </ol>
    </div>
  );
}

function PlanRow({ step }: { step: PlanStepView }) {
  if (step.status === 'completed') {
    return (
      <li className="chat-plan-row text-text-tertiary line-through">
        <span className="chat-plan-box chat-plan-box-done">
          <Check className="h-3 w-3" />
        </span>
        <span className="min-w-0 flex-1">{step.step}</span>
      </li>
    );
  }
  if (step.status === 'in_progress') {
    return (
      <li className="chat-plan-row font-medium text-text-primary">
        <span className="chat-plan-box chat-plan-box-active" aria-hidden />
        <span className="min-w-0 flex-1">{step.step}</span>
        <span className="shrink-0 text-[11px] text-status-info">进行中</span>
      </li>
    );
  }
  return (
    <li className="chat-plan-row text-text-secondary">
      <span className="chat-plan-box" aria-hidden />
      <span className="min-w-0 flex-1">{step.step}</span>
    </li>
  );
}
