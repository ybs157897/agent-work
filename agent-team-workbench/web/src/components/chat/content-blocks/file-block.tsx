import { ExternalLink, File, Files, FolderOpen } from 'lucide-react';
import { useState } from 'react';
import { previewPathOf, type FileBlock as FileBlockValue } from '../../../utils/content-blocks';
import { ContentBlockShell } from './content-block-shell';
import { RunFileDrawer } from './run-file-drawer';

const STATUS: Record<NonNullable<FileBlockValue['files'][number]['status']>, { label: string; tone: string }> = {
  ready: { label: '可用', tone: 'ready' },
  accepted: { label: '已接受', tone: 'accepted' },
  draft: { label: '草稿', tone: 'draft' },
  processing: { label: '处理中', tone: 'processing' },
  failed: { label: '失败', tone: 'failed' },
};

export function FileBlock({ block, runId }: { block: FileBlockValue; runId?: string }) {
  const [previewPath, setPreviewPath] = useState<string | null>(null);
  return (
    <ContentBlockShell block={block} icon={Files}>
      <ul className="chat-content-file-list" aria-label="文件列表">
        {block.files.map((file, index) => {
          const status = file.status ? STATUS[file.status] : undefined;
          // 仓库相对路径 + 已知 Run 才给站内打开；缺一则退回纯元数据。
          const openable = runId ? previewPathOf(file) : undefined;
          return (
            <li key={`${index}-${file.name}`} className="chat-content-file-row">
              <span className="chat-content-file-icon" aria-hidden><File className="h-4 w-4" /></span>
              <div className="min-w-0 flex-1">
                <div className="truncate font-medium text-text-primary" title={file.name}>{file.name}</div>
                {(file.mime || file.size) && (
                  <div className="mt-0.5 truncate text-caption text-text-tertiary">
                    {[file.mime, file.size].filter(Boolean).join(' · ')}
                  </div>
                )}
              </div>
              {status && <span className={`chat-content-file-status chat-content-file-status-${status.tone}`}>{status.label}</span>}
              {openable && (
                <button
                  type="button"
                  className="chat-content-file-action"
                  onClick={() => setPreviewPath(openable)}
                  aria-label={`打开文件：${file.name}`}
                >
                  打开<FolderOpen className="h-3.5 w-3.5" aria-hidden />
                </button>
              )}
              {file.url && (
                <a href={file.url} target="_blank" rel="noreferrer noopener" className="chat-content-file-action" aria-label={`打开文件：${file.name}`}>
                  打开<ExternalLink className="h-3.5 w-3.5" aria-hidden />
                </a>
              )}
            </li>
          );
        })}
      </ul>
      <RunFileDrawer runId={runId ?? null} path={previewPath} onClose={() => setPreviewPath(null)} />
    </ContentBlockShell>
  );
}
