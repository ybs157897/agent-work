import { Bot, KanbanSquare, LayoutDashboard, ScrollText, Settings } from 'lucide-react';
import React from 'react';
import { NavLink, useLocation } from 'react-router-dom';
import { useWorkspaceStore } from '../stores/workspace.store';

const NAV_ITEMS = [
  { to: '/', icon: <LayoutDashboard />, label: '总览', end: true },
  { to: '/agents', icon: <Bot />, label: '智能体团队' },
  { to: '/tasks', icon: <KanbanSquare />, label: '任务看板' },
  { to: '/logs', icon: <ScrollText />, label: '日志' },
  { to: '/settings', icon: <Settings />, label: '设置' },
];

const BREADCRUMBS: Record<string, string> = {
  '/': '总览',
  '/agents': '智能体团队',
  '/tasks': '任务看板',
  '/logs': '日志',
  '/settings': '设置',
};

const SSE_LABEL = { online: '在线', connecting: '连接中', reconnecting: '重连中' } as const;
const SSE_DOT = {
  online: 'bg-status-success status-pulse',
  connecting: 'bg-status-standby',
  reconnecting: 'bg-status-warning status-pulse',
} as const;

export function LayoutShell({ children }: { children: React.ReactNode }) {
  const location = useLocation();
  const workspace = useWorkspaceStore((s) => s.workspace);
  const me = useWorkspaceStore((s) => s.me);
  const sseStatus = useWorkspaceStore((s) => s.sseStatus);
  const breadcrumb = BREADCRUMBS[location.pathname] ?? '';

  return (
    <div className="flex h-screen w-full bg-surface-base overflow-hidden">
      {/* 左侧 60px 图标导航（confirmed-ia） */}
      <aside className="w-[60px] shrink-0 h-full bg-surface-raised border-r border-border-subtle flex flex-col items-center py-4 z-20 relative">
        <div className="w-8 h-8 bg-brand-primary rounded mb-8 flex items-center justify-center text-white font-bold text-lg shadow-sm">
          A
        </div>
        <nav className="flex flex-col gap-2 flex-1 w-full px-2">
          {NAV_ITEMS.map((item) => (
            <NavItem key={item.to} {...item} />
          ))}
        </nav>
        <div className="mt-auto pt-4" title={me?.name ?? ''}>
          <div className="w-8 h-8 rounded-full bg-brand-primary/15 text-brand-accent flex items-center justify-center text-caption font-semibold ring-2 ring-transparent hover:ring-border-strong cursor-pointer transition-all">
            {(me?.name ?? 'D').slice(0, 1)}
          </div>
        </div>
      </aside>

      {/* 主区域 */}
      <div className="flex-1 flex flex-col min-w-0">
        <header className="h-[48px] shrink-0 px-6 flex items-center justify-between border-b border-border-subtle bg-surface-base z-10 sticky top-0">
          <div className="flex items-center text-body font-medium">
            <span className="text-text-secondary">{workspace?.name ?? '…'}</span>
            <span className="mx-2 text-text-tertiary">/</span>
            <span className="text-text-primary">{breadcrumb}</span>
          </div>
          <div className="flex items-center gap-3" title="SSE 实时事件连接状态">
            <div className={`w-2 h-2 rounded-full ${SSE_DOT[sseStatus]}`} />
            <span className="text-caption text-text-secondary">{SSE_LABEL[sseStatus]}</span>
          </div>
        </header>

        <main className="flex-1 overflow-y-auto relative isolate h-full">{children}</main>
      </div>
    </div>
  );
}

function NavItem({
  to,
  icon,
  label,
  end,
}: {
  to: string;
  icon: React.ReactNode;
  label: string;
  end?: boolean;
}) {
  return (
    <div className="relative group w-full flex justify-center">
      <NavLink
        to={to}
        end={end}
        className={({ isActive }) =>
          `w-10 h-10 rounded-lg flex items-center justify-center transition-colors duration-200 ${
            isActive
              ? 'bg-brand-primary text-white shadow-level-1'
              : 'text-text-secondary hover:bg-surface-base hover:text-text-primary'
          }`
        }
      >
        {React.cloneElement(icon as React.ReactElement, { className: 'w-5 h-5' })}
      </NavLink>
      <div className="absolute left-[60px] top-1/2 -translate-y-1/2 px-2 py-1 bg-text-primary text-text-inverse text-caption rounded opacity-0 invisible group-hover:opacity-100 group-hover:visible transition-all whitespace-nowrap z-50 pointer-events-none">
        {label}
      </div>
    </div>
  );
}
