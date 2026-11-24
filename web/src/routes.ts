export type PageId = 'overview' | 'gpu' | 'memory' | 'disk' | 'data' | 'ai' | 'settings';
export const pages: PageId[] = ['overview', 'gpu', 'memory', 'disk', 'data', 'ai', 'settings'];
// Pre-2026-11-24 hashes keep working after the navigation was consolidated.
export const legacyRoutes: Record<string, [PageId, string?]> = {
  gpus: ['gpu', 'health'], models: ['gpu', 'ranking'], incidents: ['gpu', 'events'], validations: ['gpu', 'degradation'],
  hardware: ['memory'], quality: ['data', 'targets'], issues: ['data', 'issues'], about: ['settings', 'platform'],
};
// Sections that moved between pages keep their old page/view hash.
export const movedViews: Record<string, [PageId, string]> = { 'settings/audit': ['data', 'audit'] };
export function parseRoute(hash: string): [PageId, string | undefined] {
  const [head, view] = hash.replace(/^#\/?/, '').split('/');
  const moved = movedViews[`${head}/${view}`];
  if (moved) return [moved[0], moved[1]];
  if ((pages as string[]).includes(head)) return [head as PageId, view || undefined];
  const legacy = legacyRoutes[head];
  return legacy ? [legacy[0], legacy[1]] : ['overview', undefined];
}
