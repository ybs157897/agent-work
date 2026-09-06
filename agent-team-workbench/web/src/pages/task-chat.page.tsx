import { ArrowUp, CheckCircle2, CircleAlert, LoaderCircle, RotateCcw } from 'lucide-react';
import { useEffect, useRef, useState } from 'react';
import type { KeyboardEvent } from 'react';
import { Link } from 'react-router-dom';
import { ApiError } from '../api/client';
import { listModels } from '../api/endpoints';
import type { TaskIntakeDraft } from '../api/task-intake';
import type { ModelEntry } from '../api/types';
import { AgentOutput } from '../components/chat/agent-output';
import { TaskIntakeQuestionGroup } from '../components/chat/task-intake-question-card';
import { Modal } from '../components/modal';
import { Button, Input, Select, Textarea } from '../components/ui';
import { useTasksStore } from '../stores/tasks.store';
import {
  canPublishTaskIntakeDraft,
  useTaskIntakeStore,
  type TaskIntakePublicationReceipt,
} from '../stores/task-intake.store';
import { captureScope, isCurrent } from '../stores/scope';
import { toast } from '../stores/toast.store';
import { useWorkspaceStore } from '../stores/workspace.store';

export const TASK_INTAKE_SCROLL_THRESHOLD = 96;

export interface TaskIntakeScrollMetrics {
  scrollHeight: number;
  scrollTop: number;
  clientHeight: number;
}

export function isTaskIntakeNearBottom(element: TaskIntakeScrollMetrics): boolean {
  return element.scrollHeight - element.scrollTop - element.clientHeight < TASK_INTAKE_SCROLL_THRESHOLD;
}

export function followTaskIntakeScroll(
  element: Pick<TaskIntakeScrollMetrics, 'scrollHeight' | 'scrollTop'>,
  shouldFollow: boolean,
): void {
  if (shouldFollow) element.scrollTop = element.scrollHeight;
}

export function parseAcceptanceCriteria(value: string): string[] {
  return value.split('\n').map((criterion) => criterion.trim()).filter(Boolean);
}

export function intakeCompatibleModels(models: readonly ModelEntry[]): ModelEntry[] {
  return models.filter((model) => (
    (model.api === 'openai-completions' || model.api === 'openai-responses')
    && Boolean(model.base_url?.trim())
    && Boolean(model.model.trim())
  ));
}

/** 独立任务对话页：澄清只产生分析回合，用户确认草案后才发布 Task。 */
export default function TaskChatPage() {
  const workspace = useWorkspaceStore((s) => s.workspace);
  const workspaceId = workspace?.id ?? '';
  const refresh = useTasksStore((s) => s.refresh);
  const [analysisModels, setAnalysisModels] = useState<ModelEntry[]>([]);
  const [analysisModelsLoading, setAnalysisModelsLoading] = useState(true);
  const [analysisModelsError, setAnalysisModelsError] = useState<string | null>(null);
  const messages = useTaskIntakeStore((s) => s.messages);
  const questionAnswers = useTaskIntakeStore((s) => s.questionAnswers);
  const draft = useTaskIntakeStore((s) => s.draft);
  const composerText = useTaskIntakeStore((s) => s.composerText);
  const priority = useTaskIntakeStore((s) => s.priority);
  const dueDate = useTaskIntakeStore((s) => s.dueDate);
  const sending = useTaskIntakeStore((s) => s.sending);
  const publishing = useTaskIntakeStore((s) => s.publishing);
  const error = useTaskIntakeStore((s) => s.error);
  const errorKind = useTaskIntakeStore((s) => s.errorKind);
  const canRetry = useTaskIntakeStore((s) => s.canRetry);
  const pendingPublication = useTaskIntakeStore((s) => s.pendingPublication);
  const publishedReceipt = useTaskIntakeStore((s) => s.publishedReceipt);
  const modelRef = useTaskIntakeStore((s) => s.modelRef);
  const intakeWorkspaceId = useTaskIntakeStore((s) => s.workspaceId);
  const intakeHydrated = useTaskIntakeStore((s) => s.hydrated);
  const hydrate = useTaskIntakeStore((s) => s.hydrate);
  const reset = useTaskIntakeStore((s) => s.reset);
  const setComposerText = useTaskIntakeStore((s) => s.setComposerText);
  const setModelRef = useTaskIntakeStore((s) => s.setModelRef);
  const analyze = useTaskIntakeStore((s) => s.analyze);
  const retryAnalyze = useTaskIntakeStore((s) => s.retryAnalyze);
  const submitQuestionAnswers = useTaskIntakeStore((s) => s.submitQuestionAnswers);
  const setQuestionSelection = useTaskIntakeStore((s) => s.setQuestionSelection);
  const setQuestionSupplement = useTaskIntakeStore((s) => s.setQuestionSupplement);
  const setDraftField = useTaskIntakeStore((s) => s.setDraftField);
  const setPriority = useTaskIntakeStore((s) => s.setPriority);
  const setDueDate = useTaskIntakeStore((s) => s.setDueDate);
  const publish = useTaskIntakeStore((s) => s.publish);
  const clear = useTaskIntakeStore((s) => s.clear);
  const transcriptRef = useRef<HTMLDivElement>(null);
  const followStreamRef = useRef(true);
  const [restartOpen, setRestartOpen] = useState(false);

  useEffect(() => {
    let active = true;
    setAnalysisModelsLoading(true);
    setAnalysisModelsError(null);
    void listModels()
      .then(({ items }) => {
        if (!active) return;
        setAnalysisModels(intakeCompatibleModels(items));
      })
      .catch((loadError: unknown) => {
        if (!active) return;
        setAnalysisModels([]);
        setAnalysisModelsError(loadError instanceof ApiError ? loadError.message : '分析模型列表加载失败');
      })
      .finally(() => {
        if (active) setAnalysisModelsLoading(false);
      });
    return () => { active = false; };
  }, []);

  useEffect(() => {
    if (workspaceId) hydrate(workspaceId);
    else if (intakeWorkspaceId !== null || intakeHydrated) reset();
  }, [hydrate, intakeHydrated, intakeWorkspaceId, reset, workspaceId]);

  useEffect(() => {
    const node = transcriptRef.current;
    if (!node) return;
    followStreamRef.current = true;
    const syncFollow = () => {
      followStreamRef.current = isTaskIntakeNearBottom(node);
    };
    syncFollow();
    node.addEventListener('scroll', syncFollow, { passive: true });
    return () => node.removeEventListener('scroll', syncFollow);
  }, [workspaceId]);

  useEffect(() => {
    const node = transcriptRef.current;
    if (!node) return;
    followTaskIntakeScroll(node, followStreamRef.current);
  }, [messages.length, sending]);

  const send = async () => {
    if (!workspaceId || sending || publishing || pendingPublication || publishedReceipt || !composerText.trim()) return;
    await analyze(workspaceId, composerText);
  };

  const submit = async () => {
    if (!workspaceId || publishing || sending || publishedReceipt) return;
    const scope = captureScope();
    try {
      const item = await publish(workspaceId);
      if (!item || !isCurrent(scope)) return;
      try {
        await refresh();
      } catch (refreshError) {
        toast.error(refreshError instanceof Error ? `任务已发布，但列表刷新失败：${refreshError.message}` : '任务已发布，但列表刷新失败');
      }
      if (!isCurrent(scope)) return;
      toast.success(`已发布任务「${item.title}」，本轮对话已保留`);
    } catch {
      // Store keeps visible error and the exact request for recovery.
    }
  };

  const handleComposerKeyDown = (event: KeyboardEvent<HTMLTextAreaElement>) => {
    if (event.key !== 'Enter' || event.shiftKey || event.nativeEvent.isComposing) return;
    event.preventDefault();
    void send();
  };

  const draftReady = canPublishTaskIntakeDraft(draft);
  const fieldsDisabled = Boolean(pendingPublication) || publishing || Boolean(publishedReceipt);
  const hasUnpublishedConversation = messages.length > 0 || Boolean(draft) || Boolean(composerText) || Boolean(error);
  const canStartNew = Boolean(publishedReceipt) || hasUnpublishedConversation;
  const restartLabel = publishedReceipt ? '开始新的任务对话' : '重新开始';
  const confirmRestart = () => {
    if (pendingPublication || sending || publishing) return;
    clear();
    setRestartOpen(false);
  };

  return (
    <div className="tx-scope chat-languagegui-skin flex h-full min-h-0 w-full flex-col overflow-hidden" data-task-chat>
      <header className="chat-chrome flex h-12 shrink-0 items-center justify-between border-b border-border-subtle bg-surface-raised px-comfortable">
        <div className="flex min-w-0 items-center gap-snug">
          <h1 className="truncate text-body-lg font-semibold text-text-primary">任务对话</h1>
          <span className="hidden truncate text-caption text-text-tertiary sm:inline">先对话澄清，再发布任务</span>
        </div>
        {workspace && <span className="max-w-48 truncate text-caption text-text-tertiary" title={workspace.name}>{workspace.name}</span>}
      </header>

      <div className="min-h-0 flex-1 flex flex-col overflow-hidden">
        <div ref={transcriptRef} data-task-chat-scroll="transcript" className="relative min-h-0 flex-1 overflow-y-auto overscroll-contain px-comfortable py-comfortable">
          <div className="chat-thread space-y-3">
            {messages.length === 0 && (
              <section className="rounded-card border border-border-subtle bg-surface-raised px-comfortable py-snug shadow-card" aria-label="任务澄清说明">
                <h2 className="text-body-lg font-semibold text-text-primary">先聊清楚，再发布任务</h2>
                <p className="mt-tight text-body text-text-secondary">
                  先说说你想达成的结果、背景和限制。Agent 会根据你的补充整理任务草案，确认后才会进入执行队列。
                </p>
              </section>
            )}

            {messages.map((message) => (
              message.role === 'user' ? (
                <article key={message.id} className="chat-user-turn" aria-label="你的消息">
                  <div className="chat-user-card whitespace-pre-wrap break-words">{message.content}</div>
                </article>
              ) : (
                <article key={message.id} className="chat-assistant-turn" aria-label="任务分析回复">
                  <AgentOutput text={message.content} showCaret={false} />
                  {message.questions && message.questions.length > 0 && (
                    <TaskIntakeQuestionGroup
                      messageId={message.id}
                      questions={message.questions}
                      answers={questionAnswers}
                      disabled={sending || publishing || Boolean(pendingPublication) || Boolean(publishedReceipt)}
                      onSelection={setQuestionSelection}
                      onSupplement={setQuestionSupplement}
                      onSubmit={(questionMessageId) => void submitQuestionAnswers(workspaceId, questionMessageId)}
                    />
                  )}
                </article>
              )
            ))}

            {sending && (
              <div className="flex items-center gap-tight text-caption text-text-secondary" role="status" aria-live="polite">
                <LoaderCircle className="h-4 w-4 animate-spin text-brand-primary motion-reduce:animate-none" aria-hidden />
                正在分析你的描述…
              </div>
            )}

            {error && (
              <div className="flex items-start gap-tight rounded-card border border-status-error/30 bg-status-error/5 px-snug py-tight text-caption text-status-error" role="alert" aria-live="assertive">
                <CircleAlert className="mt-px h-4 w-4 shrink-0" aria-hidden />
                <p className="min-w-0 flex-1">{error}</p>
                {errorKind === 'analyze' && canRetry && (
                  <button
                    type="button"
                    onClick={() => void retryAnalyze()}
                    className="inline-flex shrink-0 items-center gap-micro rounded-button border border-status-error/35 px-tight py-micro font-medium text-status-error transition-colors hover:bg-status-error/5 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-status-error/30"
                  >
                    <RotateCcw className="h-3.5 w-3.5" aria-hidden />
                    重试分析
                  </button>
                )}
              </div>
            )}

            {draft && (
              <TaskDraftCard
                draft={draft}
                priority={priority}
                dueDate={dueDate}
                ready={draftReady}
                disabled={fieldsDisabled}
                pending={Boolean(pendingPublication)}
                publishing={publishing}
                onFieldChange={setDraftField}
                onPriorityChange={setPriority}
                onDueDateChange={setDueDate}
                onSubmit={() => void submit()}
              />
            )}

            {publishedReceipt && <TaskIntakePublicationReceiptCard receipt={publishedReceipt} onStartNew={() => setRestartOpen(true)} />}
          </div>
        </div>

        <div className="chat-bottom-region shrink-0 px-comfortable pb-comfortable pt-tight">
          <div className="chat-composer-stack">
            <div className="mb-tight flex flex-wrap items-center justify-between gap-tight">
              <TaskIntakeModelSelector
                models={analysisModels}
                loading={analysisModelsLoading}
                error={analysisModelsError}
                value={modelRef}
                disabled={sending || publishing || Boolean(pendingPublication) || Boolean(publishedReceipt)}
                onChange={setModelRef}
                compact
              />
              <Button
                type="button"
                size="sm"
                onClick={() => setRestartOpen(true)}
                disabled={!canStartNew || sending || publishing || Boolean(pendingPublication)}
                title={pendingPublication ? '发布结果尚未确认，请先重试确认' : undefined}
              >
                {restartLabel}
              </Button>
            </div>
            <div className="chat-composer">
              <textarea
                className="chat-composer-input"
                value={composerText}
                onChange={(event) => setComposerText(event.target.value)}
                onKeyDown={handleComposerKeyDown}
                placeholder={publishedReceipt
                  ? '本轮任务已发布；点击“开始新的任务对话”继续创建任务'
                  : pendingPublication
                    ? '发布结果尚未确认，请先重试确认'
                    : messages.length > 0
                      ? '继续补充你的目标、范围或验收标准…'
                      : '先说说你想做什么，不必一次描述完整…'}
                aria-label={publishedReceipt ? '已发布任务对话' : messages.length > 0 ? '继续描述任务' : '描述任务想法'}
                disabled={sending || publishing || Boolean(pendingPublication) || Boolean(publishedReceipt)}
                rows={2}
              />
              <div className="chat-prompt-footer">
                <span className="text-caption text-text-tertiary">Enter 发送 · Shift+Enter 换行</span>
                <span className="flex-1" />
                <Button
                  type="button"
                  size="sm"
                  variant="primary"
                  onClick={() => void send()}
                  disabled={!workspaceId || !composerText.trim() || sending || publishing || Boolean(pendingPublication) || Boolean(publishedReceipt)}
                  aria-label="发送澄清消息"
                >
                  <ArrowUp className="h-4 w-4" aria-hidden />
                  发送
                </Button>
              </div>
            </div>
          </div>
        </div>
      </div>

      {restartOpen && (
        <Modal
          open
          onClose={() => setRestartOpen(false)}
          title={publishedReceipt ? '开始新的任务对话' : '重新开始任务创建'}
          width={480}
          skin="task"
          footer={(
            <div className="flex justify-end gap-snug">
              <Button type="button" onClick={() => setRestartOpen(false)}>取消</Button>
              <Button type="button" variant="danger-outline" onClick={confirmRestart}>清除并重新开始</Button>
            </div>
          )}
        >
          <p className="text-body text-text-secondary">
            {publishedReceipt
              ? '当前任务已发布。确认后将清除本轮对话记录，开始新的任务对话；已发布任务仍可从任务详情查看。'
              : '将清除当前 Workspace 的未发布对话、草案和输入内容，已发布任务不受影响。'}
          </p>
        </Modal>
      )}
    </div>
  );
}

export function TaskIntakePublicationReceiptCard({
  receipt,
  onStartNew,
}: {
  receipt: TaskIntakePublicationReceipt;
  onStartNew: () => void;
}) {
  return (
    <section className="rounded-card border border-status-success/30 bg-status-success/5 p-comfortable shadow-card" aria-label="任务发布结果">
      <div className="flex items-start gap-snug">
        <CheckCircle2 className="mt-px h-5 w-5 shrink-0 text-status-success" aria-hidden />
        <div className="min-w-0 flex-1">
          <h2 className="text-body-lg font-semibold text-text-primary">任务已发布</h2>
          <p className="mt-micro break-words text-body text-text-secondary">「{receipt.title}」已进入执行队列，本轮对话已保留。</p>
          <div className="mt-snug flex flex-wrap items-center gap-snug">
            <Link
              to={`/tasks/${encodeURIComponent(receipt.workItemId)}`}
              className="text-body font-medium text-brand-primary underline decoration-brand-primary/35 underline-offset-2 hover:text-brand-accent"
            >
              查看任务
            </Link>
            <button
              type="button"
              onClick={onStartNew}
              className="rounded-button border border-border-strong bg-surface-raised px-snug py-micro text-caption font-medium text-text-secondary transition-colors hover:border-brand-primary/35 hover:text-brand-primary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-brand-primary/40"
            >
              开始新的任务对话
            </button>
          </div>
        </div>
      </div>
    </section>
  );
}

export function TaskIntakeModelSelector({
  models,
  loading,
  error,
  value,
  disabled,
  onChange,
  compact = false,
}: {
  models: readonly ModelEntry[];
  loading: boolean;
  error: string | null;
  value: string;
  disabled: boolean;
  onChange: (modelRef: string) => void;
  compact?: boolean;
}) {
  const selectedModel = models.find((model) => model.id === value);
  const keepSavedValue = Boolean(value && !selectedModel && (loading || error));
  return (
    <section className={compact ? 'min-w-0 flex-1' : 'rounded-card border border-border-subtle bg-surface-raised px-snug py-tight shadow-card'} aria-label="任务分析模型">
      <div className="flex flex-wrap items-center gap-snug">
        <label className="flex min-w-0 flex-1 items-center gap-tight">
          <span className="shrink-0 text-caption font-medium text-text-secondary">分析模型</span>
          <Select
            value={keepSavedValue ? value : selectedModel ? value : ''}
            onChange={(event) => onChange(event.target.value)}
            disabled={disabled}
            wrapperClassName="min-w-0 flex-1"
            aria-label="分析模型"
          >
            <option value="">使用任务默认模型</option>
            {keepSavedValue && <option value={value}>已保存选择：{value}</option>}
            {models.map((model) => (
              <option key={model.id} value={model.id}>
                {model.display_name} · {model.provider}
              </option>
            ))}
          </Select>
        </label>
        {loading && <span className="text-caption text-text-tertiary" role="status">正在读取可用模型…</span>}
      </div>
      {error && (
        <p className="mt-tight text-caption text-status-warning" role="status">
          无法加载分析模型列表，将保留默认选项。{error}
        </p>
      )}
      {!loading && !error && models.length === 0 && (
        <p className="mt-tight text-caption text-text-tertiary">暂无可直接调用的分析模型，可使用任务默认模型。</p>
      )}
    </section>
  );
}

export function TaskDraftCard({
  draft,
  priority,
  dueDate,
  ready,
  disabled,
  pending,
  publishing,
  onFieldChange,
  onPriorityChange,
  onDueDateChange,
  onSubmit,
}: {
  draft: TaskIntakeDraft;
  priority: 'low' | 'medium' | 'high' | 'urgent';
  dueDate: string;
  ready: boolean;
  disabled: boolean;
  pending: boolean;
  publishing: boolean;
  onFieldChange: (field: keyof TaskIntakeDraft, value: string | string[]) => void;
  onPriorityChange: (priority: 'low' | 'medium' | 'high' | 'urgent') => void;
  onDueDateChange: (dueDate: string) => void;
  onSubmit: () => void;
}) {
  const acceptanceText = draft.acceptance_criteria.join('\n');
  return (
    <section className="rounded-card border border-brand-primary/25 bg-surface-raised p-comfortable shadow-card" aria-labelledby="task-intake-draft-title">
      <div className="flex flex-wrap items-start justify-between gap-snug border-b border-border-subtle pb-snug">
        <div>
          <h2 id="task-intake-draft-title" className="text-h3 text-text-primary">任务草案</h2>
          <p className="mt-micro text-caption text-text-secondary">
            {pending ? '发布结果尚未确认，已冻结本次请求。请重试确认以避免重复创建。' : '请检查并编辑内容，确认后才会发布。'}
          </p>
        </div>
        <span className="rounded-full border border-border-subtle bg-surface-sunken px-snug py-micro text-caption text-text-secondary">
          {pending ? '待确认结果' : '待你确认'}
        </span>
      </div>

      <fieldset disabled={disabled} className="mt-comfortable space-y-base">
        <label className="block">
          <span className="text-body text-text-secondary">标题</span>
          <Input
            value={draft.title}
            onChange={(event) => onFieldChange('title', event.target.value)}
            placeholder="任务标题"
            aria-required="true"
          />
        </label>
        <label className="block">
          <span className="text-body text-text-secondary">描述</span>
          <Textarea
            value={draft.description}
            onChange={(event) => onFieldChange('description', event.target.value)}
            rows={4}
            className="resize-y"
            placeholder="背景、范围和限制"
          />
        </label>
        <label className="block">
          <span className="text-body text-text-secondary">验收标准</span>
          <Textarea
            value={acceptanceText}
            onChange={(event) => onFieldChange('acceptance_criteria', parseAcceptanceCriteria(event.target.value))}
            rows={4}
            className="resize-y"
            placeholder={'每行一条，例如：失败时能看到恢复原因'}
            aria-required="true"
          />
          {!draft.acceptance_criteria.some((item) => item.trim()) && (
            <span className="mt-micro block text-caption text-status-error" role="alert">至少填写一条验收标准。</span>
          )}
        </label>
        <div className="grid grid-cols-1 gap-snug sm:grid-cols-2">
          <label className="block">
            <span className="text-body text-text-secondary">优先级</span>
            <Select value={priority} onChange={(event) => onPriorityChange(event.target.value as typeof priority)}>
              <option value="low">低优</option>
              <option value="medium">中优</option>
              <option value="high">高优</option>
              <option value="urgent">紧急</option>
            </Select>
          </label>
          <label className="block">
            <span className="text-body text-text-secondary">截止日</span>
            <Input type="date" value={dueDate} onChange={(event) => onDueDateChange(event.target.value)} />
          </label>
        </div>
      </fieldset>

      <div className="mt-comfortable flex flex-wrap items-center justify-between gap-snug border-t border-border-subtle pt-snug">
        <p className="text-caption text-text-tertiary">
          {pending ? '请确认同一个发布请求的结果，成功后草案会清空。' : ready ? '确认发布后任务才会进入执行队列。' : '补充标题和至少一条验收标准后才能发布。'}
        </p>
        <Button type="button" variant="primary" onClick={onSubmit} disabled={!ready || publishing}>
          {publishing ? '确认中…' : pending ? '重试确认' : '确认发布'}
        </Button>
      </div>
    </section>
  );
}
