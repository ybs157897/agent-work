import { AnimatePresence, motion } from 'framer-motion';
import { useEffect } from 'react';
import { Navigate, Route, Routes, useLocation } from 'react-router-dom';
import { ErrorState, Loading } from './components/async-state';
import { LayoutShell } from './components/layout-shell';
import { Toaster } from './components/toast';
import AgentsPage from './pages/agents.page';
import DashboardPage from './pages/dashboard.page';
import LogsPage from './pages/logs.page';
import SettingsPage from './pages/settings.page';
import TasksPage from './pages/tasks.page';
import { bootstrap } from './stores/bootstrap';
import { useWorkspaceStore } from './stores/workspace.store';

export default function App() {
  const phase = useWorkspaceStore((s) => s.phase);
  const error = useWorkspaceStore((s) => s.error);

  useEffect(() => {
    void bootstrap();
  }, []);

  if (phase === 'error') {
    return (
      <div className="h-screen flex items-center justify-center bg-surface-base">
        <ErrorState message={error ?? '加载失败'} onRetry={() => void bootstrap()} />
      </div>
    );
  }
  if (phase !== 'ready') {
    return (
      <div className="h-screen flex items-center justify-center bg-surface-base">
        <Loading label="正在连接控制平面…" />
      </div>
    );
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
  return (
    <AnimatePresence mode="wait">
      <motion.div
        key={location.pathname}
        initial={{ opacity: 0, y: 5 }}
        animate={{ opacity: 1, y: 0 }}
        exit={{ opacity: 0, y: -5 }}
        transition={{ duration: 0.15, ease: 'easeOut' }}
        className="min-h-full"
      >
        <Routes location={location}>
          <Route path="/" element={<DashboardPage />} />
          <Route path="/agents" element={<AgentsPage />} />
          <Route path="/tasks" element={<TasksPage />} />
          <Route path="/logs" element={<LogsPage />} />
          <Route path="/settings" element={<SettingsPage />} />
          <Route path="*" element={<Navigate to="/" replace />} />
        </Routes>
      </motion.div>
    </AnimatePresence>
  );
}
