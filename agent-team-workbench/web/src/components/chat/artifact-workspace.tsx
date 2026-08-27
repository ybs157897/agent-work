import { File, FileText, Image, Package, Table, X } from 'lucide-react';
import type { Artifact } from '../../api/types';
import { EmptyState } from '../ui';
import { formatTime } from '../../utils/format';
import { artifactBasename, classifyMime, formatBytes } from '../../utils/artifact-visuals';

/** mime 类别图标：工作区与聊天区摘要卡共用。 */
export function ArtifactMimeIcon({ mime, className }: { mime: string; className?: string }) {
  const cls = className ?? 'h-4 w-4';
  switch (classifyMime(mime)) {
    case 'text':
      return <FileText className={`${cls} shrink-0 text-text-tertiary`} />;
    case 'data':
      return <Table className={`${cls} shrink-0 text-text-tertiary`} />;
    case 'image':
      return <Image className={`${cls} shrink-0 text-text-tertiary`} />;
    default:
      return <File className={`${cls} shrink-0 text-text-tertiary`} />;
  }
}

/**
 * 右侧工作区面板：承载本会话全部成果清单（聊天区只放摘要，成果在这里展开）。
 * 后端当前只暴露元数据（无内容端点），面板是清单不是预览器。
 */
export function ArtifactWorkspace({ artifacts, onClose }: { artifacts: Artifact[]; onClose: () => void }) {
  return (
    <div className="flex min-h-0 w-80 shrink-0 flex-col border-l border-border-strong bg-surface-warm">
      <div className="flex h-12 shrink-0 items-center justify-between border-b border-border-subtle px-4">
        <span className="font-display text-body-lg text-text-primary">工作区</span>
        <button
          type="button"
          aria-label="关闭工作区"
          title="关闭工作区"
          onClick={onClose}
          className="inline-flex h-8 w-8 items-center justify-center rounded-button text-text-tertiary transition-colors hover:bg-surface-sunken hover:text-text-primary"
        >
          <X className="h-4 w-4" />
        </button>
      </div>
      <div className="flex-1 overflow-y-auto p-2 space-y-1">
        {artifacts.map((a) => (
          <div key={a.id} className="rounded-card border border-border-subtle bg-surface-raised/65 px-3 py-2 shadow-card">
            <div className="flex items-center gap-2 min-w-0">
              <ArtifactMimeIcon mime={a.mime} />
              <span className="text-body text-text-primary truncate">{artifactBasename(a.logical_path)}</span>
            </div>
            <div className="mt-0.5 flex items-center gap-1.5 text-caption text-text-tertiary">
              <span className="tabular-nums">{formatBytes(a.size)}</span>
              <span aria-hidden>·</span>
              <span className="tabular-nums">{formatTime(a.created_at)}</span>
              <span aria-hidden>·</span>
              {a.status === 'accepted' ? (
                <span className="text-status-success">已接受</span>
              ) : (
                <span className="text-status-warning">草稿</span>
              )}
            </div>
          </div>
        ))}
        {artifacts.length === 0 && (
          <EmptyState
            icon={<Package className="w-5 h-5" />}
            title="暂无成果"
            description="Agent 生成的文档、表格与代码会出现在这里"
            className="py-10"
          />
        )}
      </div>
    </div>
  );
}
