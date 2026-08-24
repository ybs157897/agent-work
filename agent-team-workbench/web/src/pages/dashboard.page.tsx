import {
  Activity,
  AlertCircle,
  ArrowRight,
  Bot,
  CheckCircle2,
  Circle,
  CircleDot,
  Clock3,
  MessageSquare,
  PlusCircle,
  ShieldAlert,
  type LucideIcon,
} from 'lucide-react';
import { Link } from 'react-router-dom';
import type { BoardCounts } from '../api/types';
import { Avatar } from '../components/avatar';
import { Loading } from '../components/async-state';
import { PresenceDot, presenceText } from '../components/status';
import { useAgentsStore } from '../stores/agents.store';
import { useDashboardStore } from '../stores/dashboard.store';
import { useTasksStore } from '../stores/tasks.store';
import { useWorkspaceStore } from '../stores/workspace.store';
import { formatTime } from '../utils/format';

const ROLE_LABELS: Record<string, string> = {
  pm: '产品经理',
  architect: '架构师',
  ui: 'UI 设计',
  developer: '开发工程师',
  reviewer: '评审验收',
};

const TASK_META = [
  { key: 'todo', label: '待办', color: 'bg-slate-400', icon: Circle },
  { key: 'in_progress', label: '进行中', color: 'bg-brand-primary', icon: CircleDot },
  { key: 'completed', label: '已完成', color: 'bg-status-success', icon: CheckCircle2 },
  { key: 'blocked', label: '已阻塞', color: 'bg-status-error', icon: ShieldAlert },
] as const;

type ActivityTone = 'brand' | 'success' | 'error' | 'neutral';

const ACTIVITY_META: Record<string, { label: string; tone: ActivityTone }> = {
  'run.created': { label: '运行已创建', tone: 'brand' },
  'run.started': { label: '运行已开始', tone: 'brand' },
  'run.succeeded': { label: '运行已完成', tone: 'success' },
  'run.failed': { label: '运行失败', tone: 'error' },
  'work_item.created': { label: '新建任务', tone: 'neutral' },
  'work_item.completed': { label: '任务已完成', tone: 'success' },
  'work_item.blocked': { label: '任务已阻塞', tone: 'error' },
  'runtime.created': { label: 'Runtime 已添加', tone: 'neutral' },
};

export function roleLabel(role: string): string {
  return ROLE_LABELS[role] ?? role;
}

export function activityMeta(kind: string): { label: string; tone: ActivityTone } {
  if (ACTIVITY_META[kind]) return ACTIVITY_META[kind];
  if (/failed|error|blocked/.test(kind)) return { label: '异常活动', tone: 'error' };
  if (/completed|succeeded|resolved/.test(kind)) return { label: '已完成', tone: 'success' };
  return { label: '系统活动', tone: 'neutral' };
}

export function taskPercent(count: number, total: number): number {
  return total > 0 ? Math.round((count / total) * 100) : 0;
}

/** 总览：统计、Agent 状态、任务分布与近期活动（协议 §5.2 dashboard 投影）。 */
export default function DashboardPage() {
  const workspace = useWorkspaceStore((s) => s.workspace);
  const dashboard = useDashboardStore((s) => s.dashboard);
  const agents = useAgentsStore((s) => s.agents);
  const workItems = useTasksStore((s) => s.items);

  if (!dashboard) return <Loading />;

  const counts = dashboard.board_counts;
  const taskTotal = counts.todo + counts.in_progress + counts.completed + counts.blocked;
  const progressingTasks = counts.in_progress + counts.blocked;

  return (
    <div className="page-shell max-w-none">
      <header className="flex items-end justify-between gap-8">
        <div>
          <h1 className="text-h1 text-text-primary">工作台总览</h1>
          <p className="mt-1.5 text-body text-text-secondary">集中查看团队资源、任务推进和最近运行活动。</p>
        </div>
        <div className="flex shrink-0 items-center gap-2">
          <Link to="/chat" className="btn-secondary">
            <MessageSquare className="h-4 w-4" />
            发起对话
          </Link>
          <Link to="/tasks" className="btn-primary">
            进入任务看板
            <ArrowRight className="h-4 w-4" />
          </Link>
        </div>
      </header>

      <section className="ui-card overflow-hidden" aria-label="工作区运行概览">
        <div className="grid lg:grid-cols-[minmax(340px,1.3fr)_repeat(3,minmax(160px,0.7fr))]">
          <div className="flex min-w-0 items-center gap-4 border-b border-border-subtle bg-brand-muted/55 p-comfortable lg:border-b-0 lg:border-r">
            <div className="flex h-11 w-11 shrink-0 items-center justify-center rounded-lg bg-brand-primary text-white shadow-sm shadow-brand-primary/15">
              <Activity className="h-5 w-5" />
            </div>
            <div className="min-w-0">
              <p className="text-caption font-medium text-brand-accent">当前工作区</p>
              <h2 className="mt-0.5 truncate text-h3 text-text-primary">{workspace?.name ?? 'Team'}</h2>
              <p className="mt-1 text-caption text-text-secondary">
                {dashboard.active_agents} 个 Agent 已启用，{progressingTasks} 项任务正在推进
              </p>
            </div>
          </div>
          <SummaryMetric label="可用 Agent" value={dashboard.active_agents} icon={Bot} />
          <SummaryMetric label="正在运行" value={dashboard.running_tasks} icon={Clock3} />
          <SummaryMetric label="今日完成" value={dashboard.completed_today} icon={CheckCircle2} />
        </div>
      </section>

      <section className="grid items-stretch gap-comfortable xl:grid-cols-[minmax(0,1.75fr)_minmax(340px,0.75fr)]">
        <div className="ui-card overflow-hidden">
          <SectionHeader
            title="Agent 运行概览"
            description="当前职责、在手任务与在线状态"
            action={{ to: '/agents', label: '管理 Agent' }}
          />
          <div className="grid gap-x-3 p-snug md:grid-cols-2">
            {agents.map((agent) => {
              const currentTask = workItems.find(
                (task) => task.agent_profile_id === agent.id && task.status === 'in_progress',
              );
              return (
                <div
                  key={agent.id}
                  className="group flex min-w-0 items-center gap-3.5 rounded-lg border border-transparent px-3 py-3 transition-colors hover:border-border-subtle hover:bg-surface-base"
                >
                  <Avatar name={agent.name} url={agent.avatar} size={42} />
                  <div className="min-w-0 flex-1">
                    <div className="flex min-w-0 items-center gap-2">
                      <span className="truncate text-body font-semibold text-text-primary">{agent.name}</span>
                      <span className="shrink-0 rounded-md bg-surface-sunken px-1.5 py-0.5 text-[11px] font-medium text-text-secondary">
                        {roleLabel(agent.role)}
                      </span>
                    </div>
                    <p className="mt-1 truncate text-caption text-text-secondary">
                      {currentTask?.title ?? '当前无进行中任务'}
                    </p>
                  </div>
                  <div className="flex shrink-0 items-center gap-1.5 text-caption text-text-tertiary">
                    <PresenceDot presence={agent.presence} pulse={agent.presence === 'busy'} />
                    <span>{presenceText(agent.presence)}</span>
                  </div>
                </div>
              );
            })}
            {agents.length === 0 && (
              <div className="col-span-full flex min-h-40 flex-col items-center justify-center text-center">
                <Bot className="mb-3 h-7 w-7 text-text-tertiary" />
                <p className="text-body font-medium text-text-primary">还没有 Agent</p>
                <p className="mt-1 text-caption text-text-tertiary">添加 Agent 后将在这里显示运行状态。</p>
              </div>
            )}
          </div>
        </div>

        <div className="ui-card overflow-hidden">
          <SectionHeader
            title="任务分布"
            description="当前看板中的任务状态"
            action={{ to: '/tasks', label: '查看任务' }}
          />
          <div className="p-comfortable pt-5">
            <div className="flex items-end justify-between gap-4">
              <div>
                <p className="text-caption text-text-tertiary">任务总数</p>
                <p className="mt-1 text-[34px] font-bold leading-none tracking-tight text-text-primary tabular-nums">
                  {taskTotal}
                </p>
              </div>
              <p className="text-right text-caption text-text-secondary">
                <span className="font-semibold text-brand-accent">{taskPercent(counts.in_progress, taskTotal)}%</span>
                <br />正在进行
              </p>
            </div>

            <TaskDistribution counts={counts} total={taskTotal} />

            <div className="mt-5 space-y-0.5">
              {TASK_META.map(({ key, label, color, icon: Icon }) => (
                <div key={key} className="flex items-center justify-between rounded-lg px-2 py-2.5 hover:bg-surface-base">
                  <div className="flex items-center gap-2.5">
                    <Icon className="h-4 w-4 text-text-tertiary" />
                    <span className="text-body text-text-secondary">{label}</span>
                  </div>
                  <div className="flex items-center gap-3">
                    <span className="text-caption text-text-tertiary tabular-nums">{taskPercent(counts[key], taskTotal)}%</span>
                    <span className="w-6 text-right text-body font-semibold text-text-primary tabular-nums">{counts[key]}</span>
                    <span className={`h-2 w-2 rounded-full ${color}`} aria-hidden="true" />
                  </div>
                </div>
              ))}
            </div>
          </div>
        </div>
      </section>

      <section className="ui-card overflow-hidden">
        <SectionHeader
          title="近期活动"
          description="任务、运行与 Runtime 的最新变化"
          action={{ to: '/logs', label: '查看全部日志' }}
        />
        <div className="px-comfortable pb-snug">
          {dashboard.recent_activities.slice(0, 8).map((log) => {
            const meta = activityMeta(log.kind);
            return (
              <div
                key={log.id}
                className="grid min-h-14 grid-cols-[34px_132px_minmax(0,1fr)_76px] items-center gap-3 border-b border-border-subtle/80 py-2.5 last:border-b-0"
              >
                <ActivityIcon tone={meta.tone} />
                <span className="text-caption font-medium text-text-secondary">{meta.label}</span>
                <span className="truncate text-body text-text-primary" title={log.message}>
                  {log.message}
                </span>
                <time className="text-right text-caption text-text-tertiary tabular-nums" dateTime={log.occurred_at}>
                  {formatTime(log.occurred_at)}
                </time>
              </div>
            );
          })}
          {dashboard.recent_activities.length === 0 && (
            <div className="flex min-h-40 flex-col items-center justify-center text-center">
              <Activity className="mb-3 h-7 w-7 text-text-tertiary" />
              <p className="text-body font-medium text-text-primary">暂无活动记录</p>
              <p className="mt-1 text-caption text-text-tertiary">任务或运行状态变化后会出现在这里。</p>
            </div>
          )}
        </div>
      </section>
    </div>
  );
}

function SummaryMetric({ label, value, icon: Icon }: { label: string; value: number; icon: LucideIcon }) {
  return (
    <div className="flex min-w-0 items-center justify-between gap-4 border-b border-border-subtle p-comfortable last:border-b-0 lg:border-b-0 lg:border-r lg:last:border-r-0">
      <div>
        <p className="text-caption text-text-secondary">{label}</p>
        <p className="mt-1.5 text-[30px] font-bold leading-none tracking-tight text-text-primary tabular-nums">{value}</p>
      </div>
      <div className="flex h-9 w-9 shrink-0 items-center justify-center rounded-lg bg-surface-sunken text-text-secondary">
        <Icon className="h-[18px] w-[18px]" />
      </div>
    </div>
  );
}

function SectionHeader({
  title,
  description,
  action,
}: {
  title: string;
  description: string;
  action: { to: string; label: string };
}) {
  return (
    <div className="flex items-center justify-between gap-4 border-b border-border-subtle px-comfortable py-base">
      <div className="min-w-0">
        <h2 className="text-body font-semibold text-text-primary">{title}</h2>
        <p className="mt-0.5 truncate text-caption text-text-tertiary">{description}</p>
      </div>
      <Link
        to={action.to}
        className="group inline-flex shrink-0 items-center gap-1.5 rounded-md px-2 py-1.5 text-caption font-medium text-text-secondary transition-colors hover:bg-surface-base hover:text-brand-accent"
      >
        {action.label}
        <ArrowRight className="h-3.5 w-3.5 transition-transform group-hover:translate-x-0.5" />
      </Link>
    </div>
  );
}

function TaskDistribution({ counts, total }: { counts: BoardCounts; total: number }) {
  if (total === 0) {
    return (
      <div className="mt-5 flex h-2 overflow-hidden rounded-full bg-surface-sunken" aria-label="当前没有任务" />
    );
  }

  return (
    <div className="mt-5 flex h-2 overflow-hidden rounded-full bg-surface-sunken" aria-label={`共 ${total} 个任务`}>
      {TASK_META.map(({ key, label, color }) => {
        const width = (counts[key] / total) * 100;
        if (width === 0) return null;
        return (
          <span
            key={key}
            className={`${color} h-full first:rounded-l-full last:rounded-r-full`}
            style={{ width: `${width}%` }}
            title={`${label} ${counts[key]} 个`}
          />
        );
      })}
    </div>
  );
}

function ActivityIcon({ tone }: { tone: ActivityTone }) {
  const styles: Record<ActivityTone, string> = {
    brand: 'bg-brand-muted text-brand-accent',
    success: 'bg-emerald-50 text-status-success',
    error: 'bg-red-50 text-status-error',
    neutral: 'bg-surface-sunken text-text-secondary',
  };

  let Icon: LucideIcon = PlusCircle;
  if (tone === 'brand') Icon = CircleDot;
  if (tone === 'success') Icon = CheckCircle2;
  if (tone === 'error') Icon = AlertCircle;

  return (
    <span className={`flex h-8 w-8 items-center justify-center rounded-lg ${styles[tone]}`} aria-hidden="true">
      <Icon className="h-4 w-4" />
    </span>
  );
}
