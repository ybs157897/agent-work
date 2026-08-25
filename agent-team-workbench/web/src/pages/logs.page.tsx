import { AlertCircle, CheckCircle2, Circle, RefreshCw } from 'lucide-react';
import { useEffect } from 'react';
import { Loading } from '../components/async-state';
import { Button, Card, EmptyState } from '../components/ui';
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
        <h2 className="page-title">日志</h2>
        <Button onClick={() => void refresh()}>
          <RefreshCw className="w-4 h-4" />
          刷新
        </Button>
      </header>

      <section>
        <Card padded>
          {!loaded ? (
            <Loading />
          ) : items.length === 0 ? (
            <EmptyState
              icon={<Circle className="w-5 h-5" />}
              title="暂无活动记录"
              description="任务创建、运行推进与审批动态会实时出现在这里"
            />
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
        </Card>
      </section>
    </main>
  );
}

function KindIcon({ kind }: { kind: string }) {
  if (/blocked|failed|error/.test(kind)) return <AlertCircle className="w-4 h-4 text-status-error" />;
  if (/completed|created|resolved/.test(kind)) return <CheckCircle2 className="w-4 h-4 text-status-success" />;
  return <Circle className="w-4 h-4 text-brand-accent" />;
}
