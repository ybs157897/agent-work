const FULL_BLEED_PATHS = new Set(['/chat', '/task-chat', '/task-chat/', '/models', '/agents']);

export function isFullBleedPath(pathname: string): boolean {
  return FULL_BLEED_PATHS.has(pathname);
}

/** Agent 与任务对话共用完整阅读面，页面自行提供标题与输入区。 */
export function isChatPath(pathname: string): boolean {
  return pathname === '/chat' || pathname === '/task-chat' || pathname === '/task-chat/';
}

/** Task 列表与详情共用 Plane 白底工作面，不挂宣纸 mesh / 山水层。 */
export function isTasksPath(pathname: string): boolean {
  return pathname === '/tasks' || pathname.startsWith('/tasks/');
}

/** `<main>` 的滚动与皮肤：对话页挂 tx-scope；Task 工作区挂 plane-board-shell；其余保留宣纸 mesh。 */
export function mainContentClassName(pathname: string): string {
  const tasksPath = isTasksPath(pathname);
  const fullBleed = isFullBleedPath(pathname) || tasksPath;
  const chat = isChatPath(pathname);
  return [
    'relative isolate min-h-0 flex-1 focus:outline-none',
    fullBleed ? 'flex flex-col overflow-hidden' : 'overflow-y-auto',
    chat ? 'tx-scope' : tasksPath ? 'plane-board-shell' : 'mesh-bg',
  ].join(' ');
}
