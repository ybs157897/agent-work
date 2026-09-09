import { AlertCircle, Check, ChevronDown, CircleHelp, ListChecks, LoaderCircle, RotateCcw } from 'lucide-react';
import type {
  ChatAnalysisItem,
  ChatAnalysisProjection,
  ChatAnalysisQuestion,
  ChatAnalysisSource,
} from '../../api/chat-analysis';
import type { ChatSource } from '../../api/chat-sources';
import type { ChatAnalysisDraft } from '../../stores/chat-analysis.store';
import { Button } from '../ui';

function itemKindLabel(kind: ChatAnalysisItem['kind']): string {
  switch (kind) {
    case 'requirement': return '需求';
    case 'normal': return '正常场景';
    case 'exception': return '异常场景';
    case 'conflict': return '材料冲突';
    default: return '待核对';
  }
}

function sourceKindLabel(kind: ChatAnalysisSource['kind']): string {
  switch (kind) {
    case 'attachment': return '附件';
    case 'conversation': return '对话';
    case 'code': return '代码';
    default: return '知识';
  }
}

function sourceDisplayLabel(source: ChatAnalysisSource, conversationSources: readonly ChatSource[]): string {
  if (source.kind === 'attachment') {
    const attachment = conversationSources.find((item) => item.id === source.ref);
    return attachment?.filename ? `附件：${attachment.filename}` : '附件原件';
  }
  if (source.kind === 'code') return `代码：${source.ref}`;
  if (source.kind === 'knowledge') return `知识：${source.ref}${source.version ? ` · v${source.version}` : ''}`;
  return '当前对话';
}

function statusLabel(status: ChatAnalysisProjection['status']): string {
  switch (status) {
    case 'analyzing': return '正在整理';
    case 'needs_answer': return '等待你的回答';
    case 'ready': return '已整理，待确认';
    case 'failed': return '整理失败';
    case 'stale': return '内容已变化';
    default: return '尚未开始';
  }
}

function questionHasAnswer(question: ChatAnalysisQuestion, draft: ChatAnalysisDraft | null): boolean {
  if (!draft || draft.questionId !== question.id) return false;
  return draft.selectedOptionIds.length > 0 || draft.text.trim().length > 0;
}

export function ChatAnalysisPanel({
  projection,
  draft,
  loading,
  submitting,
  error,
  onSelection,
  onText,
  onSubmit,
  onRestart,
  onRefresh,
  onRestoreDraftText,
  onDiscardDraft,
  conversationSources,
}: {
  projection: ChatAnalysisProjection | null;
  draft: ChatAnalysisDraft | null;
  loading: boolean;
  submitting: boolean;
  error: string | null;
  onSelection: (selectedOptionIds: string[]) => void;
  onText: (text: string) => void;
  onSubmit: (disposition: 'answered' | 'deferred') => void;
  onRestart: () => void;
  onRefresh: () => void;
  onRestoreDraftText: () => void;
  onDiscardDraft: () => void;
  conversationSources: readonly ChatSource[];
}) {
  const question = projection?.current_question;
  const activeDraft = question && projection && draft?.questionId === question.id && draft.revision === projection.revision ? draft : null;
  const staleDraft = draft && (!question || !projection || draft.questionId !== question.id || draft.revision !== projection.revision) ? draft : null;
  const displayError = error ?? projection?.error ?? null;
  const referencedItems = projection?.document && question
    ? projection.document.items.filter((item) => question.item_ids.includes(item.id))
    : [];
  if (!projection && !loading && !displayError) return null;
  if (projection?.status === 'idle' && !projection.document && !question && !loading && !displayError) return null;

  return (
    <section className="rounded-card border border-brand-primary/25 bg-surface-raised px-snug py-tight shadow-card" aria-label="需求分析">
      <header className="flex flex-wrap items-center justify-between gap-tight">
        <div className="flex min-w-0 items-center gap-tight">
          <ListChecks className="h-4 w-4 shrink-0 text-brand-primary" aria-hidden="true" />
          <span className="text-body font-semibold text-text-primary">需求分析</span>
          <span className="rounded-full bg-brand-muted/55 px-tight py-micro text-caption text-text-secondary">{statusLabel(projection?.status ?? 'idle')}</span>
          {projection && projection.pending_count > 0 && <span className="text-caption text-text-tertiary">还有 {projection.pending_count} 项待确认</span>}
        </div>
        {loading && <LoaderCircle className="h-4 w-4 animate-spin text-brand-primary motion-reduce:animate-none" aria-label="需求分析加载中" />}
        {!loading && displayError && <Button type="button" size="sm" variant="ghost" onClick={projection?.status === 'failed' ? onRestart : onRefresh}><RotateCcw className="h-3.5 w-3.5" aria-hidden="true" />{projection?.status === 'failed' ? '重新整理' : '刷新状态'}</Button>}
      </header>

      {displayError && <div className="mt-tight flex items-start gap-micro text-caption text-status-error" role="alert"><AlertCircle className="mt-px h-3.5 w-3.5 shrink-0" aria-hidden="true" /><span>{displayError}</span></div>}
      {projection?.document?.summary && <p className="mt-tight whitespace-pre-wrap text-body text-text-secondary">{projection.document.summary}</p>}

      {projection?.document && (projection.document.items.length > 0 || projection.document.sources.length > 0) && (
        <details className="mt-tight rounded-button border border-border-subtle bg-surface-base px-tight py-micro">
          <summary className="flex cursor-pointer list-none items-center gap-micro text-caption font-medium text-text-secondary"><ChevronDown className="h-3.5 w-3.5" aria-hidden="true" />查看分析事项与来源</summary>
          {projection.document.items.length > 0 && (
            <ul className="mt-tight space-y-tight" aria-label="结构化分析事项">
              {projection.document.items.map((item) => (
                <li key={item.id} className="rounded-button border border-border-subtle bg-surface-raised px-tight py-tight">
                  <div className="flex flex-wrap items-center gap-micro"><span className="text-caption font-medium text-text-primary">{item.title}</span><span className="text-caption text-text-tertiary">{itemKindLabel(item.kind)} · {item.basis === 'observed' ? '已观察' : item.basis === 'proposed' ? '建议' : '待核对'}</span></div>
                  <p className="mt-micro whitespace-pre-wrap text-caption text-text-secondary">{item.detail}</p>
                  {item.impact && <p className="mt-micro text-caption text-text-tertiary">影响：{item.impact}</p>}
                  {item.recommendation && <p className="mt-micro text-caption text-text-tertiary">建议：{item.recommendation}</p>}
                  {item.source_ids.length > 0 && <div className="mt-tight flex flex-wrap items-center gap-micro text-caption text-text-tertiary"><span>来源：</span>{item.source_ids.map((sourceId) => { const source = projection.document?.sources.find((candidate) => candidate.id === sourceId); return source ? <span key={source.id} className="rounded-full border border-border-subtle bg-surface-sunken/45 px-tight py-micro" title={`${source.ref}${source.locator ? ` · ${source.locator}` : ''}`}>{sourceDisplayLabel(source, conversationSources)}{source.locator ? ` · ${source.locator}` : ''}</span> : null; })}</div>}
                </li>
              ))}
            </ul>
          )}
          {projection.document.sources.length > 0 && (
            <ul className="mt-tight space-y-micro" aria-label="分析来源详情">
              {projection.document.sources.map((source) => (
                <li key={source.id} className="rounded-button bg-surface-sunken/45 px-tight py-micro text-caption text-text-secondary">
                  <div className="flex flex-wrap items-center gap-micro"><span className="font-medium">{sourceDisplayLabel(source, conversationSources)}</span><span className="text-text-tertiary">· {source.read_status === 'read' ? 'AI 报告已读' : source.read_status === 'partial' ? 'AI 报告部分读取' : 'AI 报告读取失败'}</span></div>
                  <p className="mt-micro break-all text-text-tertiary">来源标识：{sourceKindLabel(source.kind)} · {source.ref}</p>
                  {source.locator && <p className="mt-micro">定位：{source.locator}</p>}
                  {source.coverage && <p className="mt-micro">范围：{source.coverage}</p>}
                  {source.quote && <p className="mt-micro whitespace-pre-wrap">摘录：{source.quote}</p>}
                  {source.limitations && <p className="mt-micro text-status-warning">限制：{source.limitations}</p>}
                </li>
              ))}
            </ul>
          )}
        </details>
      )}

      {staleDraft && (
        <div className="mt-tight rounded-button border border-status-warning/30 bg-status-warning/5 px-tight py-tight text-caption text-status-warning" role="status">
          <p>已保留上一版问题的未提交回答，当前问题或来源版本已更新。旧选项不会自动套用，请核对后重新填写。</p>
          <p className="mt-micro whitespace-pre-wrap">{staleDraft.text || staleDraft.selectedOptionIds.join('、')}</p>
          <button type="button" className="mt-micro mr-tight font-medium underline" onClick={onRestoreDraftText}>保留补充文字并重新填写</button>
          <button type="button" className="mt-micro font-medium underline" onClick={onDiscardDraft}>丢弃旧草稿</button>
        </div>
      )}

      {question && (
        <fieldset className="mt-tight rounded-card border border-border-subtle bg-surface-base px-snug py-tight" disabled={submitting || Boolean(staleDraft)} aria-label="当前需求分析问题">
          <legend className="flex max-w-full items-start gap-tight px-micro text-body font-medium text-text-primary"><CircleHelp className="mt-px h-4 w-4 shrink-0 text-brand-primary" aria-hidden="true" /><span>{question.prompt}</span></legend>
          {referencedItems.length > 0 && <p className="mt-micro text-caption text-text-tertiary">关联事项：{referencedItems.map((item) => item.title).join('、')}</p>}
          {question.options.length > 0 && (
            <div className="mt-tight grid gap-tight sm:grid-cols-2">
              {question.options.map((option) => {
                const checked = activeDraft?.selectedOptionIds.includes(option.id) ?? false;
                return (
                  <label key={option.id} className={`flex cursor-pointer items-start gap-tight rounded-button border px-snug py-tight text-body transition-colors ${checked ? 'border-brand-primary/45 bg-brand-muted/45 text-text-primary' : 'border-border-subtle bg-surface-raised text-text-secondary hover:border-brand-primary/30'}`}>
                    <input
                      type={question.selection === 'multiple' ? 'checkbox' : 'radio'}
                      name={`chat-analysis-${question.id}`}
                      value={option.id}
                      checked={checked}
                      onChange={() => onSelection(question.selection === 'multiple'
                        ? checked
                          ? (activeDraft?.selectedOptionIds ?? []).filter((id) => id !== option.id)
                          : [...(activeDraft?.selectedOptionIds ?? []), option.id]
                        : [option.id])}
                      className="mt-0.5 accent-brand-primary"
                    />
                    <span className="min-w-0 break-words">{option.label}</span>
                    {checked && <Check className="ml-auto h-4 w-4 shrink-0 text-brand-primary" aria-hidden="true" />}
                  </label>
                );
              })}
            </div>
          )}
          <label className="mt-tight block"><span className="text-caption text-text-secondary">补充说明{question.selection === 'text' ? '' : '（可选）'}</span><textarea value={activeDraft?.text ?? ''} onChange={(event) => onText(event.target.value)} rows={2} className="mt-micro w-full resize-y rounded-button border border-border-strong bg-surface-raised px-snug py-tight text-body text-text-primary outline-none transition-shadow focus:border-brand-primary/40 focus:ring-2 focus:ring-brand-primary/20" placeholder={question.selection === 'text' ? '请填写你的回答' : '可以补充选项之外的情况'} aria-label={`${question.prompt}补充说明`} /></label>
          <div className="mt-tight flex flex-wrap items-center justify-end gap-tight">
            <Button type="button" variant="ghost" onClick={() => onSubmit('deferred')} disabled={submitting}>暂不确定</Button>
            <Button type="button" variant="primary" onClick={() => onSubmit('answered')} disabled={submitting || !questionHasAnswer(question, activeDraft)}>{submitting ? '保存中…' : '保存回答并继续'}</Button>
          </div>
        </fieldset>
      )}
      {projection?.status === 'ready' && !question && <p className="mt-tight text-caption text-text-secondary" role="status">待确认事项已收集，尚未自动确认或发布任务。</p>}
    </section>
  );
}
