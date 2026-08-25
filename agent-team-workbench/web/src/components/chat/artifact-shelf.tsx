import { Package } from 'lucide-react';
import type { Artifact } from '../../api/types';
import { artifactBasename, formatBytes } from '../../utils/artifact-visuals';
import { ArtifactMimeIcon } from './artifact-workspace';

const SHELF_VISIBLE_ROWS = 4;

/**
 * 聊天区成果摘要卡：只放摘要与「打开工作区」入口，真实清单在右侧工作区
 * （聊天区负责说明，工作区负责承载）。无成果时不渲染。
 */
export function ArtifactShelf({ artifacts, onOpen }: { artifacts: Artifact[]; onOpen: () => void }) {
  if (artifacts.length === 0) return null;
  const visible = artifacts.slice(0, SHELF_VISIBLE_ROWS);
  const hidden = artifacts.length - visible.length;

  return (
    <div className="rounded-lg border border-border-subtle bg-surface-raised px-3 py-2">
      <div className="flex items-center justify-between gap-2">
        <div className="flex items-center gap-1.5 min-w-0">
          <Package className="h-3.5 w-3.5 shrink-0 text-text-tertiary" />
          <span className="text-caption font-medium text-text-primary">
            已生成 {artifacts.length} 个成果
          </span>
        </div>
        <button
          type="button"
          onClick={onOpen}
          className="shrink-0 text-caption font-medium text-brand-primary transition-colors hover:text-brand-accent"
        >
          打开工作区
        </button>
      </div>
      <div className="mt-1.5 space-y-1">
        {visible.map((a) => (
          <div key={a.id} className="flex items-center gap-2 min-w-0">
            <ArtifactMimeIcon mime={a.mime} className="h-3.5 w-3.5" />
            <span className="min-w-0 flex-1 truncate text-caption text-text-secondary">
              {artifactBasename(a.logical_path)}
            </span>
            <span className="shrink-0 text-caption tabular-nums text-text-tertiary">{formatBytes(a.size)}</span>
          </div>
        ))}
        {hidden > 0 && <div className="text-caption text-text-tertiary">还有 {hidden} 项…</div>}
      </div>
    </div>
  );
}
