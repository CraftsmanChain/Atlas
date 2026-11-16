// Only public display fields are cached; credentials and live metrics never are.
const brandKey = 'atlas.public-brand.v1';
const brandFields = ['instance_name', 'product_name', 'product_tagline', 'environment'] as const;
type Brand = Record<(typeof brandFields)[number], string>;
export function readPlatformBrand<T extends Brand>(fallback: T): T {
  try {
    const value = JSON.parse(localStorage.getItem(brandKey) || 'null');
    if (value && brandFields.every(field => typeof value[field] === 'string' && value[field].length <= 256)) {
      return { ...fallback, ...Object.fromEntries(brandFields.map(field => [field, value[field]])) };
    }
  } catch { /* Storage may be unavailable; the independent request still runs. */ }
  return fallback;
}
export function writePlatformBrand(value: Brand) {
  try { localStorage.setItem(brandKey, JSON.stringify(Object.fromEntries(brandFields.map(field => [field, value[field]])))); } catch { /* Optional cache. */ }
}
export async function fetchJSON(url: string, signal?: AbortSignal) {
  const response = await fetch(url, { signal: AbortSignal.any([AbortSignal.timeout(15000), ...(signal ? [signal] : [])]) });
  if (!response.ok) throw new Error(`HTTP ${response.status}`);
  return response.json();
}

// List endpoints cap `limit` server-side (inventory: 2000). Page with offset
// until meta.total rows are loaded, so fleet-wide counts are never truncated.
export async function fetchAllPages<T>(url: string, signal?: AbortSignal, pageSize = 2000, maxPages = 100): Promise<T[]> {
  const sep = url.includes('?') ? '&' : '?';
  const rows: T[] = [];
  for (let page = 0; page < maxPages; page++) {
    const data = await fetchJSON(`${url}${sep}limit=${pageSize}&offset=${page * pageSize}`, signal);
    const batch = (data.data || []) as T[];
    rows.push(...batch);
    const total = Number(data.meta?.total);
    if (batch.length === 0 || !Number.isFinite(total) || rows.length >= total) break;
  }
  return rows;
}
