/** 时间显示：协议为 RFC 3339 UTC，界面显示本地时间。 */

export function formatTime(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  return d.toLocaleTimeString('zh-CN', { hour12: false });
}

export function formatDateTime(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  return `${d.toLocaleDateString('zh-CN')} ${d.toLocaleTimeString('zh-CN', { hour12: false })}`;
}

/** 业务截止日 YYYY-MM-DD（workspace 时区由服务端负责）。 */
export function formatDueDate(date: string | null | undefined): string {
  if (!date) return '';
  return date.slice(5).replace('-', '/');
}
