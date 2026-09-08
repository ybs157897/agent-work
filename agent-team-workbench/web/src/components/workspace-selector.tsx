import { Select } from './ui';
import { switchWorkspace } from '../stores/bootstrap';
import { useWorkspaceStore } from '../stores/workspace.store';
import type { Workspace } from '../api/types';

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
  return (
    <WorkspaceSelectorView
      workspaces={workspaces}
      selectedWorkspaceId={selectedWorkspaceId}
      disabled={switching || phase !== 'ready' || workspaces.length === 0}
      announcement={switching ? '正在切换工作区…' : notice ?? ''}
      onChange={(id) => void switchWorkspace(id)}
    />
  );
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
            {workspace.name}
          </option>
        ))}
      </Select>
      <p role="status" aria-live="polite" className="sr-only">
        {announcement}
      </p>
    </div>
  );
}
