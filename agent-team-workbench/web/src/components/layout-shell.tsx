import { BookOpen, Bot, Cpu, KanbanSquare, Layers3, LayoutDashboard, MessageSquare, ScrollText, Settings, type LucideIcon } from 'lucide-react';
import { useEffect, useRef } from 'react';
import React from 'react';
import { NavLink, useLocation, useNavigate } from 'react-router-dom';
import { SseStatusPill } from './sse-status';
import { WorkspaceSelector } from './workspace-selector';
import { useWorkspaceStore } from '../stores/workspace.store';
import { applyWorkbenchTheme, useWorkbenchThemeStore } from '../stores/workbench-theme.store';
import { isChatPath, isTasksPath, mainContentClassName } from '../utils/route-layout';
import { taskPeekBackground } from '../utils/task-peek';

const NAV_ITEMS = [
  { to: '/', icon: LayoutDashboard, label: '总览', end: true },
  { to: '/agents', icon: Bot, label: '智能体配置' },
  { to: '/tasks', icon: KanbanSquare, label: '任务看板' },
  { to: '/chat', icon: MessageSquare, label: '对话' },
  { to: '/models', icon: Cpu, label: '模型' },
  { to: '/library', icon: BookOpen, label: '知识库' },
  { to: '/logs', icon: ScrollText, label: '日志' },
  { to: '/settings', icon: Settings, label: '设置' },
];

const BREADCRUMBS: Record<string, string> = {
  '/': '总览',
  '/agents': '智能体配置',
  '/tasks': '任务看板',
  '/chat': '对话',
  '/models': '模型',
  '/library': '知识库',
  '/logs': '日志',
  '/settings': '设置',
};

export function LayoutShell({ children }: { children: React.ReactNode }) {
  const location = useLocation();
  const navigate = useNavigate();
  const workspace = useWorkspaceStore((state) => state.workspace);
  const switching = useWorkspaceStore((state) => state.switching);
  const rememberRoute = useWorkspaceStore((state) => state.rememberRoute);
  const lastRouteFor = useWorkspaceStore((state) => state.lastRouteFor);
  const theme = useWorkbenchThemeStore((state) => state.theme);
  const previousWorkspaceRef = useRef<string | null>(null);
  const backgroundLocation = taskPeekBackground(location.state);
  const backgroundPath = backgroundLocation?.pathname === '/tasks/' ? '/tasks' : backgroundLocation?.pathname;
  const breadcrumb = backgroundLocation
    ? BREADCRUMBS[backgroundPath ?? ''] ?? ''
    : location.pathname.startsWith('/tasks/') ? '任务详情' : BREADCRUMBS[location.pathname] ?? '';
  const isChat = isChatPath(location.pathname);
  const isTasksWorkspace = isTasksPath(location.pathname);

  useEffect(() => {
    applyWorkbenchTheme(theme);
  }, [theme]);

  useEffect(() => {
    if (!workspace?.id || switching) return;
    const workspaceId = workspace.id;
    const currentRoute = workspaceRoute(location.pathname, location.search, location.hash, workspaceId);
    const rawRoute = `${location.pathname}${location.search}${location.hash}`;
    const previousWorkspaceId = previousWorkspaceRef.current;
    previousWorkspaceRef.current = workspaceId;
    if (previousWorkspaceId === null && rawRoute !== currentRoute) {
      navigate(currentRoute, { replace: true });
      rememberRoute(workspaceId, currentRoute);
      return;
    }
    if (previousWorkspaceId !== null && previousWorkspaceId !== workspaceId) {
      const remembered = lastRouteFor(workspaceId) ?? `/chat?ws=${encodeURIComponent(workspaceId)}`;
      if (remembered !== currentRoute) navigate(remembered, { replace: true });
      return;
    }
    rememberRoute(workspaceId, currentRoute);
  }, [lastRouteFor, location.hash, location.pathname, location.search, navigate, rememberRoute, switching, workspace?.id]);

  return (
    <div className="workbench-theme relative flex h-dvh w-full flex-row overflow-hidden bg-surface-base" data-theme={theme}>
      <a
        href="#main-content"
        className="sr-only focus:not-sr-only focus:absolute focus:left-snug focus:top-snug focus:z-50 focus:rounded-button focus:border focus:border-border-strong focus:bg-surface-raised focus:px-base focus:py-tight focus:text-body focus:text-text-primary focus:shadow-level-2"
      >
        跳到主要内容
      </a>

      <aside className="workbench-sidebar z-20 flex h-full shrink-0 flex-col overflow-hidden border-r border-sidebar-border bg-sidebar text-text-on-sidebar shadow-level-3">
        <SidebarContents />
      </aside>

      <div className="flex min-h-0 min-w-0 flex-1 flex-col">
        {isChat ? null : (
          <header
            className={`sticky top-0 z-10 flex h-14 shrink-0 items-center justify-between border-b px-comfortable ${
              isTasksWorkspace ? 'plane-board-chrome' : 'border-border-subtle bg-surface-glass/90 backdrop-blur-md'
            }`}
          >
            <div className="flex min-w-0 items-center gap-snug text-body">
              <span className="plane-board-chrome-accent h-5 w-1 shrink-0 rounded-sm bg-brand-primary" aria-hidden="true" />
              <span className="plane-board-chrome-muted truncate font-medium text-text-tertiary">
                {workspace?.name ?? '…'}
              </span>
              <span className="plane-board-chrome-sep text-border-strong" aria-hidden="true">
                /
              </span>
              <span
                className="plane-board-chrome-title truncate font-zh text-body-lg font-semibold tracking-tight text-text-primary"
              >
                {breadcrumb}
              </span>
            </div>
            <SseStatusPill />
          </header>
        )}

        <main
          id="main-content"
          tabIndex={-1}
          className={mainContentClassName(location.pathname)}
        >
          {children}
        </main>
      </div>
    </div>
  );
}

function workspaceRoute(pathname: string, search: string, hash: string, workspaceId: string): string {
  const normalizedPath = pathname === '/task-chat' || pathname === '/task-chat/' ? '/chat' : pathname;
  const params = new URLSearchParams(search);
  params.set('ws', workspaceId);
  const query = params.toString();
  return `${normalizedPath}${query ? `?${query}` : ''}${hash}`;
}

function SidebarContents() {
  const me = useWorkspaceStore((state) => state.me);

  return (
    <>
      <div className="flex h-14 shrink-0 items-center gap-snug border-b border-sidebar-border px-base">
        <div className="flex h-8 w-8 shrink-0 items-center justify-center rounded-button border border-brand-primary/25 bg-brand-muted text-brand-primary">
          <Layers3 className="h-4 w-4" aria-hidden="true" />
        </div>
        <div className="min-w-0">
          <div className="truncate font-zh text-body-lg font-semibold text-text-on-sidebar-active">Agent Team</div>
          <div className="truncate text-caption tracking-[0.08em] text-text-on-sidebar/70">团队工作台</div>
        </div>
      </div>

      {/* Workspace 切换器：持久侧栏（Chat 页无普通 header，只放 header 会在对话页消失）。 */}
      <div className="shrink-0 border-b border-sidebar-border px-tight py-tight">
        <WorkspaceSelector />
      </div>

      <nav className="flex-1 space-y-micro overflow-y-auto px-tight py-base" aria-label="主导航">
        {NAV_ITEMS.map((item) => (
          <NavItem key={item.to} {...item} />
        ))}
      </nav>

      <div className="shrink-0 border-t border-sidebar-border p-tight" title={me?.name ?? ''}>
        <div className="flex min-h-12 items-center gap-snug rounded-button px-tight py-tight transition-colors duration-motion hover:bg-sidebar-hover">
          <div className="flex h-8 w-8 shrink-0 items-center justify-center rounded-button border border-brand-primary/20 bg-brand-muted text-body-lg font-semibold text-brand-primary">
            {(me?.name ?? 'D').slice(0, 1)}
          </div>
          <div className="min-w-0 overflow-hidden">
            <div className="truncate text-body font-medium text-text-on-sidebar-active">{me?.name ?? '…'}</div>
            <div className="truncate text-caption text-text-on-sidebar">{me?.role ?? ''}</div>
          </div>
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
  return (
    <NavLink
      to={to}
      end={end}
      className={({ isActive }) =>
        `group relative flex min-h-10 items-center gap-snug rounded-button px-snug py-tight text-body transition-colors duration-motion focus-visible:ring-offset-sidebar ${
          isActive
            ? 'bg-sidebar-hover text-text-on-sidebar-active'
            : 'text-text-on-sidebar hover:bg-sidebar-hover hover:text-text-on-sidebar-active'
        }`
      }
    >
      {({ isActive }) => (
        <>
          {isActive ? <span className="absolute left-0 top-1/2 h-5 w-0.5 -translate-y-1/2 rounded-full bg-brand-primary" aria-hidden="true" /> : null}
          <Icon
            strokeWidth={1.6}
            className={`h-[18px] w-[18px] shrink-0 ${
              isActive ? 'text-brand-primary' : 'text-text-on-sidebar group-hover:text-text-on-sidebar-active'
            }`}
          />
          <span className="min-w-0 truncate whitespace-nowrap">{label}</span>
        </>
      )}
    </NavLink>
  );
}
