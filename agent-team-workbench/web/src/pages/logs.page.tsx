import { AlertCircle, CheckCircle2, Circle, RefreshCw } from 'lucide-react';
import { useEffect } from 'react';
import { EmptyState, Loading } from '../components/async-state';
import { useLogsStore } from '../stores/logs.store';
import { formatDateTime } from '../utils/format';

/** 日志页：活动流查询 + SSE 实时前插（协议 §5.2）。 */
export default function LogsPage() {
  const items = useLogsStore((s) => s.items);
  const loaded = useLogsStore((s) => s.loaded);
  const refresh = useLogsStore((s) => s.refresh);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  return (
    <div className="layout-safe space-y-stack-md py-comfortable">
      <div className="flex items-center justify-between">
        <h2 className="text-h2 text-text-primary tracking-tight">日志</h2>
        <button
          onClick={() => void refresh()}
          className="flex items-center gap-1.5 bg-transparent border border-border-strong text-text-secondary rounded-button px-base py-tight text-body font-medium hover:bg-surface-raised transition-colors"
        >
          <RefreshCw className="w-4 h-4" />
          刷新
        </button>
      </div>

      <div className="bg-surface-raised rounded-card shadow-level-1 border border-border-subtle p-comfortable">
        {!loaded ? (
          <Loading />
        ) : items.length === 0 ? (
          <EmptyState label="暂无活动记录" />
        ) : (
          <div className="space-y-1">
            {items.map((log) => (
              <div
                key={log.id}
                className="flex items-center gap-comfortable p-snug hover:bg-surface-base rounded-md transition-colors"
              >
                <span className="text-caption text-text-tertiary tabular-nums shrink-0 w-36">
                  {formatDateTime(log.occurred_at)}
                </span>
                <div className="flex items-center gap-2 shrink-0 w-44">
                  <KindIcon kind={log.kind} />
                  <span className="font-medium text-body text-text-primary truncate">{log.kind}</span>
                </div>
                <span className="text-body text-text-secondary truncate flex-1">{log.message}</span>
              </div>
            ))}
          </div>
        )}
      </div>
    </div>
  );
}

function KindIcon({ kind }: { kind: string }) {
  if (/blocked|failed|error/.test(kind)) return <AlertCircle className="w-4 h-4 text-status-error" />;
  if (/completed|created|resolved/.test(kind)) return <CheckCircle2 className="w-4 h-4 text-status-success" />;
  return <Circle className="w-4 h-4 text-brand-accent" />;
}
