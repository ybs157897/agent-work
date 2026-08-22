import { MessageSquare, Plus, RefreshCw } from 'lucide-react';
import { useEffect, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { ApiError } from '../api/client';
import {
  createAgent,
  listRuntimeBindings,
  listWorkItems,
  patchAgent,
  reloadAgentConfigs,
} from '../api/endpoints';
import type { AgentPolicy, AgentProfile, RuntimeBinding, WorkItem } from '../api/types';
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

/** 权限工具词汇表（映射 DSH 工具片段；未知工具在渲染配置时裁剪）。 */
const TOOL_OPTIONS = [
  { value: 'bash', label: 'bash（持久 shell，高风险）' },
  { value: 'editor', label: 'editor（文件编辑）' },
  { value: 'fs', label: 'fs（文件读写）' },
  { value: 'todo', label: 'todo（任务清单）' },
  { value: 'subagent', label: 'subagent（子代理）' },
];

/** 智能体团队：卡片网格 + 详情抽屉（配置回写 agents/ 文件目录，文件为真相源）。 */
export default function AgentsPage() {
  const agents = useAgentsStore((s) => s.agents);
  const selectedAgentId = useAgentsStore((s) => s.selectedAgentId);
  const select = useAgentsStore((s) => s.select);
  const refresh = useAgentsStore((s) => s.refresh);
  const workspace = useWorkspaceStore((s) => s.workspace);
  const [addOpen, setAddOpen] = useState(false);
  const [reloading, setReloading] = useState(false);

  const selected = agents.find((a) => a.id === selectedAgentId) ?? null;

  const reloadConfigs = async () => {
    if (!workspace) return;
    setReloading(true);
    try {
      const res = await reloadAgentConfigs(workspace.id);
      await refresh();
      toast.success(`配置目录已重载：新增 ${res.created}，更新 ${res.updated}`);
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : '重载失败');
    } finally {
      setReloading(false);
    }
  };

  return (
    <div className="layout-safe space-y-stack-md py-comfortable relative">
      <div className="flex items-center justify-between">
        <h2 className="text-h2 text-text-primary tracking-tight">智能体团队</h2>
        <div className="flex items-center gap-2">
          <button
            onClick={reloadConfigs}
            disabled={reloading}
            title="重新扫描 agents/ 配置目录（文件为真相源）"
            className="flex items-center gap-1.5 bg-transparent border border-border-strong text-text-secondary rounded-button px-base py-tight font-medium transition-colors hover:bg-surface-raised disabled:opacity-50"
          >
            <RefreshCw className={`w-4 h-4 ${reloading ? 'animate-spin' : ''}`} />
            <span>重载配置</span>
          </button>
          <button
            onClick={() => setAddOpen(true)}
            className="flex items-center gap-1.5 bg-transparent border border-brand-primary text-brand-primary rounded-button px-base py-tight font-medium transition-colors duration-150 hover:bg-brand-primary/5 active:scale-[0.98]"
          >
            <Plus className="w-4 h-4" />
            <span>添加 Agent</span>
          </button>
        </div>
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

      <Drawer open={selected !== null} onClose={() => select(null)} width={360}>
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
  const navigate = useNavigate();
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
    <div className="p-comfortable flex flex-col h-full overflow-y-auto">
      <div className="flex flex-col items-center mt-stack-md mb-stack-md text-center shrink-0">
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
        <button
          onClick={() => navigate(`/chat?agent=${agent.id}`)}
          className="mt-3 flex items-center gap-1.5 bg-brand-primary text-white rounded-button px-base py-tight text-body font-medium transition-all hover:bg-brand-accent active:scale-[0.98]"
        >
          <MessageSquare className="w-4 h-4" />
          发起对话
        </button>
      </div>

      <div className="flex-1 space-y-stack-md">
        <section>
          <h3 className="text-body font-semibold text-text-primary mb-snug">技能</h3>
          <ul className="space-y-1">
            {agent.skills.map((s, i) => (
              <li key={i} className="text-body text-text-secondary flex items-center gap-2">
                <div className="w-1 h-1 rounded-full bg-text-tertiary" />
                {s}
              </li>
            ))}
            {agent.skills.length === 0 && <li className="text-caption text-text-tertiary">未配置技能</li>}
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

        <AgentConfigSection key={`${agent.id}:${agent.version}`} agent={agent} />
      </div>

      <div className="pt-comfortable mt-auto border-t border-border-subtle flex items-center justify-between shrink-0">
        <span className="text-body font-medium text-text-primary">调度开关</span>
        <Toggle checked={enabled} onChange={onToggle} disabled={toggling} />
      </div>
    </div>
  );
}

/** Agent 配置面板：提示词 / 模型 / Runtime 偏好 / 权限；保存回写 agents/<slug>/ 文件。 */
function AgentConfigSection({ agent }: { agent: AgentProfile }) {
  const workspace = useWorkspaceStore((s) => s.workspace);
  const upsert = useAgentsStore((s) => s.upsert);

  const [instructions, setInstructions] = useState(agent.instructions ?? '');
  const [provider, setProvider] = useState(agent.model_override?.provider ?? '');
  const [model, setModel] = useState(agent.model_override?.model ?? '');
  const [preferred, setPreferred] = useState(agent.runtime_preference?.preferred ?? '');
  const [fallbacks, setFallbacks] = useState<string[]>(agent.runtime_preference?.fallbacks ?? []);
  const [tools, setTools] = useState<string[]>(agent.policy?.tools ?? []);
  const [approvalPolicy, setApprovalPolicy] = useState<string>(agent.policy?.approval_policy || 'auto');
  const [sandbox, setSandbox] = useState(agent.policy?.sandbox ?? 'workspace_only');
  const [bindings, setBindings] = useState<RuntimeBinding[]>([]);
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    if (!workspace) return;
    listRuntimeBindings(workspace.id)
      .then(({ items }) => setBindings(items))
      .catch(() => setBindings([]));
  }, [workspace]);

  const toggleIn = (list: string[], v: string) =>
    list.includes(v) ? list.filter((x) => x !== v) : [...list, v];

  const save = async () => {
    setSaving(true);
    try {
      const policy: AgentPolicy = {
        tools,
        approval_policy: approvalPolicy as AgentPolicy['approval_policy'],
        sandbox,
      };
      const updated = await patchAgent(agent.id, {
        instructions,
        runtime_preference: { preferred, fallbacks },
        model_override: { provider, model },
        policy,
        expected_version: agent.version,
      });
      upsert(updated);
      toast.success(`配置已保存并回写 agents/${updated.slug ?? ''}/`);
    } catch (err) {
      if (err instanceof ApiError && err.isVersionConflict) {
        toast.error('配置已被他人修改，请关闭抽屉后重试');
      } else {
        toast.error(err instanceof ApiError ? err.message : '保存失败');
      }
    } finally {
      setSaving(false);
    }
  };

  const inputCls =
    'mt-1 w-full rounded-input border border-border-strong bg-surface-raised px-snug py-tight text-body outline-none focus:ring-2 focus:ring-brand-primary/30';

  return (
    <section className="rounded-lg border border-border-subtle p-snug space-y-base">
      <div className="flex items-center justify-between">
        <h3 className="text-body font-semibold text-text-primary">配置</h3>
        {agent.slug && <span className="text-caption text-text-tertiary">agents/{agent.slug}/</span>}
      </div>

      <label className="block">
        <span className="text-caption text-text-tertiary">系统提示词（prompt.md）</span>
        <textarea
          value={instructions}
          onChange={(e) => setInstructions(e.target.value)}
          rows={5}
          className={`${inputCls} resize-y font-mono text-caption`}
          placeholder="角色职责、输出约束、协作约定…"
        />
      </label>

      <div className="grid grid-cols-2 gap-snug">
        <label className="block">
          <span className="text-caption text-text-tertiary">Provider</span>
          <input value={provider} onChange={(e) => setProvider(e.target.value)} className={inputCls} placeholder="deepseek" />
        </label>
        <label className="block">
          <span className="text-caption text-text-tertiary">模型</span>
          <input value={model} onChange={(e) => setModel(e.target.value)} className={inputCls} placeholder="deepseek-v4-flash" />
        </label>
      </div>

      <label className="block">
        <span className="text-caption text-text-tertiary">首选 Runtime</span>
        <select value={preferred} onChange={(e) => setPreferred(e.target.value)} className={inputCls}>
          <option value="">默认（mock）</option>
          {bindings.map((b) => (
            <option key={b.id} value={b.runtime_label}>
              {b.runtime_label}（{b.provider}/{b.model}）
            </option>
          ))}
        </select>
      </label>

      <div>
        <span className="text-caption text-text-tertiary">回退 Runtime</span>
        <div className="mt-1 flex flex-wrap gap-2">
          {bindings
            .filter((b) => b.runtime_label !== preferred)
            .map((b) => (
              <label key={b.id} className="flex items-center gap-1 text-caption text-text-secondary">
                <input
                  type="checkbox"
                  checked={fallbacks.includes(b.runtime_label)}
                  onChange={() => setFallbacks(toggleIn(fallbacks, b.runtime_label))}
                />
                {b.runtime_label}
              </label>
            ))}
        </div>
      </div>

      <div>
        <span className="text-caption text-text-tertiary">工具白名单</span>
        <div className="mt-1 space-y-1">
          {TOOL_OPTIONS.map((t) => (
            <label key={t.value} className="flex items-center gap-2 text-caption text-text-secondary">
              <input
                type="checkbox"
                checked={tools.includes(t.value)}
                onChange={() => setTools(toggleIn(tools, t.value))}
              />
              {t.label}
            </label>
          ))}
        </div>
      </div>

      <div className="grid grid-cols-2 gap-snug">
        <label className="block">
          <span className="text-caption text-text-tertiary">审批策略</span>
          <select value={approvalPolicy} onChange={(e) => setApprovalPolicy(e.target.value)} className={inputCls}>
            <option value="auto">auto（不审批）</option>
            <option value="approve_high_risk">approve_high_risk（高风险需审批）</option>
            <option value="manual">manual（人工；剔除高风险工具）</option>
          </select>
        </label>
        <label className="block">
          <span className="text-caption text-text-tertiary">Sandbox</span>
          <select value={sandbox} onChange={(e) => setSandbox(e.target.value)} className={inputCls}>
            <option value="workspace_only">workspace_only</option>
            <option value="full_access">full_access</option>
          </select>
        </label>
      </div>

      <button
        onClick={save}
        disabled={saving}
        className="w-full bg-brand-primary text-white rounded-button px-base py-tight text-body font-medium transition-all hover:bg-brand-accent active:scale-[0.98] disabled:opacity-50"
      >
        {saving ? '保存中…' : '保存配置（回写文件目录）'}
      </button>
    </section>
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
      toast.success(`已添加 Agent ${name.trim()}（保存配置后自动归档到 agents/ 目录）`);
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
