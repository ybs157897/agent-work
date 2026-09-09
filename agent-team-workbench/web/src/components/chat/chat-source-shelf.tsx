import { Check, Download, FileText, Loader2, RefreshCw, ShieldCheck } from 'lucide-react';
import { useState } from 'react';
import type { ChatSource, ChatSourceStatus } from '../../api/chat-sources';
import { downloadChatSource } from '../../api/chat-sources';
import { useWorkspaceStore } from '../../stores/workspace.store';
import { formatBytes } from '../../utils/artifact-visuals';
import { Button } from '../ui';

export function chatSourceStatusLabel(status: ChatSourceStatus): string {
  switch (status) {
    case 'saved':
      return '原件已保存';
    case 'handed_to_agent':
      return '已交给 Agent';
    case 'read':
      return '读取状态待运行证据';
    case 'failed':
      return '交付失败';
    case 'unready':
      return '等待交付';
    default:
      return '状态待确认';
  }
}

function sourceAvailabilityLabel(source: ChatSource): string {
  if (source.available === false) return source.availability_error ? `原件不可用：${source.availability_error}` : '原件不可用';
  return chatSourceStatusLabel(source.status);
}

function downloadBlob(filename: string, blob: Blob): void {
  if (typeof URL === 'undefined' || typeof URL.createObjectURL !== 'function' || typeof document === 'undefined') {
    throw new Error('当前浏览器不支持附件下载');
  }
  const url = URL.createObjectURL(blob);
  const anchor = document.createElement('a');
  anchor.href = url;
  anchor.download = filename;
  anchor.rel = 'noreferrer';
  anchor.click();
  window.setTimeout(() => URL.revokeObjectURL(url), 0);
}

export function ChatSourceShelf({
  chatId,
  sources,
  loading,
  error,
  onRetry,
}: {
  chatId: string | null;
  sources: readonly ChatSource[];
  loading: boolean;
  error?: string;
  onRetry: () => void;
}) {
  const [downloading, setDownloading] = useState<string | null>(null);
  const [downloadError, setDownloadError] = useState<string | null>(null);

  if (!chatId || (sources.length === 0 && !loading && !error)) return null;

  const download = async (source: ChatSource) => {
    const startedWorkspaceId = useWorkspaceStore.getState().selectedWorkspaceId ?? useWorkspaceStore.getState().workspace?.id ?? '';
    setDownloading(source.id);
    setDownloadError(null);
    try {
      const blob = await downloadChatSource(chatId, source.id);
      const currentWorkspaceId = useWorkspaceStore.getState().selectedWorkspaceId ?? useWorkspaceStore.getState().workspace?.id ?? '';
      if (currentWorkspaceId !== startedWorkspaceId) return;
      downloadBlob(source.filename, blob);
    } catch (cause) {
      const currentWorkspaceId = useWorkspaceStore.getState().selectedWorkspaceId ?? useWorkspaceStore.getState().workspace?.id ?? '';
      if (currentWorkspaceId !== startedWorkspaceId) return;
      setDownloadError(cause instanceof Error ? cause.message : '附件下载失败');
    } finally {
      setDownloading(null);
    }
  };

  return (
    <section className="rounded-card border border-border-subtle bg-surface-raised/70 px-snug py-tight" aria-label="本对话资料">
      <div className="flex flex-wrap items-center justify-between gap-tight">
        <div className="flex items-center gap-micro text-caption font-medium text-text-secondary">
          <ShieldCheck className="h-3.5 w-3.5 text-brand-primary" aria-hidden="true" />
          <span>本对话资料</span>
          <span className="text-text-tertiary">本对话原件</span>
        </div>
        {loading && <Loader2 className="h-3.5 w-3.5 animate-spin text-text-tertiary" aria-label="资料状态加载中" />}
        {!loading && error && (
          <Button type="button" variant="ghost" size="sm" onClick={onRetry}>
            <RefreshCw className="h-3.5 w-3.5" aria-hidden="true" />重试
          </Button>
        )}
      </div>
      {error && <p className="mt-micro text-caption text-status-danger" role="alert">{error}</p>}
      {downloadError && <p className="mt-micro text-caption text-status-danger" role="alert">{downloadError}</p>}
      {sources.length > 0 && (
        <ul className="mt-tight flex flex-wrap gap-tight" aria-label="已关联资料">
          {sources.map((source) => (
            <li key={source.id} className="flex min-w-0 max-w-full items-center gap-tight rounded-button border border-border-subtle bg-surface-base px-tight py-micro">
              <FileText className="h-3.5 w-3.5 shrink-0 text-text-tertiary" aria-hidden="true" />
              <span className="min-w-0">
                <span className="block max-w-[15rem] truncate text-caption font-medium text-text-primary" title={source.filename}>{source.filename}</span>
                <span className="flex flex-wrap items-center gap-micro text-caption text-text-tertiary">
                  <span>{formatBytes(source.size)}</span>
                  <span aria-hidden="true">·</span>
                  <span className={source.available === false || source.status === 'failed' ? 'text-status-danger' : 'text-text-tertiary'}>{sourceAvailabilityLabel(source)}</span>
                </span>
              </span>
              {source.status === 'handed_to_agent' && source.available !== false ? <Check className="h-3.5 w-3.5 shrink-0 text-status-success" aria-label="已交给 Agent" /> : null}
              <button
                type="button"
                className="inline-flex h-6 w-6 shrink-0 items-center justify-center rounded-button text-text-tertiary transition-colors hover:bg-surface-sunken hover:text-text-primary disabled:cursor-wait disabled:opacity-60"
                onClick={() => void download(source)}
                disabled={downloading === source.id || source.available === false}
                aria-label={`下载附件：${source.filename}`}
                title={source.available === false ? '原件不可用' : '下载原件'}
              >
                {downloading === source.id ? <Loader2 className="h-3.5 w-3.5 animate-spin" aria-hidden="true" /> : <Download className="h-3.5 w-3.5" aria-hidden="true" />}
              </button>
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}
