import { Pencil, Plus, Radar, RefreshCw } from 'lucide-react';
import { useEffect, useState } from 'react';
import { ApiError } from '../api/client';
import {
  createRuntimeBinding,
  listRuntimeBindings,
  patchRuntimeBinding,
  patchWorkspace,
  probeRuntimeBinding,
} from '../api/endpoints';
import type { ProbeResult, RuntimeBinding } from '../api/types';
import { Modal } from '../components/modal';
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
    <div className="layout-safe space-y-stack-md py-comfortable">
      <h2 className="text-h2 text-text-primary tracking-tight">设置</h2>

      <section className="bg-surface-raised rounded-card shadow-level-1 border border-border-subtle p-comfortable">
        <div className="flex items-center justify-between mb-comfortable">
          <h3 className="text-h3 text-text-primary">Workspace</h3>
          <button
            onClick={() => setEditingWs(true)}
            className="flex items-center gap-1 text-caption border border-border-strong text-text-secondary rounded-button px-2 py-1 hover:bg-surface-base transition-colors"
          >
            <Pencil className="w-3.5 h-3.5" />
            编辑
          </button>
        </div>
        <dl className="grid grid-cols-2 gap-base max-w-xl text-body">
          <Field label="名称" value={workspace?.name} />
          <Field label="时区" value={workspace?.timezone} />
          <Field label="ID" value={workspace?.id} mono />
          <Field label="版本" value={workspace ? String(workspace.version) : undefined} />
        </dl>
      </section>

      {workspace && <WorkspaceEditModal open={editingWs} onClose={() => setEditingWs(false)} />}

      <section className="bg-surface-raised rounded-card shadow-level-1 border border-border-subtle p-comfortable">
        <h3 className="text-h3 text-text-primary mb-comfortable">系统健康</h3>
        <div className="grid grid-cols-2 gap-base max-w-xl text-body">
          <div>
            <span className="text-caption text-text-tertiary block mb-1">控制平面</span>
            <span className="inline-flex items-center gap-2">
              <span
                className={`w-2 h-2 rounded-full ${
                  health?.control_plane === 'healthy' ? 'bg-status-success' : 'bg-status-error'
                }`}
              />
              <span className="text-text-primary">{health?.control_plane ?? '未知'}</span>
            </span>
          </div>
          <div>
            <span className="text-caption text-text-tertiary block mb-1">Runner</span>
            <span className="text-text-primary">
              {health?.runners?.length ? `${health.runners.length} 个已连接` : '无连接（Mock Adapter 内置）'}
            </span>
          </div>
          <div>
            <span className="text-caption text-text-tertiary block mb-1">SSE 事件流</span>
            <span className="text-text-primary">
              {sseStatus === 'online' ? '在线' : sseStatus === 'connecting' ? '连接中' : '重连中'} · 游标{' '}
              {eventCursor}
            </span>
          </div>
        </div>
      </section>

      <RuntimeBindingsSection workspaceId={workspace?.id} />

      <section className="bg-surface-raised rounded-card shadow-level-1 border border-border-subtle p-comfortable">
        <h3 className="text-h3 text-text-primary mb-comfortable">当前用户</h3>
        <dl className="grid grid-cols-2 gap-base max-w-xl text-body">
          <Field label="用户" value={me?.name} />
          <Field label="角色" value={me?.role} />
          <Field label="用户 ID" value={me?.user_id} mono />
          <Field label="会话时间" value={formatDateTime(new Date().toISOString())} />
        </dl>
      </section>
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
    <section className="bg-surface-raised rounded-card shadow-level-1 border border-border-subtle p-comfortable">
      <div className="flex items-center justify-between mb-comfortable">
        <h3 className="text-h3 text-text-primary">Runtime 绑定</h3>
        <div className="flex items-center gap-2">
          <button
            onClick={load}
            className="flex items-center gap-1.5 bg-transparent border border-border-strong text-text-secondary rounded-button px-base py-tight text-body font-medium hover:bg-surface-base transition-colors"
          >
            <RefreshCw className="w-4 h-4" />
            刷新
          </button>
          <button
            onClick={() => setEditing('new')}
            className="flex items-center gap-1.5 bg-transparent border border-brand-primary text-brand-primary rounded-button px-base py-tight text-body font-medium transition-colors hover:bg-brand-primary/5"
          >
            <Plus className="w-4 h-4" />
            添加绑定
          </button>
        </div>
      </div>

      {bindings === null ? (
        <p className="text-body text-text-tertiary">加载中…</p>
      ) : bindings.length === 0 ? (
        <p className="text-body text-text-secondary">暂无 Runtime 绑定。</p>
      ) : (
        <div className="space-y-3">
          {bindings.map((b) => {
            const probeResult = probeResults[b.id];
            return (
              <div key={b.id} className="rounded-lg border border-border-subtle p-base">
                <div className="flex items-center justify-between mb-2">
                  <div className="flex items-center gap-2">
                    <span className="font-semibold text-body text-text-primary">{b.runtime_label}</span>
                    <span className="text-caption text-text-tertiary">
                      {b.adapter_id}@{b.adapter_version || '—'}
                      {b.provider_version ? ` · provider ${b.provider_version}` : ''}
                    </span>
                  </div>
                  <div className="flex items-center gap-2">
                    <span
                      className={`inline-flex items-center px-2 py-0.5 rounded-full text-caption font-medium ${
                        b.status === 'ready'
                          ? 'bg-status-success/10 text-status-success'
                          : 'bg-surface-base text-text-secondary'
                      }`}
                    >
                      {b.status}
                    </span>
                    <button
                      onClick={() => setEditing(b)}
                      className="flex items-center gap-1 text-caption border border-border-strong text-text-secondary rounded-button px-2 py-1 hover:bg-surface-base transition-colors"
                    >
                      <Pencil className="w-3.5 h-3.5" />
                      编辑
                    </button>
                    <button
                      onClick={() => void probe(b.id)}
                      disabled={probing !== null}
                      className="flex items-center gap-1 text-caption border border-brand-primary text-brand-primary rounded-button px-2 py-1 hover:bg-brand-primary/5 transition-colors disabled:opacity-50"
                    >
                      <Radar className="w-3.5 h-3.5" />
                      {probing === b.id ? '探测中…' : 'Probe'}
                    </button>
                  </div>
                </div>
                <div className="text-caption text-text-secondary mb-2">
                  {b.provider} / {b.model}
                  {b.credential_ref && <span className="text-text-tertiary"> · 凭据引用 {b.credential_ref}</span>}
                </div>
                <div className="flex flex-wrap gap-1.5">
                  {Object.entries(b.capabilities ?? {}).map(([cap, level]) => (
                    <span
                      key={cap}
                      className={`inline-flex items-center px-2 py-0.5 rounded-sm text-caption border ${
                        level === 'supported' || level === 'provider_native'
                          ? 'bg-brand-primary/10 text-brand-accent border-brand-primary/20'
                          : level === 'unavailable'
                            ? 'bg-surface-base text-text-tertiary border-border-subtle line-through'
                            : 'bg-status-warning/10 text-status-warning border-status-warning/20'
                      }`}
                      title={level}
                    >
                      {cap}
                    </span>
                  ))}
                </div>
                {probeResult && (
                  <p
                    className={`text-caption mt-2 ${probeResult.ok ? 'text-status-success' : 'text-status-error'}`}
                  >
                    Probe {probeResult.ok ? 'OK' : '失败'}
                    {probeResult.error ? `：${probeResult.error}` : ''}
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
    </section>
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

  const inputCls =
    'mt-1 w-full rounded-input border border-border-strong bg-surface-raised px-snug py-tight text-body outline-none focus:ring-2 focus:ring-brand-primary/30';

  return (
    <Modal open={open} onClose={onClose} title="编辑 Workspace">
      <div className="space-y-base">
        <label className="block">
          <span className="text-body text-text-secondary">名称</span>
          <input value={name} onChange={(e) => setName(e.target.value)} className={inputCls} />
        </label>
        <label className="block">
          <span className="text-body text-text-secondary">时区</span>
          <input value={timezone} onChange={(e) => setTimezone(e.target.value)} placeholder="UTC / Asia/Shanghai" className={inputCls} />
        </label>
        <div className="flex justify-end gap-snug pt-tight">
          <button
            onClick={onClose}
            className="bg-transparent border border-border-strong text-text-secondary rounded-button px-base py-tight font-medium hover:bg-surface-base transition-colors"
          >
            取消
          </button>
          <button
            onClick={() => void save()}
            disabled={!name.trim() || saving}
            className="bg-brand-primary text-white rounded-button px-base py-tight font-medium transition-all hover:bg-brand-accent active:scale-[0.98] disabled:opacity-50"
          >
            {saving ? '保存中…' : '保存'}
          </button>
        </div>
      </div>
    </Modal>
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

  const inputCls =
    'mt-1 w-full rounded-input border border-border-strong bg-surface-raised px-snug py-tight text-body outline-none focus:ring-2 focus:ring-brand-primary/30';

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
    <Modal open onClose={onClose} title={isNew ? '添加 Runtime 绑定' : `编辑绑定 ${binding.runtime_label}`}>
      <div className="space-y-base">
        {isNew && (
          <div className="grid grid-cols-2 gap-snug">
            <label className="block">
              <span className="text-body text-text-secondary">Runtime 标签</span>
              <input value={label} onChange={(e) => setLabel(e.target.value)} placeholder="dsh_local" className={`${inputCls} font-mono`} />
            </label>
            <label className="block">
              <span className="text-body text-text-secondary">Adapter ID</span>
              <input value={adapterId} onChange={(e) => setAdapterId(e.target.value)} placeholder="dsh / kimi / codex-appserver" className={`${inputCls} font-mono`} />
            </label>
          </div>
        )}
        <div className="grid grid-cols-2 gap-snug">
          <label className="block">
            <span className="text-body text-text-secondary">Provider</span>
            <input value={provider} onChange={(e) => setProvider(e.target.value)} placeholder="deepseek" className={inputCls} />
          </label>
          <label className="block">
            <span className="text-body text-text-secondary">默认模型</span>
            <input value={model} onChange={(e) => setModel(e.target.value)} placeholder="deepseek-v4-flash" className={inputCls} />
          </label>
        </div>
        <label className="block">
          <span className="text-body text-text-secondary">凭据引用（credential_ref，如 env://DEEPSEEK_API_KEY）</span>
          <input value={credentialRef} onChange={(e) => setCredentialRef(e.target.value)} placeholder="env://DEEPSEEK_API_KEY" className={`${inputCls} font-mono`} />
        </label>
        <div className="flex justify-end gap-snug pt-tight">
          <button
            onClick={onClose}
            className="bg-transparent border border-border-strong text-text-secondary rounded-button px-base py-tight font-medium hover:bg-surface-base transition-colors"
          >
            取消
          </button>
          <button
            onClick={() => void submit()}
            disabled={!valid || saving}
            className="bg-brand-primary text-white rounded-button px-base py-tight font-medium transition-all hover:bg-brand-accent active:scale-[0.98] disabled:opacity-50"
          >
            {saving ? '保存中…' : isNew ? '创建' : '保存'}
          </button>
        </div>
      </div>
    </Modal>
  );
}

function Field({ label, value, mono }: { label: string; value?: string; mono?: boolean }) {
  return (
    <div>
      <dt className="text-caption text-text-tertiary block mb-1">{label}</dt>
      <dd className={`text-text-primary ${mono ? 'font-mono text-caption' : ''}`}>{value ?? '—'}</dd>
    </div>
  );
}
