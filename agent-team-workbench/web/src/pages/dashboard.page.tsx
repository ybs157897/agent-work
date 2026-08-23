import { AlertCircle, Bot, CheckCircle2, Circle, Zap, type LucideIcon } from 'lucide-react';
import { Avatar } from '../components/avatar';
import { Loading } from '../components/async-state';
import { PresenceDot, presenceText } from '../components/status';
import { useAgentsStore } from '../stores/agents.store';
import { useDashboardStore } from '../stores/dashboard.store';
import { useTasksStore } from '../stores/tasks.store';
import { useWorkspaceStore } from '../stores/workspace.store';
import { formatTime } from '../utils/format';

/** 总览：统计卡 + Agent 速览 + 任务快照 + 最近日志（协议 §5.2 dashboard 投影）。 */
export default function DashboardPage() {
  const workspace = useWorkspaceStore((s) => s.workspace);
  const dashboard = useDashboardStore((s) => s.dashboard);
  const agents = useAgentsStore((s) => s.agents);
  const workItems = useTasksStore((s) => s.items);

  if (!dashboard) return <Loading />;

  const counts = dashboard.board_counts;
  const taskTotal = counts.todo + counts.in_progress + counts.completed + counts.blocked;

  return (
    <div className="page-shell">
      <section className="ui-card-padded relative overflow-hidden">
        <div className="absolute inset-0 bg-gradient-to-br from-brand-primary/8 via-transparent to-brand-accent/5 pointer-events-none" />
        <div className="relative">
          <p className="text-caption font-medium uppercase tracking-wider text-brand-primary mb-2">工作台总览</p>
          <h1 className="text-h1 text-text-primary mb-2">欢迎回来，{workspace?.name ?? 'Team'}</h1>
          <p className="text-body-lg text-text-secondary">
            今天共有 <span className="font-semibold text-text-primary">{dashboard.active_agents}</span> 个 Agent 在线运行
          </p>
        </div>
      </section>

      <section className="grid grid-cols-1 md:grid-cols-3 gap-comfortable">
        <StatCard
          title="活跃 Agent 数"
          value={dashboard.active_agents}
          icon={Bot}
          accent="from-sky-500/15 to-sky-500/5 text-sky-600"
        />
        <StatCard
          title="运行中任务"
          value={dashboard.running_tasks}
          icon={Zap}
          accent="from-brand-primary/20 to-brand-primary/5 text-brand-accent"
        />
        <StatCard
          title="今日完成"
          value={dashboard.completed_today}
          icon={CheckCircle2}
          accent="from-emerald-500/15 to-emerald-500/5 text-status-success"
        />
      </section>

      <section className="grid grid-cols-1 xl:grid-cols-3 gap-comfortable items-start">
        <div className="xl:col-span-2 ui-card-padded">
          <h2 className="ui-section-title">Agent 状态速览</h2>
          <div className="grid grid-cols-1 sm:grid-cols-2 gap-snug">
            {agents.map((agent) => {
              const currentTask = workItems.find(
                (t) => t.agent_profile_id === agent.id && t.status === 'in_progress',
              );
              return (
                <div
                  key={agent.id}
                  className="flex items-center gap-snug p-snug rounded-lg border border-border-subtle bg-surface-base/60 hover:bg-surface-base hover:border-border-strong transition-colors min-w-0"
                >
                  <Avatar name={agent.name} url={agent.avatar} size={40} />
                  <div className="min-w-0 flex-1">
                    <div className="flex items-center justify-between gap-2 mb-0.5">
                      <span className="font-semibold text-body truncate">{agent.name}</span>
                      <span className="text-caption text-text-tertiary shrink-0">{agent.role}</span>
                    </div>
                    <div className="flex items-center gap-1.5">
                      <PresenceDot presence={agent.presence} />
                      <span className="text-caption text-text-secondary truncate">
                        {currentTask?.title ?? presenceText(agent.presence)}
                      </span>
                    </div>
                  </div>
                </div>
              );
            })}
            {agents.length === 0 && (
              <div className="text-caption text-text-tertiary py-4 col-span-full text-center">暂无 Agent</div>
            )}
          </div>
        </div>

        <div className="ui-card-padded">
          <h2 className="ui-section-title">任务进度快照</h2>
          <div className="space-y-2">
            <TaskStatRow label="待办" count={counts.todo} total={taskTotal} color="bg-text-tertiary" />
            <TaskStatRow label="进行中" count={counts.in_progress} total={taskTotal} color="bg-brand-accent" />
            <TaskStatRow label="完成" count={counts.completed} total={taskTotal} color="bg-status-success" />
            <TaskStatRow label="阻塞" count={counts.blocked} total={taskTotal} color="bg-status-error" />
          </div>
        </div>
      </section>

      <section className="ui-card-padded">
        <h2 className="ui-section-title">最近日志</h2>
        <div className="divide-y divide-border-subtle">
          {dashboard.recent_activities.map((log) => (
            <div
              key={log.id}
              className="flex items-center gap-comfortable py-3 first:pt-0 last:pb-0 hover:bg-surface-base/60 -mx-2 px-2 rounded-lg transition-colors"
            >
              <span className="text-caption text-text-tertiary tabular-nums shrink-0 w-16">
                {formatTime(log.occurred_at)}
              </span>
              <div className="flex items-center gap-2 shrink-0 w-36">
                <ActivityIcon kind={log.kind} />
                <span className="font-medium text-body text-text-primary truncate">{log.kind}</span>
              </div>
              <span className="text-body text-text-secondary truncate flex-1">{log.message}</span>
            </div>
          ))}
          {dashboard.recent_activities.length === 0 && (
            <div className="text-caption text-text-tertiary py-6 text-center">暂无活动记录</div>
          )}
        </div>
      </section>
    </div>
  );
}

function StatCard({
  title,
  value,
  icon: Icon,
  accent,
}: {
  title: string;
  value: number;
  icon: LucideIcon;
  accent: string;
}) {
  return (
    <div className="ui-card-padded flex items-start justify-between gap-4">
      <div>
        <h3 className="text-body text-text-secondary mb-2">{title}</h3>
        <p className="text-display text-text-primary tabular-nums">{value}</p>
      </div>
      <div className={`w-11 h-11 rounded-xl bg-gradient-to-br flex items-center justify-center shrink-0 ${accent}`}>
        <Icon className="w-5 h-5" />
      </div>
    </div>
  );
}

function ActivityIcon({ kind }: { kind: string }) {
  if (/blocked|failed|error/.test(kind)) return <AlertCircle className="w-4 h-4 text-status-error" />;
  if (/completed|created|resolved/.test(kind)) return <CheckCircle2 className="w-4 h-4 text-status-success" />;
  return <Circle className="w-4 h-4 text-brand-accent status-pulse" />;
}

function TaskStatRow({
  label,
  count,
  total,
  color,
}: {
  label: string;
  count: number;
  total: number;
  color: string;
}) {
  const pct = total > 0 ? Math.round((count / total) * 100) : 0;
  return (
    <div className="space-y-1.5">
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-2">
          <div className={`w-2 h-2 rounded-full ${color}`} />
          <span className="text-body text-text-secondary">{label}</span>
        </div>
        <span className="font-semibold text-body text-text-primary tabular-nums">{count}</span>
      </div>
      <div className="h-1.5 bg-surface-sunken rounded-full overflow-hidden">
        <div className={`h-full rounded-full ${color} transition-all duration-500`} style={{ width: `${pct}%` }} />
      </div>
    </div>
  );
}
