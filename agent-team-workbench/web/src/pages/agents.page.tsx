import { Plus } from 'lucide-react';
import { useEffect, useState } from 'react';
import { ApiError } from '../api/client';
import { createAgent, listWorkItems } from '../api/endpoints';
import type { AgentProfile, WorkItem } from '../api/types';
import { Avatar } from '../components/avatar';
import { Drawer } from '../components/drawer';
import { Modal } from '../components/modal';
import { PresenceDot, presenceText } from '../components/status';
import { Toggle } from '../components/toggle';
import { useAgentsStore } from '../stores/agents.store';
import { useRunsStore } from '../stores/runs.store';
import { useTasksStore } from '../stores/tasks.store';
import { toast } from '../stores/toast.store';
import { useWorkspaceStore } from '../stores/workspace.store';
import { formatDateTime } from '../utils/format';

const ROLE_OPTIONS = [
  { value: 'pm', label: '产品 PM' },
  { value: 'architect', label: '架构师' },
  { value: 'ui', label: 'UI 设计' },
  { value: 'developer', label: '开发' },
  { value: 'reviewer', label: 'Reviewer' },
];

/** 智能体团队：卡片网格 + 280px 详情抽屉（availability 与 presence 分离呈现，协议 §12.1）。 */
export default function AgentsPage() {
  const agents = useAgentsStore((s) => s.agents);
  const selectedAgentId = useAgentsStore((s) => s.selectedAgentId);
  const select = useAgentsStore((s) => s.select);
  const [addOpen, setAddOpen] = useState(false);

  const selected = agents.find((a) => a.id === selectedAgentId) ?? null;

  return (
    <div className="layout-safe space-y-stack-md py-comfortable relative">
      <div className="flex items-center justify-between">
        <h2 className="text-h2 text-text-primary tracking-tight">智能体团队</h2>
        <button
          onClick={() => setAddOpen(true)}
          className="flex items-center gap-1.5 bg-transparent border border-brand-primary text-brand-primary rounded-button px-base py-tight font-medium transition-colors duration-150 hover:bg-brand-primary/5 active:scale-[0.98]"
        >
          <Plus className="w-4 h-4" />
          <span>添加 Agent</span>
        </button>
      </div>

      <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-comfortable pb-24">
        {agents.map((agent, index) => (
          <AgentCard
            key={agent.id}
            agent={agent}
            onClick={() => select(agent.id)}
            className={index === 1 ? 'md:col-span-2 lg:col-span-2' : 'col-span-1'}
          />
        ))}
      </div>

      <Drawer open={selected !== null} onClose={() => select(null)}>
        {selected && <AgentDetail agent={selected} />}
      </Drawer>

      <AddAgentModal open={addOpen} onClose={() => setAddOpen(false)} />
    </div>
  );
}

function AgentCard({
  agent,
  onClick,
  className = '',
}: {
  agent: AgentProfile;
  onClick: () => void;
  className?: string;
}) {
  const workItems = useTasksStore((s) => s.items);
  const runs = useRunsStore((s) => s.runs);
  const currentTask = workItems.find(
    (t) => t.agent_profile_id === agent.id && t.status === 'in_progress',
  );
  const activeRun = currentTask?.latest_run_id ? runs[currentTask.latest_run_id] : undefined;
  const disabled = agent.availability === 'disabled';

  return (
    <div
      onClick={onClick}
      className={`bg-surface-raised rounded-card shadow-level-1 border border-border-subtle p-comfortable cursor-pointer transition-all duration-250 ease-out hover:-translate-y-[3px] hover:shadow-level-2 group flex flex-col ${className} ${
        disabled ? 'opacity-60' : ''
      }`}
    >
      <div className="flex items-center gap-snug mb-comfortable">
        <Avatar name={agent.name} url={agent.avatar} size={48} ring />
        <div className="flex-1 min-w-0">
          <div className="flex items-center justify-between">
            <h3 className="font-semibold text-body-lg text-text-primary truncate">{agent.name}</h3>
            <span className="inline-flex items-center px-2 py-0.5 rounded-sm text-caption font-medium bg-surface-base text-text-secondary border border-border-subtle">
              {agent.role}
            </span>
          </div>
        </div>
      </div>

      <div className="flex items-center gap-2 mb-snug">
        <PresenceDot presence={disabled ? 'offline' : agent.presence} />
        <span className="text-body text-text-primary font-medium">
          {disabled ? '已停用' : presenceText(agent.presence)}
        </span>
      </div>

      <p className="text-caption text-text-secondary truncate mb-comfortable flex-1">
        {currentTask?.title ?? (disabled ? '调度已停用' : '等待任务分配')}
      </p>

      {activeRun?.progress != null && (
        <div className="space-y-1 mb-comfortable">
          <div className="flex justify-end">
            <span className="text-caption text-text-tertiary tabular-nums">
              {Math.round(activeRun.progress * 100)}%
            </span>
          </div>
          <div className="h-1 bg-surface-base rounded-full overflow-hidden">
            <div
              className={`h-full ${
                activeRun.status === 'failed' ? 'bg-status-error' : 'bg-brand-primary'
              } transition-all duration-500`}
              style={{ width: `${Math.round(activeRun.progress * 100)}%` }}
            />
          </div>
        </div>
      )}

      <div className="pt-snug border-t border-border-subtle">
        <span className="text-caption font-medium text-brand-primary group-hover:text-brand-accent transition-colors">
          查看详情 →
        </span>
      </div>
    </div>
  );
}

function AgentDetail({ agent }: { agent: AgentProfile }) {
  const workspace = useWorkspaceStore((s) => s.workspace);
  const setAvailability = useAgentsStore((s) => s.setAvailability);
  const [history, setHistory] = useState<WorkItem[] | null>(null);
  const [toggling, setToggling] = useState(false);
  const enabled = agent.availability === 'enabled';

  // 任务历史：该 Agent 已完成的 WorkItem（服务端投影，协议 §4.1）。
  useEffect(() => {
    setHistory(null);
    if (!workspace) return;
    let cancelled = false;
    listWorkItems(workspace.id, { assignee: agent.id, status: 'completed' })
      .then(({ items }) => {
        if (!cancelled) setHistory(items.slice(0, 8));
      })
      .catch(() => {
        if (!cancelled) setHistory([]);
      });
    return () => {
      cancelled = true;
    };
  }, [agent.id, workspace]);

  const onToggle = async (next: boolean) => {
    if (!next && agent.presence === 'busy') {
      if (!window.confirm(`${agent.name} 正在运行任务，停用后活动 Run 将被中断。确认停用？`)) return;
    }
    setToggling(true);
    try {
      await setAvailability(agent, next);
      toast.success(next ? `已启用 ${agent.name}` : `已停用 ${agent.name}`);
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : '状态切换失败');
    } finally {
      setToggling(false);
    }
  };

  return (
    <div className="p-comfortable flex flex-col h-full">
      <div className="flex flex-col items-center mt-stack-md mb-stack-md text-center">
        <Avatar name={agent.name} url={agent.avatar} size={80} />
        <h2 className="text-h2 tracking-tight text-text-primary mt-snug">{agent.name}</h2>
        <span className="inline-flex items-center px-2 py-1 rounded text-caption font-medium bg-surface-base text-text-secondary mt-2 mb-4">
          {agent.role}
        </span>
        <div className="flex items-center gap-2">
          <PresenceDot presence={enabled ? agent.presence : 'offline'} />
          <span className="text-body text-text-secondary">
            {enabled ? presenceText(agent.presence) : '已停用'}
          </span>
        </div>
      </div>

      <div className="flex-1 overflow-y-auto space-y-stack-md">
        <section>
          <h3 className="text-body font-semibold text-text-primary mb-snug">技能</h3>
          <ul className="space-y-1">
            {agent.skills.map((s, i) => (
              <li key={i} className="text-body text-text-secondary flex items-center gap-2">
                <div className="w-1 h-1 rounded-full bg-text-tertiary" />
                {s}
              </li>
            ))}
            {agent.skills.length === 0 && (
              <li className="text-caption text-text-tertiary">未配置技能</li>
            )}
          </ul>
        </section>

        <section>
          <h3 className="text-body font-semibold text-text-primary mb-snug">任务历史</h3>
          <div className="space-y-2">
            {history === null ? (
              <div className="text-caption text-text-tertiary">加载中…</div>
            ) : history.length > 0 ? (
              history.map((h) => (
                <div key={h.id} className="text-caption">
                  <div className="flex justify-between text-text-secondary mb-0.5">
                    <span>{formatDateTime(h.updated_at)}</span>
                    <span className="text-text-tertiary">已完成</span>
                  </div>
                  <div className="text-text-primary truncate">{h.title}</div>
                </div>
              ))
            ) : (
              <div className="text-caption text-text-tertiary">暂无任务记录</div>
            )}
          </div>
        </section>
      </div>

      <div className="pt-comfortable mt-auto border-t border-border-subtle flex items-center justify-between">
        <span className="text-body font-medium text-text-primary">调度开关</span>
        <Toggle checked={enabled} onChange={onToggle} disabled={toggling} />
      </div>
    </div>
  );
}

function AddAgentModal({ open, onClose }: { open: boolean; onClose: () => void }) {
  const workspace = useWorkspaceStore((s) => s.workspace);
  const refresh = useAgentsStore((s) => s.refresh);
  const [name, setName] = useState('');
  const [role, setRole] = useState('developer');
  const [skills, setSkills] = useState('');
  const [submitting, setSubmitting] = useState(false);

  const submit = async () => {
    if (!workspace || !name.trim()) return;
    setSubmitting(true);
    try {
      await createAgent(workspace.id, {
        name: name.trim(),
        role,
        skills: skills
          .split(/[,，]/)
          .map((s) => s.trim())
          .filter(Boolean),
      });
      await refresh();
      toast.success(`已添加 Agent ${name.trim()}`);
      setName('');
      setSkills('');
      onClose();
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : '创建失败');
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Modal open={open} onClose={onClose} title="添加 Agent">
      <div className="space-y-base">
        <label className="block">
          <span className="text-body text-text-secondary">名称</span>
          <input
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="例如 Forge"
            className="mt-1 w-full rounded-input border border-border-strong bg-surface-raised px-snug py-tight text-body outline-none focus:ring-2 focus:ring-brand-primary/30"
          />
        </label>
        <label className="block">
          <span className="text-body text-text-secondary">角色</span>
          <select
            value={role}
            onChange={(e) => setRole(e.target.value)}
            className="mt-1 w-full rounded-input border border-border-strong bg-surface-raised px-snug py-tight text-body outline-none focus:ring-2 focus:ring-brand-primary/30"
          >
            {ROLE_OPTIONS.map((r) => (
              <option key={r.value} value={r.value}>
                {r.label}
              </option>
            ))}
          </select>
        </label>
        <label className="block">
          <span className="text-body text-text-secondary">技能（逗号分隔）</span>
          <input
            value={skills}
            onChange={(e) => setSkills(e.target.value)}
            placeholder="Go, React, 测试"
            className="mt-1 w-full rounded-input border border-border-strong bg-surface-raised px-snug py-tight text-body outline-none focus:ring-2 focus:ring-brand-primary/30"
          />
        </label>
        <div className="flex justify-end gap-snug pt-tight">
          <button
            onClick={onClose}
            className="bg-transparent border border-border-strong text-text-secondary rounded-button px-base py-tight font-medium hover:bg-surface-base transition-colors"
          >
            取消
          </button>
          <button
            onClick={submit}
            disabled={!name.trim() || submitting}
            className="bg-brand-primary text-white rounded-button px-base py-tight font-medium transition-all duration-150 hover:bg-brand-accent active:scale-[0.98] disabled:opacity-50 disabled:cursor-not-allowed"
          >
            {submitting ? '创建中…' : '创建'}
          </button>
        </div>
      </div>
    </Modal>
  );
}
