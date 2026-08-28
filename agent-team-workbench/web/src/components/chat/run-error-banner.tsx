import { AlertCircle, Check, ChevronDown, Clipboard, LoaderCircle, RotateCcw, X } from 'lucide-react';
import { useEffect, useId, useState } from 'react';
import type { ExecutionRun } from '../../api/types';
import { formatRunFailureMessage } from '../../utils/format-run-failure';

interface RunErrorBannerProps {
  run: ExecutionRun;
  onRetry: (runId: string) => Promise<void>;
}

const MAIN_MESSAGE_LIMIT = 500;

/** 运行级错误只放在 composer 上方；工具错误仍由工具卡自己呈现。 */
export function RunErrorBanner({ run, onRetry }: RunErrorBannerProps) {
  const [expanded, setExpanded] = useState(false);
  const [dismissed, setDismissed] = useState(false);
  const [retrying, setRetrying] = useState(false);
  const [copied, setCopied] = useState(false);
  const detailId = useId();

  useEffect(() => {
    setExpanded(false);
    setDismissed(false);
    setRetrying(false);
    setCopied(false);
  }, [run.id]);

  if (run.status !== 'failed' || dismissed) return null;

  const failure = run.failure;
  const formattedMessage = formatRunFailureMessage(failure?.code, failure?.message);
  const message = formattedMessage.length > MAIN_MESSAGE_LIMIT
    ? `${formattedMessage.slice(0, MAIN_MESSAGE_LIMIT - 1)}…`
    : formattedMessage;
  const rawDetail = failure?.message?.trim() ?? '';
  const detail = [failure?.code, rawDetail].filter(Boolean).join('\n');

  const copy = async () => {
    if (!navigator.clipboard) return;
    await navigator.clipboard.writeText(detail || message);
    setCopied(true);
    window.setTimeout(() => setCopied(false), 1600);
  };

  const retry = async () => {
    if (!failure?.retryable || retrying) return;
    setRetrying(true);
    try {
      await onRetry(run.id);
    } catch {
      setRetrying(false);
    }
  };

  return (
    <section className="rounded-card border border-status-error/30 bg-status-error/5 px-snug py-tight shadow-card" role="alert" aria-live="assertive">
      <div className="flex items-start gap-2">
        <AlertCircle className="h-4 w-4 shrink-0 text-status-error" aria-hidden />
        <div className="min-w-0 flex-1">
          <p className="text-caption font-medium text-status-error">{message}</p>
          {detail && (
            <button
              type="button"
              className="mt-1 inline-flex items-center gap-1 text-caption text-text-secondary underline-offset-2 hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-brand-primary/40"
              onClick={() => setExpanded((value) => !value)}
              aria-expanded={expanded}
              aria-controls={expanded ? detailId : undefined}
            >
              <ChevronDown className={`h-3.5 w-3.5 transition-transform ${expanded ? 'rotate-180' : ''}`} aria-hidden />
              {expanded ? '收起详情' : '查看详情'}
            </button>
          )}
        </div>
        <div className="flex shrink-0 items-center gap-1">
          {failure?.retryable && (
            <button
              type="button"
              className="inline-flex items-center gap-1 rounded-button border border-status-error/30 px-2 py-1 text-caption font-medium text-status-error transition-colors hover:bg-status-error/10 disabled:cursor-not-allowed disabled:opacity-50"
              onClick={() => void retry()}
              disabled={retrying}
              aria-busy={retrying}
            >
              {retrying ? <LoaderCircle className="h-3.5 w-3.5 animate-spin" aria-hidden /> : <RotateCcw className="h-3.5 w-3.5" aria-hidden />}
              {retrying ? '重试中…' : '重试'}
            </button>
          )}
          <button type="button" className="inline-flex h-7 w-7 items-center justify-center rounded-button text-text-tertiary transition-colors hover:bg-status-error/10 hover:text-status-error focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-brand-primary/40" onClick={() => void copy()} aria-label="复制错误信息" title="复制错误信息">
            {copied ? <Check className="h-3.5 w-3.5 text-status-success" aria-hidden /> : <Clipboard className="h-3.5 w-3.5" aria-hidden />}
          </button>
          <button type="button" className="inline-flex h-7 w-7 items-center justify-center rounded-button text-text-tertiary transition-colors hover:bg-status-error/10 hover:text-status-error focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-brand-primary/40" onClick={() => setDismissed(true)} aria-label="关闭错误提示" title="关闭">
            <X className="h-3.5 w-3.5" aria-hidden />
          </button>
        </div>
      </div>
      {expanded && detail && (
        <pre id={detailId} className="mt-2 max-h-32 overflow-auto whitespace-pre-wrap rounded-button border border-border-subtle bg-surface-sunken px-2 py-1.5 font-mono text-caption text-text-secondary">{detail}</pre>
      )}
    </section>
  );
}
