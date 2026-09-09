import { CheckCircle2, CircleHelp, LoaderCircle, SkipForward } from 'lucide-react';
import { useMemo, useState } from 'react';
import type { NativeQuestion, NativeQuestionAnswer, NativeQuestionResponse } from '../../api/questions';

type Selection = Record<string, string[]>;
type OtherText = Record<string, string>;

export function buildNativeQuestionResponse(question: NativeQuestion, selections: Selection, otherText: OtherText): NativeQuestionResponse {
  const answers: Record<string, NativeQuestionAnswer> = {};
  for (const item of question.questions) {
    const optionIds = selections[item.id] ?? [];
    const other = (otherText[item.id] ?? '').trim();
    if (item.multi_select) {
      if (optionIds.length > 0 && other) answers[item.id] = { kind: 'multi_with_other', option_ids: optionIds, other_text: other };
      else if (optionIds.length > 0) answers[item.id] = { kind: 'multi', option_ids: optionIds };
      else if (other) answers[item.id] = { kind: 'other', text: other };
    } else if (other) answers[item.id] = { kind: 'other', text: other };
    else if (optionIds.length > 0) answers[item.id] = { kind: 'single', option_id: optionIds[0] };
  }
  return { answers, method: 'click' };
}

export function NativeQuestionCard({ question, submitting, onResolve }: { question: NativeQuestion; submitting: boolean; onResolve: (response: NativeQuestionResponse) => Promise<void> }) {
  const [selections, setSelections] = useState<Selection>({});
  const [otherText, setOtherText] = useState<OtherText>({});
  const [error, setError] = useState<string | null>(null);
  const response = useMemo(() => buildNativeQuestionResponse(question, selections, otherText), [otherText, question, selections]);
  if (question.status !== 'pending') return null;
  const submit = async (next: NativeQuestionResponse) => {
    if (Object.keys(next.answers).length === 0) { setError('请选择或填写一个回答。'); return; }
    setError(null);
    try {
      await onResolve(next);
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : '提交回答失败，请重试。');
    }
  };
  const toggle = (itemID: string, optionID: string, multi: boolean) => {
    const current = selections[itemID] ?? [];
    setSelections((value) => ({ ...value, [itemID]: multi ? (current.includes(optionID) ? current.filter((id) => id !== optionID) : [...current, optionID]) : [optionID] }));
    if (!multi) setOtherText((value) => {
      if (!(itemID in value)) return value;
      const next = { ...value };
      delete next[itemID];
      return next;
    });
  };
  return (
    <section className="workbench-panel rounded-card border border-brand-primary/25 bg-surface-raised px-snug py-tight shadow-card" aria-label="需要你的回答" data-testid={`native-question-${question.id}`}>
      <div className="flex items-start gap-tight">
        <CircleHelp className="mt-0.5 h-4 w-4 shrink-0 text-brand-primary" aria-hidden="true" />
        <div className="min-w-0 flex-1">
          <p className="text-caption font-semibold text-text-primary">智能体需要你的回答</p>
          <p className="mt-micro text-caption text-text-secondary">回答后，智能体会继续处理。</p>
        </div>
        {submitting && <LoaderCircle className="h-4 w-4 animate-spin text-brand-primary motion-reduce:animate-none" aria-label="提交中" />}
      </div>
      <div className="mt-tight space-y-snug">
        {question.questions.map((item) => (
          <fieldset key={item.id} disabled={submitting} className="space-y-micro">
            <legend className="text-body font-medium text-text-primary">{item.header ? `${item.header}：` : ''}{item.question}</legend>
            {item.body && <p className="text-caption text-text-secondary">{item.body}</p>}
            <div className="mt-micro space-y-micro" role={item.multi_select ? 'group' : 'radiogroup'} aria-label={item.question}>
              {item.options.map((option) => {
                const selected = (selections[item.id] ?? []).includes(option.id);
                return <label key={option.id} className="flex cursor-pointer items-start gap-tight rounded-button border border-border-subtle bg-surface-base px-tight py-micro text-caption text-text-secondary hover:border-brand-primary/40 has-[:focus-visible]:ring-2 has-[:focus-visible]:ring-brand-primary/40">
                  <input type={item.multi_select ? 'checkbox' : 'radio'} name={`question-${question.id}-${item.id}`} checked={selected} onChange={() => toggle(item.id, option.id, item.multi_select === true)} className="mt-0.5 accent-brand-primary" />
                  <span><span className="text-text-primary">{option.label}</span>{option.description && <span className="ml-1 text-text-tertiary">{option.description}</span>}</span>
                  {selected && <CheckCircle2 className="ml-auto h-3.5 w-3.5 shrink-0 text-brand-primary" aria-hidden="true" />}
                </label>;
              })}
            </div>
            {item.allow_other === true && <label className="mt-micro block text-caption text-text-secondary">{item.other_label ?? '其他'}
              <textarea value={otherText[item.id] ?? ''} onChange={(event) => {
                const nextText = event.target.value;
                setOtherText((value) => ({ ...value, [item.id]: nextText }));
                if (!item.multi_select && nextText.trim()) setSelections((value) => ({ ...value, [item.id]: [] }));
              }} placeholder={item.other_description ?? '填写其他回答（可选）'} rows={2} className="mt-micro block w-full rounded-button border border-border-subtle bg-surface-base px-tight py-micro text-body text-text-primary outline-none focus-visible:ring-2 focus-visible:ring-brand-primary/40" />
            </label>}
          </fieldset>
        ))}
      </div>
      {error && <p className="mt-tight text-caption text-status-error" role="alert">{error}</p>}
      <div className="mt-tight flex flex-wrap justify-end gap-tight">
        <button type="button" disabled={submitting} onClick={() => void submit({ answers: Object.fromEntries(question.questions.map((item) => [item.id, { kind: 'skipped' as const }])), method: 'click' })} className="inline-flex min-h-8 items-center gap-micro rounded-button px-tight text-caption text-text-secondary hover:bg-surface-sunken disabled:cursor-not-allowed disabled:opacity-55"><SkipForward className="h-3.5 w-3.5" aria-hidden="true" />跳过</button>
        <button type="button" disabled={submitting} onClick={() => void submit(response)} className="inline-flex min-h-8 items-center gap-micro rounded-button bg-brand-primary px-snug text-caption font-medium text-text-inverse hover:bg-brand-accent disabled:cursor-not-allowed disabled:opacity-55">提交回答</button>
      </div>
    </section>
  );
}
