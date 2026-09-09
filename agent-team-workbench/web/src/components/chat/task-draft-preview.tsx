import { AlertCircle, Check, CircleCheck, LoaderCircle, LockKeyhole, RotateCcw } from 'lucide-react';
import type { ChatAnalysisDecision, ChatAnalysisItem } from '../../api/chat-analysis';
import type { PersistedPublicationDraft } from '../../stores/chat-workspace-state';
import { Button, Input } from '../ui';

function statusLabel(status: PersistedPublicationDraft['status']): string {
  switch (status) {
    case 'saving': return '保存中';
    case 'ready': return '待显式发布';
    case 'stale': return '需要重新核对';
    case 'publishing': return '发布中';
    case 'published': return '已发布';
    case 'failed': return '上次操作失败';
    default: return '编辑中';
  }
}

function receiptStatusClass(status: PersistedPublicationDraft['status']): string {
  if (status === 'published') return 'border-status-success/30 bg-status-success/5 text-status-success';
  if (status === 'stale' || status === 'failed') return 'border-status-warning/30 bg-status-warning/5 text-status-warning';
  return 'border-border-subtle bg-surface-sunken/45 text-text-secondary';
}

export function TaskDraftPreview({
  items,
  decisions,
  draft,
  analysisBlocked,
  loading,
  saving,
  publishing,
  error,
  onOpen,
  onToggleItem,
  onTitle,
  onNewDraft,
  onSave,
  onPublish,
  onRetry,
}: {
  items: readonly ChatAnalysisItem[];
  decisions: readonly ChatAnalysisDecision[];
  draft: PersistedPublicationDraft | null;
  analysisBlocked: boolean;
  loading: boolean;
  saving: boolean;
  publishing: boolean;
  error: string | null;
  onOpen: () => void;
  onToggleItem: (itemId: string) => void;
  onTitle: (title: string) => void;
  onNewDraft: () => void;
  onSave: () => void;
  onPublish: () => void;
  onRetry: () => void;
}) {
  if (!draft && items.length === 0 && !loading && !error && !analysisBlocked) return null;
  const decisionByItem = new Map(decisions.map((decision) => [decision.item_id, decision]));
  const selectedCount = draft?.itemIds.length ?? 0;
  const canSave = Boolean(draft && draft.status !== 'published' && (draft.status !== 'failed' || draft.lastOperation === 'save') && selectedCount > 0 && draft.title.trim() && !analysisBlocked && !saving && !publishing);
  const canPublish = Boolean(draft?.draftId && (draft.status === 'ready' || (draft.status === 'failed' && draft.lastOperation === 'publish' && draft.publishClientKey)) && draft.frozen && !analysisBlocked && !saving && !publishing);

  return (
    <section className="mt-tight rounded-card border border-brand-primary/25 bg-surface-raised px-snug py-tight shadow-card" aria-label="任务草案预览">
      <header className="flex flex-wrap items-start justify-between gap-tight">
        <div className="flex min-w-0 items-start gap-tight">
          <CircleCheck className="mt-px h-4 w-4 shrink-0 text-brand-primary" aria-hidden="true" />
          <div className="min-w-0">
            <h2 className="text-body font-semibold text-text-primary">任务草案预览</h2>
            <p className="mt-micro text-caption text-text-secondary">只使用当前有效的明确产品确认；保存草案不会创建任务，发布需要单独确认。</p>
          </div>
        </div>
        {draft && <span className={`rounded-full border px-tight py-micro text-caption ${receiptStatusClass(draft.status)}`}>{statusLabel(draft.status)}</span>}
      </header>

      {loading && <p className="mt-tight flex items-center gap-micro text-caption text-text-tertiary" role="status"><LoaderCircle className="h-3.5 w-3.5 animate-spin motion-reduce:animate-none" aria-hidden="true" />正在读取草案状态…</p>}
      {error && <div className="mt-tight flex items-start gap-micro text-caption text-status-error" role="alert"><AlertCircle className="mt-px h-3.5 w-3.5 shrink-0" aria-hidden="true" /><span>{error}</span><Button type="button" variant="ghost" size="sm" className="ml-auto shrink-0" onClick={onRetry}><RotateCcw className="h-3.5 w-3.5" aria-hidden="true" />重试</Button></div>}

      {!draft && items.length > 0 && <div className="mt-tight rounded-button border border-border-subtle bg-surface-base px-snug py-tight"><p className="text-caption text-text-secondary">发现 {items.length} 个当前有效的产品确认事项。</p><Button type="button" variant="primary" size="sm" className="mt-tight" onClick={onOpen}>形成任务草案</Button></div>}
      {!draft && items.length === 0 && !loading && <p className="mt-tight rounded-button border border-border-subtle bg-surface-base px-snug py-tight text-caption text-text-tertiary">当前没有可用于形成任务的有效产品确认。</p>}

      {draft && <div className="mt-tight space-y-tight">
        <fieldset disabled={saving || publishing || draft.status === 'published' || draft.status === 'failed'} aria-label="选择任务事项" className="space-y-tight">
          <legend className="text-caption font-medium text-text-secondary">选择要进入任务的已确认事项（{selectedCount}）</legend>
          {items.map((item) => {
            const decision = decisionByItem.get(item.id);
            const checked = draft.itemIds.includes(item.id);
            return <label key={item.id} className={`flex cursor-pointer items-start gap-tight rounded-button border px-tight py-tight ${checked ? 'border-brand-primary/45 bg-brand-muted/45' : 'border-border-subtle bg-surface-base'}`}>
              <input type="checkbox" checked={checked} onChange={() => onToggleItem(item.id)} className="mt-0.5 accent-brand-primary" />
              <span className="min-w-0 flex-1"><span className="flex items-center gap-micro text-caption font-medium text-text-primary">{checked && <Check className="h-3.5 w-3.5 text-brand-primary" aria-hidden="true" />}{item.title}</span><span className="mt-micro block whitespace-pre-wrap text-caption text-text-secondary">{decision?.conclusion ?? item.detail}</span>{decision?.product_version && <span className="mt-micro block text-caption text-text-tertiary">产品版本：{decision.product_version}</span>}</span>
            </label>;
          })}
        </fieldset>
        <label className="block"><span className="text-caption text-text-secondary">任务标题</span><Input value={draft.title} onChange={(event) => onTitle(event.target.value)} placeholder="例如：web-idea Java 能力" disabled={saving || publishing || draft.status === 'published' || draft.status === 'failed'} /></label>

        {draft.frozen && <div className="rounded-button border border-border-subtle bg-surface-base px-tight py-tight text-caption text-text-secondary"><div className="flex items-center gap-micro font-medium text-text-primary"><LockKeyhole className="h-3.5 w-3.5" aria-hidden="true" />服务端草案内容与基线</div><p className="mt-micro whitespace-pre-wrap">{draft.description}</p>{draft.acceptanceCriteria.length > 0 && <ul className="mt-micro list-disc space-y-micro pl-4">{draft.acceptanceCriteria.map((criterion) => <li key={criterion}>{criterion}</li>)}</ul>}<p className="mt-tight text-text-tertiary">代码基线：{draft.frozen.project_baseline.repository_identity} · HEAD {draft.frozen.project_baseline.head}</p><p className="mt-micro break-all text-text-tertiary">内容摘要：{draft.frozen.project_baseline.content_digest}</p><p className="mt-micro text-text-tertiary">来源依赖：{draft.frozen.source_dependencies.length} 项 · 确认记录：{draft.frozen.confirmation_ids.length} 条</p></div>}

        <div className="flex flex-wrap items-center justify-between gap-tight"><p className="text-caption text-text-tertiary">{draft.status === 'published' && draft.taskId ? '任务已创建，可从任务详情继续。' : draft.status === 'failed' && draft.lastOperation === 'publish' ? '发布结果尚未确认，重试会复用同一请求。' : analysisBlocked ? '当前分析材料正在复核，暂不能保存或发布。' : '保存草案后仍需点击“发布任务”。'}</p><div className="flex flex-wrap gap-tight"><Button type="button" variant="secondary" onClick={onSave} disabled={!canSave}>{saving ? '保存中…' : '保存任务草案'}</Button><Button type="button" variant="primary" onClick={onPublish} disabled={!canPublish}>{publishing ? '发布中…' : draft.status === 'failed' && draft.lastOperation === 'publish' ? '重试发布' : '发布任务'}</Button>{draft.taskId && <a href={`/tasks/${encodeURIComponent(draft.taskId)}`} className="inline-flex min-h-8 items-center rounded-button border border-border-subtle px-tight text-caption text-brand-primary hover:bg-brand-muted/35">查看任务</a>}{draft.status === 'published' && draft.taskId && <Button type="button" variant="secondary" onClick={onNewDraft}>新建任务草案</Button>}</div></div>
      </div>}
    </section>
  );
}
