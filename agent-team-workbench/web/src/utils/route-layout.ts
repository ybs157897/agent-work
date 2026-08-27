const FULL_BLEED_PATHS = new Set(['/chat', '/models', '/agents']);

export function isFullBleedPath(pathname: string): boolean {
  return FULL_BLEED_PATHS.has(pathname);
}

/** 对话页是唯一一块暗色阅读面；壳层宣纸顶栏 / mesh 只在这条路由收起。 */
export function isChatPath(pathname: string): boolean {
  return pathname === '/chat';
}

/** `<main>` 的滚动与皮肤：对话页挂 tx-scope 铺满墨底，其余路由保留宣纸 mesh。 */
export function mainContentClassName(pathname: string): string {
  const fullBleed = isFullBleedPath(pathname);
  const chat = isChatPath(pathname);
  return [
    'relative isolate min-h-0 flex-1 focus:outline-none',
    fullBleed ? 'flex flex-col overflow-hidden' : 'overflow-y-auto',
    chat ? 'tx-scope' : 'mesh-bg',
  ].join(' ');
}
