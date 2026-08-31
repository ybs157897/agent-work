import { Bot, Cpu, KanbanSquare, LayoutDashboard, MessageSquare, ScrollText, Settings, type LucideIcon } from 'lucide-react';
import { motion } from 'motion/react';
import React from 'react';
import { NavLink, useLocation } from 'react-router-dom';
import { Sidebar, SidebarBody, useSidebar } from './aceternity/sidebar';
import { InkBackdrop } from './ink/ink-backdrop';
import { SseStatusPill } from './sse-status';
import { WorkspaceSelector } from './workspace-selector';
import { useWorkspaceStore } from '../stores/workspace.store';
import { isChatPath, mainContentClassName } from '../utils/route-layout';

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

export function LayoutShell({ children }: { children: React.ReactNode }) {
  const location = useLocation();
  const workspace = useWorkspaceStore((state) => state.workspace);
  const breadcrumb = location.pathname.startsWith('/tasks/') ? '任务详情' : BREADCRUMBS[location.pathname] ?? '';
  const isChat = isChatPath(location.pathname);

  return (
    <div className="relative flex h-dvh w-full flex-col overflow-hidden bg-surface-base lg:flex-row">
      <a
        href="#main-content"
        className="sr-only focus:not-sr-only focus:absolute focus:left-snug focus:top-snug focus:z-50 focus:rounded-button focus:border focus:border-border-strong focus:bg-surface-raised focus:px-base focus:py-tight focus:text-body focus:text-text-primary focus:shadow-level-2"
      >
        跳到主要内容
      </a>

      <Sidebar animate>
        <SidebarBody className="z-20 border-r border-sidebar-border bg-sidebar p-0 text-text-on-sidebar shadow-level-3">
          <SidebarContents />
        </SidebarBody>
      </Sidebar>

      <div className="flex min-h-0 min-w-0 flex-1 flex-col">
        {isChat ? null : (
          <header className="sticky top-0 z-10 flex h-14 shrink-0 items-center justify-between border-b border-border-subtle bg-surface-glass/90 px-comfortable backdrop-blur-md">
            <div className="flex min-w-0 items-center gap-snug text-body">
              <span className="h-5 w-1 shrink-0 rounded-sm bg-brand-primary" aria-hidden="true" />
              <span className="truncate font-medium text-text-tertiary">{workspace?.name ?? '…'}</span>
              <span className="text-border-strong" aria-hidden="true">/</span>
              <span className="truncate font-display text-body-lg text-text-primary">{breadcrumb}</span>
            </div>
            <SseStatusPill />
          </header>
        )}

        <main
          id="main-content"
          tabIndex={-1}
          className={mainContentClassName(location.pathname)}
        >
          {isChat ? null : <InkBackdrop />}
          {children}
        </main>
      </div>
    </div>
  );
}

function SidebarContents() {
  const me = useWorkspaceStore((state) => state.me);
  const { open, animate } = useSidebar();
  const showText = open || !animate;

  return (
    <>
      <div className="flex h-14 shrink-0 items-center gap-snug border-b border-sidebar-border px-base">
        <div className="flex h-8 w-8 shrink-0 items-center justify-center rounded-sm border border-brand-primary/45 bg-brand-primary font-display text-h3 text-text-inverse shadow-level-1">
          策
        </div>
        <motion.div
          animate={{ opacity: showText ? 1 : 0 }}
          className={`min-w-0 overflow-hidden ${showText ? '' : 'sr-only'}`}
        >
          <div className="truncate font-display text-body-lg text-text-on-sidebar-active">Agent Team</div>
          <div className="truncate text-caption tracking-[0.12em] text-text-on-sidebar/70">案牍工作台</div>
        </motion.div>
      </div>

      {/* Workspace 切换器：持久侧栏（Chat 页无普通 header，只放 header 会在对话页消失）。 */}
      <div className="shrink-0 border-b border-sidebar-border px-tight py-tight">
        <WorkspaceSelector showText={showText} />
      </div>

      <nav className="flex-1 space-y-micro overflow-y-auto px-tight py-base" aria-label="主导航">
        {NAV_ITEMS.map((item) => (
          <NavItem key={item.to} {...item} />
        ))}
      </nav>

      <div className="shrink-0 border-t border-sidebar-border p-tight" title={me?.name ?? ''}>
        <div className="flex min-h-12 items-center gap-snug rounded-button px-tight py-tight transition-colors duration-ink hover:bg-sidebar-hover">
          <div className="flex h-8 w-8 shrink-0 items-center justify-center rounded-sm border border-brand-primary/30 bg-brand-primary/15 font-display text-body-lg text-brand-muted">
            {(me?.name ?? 'D').slice(0, 1)}
          </div>
          <motion.div
            animate={{ opacity: showText ? 1 : 0 }}
            className={`min-w-0 overflow-hidden ${showText ? '' : 'sr-only'}`}
          >
            <div className="truncate text-body font-medium text-text-on-sidebar-active">{me?.name ?? '…'}</div>
            <div className="truncate text-caption text-text-on-sidebar">{me?.role ?? ''}</div>
          </motion.div>
        </div>
      </div>
    </>
  );
}

function NavItem({
  to,
  icon: Icon,
  label,
  end,
}: {
  to: string;
  icon: LucideIcon;
  label: string;
  end?: boolean;
}) {
  const { open, animate, setOpen } = useSidebar();
  const showText = open || !animate;

  return (
    <NavLink
      to={to}
      end={end}
      title={!showText ? label : undefined}
      onClick={() => {
        if (window.matchMedia('(max-width: 1023px)').matches) setOpen(false);
      }}
      className={({ isActive }) =>
        `group relative flex min-h-10 items-center gap-snug rounded-button px-snug py-tight text-body transition-colors duration-ink focus-visible:ring-offset-sidebar ${
          isActive
            ? 'bg-sidebar-hover text-text-on-sidebar-active'
            : 'text-text-on-sidebar hover:bg-sidebar-hover hover:text-text-on-sidebar-active'
        }`
      }
    >
      {({ isActive }) => (
        <>
          {isActive ? (
            <motion.span
              layoutId="active-nav-seal"
              className="absolute left-0 top-1/2 h-5 w-1 -translate-y-1/2 rounded-sm bg-brand-primary"
            />
          ) : null}
          <Icon
            strokeWidth={1.6}
            className={`h-[18px] w-[18px] shrink-0 ${
              isActive ? 'text-brand-muted' : 'text-text-on-sidebar group-hover:text-text-on-sidebar-active'
            }`}
          />
          <motion.span
            animate={{ opacity: showText ? 1 : 0 }}
            className={`truncate whitespace-nowrap ${showText ? '' : 'sr-only'}`}
          >
            {label}
          </motion.span>
        </>
      )}
    </NavLink>
  );
}
