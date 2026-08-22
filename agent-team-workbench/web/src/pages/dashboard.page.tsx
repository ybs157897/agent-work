import { AlertCircle, Bot, CheckCircle2, Circle, Zap } from 'lucide-react';
import type { ReactNode } from 'react';
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

  return (
    <div className="layout-safe space-y-stack-md py-comfortable">
      {/* Welcome */}
      <section>
        <h1 className="text-h1 text-text-primary mb-tight tracking-tight">
          欢迎回来，{workspace?.name ?? 'Team'}
        </h1>
        <p className="text-body text-text-secondary">
          今天共有 {dashboard.active_agents} 个 Agent 在线运行
        </p>
      </section>

      {/* 统计卡 */}
      <section className="grid grid-cols-3 gap-comfortable">
        <StatCard
          title="活跃 Agent 数"
          value={dashboard.active_agents}
          icon={<Bot className="w-5 h-5 text-brand-primary" />}
        />
        <StatCard
          title="运行中任务"
          value={dashboard.running_tasks}
          icon={<Zap className="w-5 h-5 text-brand-accent" />}
        />
        <StatCard
          title="今日完成"
          value={dashboard.completed_today}
          icon={<CheckCircle2 className="w-5 h-5 text-status-success" />}
        />
      </section>

      <section className="grid grid-cols-3 gap-comfortable">
        {/* Agent 状态速览 */}
        <div className="col-span-2 bg-surface-raised rounded-card shadow-level-1 border border-border-subtle p-comfortable">
          <h2 className="text-h3 text-text-primary mb-comfortable">Agent 状态速览</h2>
          <div className="flex gap-comfortable overflow-x-auto pb-2">
            {agents.map((agent) => {
              const currentTask = workItems.find(
                (t) => t.agent_profile_id === agent.id && t.status === 'in_progress',
              );
              return (
                <div
                  key={agent.id}
                  className="flex items-center gap-snug p-snug rounded-lg border border-border-subtle shrink-0 w-64"
                >
                  <Avatar name={agent.name} url={agent.avatar} size={40} />
                  <div className="min-w-0 flex-1">
                    <div className="flex items-center justify-between mb-0.5">
                      <span className="font-semibold text-body truncate">{agent.name}</span>
                      <span className="text-caption text-text-tertiary">{agent.role}</span>
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
              <div className="text-caption text-text-tertiary py-4">暂无 Agent</div>
            )}
          </div>
        </div>

        {/* 任务进度快照 */}
        <div className="col-span-1 bg-surface-raised rounded-card shadow-level-1 border border-border-subtle p-comfortable">
          <h2 className="text-h3 text-text-primary mb-comfortable">任务进度快照</h2>
          <div className="space-y-snug">
            <TaskStatRow label="待办" count={counts.todo} color="bg-text-tertiary" />
            <TaskStatRow label="进行中" count={counts.in_progress} color="bg-brand-accent" />
            <TaskStatRow label="完成" count={counts.completed} color="bg-status-success" />
            <TaskStatRow label="阻塞" count={counts.blocked} color="bg-status-error" />
          </div>
        </div>
      </section>

      {/* 最近日志 */}
      <section className="bg-surface-raised rounded-card shadow-level-1 border border-border-subtle p-comfortable">
        <h2 className="text-h3 text-text-primary mb-comfortable">最近日志</h2>
        <div className="space-y-2">
          {dashboard.recent_activities.map((log) => (
            <div
              key={log.id}
              className="flex items-center gap-comfortable p-snug hover:bg-surface-base rounded-md transition-colors"
            >
              <span className="text-caption text-text-tertiary tabular-nums shrink-0">
                {formatTime(log.occurred_at)}
              </span>
              <div className="flex items-center gap-2 shrink-0 w-40">
                <ActivityIcon kind={log.kind} />
                <span className="font-medium text-body text-text-primary truncate">{log.kind}</span>
              </div>
              <span className="text-body text-text-secondary truncate flex-1">{log.message}</span>
            </div>
          ))}
          {dashboard.recent_activities.length === 0 && (
            <div className="text-caption text-text-tertiary py-2">暂无活动记录</div>
          )}
        </div>
      </section>
    </div>
  );
}

function StatCard({ title, value, icon }: { title: string; value: number; icon: ReactNode }) {
  return (
    <div className="bg-surface-raised rounded-card shadow-level-1 border border-border-subtle p-comfortable flex items-center justify-between">
      <div>
        <h3 className="text-body text-text-secondary mb-1">{title}</h3>
        <p className="text-display tracking-tight text-text-primary tabular-nums">{value}</p>
      </div>
      <div className="w-12 h-12 rounded-full bg-surface-base flex items-center justify-center shrink-0">
        {icon}
      </div>
    </div>
  );
}

function ActivityIcon({ kind }: { kind: string }) {
  if (/blocked|failed|error/.test(kind)) return <AlertCircle className="w-4 h-4 text-status-error" />;
  if (/completed|created|resolved/.test(kind)) return <CheckCircle2 className="w-4 h-4 text-status-success" />;
  return <Circle className="w-4 h-4 text-brand-accent status-pulse" />;
}

function TaskStatRow({ label, count, color }: { label: string; count: number; color: string }) {
  return (
    <div className="flex items-center justify-between p-2 rounded hover:bg-surface-base transition-colors">
      <div className="flex items-center gap-2">
        <div className={`w-2 h-2 rounded-full ${color}`} />
        <span className="text-body text-text-secondary">{label}</span>
      </div>
      <span className="font-medium text-body text-text-primary tabular-nums">{count}</span>
    </div>
  );
}
