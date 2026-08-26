import { AlertCircle, Bot, CheckCircle2, Circle, Inbox, Zap, type LucideIcon } from 'lucide-react';
import { useNavigate } from 'react-router-dom';

import { Avatar } from '../components/avatar';
import { PresenceDot, presenceText } from '../components/status';
import { TextGenerateEffect } from '../components/aceternity/text-generate-effect';
import { InkBentoGrid, InkBentoItem } from '../components/ink/ink-bento';
import { Button, DashboardSkeleton, EmptyState } from '../components/ui';
import { useAgentsStore } from '../stores/agents.store';
import { useDashboardStore } from '../stores/dashboard.store';
import { useTasksStore } from '../stores/tasks.store';
import { useWorkspaceStore } from '../stores/workspace.store';
import { formatTime } from '../utils/format';
import shanShuiPanorama from '../assets/ink/shan-shui-panorama.webp';

/** 总览：统计卡 + Agent 速览 + 任务快照 + 最近日志（协议 §5.2 dashboard 投影）。 */
export default function DashboardPage() {
  const workspace = useWorkspaceStore((s) => s.workspace);
  const dashboard = useDashboardStore((s) => s.dashboard);
  const agents = useAgentsStore((s) => s.agents);
  const workItems = useTasksStore((s) => s.items);
  const navigate = useNavigate();

  if (!dashboard) return <DashboardSkeleton />;

  const counts = dashboard.board_counts;
  const taskTotal = counts.todo + counts.in_progress + counts.completed + counts.blocked;

  return (
    <div className="page-shell">
      <InkBentoGrid className="md:auto-rows-auto md:grid-cols-3">
        <InkBentoItem
          className="md:col-span-3 !min-h-0 !justify-start !space-y-tight !p-comfortable"
          header={
            <img
              src={shanShuiPanorama}
              alt=""
              width={1774}
              height={887}
              className="ink-hero-landscape"
              aria-hidden="true"
            />
          }
          title={
            <div className="space-y-tight">
              <p className="text-caption font-medium uppercase tracking-wider text-brand-primary">工作台总览</p>
              <TextGenerateEffect
                as="h1"
                words={`欢迎回来，${workspace?.name ?? 'Team'}`}
                className="text-h1 tracking-normal"
                duration={0.42}
              />
            </div>
          }
          description={
            <p className="text-body-lg text-text-secondary">
              今天共有 <span className="font-semibold text-text-primary">{dashboard.active_agents}</span> 个 Agent 在线运行
            </p>
          }
        />

        <StatCard title="活跃 Agent 数" value={dashboard.active_agents} icon={Bot} tone="brand" />
        <StatCard title="运行中任务" value={dashboard.running_tasks} icon={Zap} tone="info" />
        <StatCard title="今日完成" value={dashboard.completed_today} icon={CheckCircle2} tone="success" />

        <InkBentoItem
          className="md:col-span-2 !min-h-0"
          title="Agent 状态速览"
          description={
            <div className="grid grid-cols-1 gap-snug sm:grid-cols-2" role="list" aria-label="Agent 状态列表">
              {agents.map((agent) => {
                const currentTask = workItems.find(
                  (t) => t.agent_profile_id === agent.id && t.status === 'in_progress',
                );
                return (
                  <button
                    type="button"
                    key={agent.id}
                    onClick={() => navigate(`/chat?agent=${agent.id}`)}
                    title={`与 ${agent.name} 对话`}
                    className="group flex min-w-0 items-center gap-snug rounded-button border border-border-subtle bg-surface-base/55 p-snug text-left transition-colors duration-inkFast hover:border-border-strong hover:bg-surface-base focus-visible:ring-2 focus-visible:ring-brand-primary/35"
                    role="listitem"
                  >
                    <Avatar name={agent.name} url={agent.avatar} size={40} />
                    <span className="min-w-0 flex-1">
                      <span className="mb-0.5 flex items-center justify-between gap-tight">
                        <span className="truncate font-semibold text-body text-text-primary">{agent.name}</span>
                        <span className="shrink-0 text-caption text-text-tertiary">{agent.role}</span>
                      </span>
                      <span className="flex min-w-0 items-center gap-1.5">
                        <PresenceDot presence={agent.presence} />
                        <span className="truncate text-caption text-text-secondary">
                          {currentTask?.title ?? presenceText(agent.presence)}
                        </span>
                      </span>
                    </span>
                  </button>
                );
              })}
              {agents.length === 0 && (
                <EmptyState
                  className="col-span-full"
                  icon={<Bot className="h-5 w-5" />}
                  title="暂无智能体"
                  description="配置你的第一个智能体，组建你的团队"
                  action={
                    <Button variant="primary" size="sm" onClick={() => navigate('/agents')}>
                      配置智能体
                    </Button>
                  }
                />
              )}
            </div>
          }
        />

        <InkBentoItem
          className="!min-h-0"
          title="任务进度快照"
          description={
            <div className="space-y-snug">
              <TaskStatRow label="待办" count={counts.todo} total={taskTotal} color="bg-status-standby" />
              <TaskStatRow label="进行中" count={counts.in_progress} total={taskTotal} color="bg-status-info" />
              <TaskStatRow label="完成" count={counts.completed} total={taskTotal} color="bg-status-success" />
              <TaskStatRow label="阻塞" count={counts.blocked} total={taskTotal} color="bg-status-error" />
            </div>
          }
        />

        <InkBentoItem
          className="md:col-span-3 !min-h-0"
          title="最近日志"
          description={
            <div className="divide-y divide-border-subtle" role="list" aria-label="最近活动记录">
              {dashboard.recent_activities.map((log) => (
                <div
                  key={log.id}
                  role="listitem"
                  className="grid min-w-0 grid-cols-[4rem_minmax(7rem,9rem)_minmax(0,1fr)] items-center gap-snug py-tight first:pt-0 last:pb-0"
                >
                  <time dateTime={log.occurred_at} className="text-caption tabular-nums text-text-tertiary">
                    {formatTime(log.occurred_at)}
                  </time>
                  <div className="flex min-w-0 items-center gap-tight">
                    <ActivityIcon kind={log.kind} />
                    <span className="truncate font-medium text-body text-text-primary" title={log.kind}>
                      {log.kind}
                    </span>
                  </div>
                  <span className="min-w-0 break-words text-body text-text-secondary">{log.message}</span>
                </div>
              ))}
              {dashboard.recent_activities.length === 0 && (
                <EmptyState
                  icon={<Inbox className="h-5 w-5" />}
                  title="暂无活动记录"
                  description="任务开始执行后，动态会出现在这里"
                />
              )}
            </div>
          }
        />
      </InkBentoGrid>
    </div>
  );
}

function StatCard({
  title,
  value,
  icon: Icon,
  tone,
}: {
  title: string;
  value: number;
  icon: LucideIcon;
  tone: 'brand' | 'info' | 'success';
}) {
  const toneClass = {
    brand: 'bg-brand-muted text-brand-primary',
    info: 'bg-status-info/15 text-status-info',
    success: 'bg-status-success/15 text-status-success',
  }[tone];

  return (
    <InkBentoItem
      className="!min-h-0 !flex-row !items-center !justify-between !space-y-0"
      title={<span className="text-display tabular-nums text-text-primary">{value}</span>}
      description={<span className="text-body text-text-secondary">{title}</span>}
      icon={
        <div className={`flex h-10 w-10 shrink-0 items-center justify-center rounded-md ${toneClass}`} aria-hidden="true">
          <Icon className="h-5 w-5" />
        </div>
      }
    />
  );
}

function ActivityIcon({ kind }: { kind: string }) {
  if (/blocked|failed|error/.test(kind)) return <AlertCircle className="h-4 w-4 shrink-0 text-status-error" aria-hidden />;
  if (/completed|created|resolved/.test(kind)) return <CheckCircle2 className="h-4 w-4 shrink-0 text-status-success" aria-hidden />;
  return <Circle className="status-pulse h-4 w-4 shrink-0 text-status-info" aria-hidden />;
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
      <div className="flex items-center justify-between gap-tight">
        <div className="flex items-center gap-tight">
          <div className={`h-2 w-2 shrink-0 rounded-full ${color}`} aria-hidden="true" />
          <span className="text-body text-text-secondary">{label}</span>
        </div>
        <span className="font-semibold tabular-nums text-body text-text-primary">{count}</span>
      </div>
      <div className="h-1.5 overflow-hidden rounded-full bg-surface-sunken" aria-hidden="true">
        <div className={`h-full rounded-full ${color} transition-[width] duration-inkSlow motion-reduce:transition-none`} style={{ width: `${pct}%` }} />
      </div>
    </div>
  );
}
