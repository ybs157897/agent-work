import { BellRing, Bot, History, MessageSquare, Plus, RefreshCw } from 'lucide-react';
import { useCallback, useEffect, useMemo, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { ApiError } from '../api/client';
import {
  createAgent,
  getDSHCatalog,
  listModels,
  listRuntimeBindings,
  listTaskSessions,
  listWorkItems,
  patchAgent,
  reloadAgentConfigs,
  resetTaskSession,
  wakeAgent,
} from '../api/endpoints';
import type { AgentPolicy, AgentProfile, AgentPresetEntry, CreateAgentResponse, ModelEntry, PermissionPresetEntry, RuntimeBinding, TaskSession, WorkItem } from '../api/types';
import {
  ConfigAvatar,
  ConfigEmptyState,
  ConfigFooter,
  ConfigFormCard,
  ConfigMain,
  ConfigPage,
  ConfigPageHeader,
  ConfigPanel,
  ConfigSection,
  ConfigSidebar,
  ConfigSidebarItem,
  ConfigSplit,
  ConfigToolbar,
  configInputCls,
  configLabelCls,
} from '../components/config-workbench';
import { Modal } from '../components/modal';
import { Drawer } from '../components/drawer';
import { PresenceDot, presenceText } from '../components/status';
import { Toggle } from '../components/toggle';
import { Button, Select } from '../components/ui';
import { useAgentsStore } from '../stores/agents.store';
import { toast } from '../stores/toast.store';
import { useWorkspaceStore } from '../stores/workspace.store';
import { isUserManagedAgent } from '../utils/agent-scope';

const ROLE_OPTIONS = [
  { value: 'pm', label: '产品 PM' },
  { value: 'architect', label: '架构师' },
  { value: 'ui', label: 'UI 设计' },
  { value: 'developer', label: '开发' },
  { value: 'reviewer', label: 'Reviewer' },
];

const ROLE_LABEL: Record<string, string> = Object.fromEntries(ROLE_OPTIONS.map((r) => [r.value, r.label]));

const REASONING_EFFORT_OPTIONS = [
  { value: 'minimal', label: '最低' },
  { value: 'low', label: '低' },
  { value: 'medium', label: '中' },
  { value: 'high', label: '高' },
  { value: 'xhigh', label: '极高' },
  { value: 'ultra', label: 'Ultra · 主动子 Agent（默认）' },
] as const;

type ReasoningEffort = (typeof REASONING_EFFORT_OPTIONS)[number]['value'];

export function normalizeReasoningEffort(value?: string): ReasoningEffort {
  const v = (value ?? 'ultra').trim().toLowerCase();
  return REASONING_EFFORT_OPTIONS.some((o) => o.value === v) ? (v as ReasoningEffort) : 'ultra';
}

const DEFAULT_RUNTIME = 'dsh_local';
const DEFAULT_DSH_MODEL_REF = 'deepseek-v4-flash';
const DEFAULT_AGENT_PRESET = 'standard';
const DEFAULT_PERMISSION_PRESET = 'workspace-write';
const HIDDEN_RUNTIMES = new Set(['mock', 'scripted']);

export function runtimeDisplayLabel(label: string): string {
  if (label === 'dsh_local') return 'deepseek harness';
  if (label === 'codex_local') return 'Codex app-server';
  if (label === 'kimi_local') return 'Kimi app-server';
  return label;
}

function normalizePreferred(value: string | undefined): string {
  if (!value || HIDDEN_RUNTIMES.has(value)) return DEFAULT_RUNTIME;
  return value;
}

function isRealRuntime(label: string): boolean {
  return !HIDDEN_RUNTIMES.has(label);
}

function isDshRuntime(preferred: string, binding: RuntimeBinding | null): boolean {
  if (preferred === 'dsh_local') return true;
  return binding?.adapter_id === 'dsh';
}

export function isCodexRuntime(preferred: string, binding: RuntimeBinding | null): boolean {
  if (preferred === 'codex_local') return true;
  return binding?.adapter_id === 'codex-appserver';
}

/** 系统 Coordinator 只在专属设置面出现；普通 Agent 配置列表必须过滤它。 */
export const isConfigurableAgent = isUserManagedAgent;

/** 组装唤醒请求体：instruction 修剪后为空则省略（服务端回退心跳模板渲染）。 */
export function buildWakePayload(taskKey: string, instruction: string): { task_key: string; instruction?: string } {
  const text = instruction.trim();
  return { task_key: taskKey, ...(text ? { instruction: text } : {}) };
}

/** 会话锚点行的展示名：display_id 优先，退回 session_ref。 */
export function sessionDisplayName(session: Pick<TaskSession, 'display_id' | 'session_ref'>): string {
  return session.display_id || session.session_ref || '（无 ref）';
}

/** 累计 input token 的紧凑展示。 */
export function formatTokenCount(n: number): string {
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`;
  if (n >= 1_000) return `${(n / 1_000).toFixed(1)}k`;
  return String(n);
}

export function createAgentNotice(agent: Pick<CreateAgentResponse, 'config_sync_pending'>, name: string): { kind: 'success' | 'warning'; message: string } {
  if (agent.config_sync_pending) {
    return { kind: 'warning', message: `已创建 Agent ${name}，外部配置同步待完成；修复条件后请重载配置` };
  }
  return { kind: 'success', message: `已添加 Agent ${name}` };
}

function defaultAgentPreset(presets: AgentPresetEntry[]): string {
  const usable = presets.filter((p) => !p.broken);
  return usable.find((p) => p.is_default)?.id ?? usable.find((p) => p.id === DEFAULT_AGENT_PRESET)?.id ?? usable[0]?.id ?? DEFAULT_AGENT_PRESET;
}

function normalizeAgentPreset(value: string | undefined, presets: AgentPresetEntry[]): string {
  const id = value?.trim() || '';
  if (id && presets.some((p) => p.id === id && !p.broken)) return id;
  return defaultAgentPreset(presets);
}

function normalizePermissionPreset(value: string | undefined, presets: PermissionPresetEntry[]): string {
  const id = value?.trim() || '';
  if (id && presets.some((p) => p.id === id)) return id;
  return presets.find((p) => p.id === DEFAULT_PERMISSION_PRESET)?.id ?? presets[0]?.id ?? DEFAULT_PERMISSION_PRESET;
}

export default function AgentsPage() {
  const agents = useAgentsStore((s) => s.agents);
  const configurableAgents = useMemo(
    () => agents.filter(isConfigurableAgent),
    [agents],
  );
  const refresh = useAgentsStore((s) => s.refresh);
  const workspace = useWorkspaceStore((s) => s.workspace);
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [addOpen, setAddOpen] = useState(false);
  const [reloading, setReloading] = useState(false);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  useEffect(() => {
    setSelectedId((prev) => {
      if (prev && configurableAgents.some((a) => a.id === prev)) return prev;
      return configurableAgents[0]?.id ?? null;
    });
  }, [configurableAgents]);

  const selected = configurableAgents.find((a) => a.id === selectedId) ?? null;

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
    <ConfigPage>
      <ConfigPageHeader
        title="智能体配置"
        subtitle="提示词、模型、执行模式与权限统一配置；由 Runtime Adapter 映射到 DSH、Codex、Kimi、Claude"
        actions={
          <>
            <Button type="button" onClick={() => void reloadConfigs()} disabled={reloading}>
              <RefreshCw className={`w-4 h-4 ${reloading ? 'animate-spin' : ''}`} />
              重载配置
            </Button>
            <Button type="button" variant="primary" onClick={() => setAddOpen(true)}>
              <Plus className="w-4 h-4" />
              添加 Agent
            </Button>
          </>
        }
      />

      <ConfigSplit>
        <ConfigSidebar
          title="智能体"
          footer={
            configurableAgents.length === 0 ? (
              <p className="text-caption text-text-tertiary text-center py-1">点击右上角添加第一个智能体</p>
            ) : undefined
          }
        >
          {configurableAgents.map((agent) => {
            const disabled = agent.availability === 'disabled';
            return (
              <ConfigSidebarItem
                key={agent.id}
                active={selected?.id === agent.id}
                disabled={disabled}
                onClick={() => setSelectedId(agent.id)}
                leading={
                  <div className="relative shrink-0">
                    <ConfigAvatar label={agent.name} tone={disabled ? 'muted' : 'brand'} />
                    {!disabled ? (
                      <span
                        className="absolute -bottom-0.5 -right-0.5 w-2 h-2 rounded-full bg-status-success ring-2 ring-surface-raised"
                        title="已启用"
                      />
                    ) : null}
                  </div>
                }
                title={agent.name}
                subtitle={`${ROLE_LABEL[agent.role] ?? agent.role}${agent.slug ? ` · ${agent.slug}` : ''}`}
              />
            );
          })}
        </ConfigSidebar>

        <ConfigMain>
          {!selected ? (
            <ConfigEmptyState icon={Bot} title="选择左侧智能体进行配置" hint="或点击「添加 Agent」创建新配置" />
          ) : (
            <AgentConfigPanel key={`${selected.id}:${selected.version}`} agent={selected} />
          )}
        </ConfigMain>
      </ConfigSplit>

      <AddAgentModal open={addOpen} onClose={() => setAddOpen(false)} />
    </ConfigPage>
  );
}

function AgentConfigPanel({ agent }: { agent: AgentProfile }) {
  const workspace = useWorkspaceStore((s) => s.workspace);
  const upsert = useAgentsStore((s) => s.upsert);
  const setAvailability = useAgentsStore((s) => s.setAvailability);
  const navigate = useNavigate();

  const [name, setName] = useState(agent.name);
  const [role, setRole] = useState(agent.role);
  const [instructions, setInstructions] = useState(agent.instructions ?? '');
  const [modelRef, setModelRef] = useState(agent.model_override?.ref ?? '');
  const [provider, setProvider] = useState(agent.model_override?.provider ?? '');
  const [model, setModel] = useState(agent.model_override?.model ?? '');
  const [reasoningEffort, setReasoningEffort] = useState<ReasoningEffort>(
    normalizeReasoningEffort(agent.model_override?.reasoning_effort),
  );
  const [preferred, setPreferred] = useState(agent.runtime_preference?.preferred ?? '');
  const [mode, setMode] = useState<'default' | 'plan'>(agent.runtime_preference?.mode ?? 'default');
  const [agentPreset, setAgentPreset] = useState(agent.runtime_preference?.agent_preset ?? DEFAULT_AGENT_PRESET);
  const [permissionPreset, setPermissionPreset] = useState(agent.policy?.permission_preset ?? DEFAULT_PERMISSION_PRESET);
  const [descriptionText, setDescriptionText] = useState(agent.skills.join('，'));
  const [bindings, setBindings] = useState<RuntimeBinding[]>([]);
  const [models, setModels] = useState<ModelEntry[]>([]);
  const [agentPresets, setAgentPresets] = useState<AgentPresetEntry[]>([]);
  const [permissionPresets, setPermissionPresets] = useState<PermissionPresetEntry[]>([]);
  const [saving, setSaving] = useState(false);
  const [toggling, setToggling] = useState(false);
  const [sessionsOpen, setSessionsOpen] = useState(false);
  const [wakeOpen, setWakeOpen] = useState(false);

  const enabled = agent.availability === 'enabled';

  const resetForm = useCallback(() => {
    setName(agent.name);
    setRole(agent.role);
    setInstructions(agent.instructions ?? '');
    setModelRef(agent.model_override?.ref ?? '');
    setProvider(agent.model_override?.provider ?? '');
    setModel(agent.model_override?.model ?? '');
    setReasoningEffort(normalizeReasoningEffort(agent.model_override?.reasoning_effort));
    setPreferred(normalizePreferred(agent.runtime_preference?.preferred));
    setMode(agent.runtime_preference?.mode ?? 'default');
    setAgentPreset(agent.runtime_preference?.agent_preset ?? DEFAULT_AGENT_PRESET);
    setPermissionPreset(agent.policy?.permission_preset ?? DEFAULT_PERMISSION_PRESET);
    setDescriptionText(agent.skills.join('，'));
  }, [agent]);

  useEffect(() => {
    resetForm();
  }, [resetForm]);

  useEffect(() => {
    if (!workspace) return;
    listRuntimeBindings(workspace.id)
      .then(({ items }) => setBindings(items ?? []))
      .catch(() => setBindings([]));
    listModels()
      .then(({ items }) => setModels(items ?? []))
      .catch(() => setModels([]));
    getDSHCatalog()
      // Go nil 切片在线上是 null：一律归一为数组，下游 useMemo 按数组消费。
      .then((catalog) => {
        setAgentPresets(catalog.agent_presets ?? []);
        setPermissionPresets(catalog.permission_presets ?? []);
      })
      .catch(() => {
        setAgentPresets([]);
        setPermissionPresets([]);
      });
  }, [workspace]);

  const selectedModel = models.find((m) => m.id === modelRef);
  const realBindings = useMemo(
    () => bindings.filter((b) => isRealRuntime(b.runtime_label)),
    [bindings],
  );

  const activeBinding = useMemo(
    () =>
      realBindings.find((b) => b.runtime_label === preferred) ??
      realBindings.find((b) => b.adapter_id === 'dsh') ??
      null,
    [realBindings, preferred],
  );

  const dshRuntime = isDshRuntime(preferred, activeBinding);
  const codexRuntime = isCodexRuntime(preferred, activeBinding);

  const selectedAgentPreset = useMemo(
    () => agentPresets.find((p) => p.id === agentPreset) ?? null,
    [agentPresets, agentPreset],
  );

  const selectedPermissionPreset = useMemo(
    () => permissionPresets.find((p) => p.id === permissionPreset) ?? null,
    [permissionPresets, permissionPreset],
  );

  useEffect(() => {
    if (agentPresets.length === 0) return;
    setAgentPreset((v) => normalizeAgentPreset(v, agentPresets));
  }, [agentPresets]);

  useEffect(() => {
    if (permissionPresets.length === 0) return;
    setPermissionPreset((v) => normalizePermissionPreset(v, permissionPresets));
  }, [permissionPresets]);

  const changeRuntime = (runtimeLabel: string) => {
    const binding = realBindings.find((item) => item.runtime_label === runtimeLabel) ?? null;
    setPreferred(runtimeLabel);
    if (isCodexRuntime(runtimeLabel, binding) || isDshRuntime(runtimeLabel, binding)) {
      const nextRef = models.some((item) => item.id === modelRef)
        ? modelRef
        : models.find((item) => item.id === DEFAULT_DSH_MODEL_REF)?.id ?? models[0]?.id ?? '';
      setModelRef(nextRef);
      setProvider('');
      setModel('');
      return;
    }
  };

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

  const save = async () => {
    setSaving(true);
    try {
      const policy: AgentPolicy = {
        permission_preset: permissionPreset,
        sandbox: permissionPreset,
        approval_policy: permissionPreset === 'danger-full-access' ? 'auto' : 'approve_high_risk',
      };
      const skills = descriptionText.trim() ? [descriptionText.trim()] : [];
      const updated = await patchAgent(agent.id, {
        name: name.trim() || agent.name,
        role,
        instructions,
        skills,
        runtime_preference: {
          preferred,
          fallbacks: [],
          mode,
          ...(dshRuntime ? { agent_preset: agentPreset } : {}),
        },
        model_override: modelRef
          ? { ref: modelRef, reasoning_effort: codexRuntime ? reasoningEffort : undefined }
          : { provider, model, reasoning_effort: codexRuntime ? reasoningEffort : undefined },
        policy,
        expected_version: agent.version,
      });
      upsert(updated);
      toast.success('配置已保存');
    } catch (err) {
      if (err instanceof ApiError && err.isVersionConflict) {
        toast.error('配置已被他人修改，请刷新后重试');
      } else {
        toast.error(err instanceof ApiError ? err.message : '保存失败');
      }
    } finally {
      setSaving(false);
    }
  };

  return (
    <ConfigPanel>
      <ConfigFormCard>
        <ConfigToolbar>
          <div className="flex items-center gap-3 min-w-0 flex-wrap">
            <div className="flex items-center gap-2 text-caption text-text-secondary">
              <PresenceDot presence={enabled ? agent.presence : 'offline'} />
              <span>{enabled ? presenceText(agent.presence) : '调度已停用'}</span>
            </div>
            {agent.slug ? (
              <span className="text-caption text-text-tertiary font-mono truncate">agents/{agent.slug}/</span>
            ) : null}
          </div>
          <div className="flex items-center gap-3 shrink-0">
            <span className="text-caption text-text-secondary">启用</span>
            <Toggle checked={enabled} onChange={onToggle} disabled={toggling} />
            <Button type="button" onClick={() => setSessionsOpen(true)} title="查看/重置该 Agent 的 Task 会话锚点">
              <History className="w-4 h-4" />
              任务会话
            </Button>
            <Button
              type="button"
              onClick={() => setWakeOpen(true)}
              disabled={!enabled}
              title={enabled ? '手动触发一次心跳唤醒（on_demand）' : '调度已停用，无法唤醒'}
            >
              <BellRing className="w-4 h-4" />
              唤醒
            </Button>
            <Button type="button" onClick={() => navigate(`/chat?agent=${agent.id}`)}>
              <MessageSquare className="w-4 h-4" />
              对话
            </Button>
          </div>
        </ConfigToolbar>

        <ConfigSection title="基本信息">
          <div className="grid grid-cols-1 md:grid-cols-2 xl:grid-cols-12 gap-x-5 gap-y-4">
            <label className="min-w-0 xl:col-span-4">
              <span className={configLabelCls}>名称</span>
              <input value={name} onChange={(e) => setName(e.target.value)} className={configInputCls} />
            </label>
            <label className="min-w-0 xl:col-span-3">
              <span className={configLabelCls}>角色</span>
              <Select value={role} onChange={(e) => setRole(e.target.value)} className={configInputCls} wrapperClassName="mt-0">
                {ROLE_OPTIONS.map((r) => (
                  <option key={r.value} value={r.value}>
                    {r.label}
                  </option>
                ))}
              </Select>
            </label>
            <label className="min-w-0 xl:col-span-5">
              <span className={configLabelCls}>模型</span>
              <Select value={modelRef} onChange={(e) => setModelRef(e.target.value)} className={configInputCls} wrapperClassName="mt-0">
                <option value="">自定义（手动填写）</option>
                {models.map((m) => (
                  <option key={m.id} value={m.id}>
                    {m.display_name || m.id}
                  </option>
                ))}
                {modelRef && !selectedModel ? (
                  <option value={modelRef}>{modelRef}（注册表条目已缺失）</option>
                ) : null}
              </Select>
            </label>
            {codexRuntime ? (
              <label className="min-w-0 xl:col-span-4">
                <span className={configLabelCls}>思考等级</span>
                <Select
                  value={reasoningEffort}
                  onChange={(e) => setReasoningEffort(e.target.value as ReasoningEffort)}
                  className={configInputCls}
                  wrapperClassName="mt-0"
                >
                  {REASONING_EFFORT_OPTIONS.map((o) => (
                    <option key={o.value} value={o.value}>
                      {o.label}
                    </option>
                  ))}
                </Select>
                <p className="mt-1 text-caption text-text-tertiary">写入 Codex config.toml 与 turn/start.effort。</p>
              </label>
            ) : null}
            <label className="min-w-0 xl:col-span-8">
              <span className={configLabelCls}>Runtime</span>
              <Select value={preferred} onChange={(e) => changeRuntime(e.target.value)} className={configInputCls} wrapperClassName="mt-0">
                {realBindings.map((b) => (
                  <option key={b.id} value={b.runtime_label}>
                    {runtimeDisplayLabel(b.runtime_label)}
                  </option>
                ))}
                {preferred && !realBindings.some((b) => b.runtime_label === preferred) ? (
                  <option value={preferred}>{runtimeDisplayLabel(preferred)}</option>
                ) : null}
              </Select>
              {codexRuntime ? (
                <p className="mt-1 text-caption text-text-tertiary">保存时把模型页配置写入 .agent-work/codex/config.toml，由 Codex harness 执行。</p>
              ) : null}
            </label>
          </div>

          {!modelRef ? (
            <div className="grid grid-cols-1 sm:grid-cols-2 gap-x-5 gap-y-4 pt-2">
              <label className="min-w-0">
                <span className={configLabelCls}>Provider 路由</span>
                <input value={provider} onChange={(e) => setProvider(e.target.value)} className={configInputCls} />
              </label>
              <label className="min-w-0">
                <span className={configLabelCls}>API 模型名</span>
                <input value={model} onChange={(e) => setModel(e.target.value)} className={configInputCls} />
              </label>
            </div>
          ) : null}

          <label className="block min-w-0 pt-2">
            <span className={configLabelCls}>描述</span>
            <input
              value={descriptionText}
              onChange={(e) => setDescriptionText(e.target.value)}
              placeholder="简要说明该智能体的职责、专长与协作方式"
              className={configInputCls}
            />
          </label>
        </ConfigSection>

        <ConfigSection title="运行策略" hint="统一策略会固化进每个 Run；切换策略后下一轮会创建新 provider 会话，避免污染旧上下文">
          <div className="grid grid-cols-1 sm:grid-cols-2 xl:grid-cols-3 gap-4">
            <label>
              <span className={configLabelCls}>执行模式</span>
              <Select value={mode} onChange={(e) => setMode(e.target.value as 'default' | 'plan')} className={configInputCls} wrapperClassName="mt-0">
                <option value="default">默认执行</option>
                <option value="plan">Plan（只分析与规划）</option>
              </Select>
              <p className="mt-1 text-caption text-text-tertiary">Codex/Kimi/Claude 使用原生模式；DSH 以只读沙箱翻译。</p>
            </label>
            <label>
              <span className={configLabelCls}>权限</span>
              <Select
                value={permissionPreset}
                onChange={(e) => setPermissionPreset(e.target.value)}
                className={configInputCls}
                wrapperClassName="mt-0"
                disabled={permissionPresets.length === 0}
              >
                {permissionPresets.map((p) => (
                  <option key={p.id} value={p.id}>{p.name}</option>
                ))}
                {permissionPreset && !permissionPresets.some((p) => p.id === permissionPreset) ? (
                  <option value={permissionPreset}>{permissionPreset}（未知预设）</option>
                ) : null}
              </Select>
              {selectedPermissionPreset?.description ? (
                <p className="mt-1 text-caption text-text-tertiary">{selectedPermissionPreset.description}</p>
              ) : null}
            </label>
            {dshRuntime ? (
              <label>
                <span className={configLabelCls}>DSH Agent Preset</span>
                <Select
                  value={agentPreset}
                  onChange={(e) => setAgentPreset(e.target.value)}
                  className={configInputCls}
                  wrapperClassName="mt-0"
                  disabled={agentPresets.length === 0}
                >
                  {agentPresets.map((p) => (
                    <option key={p.id} value={p.id} disabled={!!p.broken}>
                      {p.name || p.id}
                      {p.broken ? '（不可用）' : ''}
                    </option>
                  ))}
                  {agentPreset && !agentPresets.some((p) => p.id === agentPreset) ? (
                    <option value={agentPreset}>{agentPreset}（未在目录中发现）</option>
                  ) : null}
                </Select>
                {selectedAgentPreset?.description ? (
                  <p className="mt-1 text-caption text-text-tertiary">{selectedAgentPreset.description}</p>
                ) : null}
              </label>
            ) : null}
          </div>
        </ConfigSection>

        <ConfigSection title="系统提示词">
          <label className="block">
            <span className={configLabelCls}>prompt.md</span>
            <textarea
              value={instructions}
              onChange={(e) => setInstructions(e.target.value)}
              rows={16}
              className={`${configInputCls} resize-y font-mono text-caption leading-relaxed min-h-[280px]`}
              placeholder="角色职责、输出约束、协作约定…"
            />
          </label>
        </ConfigSection>
      </ConfigFormCard>

      <ConfigFooter onCancel={resetForm} onSave={() => void save()} saving={saving} />

      <TaskSessionsModal open={sessionsOpen} onClose={() => setSessionsOpen(false)} agent={agent} />
      <WakeAgentModal open={wakeOpen} onClose={() => setWakeOpen(false)} agent={agent} />
    </ConfigPanel>
  );
}

function AddAgentModal({ open, onClose }: { open: boolean; onClose: () => void }) {
  const workspace = useWorkspaceStore((s) => s.workspace);
  const refresh = useAgentsStore((s) => s.refresh);
  const [name, setName] = useState('');
  const [role, setRole] = useState('developer');
  const [description, setDescription] = useState('');
  const [bindings, setBindings] = useState<RuntimeBinding[]>([]);
  const [runtimeLabel, setRuntimeLabel] = useState(DEFAULT_RUNTIME);
  const [submitting, setSubmitting] = useState(false);

  useEffect(() => {
    if (!open || !workspace) return;
    listRuntimeBindings(workspace.id)
      .then(({ items }) => {
        const available = (items ?? []).filter((item) => isRealRuntime(item.runtime_label));
        setBindings(available);
        setRuntimeLabel((current) =>
          available.some((item) => item.runtime_label === current)
            ? current
            : available.find((item) => item.runtime_label === DEFAULT_RUNTIME)?.runtime_label ?? available[0]?.runtime_label ?? '',
        );
      })
      .catch(() => setBindings([]));
  }, [open, workspace]);

  const submit = async () => {
    if (!workspace || !name.trim()) return;
    setSubmitting(true);
    try {
      const created = await createAgent(workspace.id, {
        name: name.trim(),
        role,
        skills: description.trim() ? [description.trim()] : [],
        runtime_preference: {
          preferred: runtimeLabel,
          fallbacks: [],
          mode: 'default',
          ...(runtimeLabel === 'dsh_local' ? { agent_preset: DEFAULT_AGENT_PRESET } : {}),
        },
      });
      await refresh();
      const notice = createAgentNotice(created, name.trim());
      if (notice.kind === 'warning') toast.warning(notice.message);
      else toast.success(notice.message);
      setName('');
      setDescription('');
      onClose();
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : '创建失败');
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Drawer open={open} onClose={onClose} title="添加 Agent" width={480}>
      <div className="p-comfortable">
        <div className="space-y-base">
        <label className="block">
          <span className={configLabelCls}>名称</span>
          <input
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="例如 Forge"
            className={configInputCls}
          />
        </label>
        <label className="block">
          <span className={configLabelCls}>角色</span>
          <Select value={role} onChange={(e) => setRole(e.target.value)} className={configInputCls} wrapperClassName="mt-0">
            {ROLE_OPTIONS.map((r) => (
              <option key={r.value} value={r.value}>
                {r.label}
              </option>
            ))}
          </Select>
        </label>
        <label className="block">
          <span className={configLabelCls}>描述</span>
          <input
            value={description}
            onChange={(e) => setDescription(e.target.value)}
            placeholder="简要说明职责与专长"
            className={configInputCls}
          />
        </label>
        <label className="block">
          <span className={configLabelCls}>底层 Runtime</span>
          <Select value={runtimeLabel} onChange={(e) => setRuntimeLabel(e.target.value)} className={configInputCls} wrapperClassName="mt-0">
            {bindings.map((binding) => (
              <option key={binding.id} value={binding.runtime_label}>
                {runtimeDisplayLabel(binding.runtime_label)}
              </option>
            ))}
          </Select>
          <p className="mt-1 text-caption text-text-tertiary">
            {runtimeLabel === 'codex_local'
              ? '使用本机 Codex app-server、Codex 登录态和线程历史。'
              : '使用 DeepSeek Harness SDK Runtime。'}
          </p>
        </label>
        <div className="flex justify-end gap-snug pt-tight">
          <Button type="button" onClick={onClose}>
            取消
          </Button>
          <Button
            type="button"
            variant="primary"
            onClick={() => void submit()}
            disabled={!name.trim() || !runtimeLabel || submitting}
          >
            {submitting ? '创建中…' : '创建'}
          </Button>
        </div>
      </div>
      </div>
    </Drawer>
  );
}

/** Task 会话锚点面板：只管理 Task 的跨 Run 会话；Chat 会话不进入该投影。 */
function TaskSessionsModal({ open, onClose, agent }: { open: boolean; onClose: () => void; agent: AgentProfile }) {
  const [items, setItems] = useState<TaskSession[] | null>(null);
  const [resetting, setResetting] = useState<string | null>(null);

  useEffect(() => {
    if (!open) return;
    let cancelled = false;
    listTaskSessions(agent.id)
      .then(({ items }) => {
        if (!cancelled) setItems(items ?? []);
      })
      .catch((err) => {
        if (!cancelled) {
          setItems([]);
          toast.error(err instanceof ApiError ? err.message : '会话列表加载失败');
        }
      });
    return () => {
      cancelled = true;
    };
  }, [open, agent.id]);

  const onReset = async (session: TaskSession) => {
    if (!window.confirm(`重置任务 ${session.task_key} 的会话锚点？该任务下一轮运行将开启全新会话，历史不再续接。`)) return;
    setResetting(session.id);
    try {
      await resetTaskSession(agent.id, { task_key: session.task_key, adapter_id: session.adapter_id });
      const { items } = await listTaskSessions(agent.id);
      setItems(items);
      toast.success('任务会话锚点已重置');
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : '重置失败');
    } finally {
      setResetting(null);
    }
  };

  return (
    <Drawer open={open} onClose={onClose} title={`${agent.name} 的任务会话锚点`} width={560}>
      <div className="p-comfortable">
        {items === null ? (
          <p className="text-caption text-text-tertiary py-comfortable text-center">加载中…</p>
        ) : items.length === 0 ? (
          <p className="text-caption text-text-tertiary py-comfortable text-center">
            暂无活跃任务会话锚点（重置后的墓碑不展示）
          </p>
        ) : (
          <div className="space-y-snug max-h-[50vh] overflow-y-auto">
            {items.map((s) => (
              <div
                key={s.id}
                className="flex items-center gap-snug rounded-lg border border-border-subtle px-3 py-2"
              >
                <div className="min-w-0 flex-1">
                  <div className="flex items-center gap-2 text-caption">
                    <span className="font-mono text-text-primary truncate" title={s.task_key}>
                      {s.task_key}
                    </span>
                    <span className="shrink-0 rounded bg-surface-base px-1.5 py-0.5 text-text-tertiary">
                      {s.adapter_id}
                    </span>
                  </div>
                  <div className="mt-0.5 flex flex-wrap items-center gap-x-3 gap-y-0.5 text-caption text-text-tertiary">
                    <span className="font-mono truncate max-w-[220px]" title={s.session_ref ?? ''}>
                      {sessionDisplayName(s)}
                    </span>
                    <span>{s.runs_count} 轮</span>
                    <span>{formatTokenCount(s.input_tokens_cum)} tokens</span>
                    <span>{new Date(s.updated_at).toLocaleString()}</span>
                  </div>
                </div>
                <Button
                  type="button"
                  size="sm"
                  onClick={() => void onReset(s)}
                  disabled={resetting === s.id}
                  className="shrink-0 text-caption"
                >
                  {resetting === s.id ? '重置中…' : '重置'}
                </Button>
              </div>
            ))}
          </div>
        )}
        <p className="mt-comfortable text-caption text-text-tertiary">
          此处只管理 Task 会话；重置写入墓碑后，该任务下一轮运行将开启全新会话。Chat 会话不在此处展示或重置。
        </p>
      </div>
    </Drawer>
  );
}

/** 手动唤醒（on_demand）：锚定一个未完成的 Work Item，可附本轮指令。 */
function WakeAgentModal({ open, onClose, agent }: { open: boolean; onClose: () => void; agent: AgentProfile }) {
  const workspace = useWorkspaceStore((s) => s.workspace);
  const [tasks, setTasks] = useState<WorkItem[]>([]);
  const [taskKey, setTaskKey] = useState('');
  const [instruction, setInstruction] = useState('');
  const [submitting, setSubmitting] = useState(false);

  useEffect(() => {
    if (!open || !workspace) return;
    let cancelled = false;
    listWorkItems(workspace.id, { record_kind: 'task' })
      .then(({ items }) => {
        if (cancelled) return;
        const active = (items ?? []).filter((t) =>
          t.record_kind === 'task' && t.status !== 'completed' && t.status !== 'cancelled');
        setTasks(active);
        setTaskKey((cur) => (active.some((t) => t.id === cur) ? cur : active[0]?.id ?? ''));
      })
      .catch(() => {
        if (!cancelled) setTasks([]);
      });
    return () => {
      cancelled = true;
    };
  }, [open, workspace]);

  const submit = async () => {
    if (!taskKey) return;
    setSubmitting(true);
    try {
      const res = await wakeAgent(agent.id, buildWakePayload(taskKey, instruction));
      toast.success(`唤醒已入队（${res.status}），调度循环将在下一 tick 消费`);
      setInstruction('');
      onClose();
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : '唤醒失败');
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Modal open={open} onClose={onClose} title={`唤醒 ${agent.name}`} width={480}>
      <div className="space-y-base">
        <label className="block">
          <span className={configLabelCls}>锚定任务</span>
          <Select value={taskKey} onChange={(e) => setTaskKey(e.target.value)} className={configInputCls} wrapperClassName="mt-0">
            {tasks.length === 0 ? <option value="">（无未完成的工作项）</option> : null}
            {tasks.map((t) => (
              <option key={t.id} value={t.id}>
                {t.title || t.id}（{t.status}）
              </option>
            ))}
          </Select>
        </label>
        <label className="block">
          <span className={configLabelCls}>本轮指令（可选）</span>
          <textarea
            value={instruction}
            onChange={(e) => setInstruction(e.target.value)}
            rows={4}
            placeholder="留空则使用 Agent 的心跳模板渲染"
            className={`${configInputCls} resize-y`}
          />
        </label>
        <p className="text-caption text-text-tertiary">
          若该 Agent 未开启「手动唤醒（wake_on_demand）」，服务端将返回 422；若目标任务已有活跃 Run，本次唤醒会被合并并转发指令。
        </p>
        <div className="flex justify-end gap-snug pt-tight">
          <Button type="button" onClick={onClose}>
            取消
          </Button>
          <Button
            type="button"
            variant="primary"
            onClick={() => void submit()}
            disabled={!taskKey || submitting}
          >
            {submitting ? '入队中…' : '唤醒'}
          </Button>
        </div>
      </div>
    </Modal>
  );
}
