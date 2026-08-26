import { AlertCircle, CheckCircle2, Circle, RefreshCw } from 'lucide-react';
import { useEffect } from 'react';
import { Button, EmptyState, ListSkeleton } from '../components/ui';
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
    <main className="page-shell">
      <header className="page-header">
        <div>
          <p className="text-caption font-medium uppercase tracking-wider text-brand-primary">运行案牍</p>
          <h2 className="page-title">日志</h2>
        </div>
        <Button onClick={() => void refresh()}>
          <RefreshCw className="h-4 w-4" aria-hidden="true" />
          刷新
        </Button>
      </header>

      <section className="ink-paper-panel overflow-hidden rounded-card" aria-labelledby="log-register-title">
        <div className="flex items-end justify-between gap-base border-b border-border-subtle bg-surface-sunken/45 px-comfortable py-base">
          <div>
            <h3 id="log-register-title" className="text-h3 text-text-primary">活动登记簿</h3>
            <p className="mt-1 text-caption text-text-tertiary">按时间倒序记录任务与审批动态</p>
          </div>
          {loaded && <span className="text-caption tabular-nums text-text-tertiary">共 {items.length} 条</span>}
        </div>

        {!loaded ? (
          <div className="p-comfortable"><ListSkeleton padded={false} /></div>
        ) : items.length === 0 ? (
          <div className="p-comfortable">
            <EmptyState
              icon={<Circle className="h-5 w-5" />}
              title="暂无活动记录"
              description="任务创建、运行推进与审批动态会实时出现在这里"
            />
          </div>
        ) : (
          <div className="overflow-x-auto" role="table" aria-label="活动日志">
            <div className="min-w-[680px]">
              <div
                role="row"
                className="grid grid-cols-[10rem_12rem_minmax(0,1fr)] gap-base border-b border-border-subtle px-comfortable py-tight text-caption font-medium uppercase tracking-wider text-text-tertiary"
              >
                <span role="columnheader">时间</span>
                <span role="columnheader">事件</span>
                <span role="columnheader">记录</span>
              </div>
              <div>
                {items.map((log) => (
                  <div
                    key={log.id}
                    role="row"
                    className="grid grid-cols-[10rem_12rem_minmax(0,1fr)] gap-base border-b border-border-subtle/75 px-comfortable py-snug transition-colors duration-inkFast last:border-b-0 hover:bg-surface-base/70"
                  >
                    <time
                      role="cell"
                      dateTime={log.occurred_at}
                      className="self-start pt-0.5 font-mono text-caption tabular-nums text-text-tertiary"
                    >
                      {formatDateTime(log.occurred_at)}
                    </time>
                    <div role="cell" className="flex min-w-0 items-start gap-tight">
                      <KindIcon kind={log.kind} />
                      <span className="min-w-0 truncate font-medium text-body text-text-primary" title={log.kind}>
                        {log.kind}
                      </span>
                    </div>
                    <p role="cell" className="m-0 min-w-0 break-words text-body text-text-secondary">
                      {log.message}
                    </p>
                  </div>
                ))}
              </div>
            </div>
          </div>
        )}
      </section>
    </main>
  );
}

function KindIcon({ kind }: { kind: string }) {
  if (/blocked|failed|error/.test(kind)) return <AlertCircle className="mt-0.5 h-4 w-4 shrink-0 text-status-error" aria-hidden />;
  if (/completed|created|resolved/.test(kind)) return <CheckCircle2 className="mt-0.5 h-4 w-4 shrink-0 text-status-success" aria-hidden />;
  return <Circle className="mt-0.5 h-4 w-4 shrink-0 text-status-info" aria-hidden />;
}
