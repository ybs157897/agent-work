import { FolderPlus, LoaderCircle, RefreshCw } from 'lucide-react';
import { useEffect, useMemo, useState } from 'react';
import { ApiError } from '../api/client';
import { createWorkspace, listExecutionHostMounts, listExecutionHosts } from '../api/workspaces';
import type { Workspace } from '../api/types';
import type { HostMount } from '../api/workspace-locations';
import { Button, Input, Select } from './ui';
import { Modal } from './modal';
import { switchWorkspace } from '../stores/bootstrap';
import { useWorkspaceStore } from '../stores/workspace.store';
import { toast } from '../stores/toast.store';

/**
 * 全局 Workspace 切换器（任务控制面 RFC §12.3）：
 * - 固定放在持久 SidebarContents：Chat 页没有普通 header，只放 header 会在对话页消失；
 * - 用 Design System Select（禁裸原生 select），原生控件保证完整键盘操作；
 * - 侧栏固定展开：工作区选择器始终保留可见标签与完整键盘入口；
 * - 切换中（switching/booting）disabled；role=status aria-live 播报切换与回退提示。
 */
export function WorkspaceSelector() {
  const workspaces = useWorkspaceStore((s) => s.workspaces);
  const selectedWorkspaceId = useWorkspaceStore((s) => s.selectedWorkspaceId);
  const switching = useWorkspaceStore((s) => s.switching);
  const phase = useWorkspaceStore((s) => s.phase);
  const notice = useWorkspaceStore((s) => s.notice);
  const currentWorkspace = useWorkspaceStore((s) => s.workspace);
  const [createOpen, setCreateOpen] = useState(false);
  return (
    <>
      <WorkspaceSelectorView
        workspaces={workspaces}
        selectedWorkspaceId={selectedWorkspaceId}
        disabled={switching || phase !== 'ready'}
        announcement={switching ? '正在切换工作区…' : notice ?? ''}
        onChange={(id) => {
          if (id === OPEN_WORKSPACE_VALUE) setCreateOpen(true);
          else void switchWorkspace(id);
        }}
      />
      <WorkspaceCreateModal open={createOpen} onClose={() => setCreateOpen(false)} sourceWorkspace={currentWorkspace} />
    </>
  );
}

const OPEN_WORKSPACE_VALUE = '__open-workspace__';

export function WorkspaceCreateModal({ open, onClose, sourceWorkspace }: { open: boolean; onClose: () => void; sourceWorkspace: Workspace | null }) {
  const workspaces = useWorkspaceStore((s) => s.workspaces);
  const setWorkspaces = useWorkspaceStore((s) => s.setWorkspaces);
  const [mounts, setMounts] = useState<HostMount[]>([]);
  const [hostId, setHostId] = useState('');
  const [mountAlias, setMountAlias] = useState('');
  const [name, setName] = useState('');
  const [nameDirty, setNameDirty] = useState(false);
  const [timezone, setTimezone] = useState(sourceWorkspace?.timezone ?? 'Asia/Shanghai');
  const [loading, setLoading] = useState(false);
  const [loadingMounts, setLoadingMounts] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (!open) return;
    setTimezone(sourceWorkspace?.timezone ?? 'Asia/Shanghai');
    setName('');
    setNameDirty(false);
    setMountAlias('');
    setError(null);
    setLoadingMounts(false);
    void listExecutionHosts()
      .then(({ items }) => {
        const ready = items.filter((host) => host.status === 'ready' && host.kind === 'local');
        setHostId((current) => ready.some((host) => host.id === current) ? current : ready[0]?.id ?? '');
      })
      .catch((loadError: unknown) => setError(loadError instanceof ApiError ? loadError.message : '读取可用宿主失败'));
  }, [open, sourceWorkspace?.id, sourceWorkspace?.timezone]);

  useEffect(() => {
    if (!open || !hostId) {
      setMounts([]);
      setMountAlias('');
      return;
    }
    setLoadingMounts(true);
    void listExecutionHostMounts(hostId)
      .then(({ items }) => {
        const ready = items.filter((mount) => mount.status === 'ready');
        setMounts(ready);
        setMountAlias((current) => ready.some((mount) => mount.alias === current) ? current : '');
      })
      .catch((loadError: unknown) => {
        setMounts([]);
        setMountAlias('');
        setError(loadError instanceof ApiError ? loadError.message : '读取项目目录失败');
      })
      .finally(() => setLoadingMounts(false));
  }, [hostId, open]);

  const selectedMount = useMemo(() => mounts.find((mount) => mount.alias === mountAlias), [mountAlias, mounts]);
  const handleMountChange = (alias: string) => {
    setMountAlias(alias);
    if (nameDirty) return;
    const mount = mounts.find((item) => item.alias === alias);
    if (mount) setName(mount.display_label || mount.alias);
  };
  const submit = async () => {
    if (!sourceWorkspace) {
      setError('创建新工作区需要一个当前工作区作为 Agent 配置来源。');
      return;
    }
    if (!selectedMount || !hostId || !name.trim()) {
      setError('请选择项目目录并填写工作区名称。');
      return;
    }
    setLoading(true);
    setError(null);
    try {
      const response = await createWorkspace({
        name: name.trim(),
        timezone: timezone.trim() || 'Asia/Shanghai',
        project: {
          execution_host_id: hostId,
          mount_alias: selectedMount.alias,
          mount_generation: selectedMount.registry_generation,
          repository_identity: selectedMount.repository_identity,
        },
        source_workspace_id: sourceWorkspace.id,
      });
      const created = response.workspace ?? response;
      if (!created.id) throw new Error('创建工作区响应缺少标识');
      const next = [...workspaces.filter((workspace) => workspace.id !== created.id), created];
      setWorkspaces(next);
      onClose();
      if (created.setup && created.setup.status !== 'ready') {
        toast.info(created.setup.status === 'pending' ? `工作区「${created.name}」正在准备 Agent 配置，完成后可从选择器打开` : `工作区「${created.name}」准备失败，可从选择器打开查看错误`);
        return;
      }
      await switchWorkspace(created.id);
      toast.success(response.reused ? `已打开已有工作区「${created.name}」` : `已创建工作区「${created.name}」`);
    } catch (createError) {
      setError(createError instanceof ApiError ? createError.message : createError instanceof Error ? createError.message : '创建工作区失败');
    } finally {
      setLoading(false);
    }
  };

  return open ? (
    <Modal
      open
      onClose={() => { if (!loading) onClose(); }}
      title="创建或打开工作区"
      width={560}
      skin="task"
      footer={<div className="flex justify-end gap-snug"><Button type="button" onClick={onClose} disabled={loading}>取消</Button><Button type="button" variant="primary" onClick={() => void submit()} disabled={loading || loadingMounts || !selectedMount}>{loading ? <LoaderCircle className="h-4 w-4 animate-spin motion-reduce:animate-none" aria-hidden /> : <FolderPlus className="h-4 w-4" aria-hidden />}{loading ? '创建中…' : '创建并打开'}</Button></div>}
    >
      <div className="space-y-snug">
        <p className="text-body text-text-secondary">一个工作区固定对应一个已登记的本机项目目录。相同目录再次提交会打开已有工作区，不会复制第二份。</p>
        {error && <div className="flex items-start gap-tight rounded-card border border-status-error/30 bg-status-error/5 px-snug py-tight text-body text-status-error" role="alert"><RefreshCw className="mt-0.5 h-4 w-4 shrink-0" aria-hidden />{error}</div>}
        <div className="grid gap-snug sm:grid-cols-2">
        <label className="block"><span className="text-caption font-medium text-text-secondary">工作区名称<span className="text-status-error" aria-hidden> *</span></span><Input value={name} onChange={(event) => { setName(event.target.value); setNameDirty(true); }} placeholder="如 web-idea" disabled={loading} /></label>
          <label className="block"><span className="text-caption font-medium text-text-secondary">时区</span><Input value={timezone} onChange={(event) => setTimezone(event.target.value)} placeholder="Asia/Shanghai" disabled={loading} /></label>
        </div>
        <div className="rounded-card border border-border-subtle bg-surface-sunken/45 px-snug py-tight"><p className="text-caption font-medium text-text-secondary">执行宿主</p><p className="mt-micro text-body text-text-primary">本机（仅使用已登记的本机项目目录）</p><p className="mt-micro text-caption text-text-tertiary">不提供远程宿主选择。</p></div>
        <label className="block"><span className="text-caption font-medium text-text-secondary">项目目录<span className="text-status-error" aria-hidden> *</span></span><Select value={mountAlias} onChange={(event) => handleMountChange(event.target.value)} disabled={loading || loadingMounts || mounts.length === 0} aria-label="项目目录"><option value="">{loadingMounts ? '读取中…' : '请选择已登记目录'}</option>{mounts.map((mount) => <option key={mount.alias} value={mount.alias}>{mount.display_label || mount.alias} · {mount.repository_identity}</option>)}</Select>{selectedMount && <span className="mt-micro block text-caption text-text-tertiary">宿主代次 {selectedMount.registry_generation} · 仓库身份 {selectedMount.repository_identity}</span>}</label>
        <div className="rounded-card border border-border-subtle bg-surface-sunken/45 px-snug py-tight"><p className="text-caption font-medium text-text-secondary">Agent 配置来源</p><p className="mt-micro text-body text-text-primary">当前工作区：{sourceWorkspace?.name ?? '未加载'}</p><p className="mt-micro text-caption text-text-tertiary">创建新空间时复制当前普通 Agent 配置；对话、任务、知识、运行记录和凭据不会复制。</p></div>
      </div>
    </Modal>
  ) : null;
}

/** 纯展示视图（props 驱动；键盘流断言在此层）。 */
export function WorkspaceSelectorView({
  workspaces,
  selectedWorkspaceId,
  disabled,
  announcement,
  onChange,
}: {
  workspaces: Workspace[];
  selectedWorkspaceId: string | null;
  disabled: boolean;
  announcement: string;
  onChange: (workspaceId: string) => void;
}) {
  return (
    <div className="min-w-0">
      <Select
        aria-label="切换工作区"
        title="切换工作区"
        value={selectedWorkspaceId ?? ''}
        disabled={disabled}
        onChange={(event) => onChange(event.target.value)}
        wrapperClassName="mt-0"
      >
        {workspaces.map((workspace) => (
          <option key={workspace.id} value={workspace.id}>
            {workspace.name}{workspace.setup?.status === 'pending' ? '（配置准备中）' : workspace.setup?.status === 'failed' ? '（配置失败）' : workspace.project?.status && workspace.project.status !== 'ready' ? '（项目不可用）' : ''}
          </option>
        ))}
        <option value={OPEN_WORKSPACE_VALUE}>打开或创建工作区…</option>
      </Select>
      <p role="status" aria-live="polite" className="sr-only">
        {announcement}
      </p>
    </div>
  );
}
