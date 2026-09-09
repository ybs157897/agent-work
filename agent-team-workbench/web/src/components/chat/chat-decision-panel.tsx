import { AlertCircle, Check, ChevronDown, ClipboardCheck, LoaderCircle, RotateCcw } from 'lucide-react';
import type { ChatAnalysisDecision, ChatAnalysisItem, ChatDecisionOutcome } from '../../api/chat-analysis';
import type { ChatDecisionDraft } from '../../stores/chat-decisions.store';
import type { ChatDecisionHistoryEntry } from '../../api/chat-decisions';
import { Button } from '../ui';

function outcomeLabel(outcome: ChatDecisionOutcome): string {
  switch (outcome) {
    case 'confirmed': return '确认';
    case 'rejected': return '否决';
    default: return '待澄清';
  }
}

function decisionLabel(decision: ChatAnalysisDecision): string {
  if (decision.status === 'needs_reconfirmation') return '需重新确认';
  if (decision.status === 'stale') return '已失效';
  return `已${outcomeLabel(decision.outcome)}`;
}

export function ChatDecisionPanel({
  item,
  items,
  selectedItemId,
  decision,
  revision,
  recheckReason,
  history,
  draft,
  loading,
  historyLoading,
  submitting,
  error,
  onOutcome,
  onSelectItem,
  onConclusion,
  onBasis,
  onProductVersion,
  onSubmit,
  onRecheck,
  onRestoreDraftText,
  onDiscardDraft,
  onReviewChanges,
}: {
  item: ChatAnalysisItem | null;
  items: readonly ChatAnalysisItem[];
  selectedItemId: string | null;
  decision: ChatAnalysisDecision | null;
  revision: number;
  recheckReason?: string;
  history: readonly ChatDecisionHistoryEntry[];
  draft: ChatDecisionDraft | null;
  loading: boolean;
  historyLoading: boolean;
  submitting: boolean;
  error: string | null;
  onOutcome: (outcome: ChatDecisionOutcome) => void;
  onSelectItem: (itemId: string) => void;
  onConclusion: (value: string) => void;
  onBasis: (value: string) => void;
  onProductVersion: (value: string) => void;
  onSubmit: () => void;
  onRecheck: () => void;
  onRestoreDraftText: () => void;
  onDiscardDraft: () => void;
  onReviewChanges: () => void;
}) {
  if (!item && history.length === 0 && !loading && !error) return null;
  const staleDraft = draft && (!item || draft.itemId !== item.id || draft.revision !== revision) ? draft : null;
  const activeDraft = item && draft && draft.itemId === item.id && !staleDraft ? draft : null;

  return (
    <section className="mt-tight rounded-card border border-status-warning/30 bg-surface-raised px-snug py-tight shadow-card" aria-label="产品结论回填">
      <header className="flex flex-wrap items-center justify-between gap-tight">
        <div className="flex min-w-0 items-center gap-tight"><ClipboardCheck className="h-4 w-4 shrink-0 text-status-warning" aria-hidden="true" /><span className="text-body font-semibold text-text-primary">产品结论回填</span>{decision && <span className={`rounded-full px-tight py-micro text-caption ${decision.status === 'needs_reconfirmation' || decision.status === 'stale' ? 'bg-status-warning/10 text-status-warning' : 'bg-status-success/10 text-status-success'}`}>{decisionLabel(decision)}</span>}</div>
        <div className="flex items-center gap-tight">{loading && <LoaderCircle className="h-4 w-4 animate-spin text-brand-primary motion-reduce:animate-none" aria-label="产品结论加载中" />}{!loading && error && <Button type="button" size="sm" variant="ghost" onClick={onRecheck}><RotateCcw className="h-3.5 w-3.5" aria-hidden="true" />刷新核对</Button>}</div>
      </header>
      {error && <div className="mt-tight flex items-start gap-micro text-caption text-status-error" role="alert"><AlertCircle className="mt-px h-3.5 w-3.5 shrink-0" aria-hidden="true" /><span>{error}</span></div>}
      {recheckReason && <div className="mt-tight rounded-button border border-status-warning/30 bg-status-warning/5 px-tight py-tight text-caption text-status-warning" role="status">需要复核：{recheckReason}</div>}
      {item && <div className="mt-tight rounded-button border border-border-subtle bg-surface-base px-snug py-tight"><div className="flex flex-wrap items-center justify-between gap-tight"><div className="text-caption text-text-tertiary">当前事项</div>{items.length > 1 && <label className="flex items-center gap-micro text-caption text-text-secondary"><span className="sr-only">切换分析事项</span><select value={selectedItemId ?? item.id} onChange={(event) => onSelectItem(event.target.value)} className="max-w-[20rem] rounded-button border border-border-strong bg-surface-raised px-tight py-micro text-caption text-text-primary"><option value="" disabled>选择事项</option>{items.map((candidate) => <option key={candidate.id} value={candidate.id}>{candidate.title}</option>)}</select></label>}</div><h3 className="mt-micro text-body font-medium text-text-primary">{item.title}</h3><p className="mt-micro whitespace-pre-wrap text-caption text-text-secondary">{item.detail}</p></div>}

      {staleDraft && <div className="mt-tight rounded-button border border-status-warning/30 bg-status-warning/5 px-tight py-tight text-caption text-status-warning" role="status"><p>当前事项或分析版本已变化，旧结论草稿不会自动套用。</p><p className="mt-micro whitespace-pre-wrap">{staleDraft.conclusion}</p><button type="button" className="mt-micro mr-tight font-medium underline" onClick={onRestoreDraftText}>保留结论文字并重新填写</button><button type="button" className="mt-micro font-medium underline" onClick={onDiscardDraft}>丢弃旧草稿</button></div>}

      {item && !staleDraft && <fieldset className="mt-tight" disabled={submitting} aria-label="产品结论表单"><legend className="text-caption font-medium text-text-secondary">本次回填</legend><div className="mt-tight flex flex-wrap gap-tight">{(['confirmed', 'rejected', 'needs_clarification'] as ChatDecisionOutcome[]).map((outcome) => <label key={outcome} className={`flex cursor-pointer items-center gap-micro rounded-button border px-tight py-micro text-caption ${activeDraft?.outcome === outcome ? 'border-brand-primary/45 bg-brand-muted/45 text-text-primary' : 'border-border-subtle bg-surface-base text-text-secondary'}`}><input type="radio" name={`decision-outcome-${item.id}`} checked={activeDraft?.outcome === outcome} onChange={() => onOutcome(outcome)} className="accent-brand-primary" />{outcomeLabel(outcome)}{activeDraft?.outcome === outcome && <Check className="h-3.5 w-3.5 text-brand-primary" aria-hidden="true" />}</label>)}</div><label className="mt-tight block"><span className="text-caption text-text-secondary">结论</span><textarea value={activeDraft?.conclusion ?? ''} onChange={(event) => onConclusion(event.target.value)} rows={3} className="mt-micro w-full resize-y rounded-button border border-border-strong bg-surface-raised px-snug py-tight text-body text-text-primary outline-none focus:border-brand-primary/40 focus:ring-2 focus:ring-brand-primary/20" placeholder="开发与产品沟通后的明确结论" /></label><label className="mt-tight block"><span className="text-caption text-text-secondary">沟通依据</span><textarea value={activeDraft?.basis ?? ''} onChange={(event) => onBasis(event.target.value)} rows={2} className="mt-micro w-full resize-y rounded-button border border-border-strong bg-surface-raised px-snug py-tight text-body text-text-primary outline-none focus:border-brand-primary/40 focus:ring-2 focus:ring-brand-primary/20" placeholder="记录依据、讨论结果或需要保留的不确定性" /></label><label className="mt-tight block"><span className="text-caption text-text-secondary">产品版本</span><input value={activeDraft?.productVersion ?? ''} onChange={(event) => onProductVersion(event.target.value)} className="mt-micro w-full rounded-button border border-border-strong bg-surface-raised px-snug py-tight text-body text-text-primary outline-none focus:border-brand-primary/40 focus:ring-2 focus:ring-brand-primary/20" placeholder="例如 2026-Q3 / v1.2" /></label><div className="mt-tight flex flex-wrap justify-end gap-tight"><Button type="button" variant="ghost" onClick={onReviewChanges}>补充变更并复核</Button><Button type="button" variant="primary" onClick={onSubmit} disabled={submitting || !activeDraft?.conclusion.trim() || !activeDraft?.basis.trim() || !activeDraft?.productVersion.trim()}>{submitting ? '保存中…' : '保存产品结论'}</Button></div></fieldset>}

      {history.length > 0 && <details className="mt-tight rounded-button border border-border-subtle bg-surface-base px-tight py-micro"><summary className="flex cursor-pointer list-none items-center gap-micro text-caption font-medium text-text-secondary"><ChevronDown className="h-3.5 w-3.5" aria-hidden="true" />查看历史与已答记录（{history.length}）</summary><ul className="mt-tight space-y-micro">{history.map((entry) => <li key={entry.id} className="rounded-button bg-surface-sunken/45 px-tight py-micro text-caption text-text-secondary"><div className="flex flex-wrap items-center gap-micro"><span>{entry.kind === 'decision' ? '产品结论' : entry.kind === 'reopen' ? '重新复核' : entry.kind === 'answer' ? '开发回答' : '分析版本'}</span><span className="text-text-tertiary">revision {entry.revision}</span><span className="text-text-tertiary">{entry.created_at}</span></div>{entry.outcome && <p className="mt-micro">结果：{outcomeLabel(entry.outcome)}</p>}{entry.conclusion && <p className="mt-micro whitespace-pre-wrap">结论：{entry.conclusion}</p>}{entry.reason && <p className="mt-micro">原因：{entry.reason}</p>}</li>)}</ul>{historyLoading && <p className="mt-micro text-caption text-text-tertiary">历史加载中…</p>}</details>}
    </section>
  );
}
