import type { DiskInstallation, HealthReport, HealthRow } from './hardwareHealthReport';

export const DISK_INSTALLATION_VIEW_CONTRACT = 'disk-installation-view-v1';
export const diskInstallationViews = ['active', 'historical', 'unregistered', 'all'] as const;
export type DiskInstallationView = typeof diskInstallationViews[number];
export type DiskInstallationSummary = {
  contract_version: typeof DISK_INSTALLATION_VIEW_CONTRACT; view: DiskInstallationView;
  active_count: number; historical_count: number; unregistered_count: number; registered_scope_count: number;
};
export type DiskInstallationPayload = {
  data: HealthRow[]; installation: DiskInstallationSummary | null; summary: HealthReport['summary'];
  meta: { total: number; generated_at?: string };
};

function fail(): never { throw new Error('invalid_disk_installation_contract'); }
function object(value: unknown): Record<string, unknown> {
  if (!value || typeof value !== 'object' || Array.isArray(value)) fail();
  return value as Record<string, unknown>;
}
function count(value: unknown): number {
  if (typeof value !== 'number' || !Number.isSafeInteger(value) || value < 0) fail();
  return value;
}
function text(value: unknown, max = 128): string {
  if (typeof value !== 'string' || !value || value.length > max || Array.from(value).some(character => {
    const code = character.charCodeAt(0);
    return code < 32 || code === 127 || (code >= 0x202a && code <= 0x202e) || (code >= 0x2066 && code <= 0x2069);
  })) fail();
  return value;
}
function time(value: unknown): string | undefined {
  if (value === undefined || value === null) return undefined;
  const result = text(value, 40);
  if (!/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d{1,9})?(Z|[+-]\d{2}:\d{2})$/.test(result) || !Number.isFinite(Date.parse(result))) fail();
  return result;
}
export function diskInstallationView(value: unknown): DiskInstallationView {
  if (!diskInstallationViews.includes(value as DiskInstallationView)) fail();
  return value as DiskInstallationView;
}
export function diskInstallationListURL(value: unknown): string {
  return `/api/v1/hardware/disks?limit=1000&view=${diskInstallationView(value)}`;
}
function installation(value: unknown): DiskInstallation {
  const row = object(value), state = text(row.state);
  if (!['active', 'historical', 'unregistered'].includes(state)) fail();
  const result: DiskInstallation = { state: state as DiskInstallation['state'] };
  if (row.installation_id !== undefined) {
    result.installation_id = text(row.installation_id, 64);
    if (!/^[a-f0-9]{64}$/.test(result.installation_id)) fail();
  }
  if (row.scope_id !== undefined) result.scope_id = text(row.scope_id);
  if (row.roster_version !== undefined) {
    const version = text(row.roster_version, 20);
    if (!/^(0|[1-9][0-9]*)$/.test(version) || BigInt(version) >= 1n << 64n) fail();
    result.roster_version = version;
  }
  result.opened_at = time(row.opened_at); result.closed_at = time(row.closed_at);
  if (result.opened_at && result.closed_at && Date.parse(result.closed_at) < Date.parse(result.opened_at)) fail();
  if (state === 'active' && result.closed_at) fail();
  return result;
}
function healthSummary(value: unknown, rows: HealthRow[]): HealthReport['summary'] {
  const levels = ['critical', 'warning', 'attention', 'unknown', 'clear_for_checked_signals'];
  if (value === undefined) {
    const by_level: Record<string, number> = {};
    for (const row of rows) by_level[row.level] = (by_level[row.level] || 0) + 1;
    const assessed = rows.filter(row => row.assessment_status === 'assessed').length;
    return { total: rows.length, assessed, unknown: rows.length - assessed, by_level };
  }
  const source = object(value), byLevel = object(source.by_level), by_level: Record<string, number> = {};
  const total = count(source.total), assessed = count(source.assessed), unknown = count(source.unknown);
  if (assessed + unknown !== total) fail();
  for (const [level, number] of Object.entries(byLevel)) {
    if (!levels.includes(level)) fail();
    by_level[level] = count(number);
  }
  if (Object.values(by_level).reduce((sum, value) => sum + value, 0) !== total) fail();
  if (source.risk_count !== undefined && count(source.risk_count) !== ['critical', 'warning', 'attention'].reduce((sum, level) => sum + (by_level[level] || 0), 0)) fail();
  if (source.stale_count !== undefined && count(source.stale_count) > total) fail();
  return { total, assessed, unknown, by_level };
}
export function parseDiskInstallationPayload(value: unknown, requestedView: unknown = 'active'): DiskInstallationPayload {
  const requested = diskInstallationView(requestedView), payload = object(value);
  if (!Array.isArray(payload.data)) fail();
  const sourceSummary = payload.summary === undefined ? undefined : object(payload.summary);
  const verified = sourceSummary?.contract_version === DISK_INSTALLATION_VIEW_CONTRACT;
  if (sourceSummary?.contract_version !== undefined && !verified) fail();
  const identities = new Set<string>();
  const data = payload.data.map(value => {
    const row = object(value);
    if (typeof row.entity_id !== 'string' || identities.has(row.entity_id) || !['critical', 'warning', 'attention', 'unknown', 'clear_for_checked_signals'].includes(String(row.level)) || !['assessed', 'insufficient_data'].includes(String(row.assessment_status)) || !Array.isArray(row.evidence) || !Array.isArray(row.blockers) || !Array.isArray(row.hits)) fail();
    identities.add(row.entity_id);
    const result = { ...row } as HealthRow;
    if (verified) {
      result.installation = installation(row.installation);
      if (requested !== 'all' && result.installation.state !== requested) fail();
    } else delete result.installation;
    return result;
  });
  const summary = healthSummary(sourceSummary, data), meta = payload.meta === undefined ? {} : object(payload.meta);
  const total = meta.total === undefined ? data.length : count(meta.total);
  if (data.length > total || total > summary.total) fail();
  const rowSummary = healthSummary(undefined, data);
  if (rowSummary.assessed > summary.assessed || rowSummary.unknown > summary.unknown || Object.entries(rowSummary.by_level).some(([level, number]) => number > (summary.by_level[level] || 0))) fail();
  let result: DiskInstallationSummary | null = null;
  if (verified && sourceSummary) {
    const view = diskInstallationView(sourceSummary.view);
    if (view !== requested) fail();
    const active_count = count(sourceSummary.active_count), historical_count = count(sourceSummary.historical_count), unregistered_count = count(sourceSummary.unregistered_count), registered_scope_count = count(sourceSummary.registered_scope_count);
    const all = active_count + historical_count + unregistered_count;
    const selectedTotal = { active: active_count, historical: historical_count, unregistered: unregistered_count, all }[view];
    if (!Number.isSafeInteger(all) || data.filter(row => row.installation?.state === 'active').length > active_count || data.filter(row => row.installation?.state === 'historical').length > historical_count || data.filter(row => row.installation?.state === 'unregistered').length > unregistered_count) fail();
    if (!Number.isSafeInteger(selectedTotal) || summary.total !== selectedTotal) fail();
    result = { contract_version: DISK_INSTALLATION_VIEW_CONTRACT, view, active_count, historical_count, unregistered_count, registered_scope_count };
  }
  return { data, summary, installation: result, meta: { total, generated_at: time(meta.generated_at) } };
}
