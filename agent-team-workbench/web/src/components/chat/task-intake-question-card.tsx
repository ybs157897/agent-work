import { Check, CircleHelp } from 'lucide-react';
import { Button } from '../ui';
import type { TaskIntakeQuestion } from '../../api/task-intake';
import type { TaskIntakeQuestionAnswer, TaskIntakeQuestionAnswers } from '../../stores/task-intake.store';
import { taskIntakeQuestionAnswerKey } from '../../stores/task-intake.store';

export interface TaskIntakeQuestionCardProps {
  messageId: string;
  question: TaskIntakeQuestion;
  answer?: TaskIntakeQuestionAnswer;
  disabled?: boolean;
  onSelection: (messageId: string, questionId: string, selected: string[]) => void;
  onSupplement: (messageId: string, questionId: string, supplement: string) => void;
}

/**
 * Task intake 专用需求问题卡。它只消费服务端明示的题目与选项，
 * 不从 assistant Markdown 猜测交互，也不触碰 AgentOutput 的只读正文协议。
 */
export function TaskIntakeQuestionCard({
  messageId,
  question,
  answer,
  disabled = false,
  onSelection,
  onSupplement,
}: TaskIntakeQuestionCardProps) {
  const selected = answer?.selected ?? [];
  const locked = disabled || Boolean(answer?.lockedReason) || answer?.submitted === true;
  const lockLabel = answer?.lockedReason === 'submitted' || answer?.submitted
    ? '已提交回答'
    : answer?.lockedReason === 'continued'
      ? (selected.length > 0 || answer.supplement.trim() ? '已继续对话（选择未提交）' : '已跳过')
      : '待回答';
  const inputName = `task-intake-${messageId}-${question.id}`;

  return (
    <fieldset
      className={`rounded-card border px-snug py-tight ${locked ? 'border-border-subtle bg-surface-sunken/45' : 'border-border-subtle bg-surface-raised'}`}
      disabled={locked}
      aria-label={question.title}
    >
      <legend className="flex max-w-full items-center gap-tight px-micro text-body font-medium text-text-primary">
        <CircleHelp className="h-4 w-4 shrink-0 text-brand-primary" aria-hidden />
        <span className="min-w-0">{question.title}</span>
        <span className="shrink-0 text-caption font-normal text-text-tertiary">{question.multiple ? '可多选' : '单选'} · {lockLabel}</span>
      </legend>
      <div className="mt-tight grid gap-tight sm:grid-cols-2">
        {question.options.map((option) => {
          const checked = selected.includes(option);
          return (
            <label
              key={option}
              className={`flex cursor-pointer items-start gap-tight rounded-button border px-snug py-tight text-body transition-colors ${checked ? 'border-brand-primary/45 bg-brand-muted/45 text-text-primary' : 'border-border-subtle bg-surface-base text-text-secondary hover:border-brand-primary/30'} ${locked ? 'cursor-default opacity-75' : ''}`}
            >
              <input
                type={question.multiple ? 'checkbox' : 'radio'}
                name={inputName}
                value={option}
                checked={checked}
                onChange={() => {
                  const next = question.multiple
                    ? checked ? selected.filter((item) => item !== option) : [...selected, option]
                    : [option];
                  onSelection(messageId, question.id, next);
                }}
                className="mt-0.5 accent-brand-primary"
              />
              <span className="min-w-0 break-words">{option}</span>
              {checked && <Check className="ml-auto h-4 w-4 shrink-0 text-brand-primary" aria-hidden />}
            </label>
          );
        })}
      </div>
      <label className="mt-tight block">
        <span className="text-caption text-text-secondary">补充说明（可选）</span>
        <textarea
          value={answer?.supplement ?? ''}
          onChange={(event) => onSupplement(messageId, question.id, event.target.value)}
          rows={2}
          placeholder="没有合适选项时，可直接写在这里"
          className="mt-micro w-full resize-y rounded-button border border-border-strong bg-surface-raised px-snug py-tight text-body text-text-primary outline-none transition-shadow focus:border-brand-primary/40 focus:ring-2 focus:ring-brand-primary/20 disabled:cursor-default disabled:opacity-75"
          disabled={locked}
          aria-label={`${question.title}补充说明`}
        />
      </label>
    </fieldset>
  );
}

export function TaskIntakeQuestionGroup({
  messageId,
  questions,
  answers,
  disabled = false,
  onSelection,
  onSupplement,
  onSubmit,
}: {
  messageId: string;
  questions: readonly TaskIntakeQuestion[];
  answers: TaskIntakeQuestionAnswers;
  disabled?: boolean;
  onSelection: TaskIntakeQuestionCardProps['onSelection'];
  onSupplement: TaskIntakeQuestionCardProps['onSupplement'];
  onSubmit: (messageId: string) => void;
}) {
  const keyFor = (question: TaskIntakeQuestion) => taskIntakeQuestionAnswerKey(messageId, question.id);
  const openQuestions = questions.filter((question) => {
    const answer = answers[keyFor(question)];
    return !answer?.lockedReason && !answer?.submitted;
  });
  const missingQuestions = openQuestions.filter((question) => {
    const answer = answers[keyFor(question)];
    return !answer || (!answer.selected.length && !answer.supplement.trim());
  });
  if (questions.length === 0) return null;

  return (
    <section className="space-y-snug" aria-label="需求澄清问题">
      {questions.map((question) => (
        <TaskIntakeQuestionCard
          key={question.id}
          messageId={messageId}
          question={question}
          answer={answers[keyFor(question)]}
          disabled={disabled}
          onSelection={onSelection}
          onSupplement={onSupplement}
        />
      ))}
      {openQuestions.length > 0 && (
        <div className="flex flex-wrap items-center justify-between gap-snug rounded-card border border-brand-primary/20 bg-brand-muted/25 px-snug py-tight">
          <p className="text-caption text-text-secondary">
            {missingQuestions.length > 0
              ? `仍待回答：${missingQuestions.map((question) => question.title).join('、')}`
              : '可以提交这些回答，也可以继续补充更多背景。'}
          </p>
          <Button type="button" size="sm" variant="primary" onClick={() => onSubmit(messageId)} disabled={disabled}>
            提交回答
          </Button>
        </div>
      )}
    </section>
  );
}
