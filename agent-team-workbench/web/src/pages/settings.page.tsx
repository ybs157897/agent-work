import { Radar, RefreshCw } from 'lucide-react';
import { useEffect, useState } from 'react';
import { ApiError } from '../api/client';
import { listRuntimeBindings, probeRuntimeBinding } from '../api/endpoints';
import type { ProbeResult, RuntimeBinding } from '../api/types';
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

  return (
    <div className="layout-safe space-y-stack-md py-comfortable">
      <h2 className="text-h2 text-text-primary tracking-tight">设置</h2>

      <section className="bg-surface-raised rounded-card shadow-level-1 border border-border-subtle p-comfortable">
        <h3 className="text-h3 text-text-primary mb-comfortable">Workspace</h3>
        <dl className="grid grid-cols-2 gap-base max-w-xl text-body">
          <Field label="名称" value={workspace?.name} />
          <Field label="时区" value={workspace?.timezone} />
          <Field label="ID" value={workspace?.id} mono />
          <Field label="版本" value={workspace ? String(workspace.version) : undefined} />
        </dl>
      </section>

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
        <button
          onClick={load}
          className="flex items-center gap-1.5 bg-transparent border border-border-strong text-text-secondary rounded-button px-base py-tight text-body font-medium hover:bg-surface-base transition-colors"
        >
          <RefreshCw className="w-4 h-4" />
          刷新
        </button>
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
    </section>
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
