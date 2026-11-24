export type PageId = 'overview' | 'gpu' | 'memory' | 'disk' | 'data' | 'settings';
export const pages: PageId[] = ['overview', 'gpu', 'memory', 'disk', 'data', 'settings'];
// Pre-2026-11-24 hashes keep working after the navigation was consolidated.
export const legacyRoutes: Record<string, [PageId, string?]> = {
  gpus: ['gpu', 'health'], models: ['gpu', 'ranking'], incidents: ['gpu', 'events'], validations: ['gpu', 'degradation'],
  hardware: ['memory'], quality: ['data', 'targets'], issues: ['data', 'issues'], about: ['settings', 'platform'],
};
export function parseRoute(hash: string): [PageId, string | undefined] {
  const [head, view] = hash.replace(/^#\/?/, '').split('/');
  if ((pages as string[]).includes(head)) return [head as PageId, view || undefined];
  const legacy = legacyRoutes[head];
  return legacy ? [legacy[0], legacy[1]] : ['overview', undefined];
}
