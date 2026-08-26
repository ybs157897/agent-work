const FULL_BLEED_PATHS = new Set(['/chat', '/models', '/agents']);

export function isFullBleedPath(pathname: string): boolean {
  return FULL_BLEED_PATHS.has(pathname);
}
