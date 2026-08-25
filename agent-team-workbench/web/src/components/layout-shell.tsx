import { Bot, Cpu, KanbanSquare, LayoutDashboard, MessageSquare, ScrollText, Settings } from 'lucide-react';
import React from 'react';
import { NavLink, useLocation } from 'react-router-dom';
import { StatusPill } from './ui';
import { useWorkspaceStore } from '../stores/workspace.store';

const NAV_ITEMS = [
  { to: '/', icon: LayoutDashboard, label: '总览', end: true },
  { to: '/agents', icon: Bot, label: '智能体配置' },
  { to: '/tasks', icon: KanbanSquare, label: '任务看板' },
  { to: '/chat', icon: MessageSquare, label: '对话' },
  { to: '/models', icon: Cpu, label: '模型' },
  { to: '/logs', icon: ScrollText, label: '日志' },
  { to: '/settings', icon: Settings, label: '设置' },
];

const BREADCRUMBS: Record<string, string> = {
  '/': '总览',
  '/agents': '智能体配置',
  '/tasks': '任务看板',
  '/chat': '对话',
  '/models': '模型',
  '/logs': '日志',
  '/settings': '设置',
};

const SSE_LABEL = { online: '实时连接', connecting: '连接中', reconnecting: '重连中' } as const;
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
  const isFullBleed = location.pathname === '/chat' || location.pathname === '/models' || location.pathname === '/agents';

  return (
    <div className="relative flex h-screen w-full bg-surface-base overflow-hidden">
      {/* 键盘用户的跳转入口：平时视觉隐藏，聚焦时显现（DESIGN.md 无障碍条） */}
      <a
        href="#main-content"
        className="sr-only focus:not-sr-only focus:absolute focus:left-snug focus:top-snug focus:z-50 focus:rounded-button focus:border focus:border-border-strong focus:bg-surface-raised focus:px-base focus:py-tight focus:text-body focus:text-text-primary focus:shadow-level-2"
      >
        跳到主要内容
      </a>
      <aside className="w-[220px] shrink-0 h-full bg-sidebar border-r border-sidebar-border flex flex-col z-20">
        <div className="h-14 shrink-0 px-4 flex items-center gap-3 border-b border-sidebar-border">
          <div className="w-8 h-8 rounded-lg bg-gradient-to-br from-brand-primary to-brand-accent flex items-center justify-center text-text-inverse font-bold text-sm shadow-md shadow-brand-primary/25">
            A
          </div>
          <div className="min-w-0">
            <div className="font-display text-body font-semibold text-text-on-sidebar-active leading-tight truncate">
              Agent Team
            </div>
            <div className="text-caption text-text-on-sidebar/70 leading-tight">Workbench</div>
          </div>
        </div>

        <nav className="flex-1 overflow-y-auto px-3 py-4 space-y-1" aria-label="主导航">
          {NAV_ITEMS.map((item) => (
            <NavItem key={item.to} {...item} />
          ))}
        </nav>

        <div className="shrink-0 p-3 border-t border-sidebar-border" title={me?.name ?? ''}>
          <div className="flex items-center gap-3 px-2 py-2 rounded-lg hover:bg-sidebar-hover transition-colors">
            <div className="w-9 h-9 rounded-full bg-brand-primary/20 text-brand-primary flex items-center justify-center text-caption font-semibold shrink-0 ring-1 ring-brand-primary/20">
              {(me?.name ?? 'D').slice(0, 1)}
            </div>
            <div className="min-w-0">
              <div className="text-body font-medium text-text-on-sidebar-active truncate leading-tight">
                {me?.name ?? '…'}
              </div>
              <div className="text-caption text-text-on-sidebar truncate leading-tight">
                {me?.role ?? ''}
              </div>
            </div>
          </div>
        </div>
      </aside>

      <div className="flex-1 flex flex-col min-w-0">
        <header className="h-14 shrink-0 px-6 flex items-center justify-between border-b border-border-subtle bg-surface-raised/80 backdrop-blur-md z-10 sticky top-0">
          <div className="flex items-center gap-2 text-body">
            <span className="text-text-tertiary font-medium">{workspace?.name ?? '…'}</span>
            <span className="text-text-tertiary/60">/</span>
            <span className="text-text-primary font-semibold">{breadcrumb}</span>
          </div>
          <StatusPill title="SSE 实时事件连接状态">
            <span className={`w-2 h-2 rounded-full ${SSE_DOT[sseStatus]}`} />
            <span>{SSE_LABEL[sseStatus]}</span>
          </StatusPill>
        </header>

        <main
          id="main-content"
          tabIndex={-1}
          className={`flex-1 min-h-0 relative isolate mesh-bg focus:outline-none ${
            isFullBleed ? 'flex flex-col overflow-hidden' : 'overflow-y-auto'
          }`}
        >
          {children}
        </main>
      </div>
    </div>
  );
}

function NavItem({
  to,
  icon: Icon,
  label,
  end,
}: {
  to: string;
  icon: React.ComponentType<{ className?: string }>;
  label: string;
  end?: boolean;
}) {
  return (
    <NavLink
      to={to}
      end={end}
      className={({ isActive }) =>
        `group relative flex items-center gap-3 px-3 py-2.5 rounded-lg text-body transition-all duration-200 focus-visible:ring-offset-sidebar ${
          isActive
            ? 'bg-sidebar-hover text-text-on-sidebar-active font-semibold shadow-sm'
            : 'text-text-on-sidebar font-medium hover:bg-sidebar-hover hover:text-text-on-sidebar-active'
        }`
      }
    >
      {({ isActive }) => (
        <>
          {isActive && (
            <span className="absolute left-0 top-1/2 -translate-y-1/2 w-0.5 h-5 rounded-full bg-brand-primary" />
          )}
          <Icon className={`w-[18px] h-[18px] shrink-0 ${isActive ? 'text-brand-primary' : 'text-text-on-sidebar group-hover:text-text-on-sidebar-active'}`} />
          <span className="truncate">{label}</span>
        </>
      )}
    </NavLink>
  );
}
