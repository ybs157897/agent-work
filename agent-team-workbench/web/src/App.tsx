import { AnimatePresence, motion, useReducedMotion } from 'motion/react';
import { lazy, Suspense, useEffect } from 'react';
import { Route, Routes, useLocation } from 'react-router-dom';
import { ErrorState } from './components/async-state';
import { AppShellSkeleton, Skeleton } from './components/ui';
import { LayoutShell } from './components/layout-shell';
import { Toaster } from './components/toast';
import DashboardPage from './pages/dashboard.page';
import NotFoundPage from './pages/not-found.page';
import { bootstrap } from './stores/bootstrap';
import { useWorkspaceStore } from './stores/workspace.store';
import { inkMotion } from './design/motion';
import { isFullBleedPath } from './utils/route-layout';

const AgentsPage = lazy(() => import('./pages/agents.page'));
const ChatPage = lazy(() => import('./pages/chat.page'));
const LogsPage = lazy(() => import('./pages/logs.page'));
const ModelsPage = lazy(() => import('./pages/models.page'));
const SettingsPage = lazy(() => import('./pages/settings.page'));
const TasksPage = lazy(() => import('./pages/tasks.page'));

export default function App() {
  const phase = useWorkspaceStore((s) => s.phase);
  const error = useWorkspaceStore((s) => s.error);

  useEffect(() => {
    void bootstrap();
  }, []);

  if (phase === 'error') {
    return (
      <div className="flex h-dvh items-center justify-center bg-surface-base">
        <ErrorState message={error ?? '加载失败'} onRetry={() => void bootstrap()} />
      </div>
    );
  }
  if (phase !== 'ready') {
    return <AppShellSkeleton />;
  }

  return (
    <LayoutShell>
      <AnimatedRoutes />
      <Toaster />
    </LayoutShell>
  );
}

function AnimatedRoutes() {
  const location = useLocation();
  const reduceMotion = useReducedMotion();
  const isFullBleed = isFullBleedPath(location.pathname);
  return (
    <AnimatePresence mode="wait">
      <motion.div
        key={location.pathname}
        initial={reduceMotion ? false : { opacity: 0, y: 4 }}
        animate={{ opacity: 1, y: 0 }}
        exit={reduceMotion ? { opacity: 1 } : { opacity: 0, y: -4 }}
        transition={{ duration: reduceMotion ? 0 : inkMotion.duration.normal, ease: inkMotion.easeOut }}
        className={
          isFullBleed
            ? 'h-full min-h-0 flex flex-col overflow-hidden'
            : 'min-h-full flex flex-col'
        }
      >
        <Suspense fallback={<RouteFallback fullBleed={isFullBleed} />}>
          <Routes location={location}>
            <Route path="/" element={<DashboardPage />} />
            <Route path="/agents" element={<AgentsPage />} />
            <Route path="/tasks" element={<TasksPage />} />
            <Route path="/chat" element={<ChatPage />} />
            <Route path="/models" element={<ModelsPage />} />
            <Route path="/logs" element={<LogsPage />} />
            <Route path="/settings" element={<SettingsPage />} />
            <Route path="*" element={<NotFoundPage />} />
          </Routes>
        </Suspense>
      </motion.div>
    </AnimatePresence>
  );
}

function RouteFallback({ fullBleed }: { fullBleed: boolean }) {
  return (
    <div
      className={fullBleed ? 'grid h-full min-h-0 grid-cols-[16rem_1fr] gap-0' : 'page-shell'}
      role="status"
      aria-label="页面加载中"
    >
      {fullBleed ? <Skeleton className="h-full rounded-none border-r border-border-subtle" /> : null}
      <div className={fullBleed ? 'space-y-base p-comfortable' : 'space-y-base'}>
        <Skeleton className="h-8 w-48" />
        <Skeleton className="h-24 w-full rounded-card" />
        <Skeleton className="h-40 w-full rounded-card" />
      </div>
    </div>
  );
}
