/**
 * Artifact 展示辅助（纯函数）：mime 归类、文件名提取、字节数格式化。
 * 后端只暴露成果元数据（无内容端点），本层只做清单级呈现。
 */

export type ArtifactMimeKind = 'text' | 'data' | 'image' | 'binary';

/** mime 归类：决定工作区/摘要卡的图标族。 */
export function classifyMime(mime: string): ArtifactMimeKind {
  const m = mime.toLowerCase();
  if (m.includes('csv') || m.includes('tsv') || m.includes('tab-separated')) return 'data';
  if (m.startsWith('image/')) return 'image';
  if (
    m.startsWith('text/') ||
    m === 'application/json' ||
    m === 'application/xml' ||
    m === 'application/x-yaml'
  ) {
    return 'text';
  }
  return 'binary';
}

/** 取逻辑路径最后一段；空或纯斜杠回落原文。 */
export function artifactBasename(logicalPath: string): string {
  const segments = logicalPath.split('/').filter((s) => s !== '');
  return segments.length > 0 ? segments[segments.length - 1] : logicalPath;
}

const BYTE_UNITS = ['B', 'KB', 'MB', 'GB'] as const;

/** 1024 进制字节数；≥KB 保留一位小数并去掉 .0；负数/NaN 归 0 B。 */
export function formatBytes(size: number): string {
  if (!Number.isFinite(size) || size <= 0) return '0 B';
  let value = size;
  let unit = 0;
  while (value >= 1024 && unit < BYTE_UNITS.length - 1) {
    value /= 1024;
    unit += 1;
  }
  if (unit === 0) return `${Math.round(value)} B`;
  const fixed = value.toFixed(1);
  return `${fixed.endsWith('.0') ? fixed.slice(0, -2) : fixed} ${BYTE_UNITS[unit]}`;
}
