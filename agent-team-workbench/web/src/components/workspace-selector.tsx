import { useSidebar } from './aceternity/sidebar';
import { Select } from './ui';
import { switchWorkspace } from '../stores/bootstrap';
import { useWorkspaceStore } from '../stores/workspace.store';
import type { Workspace } from '../api/types';

/**
 * 全局 Workspace 切换器（任务控制面 RFC §12.3）：
 * - 固定放在持久 SidebarContents：Chat 页没有普通 header，只放 header 会在对话页消失；
 * - 用 Design System Select（禁裸原生 select），原生控件保证完整键盘操作；
 * - 侧栏折叠态整壳 sr-only：aria-label/tooltip 保留、键盘入口保留（sr-only 仍可聚焦）；
 * - 切换中（switching/booting）disabled；role=status aria-live 播报切换与回退提示。
 */
export function WorkspaceSelector({ showText }: { showText: boolean }) {
  const workspaces = useWorkspaceStore((s) => s.workspaces);
  const selectedWorkspaceId = useWorkspaceStore((s) => s.selectedWorkspaceId);
  const switching = useWorkspaceStore((s) => s.switching);
  const phase = useWorkspaceStore((s) => s.phase);
  const notice = useWorkspaceStore((s) => s.notice);
  const { open, animate } = useSidebar();
  // 首次展开动画期间 sidebar 尚未 open，与 NavItem 的 showText 口径一致。
  const expanded = showText && (open || !animate);

  return (
    <WorkspaceSelectorView
      workspaces={workspaces}
      selectedWorkspaceId={selectedWorkspaceId}
      disabled={switching || phase !== 'ready' || workspaces.length === 0}
      expanded={expanded}
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
  expanded,
  announcement,
  onChange,
}: {
  workspaces: Workspace[];
  selectedWorkspaceId: string | null;
  disabled: boolean;
  expanded: boolean;
  announcement: string;
  onChange: (workspaceId: string) => void;
}) {
  return (
    <div className="min-w-0">
      <span className={expanded ? 'block' : 'sr-only'}>
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
      </span>
      <p role="status" aria-live="polite" className="sr-only">
        {announcement}
      </p>
    </div>
  );
}
