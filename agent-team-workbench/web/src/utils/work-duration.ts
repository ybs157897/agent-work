/** 活动组「已工作」中文耗时（对齐 Codex worked-for 文案）。 */
export function formatWorkDurationZh(startedAt?: string, completedAt?: string): string | null {
  if (!startedAt || !completedAt) return null;
  const ms = Date.parse(completedAt) - Date.parse(startedAt);
  if (!Number.isFinite(ms) || ms < 0) return null;

  if (ms < 1000) return `${Math.round(ms)} 毫秒`;

  if (ms < 60_000) {
    const sec = Math.round(ms / 100) / 10;
    const label = Number.isInteger(sec) ? String(sec) : String(sec);
    return `${label} 秒`;
  }

  const min = Math.floor(ms / 60_000);
  const sec = Math.round((ms % 60_000) / 1000);
  if (sec <= 0) return `${min} 分`;
  return `${min} 分 ${sec} 秒`;
}

/** 从 RFC3339 时间戳集合取最早→最晚的中文耗时。 */
export function formatWorkDurationFromRange(starts: readonly string[], ends: readonly string[]): string | null {
  if (!starts.length || !ends.length) return null;
  const earliest = starts.reduce((a, b) => (a <= b ? a : b));
  const latest = ends.reduce((a, b) => (a >= b ? a : b));
  return formatWorkDurationZh(earliest, latest);
}
