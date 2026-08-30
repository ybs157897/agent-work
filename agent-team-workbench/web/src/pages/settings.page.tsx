import { CheckCircle2, LockKeyhole, Pencil, Plus, Radar, RefreshCw, ShieldCheck } from 'lucide-react';
import type React from 'react';
import { useCallback, useEffect, useState } from 'react';
import { ApiError } from '../api/client';
import {
  createRuntimeBinding,
  getCoordinatorConfig,
  listRuntimeBindings,
  patchCoordinatorConfig,
  patchRuntimeBinding,
  patchWorkspace,
  probeRuntimeBinding,
} from '../api/endpoints';
import type { CoordinatorConfig, CoordinatorRuntime, ProbeResult, RuntimeBinding } from '../api/types';
import { Drawer } from '../components/drawer';
import { InkBentoGrid, InkBentoItem } from '../components/ink/ink-bento';
import { Toggle } from '../components/toggle';
import { Button, Card, EmptyState, Input, Select, Skeleton } from '../components/ui';
import { useChatPreferencesStore } from '../stores/chat-preferences.store';
import { toast } from '../stores/toast.store';
import { useWorkspaceStore } from '../stores/workspace.store';
import { formatDateTime } from '../utils/format';

/** 设置页：Workspace 信息、健康投影、RuntimeBinding 列表与 probe（协议 §5.2）。 */
export default function SettingsPage() {
  const workspace = useWorkspaceStore((s) => s.workspace);
  const me = useWorkspaceStore((s) => s.me);
  const health = useWorkspaceStore((s) => s.health);
  const sseStatus = useWorkspaceStore((s) => s.sseStatus);
  const eventCursor = useWorkspaceStore((s) => s.eventCursor);
  const [editingWs, setEditingWs] = useState(false);

  return (
    <main className="page-shell">
      <header className="page-header">
        <h2 className="page-title">设置</h2>
      </header>

      <InkBentoGrid className="grid-cols-1 gap-snug md:auto-rows-auto md:grid-cols-2">
        <InkBentoItem
          className="!justify-start !space-y-snug p-comfortable"
          title={
            <div className="flex items-start justify-between gap-snug">
              <div>
                <p className="text-caption uppercase tracking-widest text-text-tertiary">卷宗 · Workspace</p>
                <h3 className="mt-1 font-display text-h3 text-text-primary">{workspace?.name ?? '未加载'}</h3>
              </div>
              <Button size="sm" onClick={() => setEditingWs(true)}>
                <Pencil className="w-3.5 h-3.5" />
                编辑
              </Button>
            </div>
          }
          description={
            <dl className="grid grid-cols-2 gap-x-comfortable gap-y-snug text-body">
              <Field label="时区" value={workspace?.timezone} />
              <Field label="版本" value={workspace ? String(workspace.version) : undefined} />
              <Field label="Workspace ID" value={workspace?.id} mono />
            </dl>
          }
        />

        <InkBentoItem
          className="!justify-start !space-y-snug p-comfortable"
          title={
            <div className="flex items-start justify-between gap-snug">
              <div>
                <p className="text-caption uppercase tracking-widest text-text-tertiary">脉象 · System Health</p>
                <h3 className="mt-1 font-display text-h3 text-text-primary">系统健康</h3>
              </div>
              <span className="text-caption text-text-tertiary tabular-nums">游标 {eventCursor}</span>
            </div>
          }
          description={
            <div className="grid grid-cols-3 gap-snug text-body">
              <HealthMetric label="控制平面">
                <span className="inline-flex items-center gap-1.5 text-text-primary">
                  <span className={`h-2 w-2 rounded-full ${health?.control_plane === 'healthy' ? 'bg-status-success' : 'bg-status-error'}`} />
                  {health?.control_plane ?? '未知'}
                </span>
              </HealthMetric>
              <HealthMetric label="Runner">
                <span className="text-text-primary">
                  {health?.runners?.length ? `${health.runners.length} 个已连接` : '内置 Mock'}
                </span>
              </HealthMetric>
              <HealthMetric label="SSE 事件流">
                <span className="text-text-primary">
                  {sseStatus === 'online' ? '在线' : sseStatus === 'connecting' ? '连接中' : '重连中'}
                </span>
              </HealthMetric>
            </div>
          }
        />
      </InkBentoGrid>

      {workspace && <WorkspaceEditModal open={editingWs} onClose={() => setEditingWs(false)} />}

      <RuntimeBindingsSection workspaceId={workspace?.id} />

      <CoordinatorSettingsSection workspaceId={workspace?.id} />

      <ChatDisplayPreferencesSection />

      <Card padded className="!p-base">
        <div className="mb-snug flex items-center justify-between">
          <h3 className="text-h3 text-text-primary">当前用户</h3>
          <span className="text-caption text-text-tertiary">访问身份</span>
        </div>
        <dl className="grid grid-cols-2 gap-x-comfortable gap-y-snug max-w-2xl text-body md:grid-cols-4">
          <Field label="用户" value={me?.name} />
          <Field label="角色" value={me?.role} />
          <Field label="用户 ID" value={me?.user_id} mono />
          <Field label="会话时间" value={formatDateTime(new Date().toISOString())} />
        </dl>
      </Card>
    </main>
  );
}

const COORDINATOR_RUNTIME_OPTIONS: { value: CoordinatorRuntime; label: string }[] = [
  { value: 'codex_local', label: 'Codex' },
  { value: 'kimi_local', label: 'Kimi' },
];

const COORDINATOR_REASONING_OPTIONS = [
  { value: 'minimal', label: '最低' },
  { value: 'low', label: '低' },
  { value: 'medium', label: '中' },
  { value: 'high', label: '高' },
  { value: 'xhigh', label: '极高' },
];

function coordinatorRuntime(config: CoordinatorConfig): CoordinatorRuntime {
  return config.runtime_label || 'codex_local';
}

function coordinatorModel(config: CoordinatorConfig): string {
  // "mock" is the internal fallback for installations that do not yet have
  // Codex/Kimi. It must not leak into the editable model-ref field: after the
  // user selects a production runtime, an empty value means "follow runtime
  // default", while the literal mock ref would be rejected by the registry.
  if (config.runtime_label === 'mock' && config.model_ref === 'mock') return '';
  return config.model_ref;
}

/**
 * 系统 Coordinator 设置：只开放底座选择，绝不渲染提示词输入框。
 * 服务端仍是最终权限边界；前端不向 PATCH 请求构造 instructions/prompt 字段。
 */
export function CoordinatorSettingsSection({ workspaceId }: { workspaceId?: string }) {
  const [config, setConfig] = useState<CoordinatorConfig | null>(null);
  const [loading, setLoading] = useState(false);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | undefined>();
  const [runtime, setRuntime] = useState<CoordinatorRuntime>('codex_local');
  const [model, setModel] = useState('');
  const [reasoning, setReasoning] = useState('medium');
  const [fallbackRuntime, setFallbackRuntime] = useState<CoordinatorRuntime | ''>('kimi_local');
  const [fallbackModel, setFallbackModel] = useState('');

  const load = useCallback(async () => {
    if (!workspaceId) return;
    setLoading(true);
    setError(undefined);
    try {
      const next = await getCoordinatorConfig(workspaceId);
      setConfig(next);
      setRuntime(coordinatorRuntime(next));
      setModel(coordinatorModel(next));
      setReasoning(next.reasoning_effort || 'medium');
      setFallbackRuntime(next.fallback_runtime_label ?? '');
      setFallbackModel(next.fallback_model_ref ?? '');
    } catch (loadError) {
      setError(loadError instanceof ApiError ? loadError.message : 'Coordinator 配置加载失败');
    } finally {
      setLoading(false);
    }
  }, [workspaceId]);

  useEffect(() => {
    void load();
  }, [load]);

  const save = async () => {
    if (!workspaceId || !config) return;
    setSaving(true);
    setError(undefined);
    try {
      const next = await patchCoordinatorConfig(workspaceId, {
        runtime_label: runtime,
        model_ref: model.trim(),
        reasoning_effort: reasoning,
        fallback_runtime_label: fallbackRuntime,
        fallback_model_ref: fallbackModel.trim(),
        expected_version: config.version,
      });
      setConfig(next);
      setRuntime(coordinatorRuntime(next));
      setModel(coordinatorModel(next));
      setReasoning(next.reasoning_effort || reasoning);
      setFallbackRuntime(next.fallback_runtime_label ?? '');
      setFallbackModel(next.fallback_model_ref ?? '');
      toast.success('Coordinator 底座配置已更新');
    } catch (saveError) {
      if (saveError instanceof ApiError && saveError.isVersionConflict) {
        setError('配置已被其他窗口修改，请刷新后重试');
      } else {
        setError(saveError instanceof ApiError ? saveError.message : 'Coordinator 配置保存失败');
      }
    } finally {
      setSaving(false);
    }
  };

  return (
    <Card padded className="!p-base">
      <div className="flex flex-wrap items-start justify-between gap-snug">
        <div>
          <p className="text-caption uppercase tracking-widest text-text-tertiary">任务控制线 · System Agent</p>
          <h3 className="mt-1 text-h3 text-text-primary">Task Coordinator</h3>
          <p className="mt-1 max-w-2xl text-body text-text-secondary">任务创建后由它自动接取、拆分、选 Agent、重试与收口。</p>
        </div>
        <div className="inline-flex items-center gap-tight rounded-full border border-status-success/20 bg-status-success/10 px-snug py-micro text-caption text-status-success">
          <ShieldCheck className="h-3.5 w-3.5" aria-hidden />
          系统默认 Agent
        </div>
      </div>

      {loading && <div className="mt-snug space-y-tight"><Skeleton className="h-12 w-full" /><Skeleton className="h-12 w-2/3" /></div>}
      {!loading && error && (
        <div className="mt-snug flex items-center justify-between gap-snug rounded-button border border-status-error/30 bg-status-error/5 px-snug py-tight" role="alert">
          <span className="text-body text-status-error">{error}</span>
          <Button type="button" size="sm" onClick={() => void load()}><RefreshCw className="h-3.5 w-3.5" aria-hidden />重试</Button>
        </div>
      )}
      {!loading && config && (
        <div className="mt-snug space-y-base">
          <div className="grid gap-snug md:grid-cols-2">
            <label className="block">
              <span className="text-body text-text-secondary">主运行时</span>
              <Select value={runtime} onChange={(event) => {
                const nextRuntime = event.target.value as CoordinatorRuntime;
                setRuntime(nextRuntime);
                if (fallbackRuntime === nextRuntime) setFallbackRuntime('');
              }}>
                {runtime === 'mock' && <option value="mock" disabled>未配置 Codex/Kimi（当前为测试 Runtime）</option>}
                {COORDINATOR_RUNTIME_OPTIONS.map((option) => <option key={option.value} value={option.value}>{option.label}</option>)}
              </Select>
              {runtime === 'mock' && <span className="mt-1 block text-caption text-status-warning">请选择 Codex 或 Kimi 后再保存；mock 不允许作为生产 Coordinator 底座。</span>}
            </label>
            <label className="block">
              <span className="text-body text-text-secondary">模型引用</span>
              <Input value={model} onChange={(event) => setModel(event.target.value)} placeholder="留空跟随底座默认" className="font-mono" />
            </label>
            <label className="block">
              <span className="text-body text-text-secondary">推理强度</span>
              <Select value={reasoning} onChange={(event) => setReasoning(event.target.value)}>
                {COORDINATOR_REASONING_OPTIONS.map((option) => <option key={option.value} value={option.value}>{option.label}</option>)}
              </Select>
            </label>
            <label className="block">
              <span className="text-body text-text-secondary">备用运行时</span>
              <Select value={fallbackRuntime} onChange={(event) => setFallbackRuntime(event.target.value as CoordinatorRuntime | '')}>
                <option value="">不设置备用</option>
                {COORDINATOR_RUNTIME_OPTIONS.filter((option) => option.value !== runtime).map((option) => <option key={option.value} value={option.value}>{option.label}</option>)}
              </Select>
            </label>
            <label className="block">
              <span className="text-body text-text-secondary">备用模型引用</span>
              <Input value={fallbackModel} onChange={(event) => setFallbackModel(event.target.value)} placeholder="留空跟随备用底座默认" className="font-mono" />
            </label>
          </div>

          <div className="grid gap-snug border-t border-border-subtle pt-snug md:grid-cols-[minmax(0,1fr)_auto] md:items-center">
            <div className="flex items-start gap-2">
              <LockKeyhole className="mt-0.5 h-4 w-4 shrink-0 text-text-tertiary" aria-hidden />
              <div>
                <p className="text-body font-medium text-text-primary">内置提示词已锁定</p>
                <p className="mt-0.5 text-caption text-text-secondary">版本 {config.prompt_version || 'system-default'} · 不提供编辑入口，API 也拒绝修改。</p>
              </div>
            </div>
            <Button type="button" variant="primary" onClick={() => void save()} disabled={saving || runtime === 'mock'}>
              {saving ? '保存中…' : '保存底座配置'}
            </Button>
          </div>
          <p className="flex items-center gap-tight text-caption text-text-tertiary"><CheckCircle2 className="h-3.5 w-3.5 text-status-success" aria-hidden />Task 的每个 Coordinator 会话独立持久化，Chat 不会进入这条控制线。</p>
        </div>
      )}
    </Card>
  );
}

function ChatDisplayPreferencesSection() {
  const showReasoning = useChatPreferencesStore((state) => state.showReasoning);
  const groupExploreTools = useChatPreferencesStore((state) => state.groupExploreTools);
  const groupTerminalTools = useChatPreferencesStore((state) => state.groupTerminalTools);
  const groupChangesTools = useChatPreferencesStore((state) => state.groupChangesTools);
  const setPreference = useChatPreferencesStore((state) => state.setPreference);

  return (
    <Card padded className="!p-base">
      <div className="mb-snug">
        <p className="text-caption uppercase tracking-widest text-text-tertiary">对话 · ZCode</p>
        <h3 className="mt-1 text-h3 text-text-primary">过程展示</h3>
        <p className="mt-1 text-caption text-text-tertiary">控制思考可见性与工具的展示层分组；不改变底层事件。</p>
      </div>
      <div className="divide-y divide-border-subtle rounded-card border border-border-subtle bg-surface-base">
        <PreferenceRow
          label="显示全部思考"
          description="关闭后每个 Run 只保留第一段思考，与 ZCode messageStreamShowReasoning 一致。"
          checked={showReasoning}
          onChange={(checked) => setPreference('showReasoning', checked)}
        />
        <PreferenceRow
          label="合并探索工具"
          description="把连续 Read/Search 调用收进 Explore 摘要。"
          checked={groupExploreTools}
          onChange={(checked) => setPreference('groupExploreTools', checked)}
        />
        <PreferenceRow
          label="合并执行工具"
          description="把连续 Bash/Code 调用收进 Execute 摘要。"
          checked={groupTerminalTools}
          onChange={(checked) => setPreference('groupTerminalTools', checked)}
        />
        <PreferenceRow
          label="合并文件变更"
          description="把连续 Write/Edit 调用收进 Changes 摘要；ZCode 默认关闭。"
          checked={groupChangesTools}
          onChange={(checked) => setPreference('groupChangesTools', checked)}
        />
      </div>
    </Card>
  );
}

function PreferenceRow({
  label,
  description,
  checked,
  onChange,
}: {
  label: string;
  description: string;
  checked: boolean;
  onChange: (checked: boolean) => void;
}) {
  return (
    <div className="flex min-h-14 items-center justify-between gap-comfortable px-snug py-base">
      <div className="min-w-0">
        <p className="text-body font-medium text-text-primary">{label}</p>
        <p className="mt-0.5 text-caption text-text-tertiary">{description}</p>
      </div>
      <Toggle checked={checked} onChange={onChange} ariaLabel={label} />
    </div>
  );
}

function RuntimeBindingsSection({ workspaceId }: { workspaceId?: string }) {
  const [bindings, setBindings] = useState<RuntimeBinding[] | null>(null);
  const [probing, setProbing] = useState<string | null>(null);
  const [probeResults, setProbeResults] = useState<Record<string, ProbeResult>>({});
  const [editing, setEditing] = useState<RuntimeBinding | 'new' | null>(null);

  const load = () => {
    if (!workspaceId) return;
    listRuntimeBindings(workspaceId)
      .then(({ items }) => setBindings(items))
      .catch((err) => toast.error(err instanceof ApiError ? err.message : '加载 Runtime 绑定失败'));
  };

  useEffect(load, [workspaceId]);

  const probe = async (id: string) => {
    setProbing(id);
    try {
      const result = await probeRuntimeBinding(id);
      setProbeResults((s) => ({ ...s, [id]: result }));
      toast.success(result.ok ? '探测成功' : `探测失败：${result.error ?? '未知错误'}`);
      load();
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : '探测失败');
    } finally {
      setProbing(null);
    }
  };

  return (
    <Card padded className="!p-base">
      <div className="flex items-center justify-between mb-snug">
        <div>
          <p className="text-caption uppercase tracking-widest text-text-tertiary">注册表 · Runtime</p>
          <h3 className="mt-1 text-h3 text-text-primary">运行时绑定</h3>
        </div>
        <div className="flex items-center gap-2">
          <Button onClick={load}>
            <RefreshCw className="w-4 h-4" />
            刷新
          </Button>
          <Button variant="ghost" onClick={() => setEditing('new')}>
            <Plus className="w-4 h-4" />
            添加绑定
          </Button>
        </div>
      </div>

      {bindings === null ? (
        <div className="space-y-3">
          <Skeleton className="h-24 w-full" />
          <Skeleton className="h-24 w-full" />
        </div>
      ) : bindings.length === 0 ? (
        <EmptyState
          icon={<Radar className="w-5 h-5" />}
          title="暂无 Runtime 绑定"
          description="添加一个绑定以接入真实 Runtime 适配器"
          action={
            <Button variant="ghost" size="sm" onClick={() => setEditing('new')}>
              <Plus className="w-3.5 h-3.5" />
              添加绑定
            </Button>
          }
        />
      ) : (
        <div className="divide-y divide-border-subtle rounded-card border border-border-subtle bg-surface-base">
          {bindings.map((b) => {
            const probeResult = probeResults[b.id];
            return (
              <div key={b.id} className="grid gap-snug px-snug py-base md:grid-cols-[minmax(12rem,1.25fr)_minmax(14rem,1.5fr)_auto] md:items-center">
                <div className="min-w-0">
                  <div className="flex items-center gap-2">
                    <span className="truncate font-medium text-body text-text-primary">{b.runtime_label}</span>
                    <span className={`inline-flex shrink-0 items-center rounded-full px-2 py-0.5 text-caption font-medium ${b.status === 'ready' ? 'bg-status-success/10 text-status-success' : 'bg-surface-raised text-text-secondary'}`}>
                      {b.status}
                    </span>
                  </div>
                  <p className="mt-1 truncate font-mono text-caption text-text-tertiary">
                      {b.adapter_id}@{b.adapter_version || '-'}
                      {b.provider_version ? ` · provider ${b.provider_version}` : ''}
                  </p>
                </div>
                <div className="min-w-0 text-caption text-text-secondary">
                  <p className="truncate">{b.provider} / {b.model}</p>
                  {b.credential_ref && <p className="mt-1 truncate text-text-tertiary">凭据引用 · {b.credential_ref}</p>}
                </div>
                <div className="flex items-center justify-start gap-tight md:justify-end">
                  <Button size="sm" onClick={() => setEditing(b)}>
                    <Pencil className="w-3.5 h-3.5" />
                    编辑
                  </Button>
                  <Button size="sm" variant="ghost" onClick={() => void probe(b.id)} disabled={probing !== null}>
                    <Radar className="w-3.5 h-3.5" />
                    {probing === b.id ? '探测中…' : '探测'}
                  </Button>
                </div>
                <div className="md:col-span-3 flex flex-wrap gap-1.5">
                  {Object.entries(b.capabilities ?? {}).map(([cap, level]) => (
                    <span
                      key={cap}
                      className={`inline-flex items-center rounded-sm border px-2 py-0.5 text-caption ${
                        level === 'supported' || level === 'provider_native'
                          ? 'bg-brand-primary/10 text-brand-accent border-brand-primary/20'
                          : level === 'unavailable'
                            ? 'bg-surface-raised text-text-tertiary border-border-subtle line-through'
                            : 'bg-status-warning/10 text-status-warning border-status-warning/20'
                      }`}
                      title={level}
                    >
                      {cap}
                    </span>
                  ))}
                </div>
                {probeResult && (
                  <p className={`md:col-span-3 text-caption ${probeResult.ok ? 'text-status-success' : 'text-status-error'}`}>
                    最近探测：{probeResult.ok ? '可用' : '失败'}{probeResult.error ? ` · ${probeResult.error}` : ''}
                  </p>
                )}
              </div>
            );
          })}
        </div>
      )}

      {editing !== null && workspaceId && (
        <BindingEditModal
          workspaceId={workspaceId}
          binding={editing === 'new' ? null : editing}
          onClose={() => setEditing(null)}
          onSaved={() => {
            setEditing(null);
            load();
          }}
        />
      )}
    </Card>
  );
}

/** Workspace 名称/时区编辑（乐观锁；冲突提示刷新）。 */
function WorkspaceEditModal({ open, onClose }: { open: boolean; onClose: () => void }) {
  const workspace = useWorkspaceStore((s) => s.workspace);
  const setWorkspace = useWorkspaceStore((s) => s.setWorkspace);
  const [name, setName] = useState(workspace?.name ?? '');
  const [timezone, setTimezone] = useState(workspace?.timezone ?? '');
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    if (open) {
      setName(workspace?.name ?? '');
      setTimezone(workspace?.timezone ?? '');
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open]);

  const save = async () => {
    if (!workspace) return;
    setSaving(true);
    try {
      const updated = await patchWorkspace(workspace.id, {
        name: name.trim(),
        timezone: timezone.trim(),
        expected_version: workspace.version,
      });
      setWorkspace(updated);
      toast.success('Workspace 已更新');
      onClose();
    } catch (err) {
      if (err instanceof ApiError && err.isVersionConflict) {
        toast.error('已被他人修改，请刷新后重试');
      } else {
        toast.error(err instanceof ApiError ? err.message : '保存失败');
      }
    } finally {
      setSaving(false);
    }
  };

  return (
    <Drawer open={open} onClose={onClose} title="编辑 Workspace" width={480}>
      <div className="p-comfortable">
        <div className="space-y-base">
        <label className="block">
          <span className="text-body text-text-secondary">名称</span>
          <Input value={name} onChange={(e) => setName(e.target.value)} />
        </label>
        <label className="block">
          <span className="text-body text-text-secondary">时区</span>
          <Input
            value={timezone}
            onChange={(e) => setTimezone(e.target.value)}
            placeholder="UTC / Asia/Shanghai"
          />
        </label>
        <div className="flex justify-end gap-snug pt-tight">
          <Button onClick={onClose}>取消</Button>
          <Button variant="primary" onClick={() => void save()} disabled={!name.trim() || saving}>
            {saving ? '保存中…' : '保存'}
          </Button>
        </div>
      </div>
      </div>
    </Drawer>
  );
}

/** Runtime 绑定新建/编辑；凭据只存引用（credential_ref），不接收明文。 */
function BindingEditModal({
  workspaceId,
  binding,
  onClose,
  onSaved,
}: {
  workspaceId: string;
  binding: RuntimeBinding | null;
  onClose: () => void;
  onSaved: () => void;
}) {
  const isNew = binding === null;
  const [label, setLabel] = useState(binding?.runtime_label ?? '');
  const [adapterId, setAdapterId] = useState(binding?.adapter_id ?? '');
  const [provider, setProvider] = useState(binding?.provider ?? '');
  const [model, setModel] = useState(binding?.model ?? '');
  const [credentialRef, setCredentialRef] = useState(binding?.credential_ref ?? '');
  const [saving, setSaving] = useState(false);

  const submit = async () => {
    setSaving(true);
    try {
      if (isNew) {
        await createRuntimeBinding(workspaceId, {
          runtime_label: label.trim(),
          adapter_id: adapterId.trim(),
          provider: provider.trim(),
          model: model.trim(),
          credential_ref: credentialRef.trim(),
        });
        toast.success(`已添加绑定 ${label.trim()}`);
      } else {
        await patchRuntimeBinding(binding.id, {
          provider: provider.trim(),
          model: model.trim(),
          credential_ref: credentialRef.trim(),
          expected_version: binding.version,
        });
        toast.success(`已更新绑定 ${binding.runtime_label}`);
      }
      onSaved();
    } catch (err) {
      if (err instanceof ApiError && err.isVersionConflict) {
        toast.error('绑定已被他人修改，请刷新后重试');
      } else {
        toast.error(err instanceof ApiError ? err.message : '保存失败');
      }
    } finally {
      setSaving(false);
    }
  };

  const valid = isNew ? label.trim() !== '' && adapterId.trim() !== '' : true;

  return (
    <Drawer open onClose={onClose} title={isNew ? '添加 Runtime 绑定' : `编辑绑定 ${binding.runtime_label}`} width={480}>
      <div className="p-comfortable">
        <div className="space-y-base">
        {isNew && (
          <div className="grid grid-cols-2 gap-snug">
            <label className="block">
              <span className="text-body text-text-secondary">Runtime 标签</span>
              <Input
                value={label}
                onChange={(e) => setLabel(e.target.value)}
                placeholder="dsh_local"
                className="font-mono"
              />
            </label>
            <label className="block">
              <span className="text-body text-text-secondary">Adapter ID</span>
              <Input
                value={adapterId}
                onChange={(e) => setAdapterId(e.target.value)}
                placeholder="dsh / kimi / codex-appserver"
                className="font-mono"
              />
            </label>
          </div>
        )}
        <div className="grid grid-cols-2 gap-snug">
          <label className="block">
            <span className="text-body text-text-secondary">Provider</span>
            <Input value={provider} onChange={(e) => setProvider(e.target.value)} placeholder="deepseek" />
          </label>
          <label className="block">
            <span className="text-body text-text-secondary">默认模型</span>
            <Input value={model} onChange={(e) => setModel(e.target.value)} placeholder="deepseek-v4-flash" />
          </label>
        </div>
        <label className="block">
          <span className="text-body text-text-secondary">凭据引用（credential_ref，如 env://DEEPSEEK_API_KEY）</span>
          <Input
            value={credentialRef}
            onChange={(e) => setCredentialRef(e.target.value)}
            placeholder="env://DEEPSEEK_API_KEY"
            className="font-mono"
          />
        </label>
        <div className="flex justify-end gap-snug pt-tight">
          <Button onClick={onClose}>取消</Button>
          <Button variant="primary" onClick={() => void submit()} disabled={!valid || saving}>
            {saving ? '保存中…' : isNew ? '创建' : '保存'}
          </Button>
        </div>
      </div>
      </div>
    </Drawer>
  );
}

function Field({ label, value, mono }: { label: string; value?: string; mono?: boolean }) {
  return (
    <div>
      <dt className="text-caption text-text-tertiary block mb-1">{label}</dt>
      <dd className={`text-text-primary ${mono ? 'font-mono text-caption' : ''}`}>{value ?? '-'}</dd>
    </div>
  );
}

function HealthMetric({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="min-w-0">
      <span className="mb-1 block text-caption text-text-tertiary">{label}</span>
      <div className="truncate">{children}</div>
    </div>
  );
}
