import { Package } from 'lucide-react';
import type { Artifact } from '../../api/types';
import { artifactBasename } from '../../utils/artifact-visuals';
import { ArtifactMimeIcon } from './artifact-workspace';

/**
 * 成果摘要条（单行）：只报数量与最近产物，真实清单在右侧工作区承载
 * （聊天区负责说明，工作区负责承载）。无成果时不渲染。
 */
export function ArtifactShelf({ artifacts, onOpen }: { artifacts: Artifact[]; onOpen: () => void }) {
  if (artifacts.length === 0) return null;
  const latest = artifacts[artifacts.length - 1];

  return (
    <div className="flex items-center gap-2 rounded-lg border border-border-subtle/70 bg-surface-raised/60 px-snug py-tight">
      <Package className="h-3.5 w-3.5 shrink-0 text-text-tertiary" aria-hidden />
      <span className="shrink-0 text-caption font-medium text-text-primary">已生成 {artifacts.length} 个成果</span>
      <span className="hidden min-w-0 flex-1 truncate text-caption text-text-tertiary sm:inline">
        最新：<ArtifactMimeIcon mime={latest.mime} className="mr-1 inline h-3 w-3 -translate-y-px" />
        {artifactBasename(latest.logical_path)}
      </span>
      <button
        type="button"
        onClick={onOpen}
        className="ml-auto shrink-0 text-caption font-medium text-brand-primary transition-colors hover:text-brand-accent"
      >
        打开工作区
      </button>
    </div>
  );
}
