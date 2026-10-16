import type { HealthRow } from './hardwareHealthReport';

export const DISK_HISTORY_WINDOW_CONTRACT = 'disk-history-window-v2';
export type DiskWindowFeature = {
  field: string; kind: 'counter_delta' | 'gauge_change' | 'minimum' | 'maximum' | 'mean';
  value?: string; unit: string; status: 'available' | 'partial' | 'blocked'; blockers: string[];
  start_at?: string; end_at?: string;
};
export type DiskHistoryWindow = {
  contract_version?: string; window: '1h' | '24h' | '7d'; sample_count: number;
  first_observed_at?: string; last_observed_at?: string; max_gap_seconds?: number;
  coverage_status: 'collecting' | 'partial' | 'complete' | 'unknown';
  expected_sample_count?: number; occupied_sample_count?: number; coverage_ratio?: number; covered_seconds?: number;
  window_start_at?: string; window_end_at?: string;
  excluded_future_count?: number; excluded_invalid_time_count?: number; duplicate_observation_count?: number;
  blockers: string[]; features: DiskWindowFeature[];
};
export type DiskHistoryPayload = { data: HealthRow[]; meta: { total: number }; windows: DiskHistoryWindow[] };

function fail(): never { throw new Error('invalid_disk_history_contract'); }
function object(value: unknown): Record<string, unknown> {
  if (!value || typeof value !== 'object' || Array.isArray(value)) fail();
  return value as Record<string, unknown>;
}
function text(value: unknown): string {
  if (typeof value !== 'string' || value.length > 256 || Array.from(value).some(character => {
    const code = character.charCodeAt(0);
    return code < 32 || code === 127 || (code >= 0x202a && code <= 0x202e) || (code >= 0x2066 && code <= 0x2069);
  })) fail();
  return value;
}
function count(value: unknown): number {
  if (typeof value !== 'number' || !Number.isSafeInteger(value) || value < 0) fail();
  return value;
}
function time(value: unknown): string | undefined {
  if (value === undefined || value === null) return undefined;
  const result = text(value);
  if (!/^\d{4}-\d{2}-\d{2}T/.test(result) || !Number.isFinite(Date.parse(result))) fail();
  return result;
}
function reasons(value: unknown): string[] {
  if (!Array.isArray(value) || value.length > 100) fail();
  return value.map(text);
}
function feature(value: unknown): DiskWindowFeature {
  const row = object(value), kind = text(row.kind), status = text(row.status);
  if (!['counter_delta', 'gauge_change', 'minimum', 'maximum', 'mean'].includes(kind) || !['available', 'partial', 'blocked'].includes(status)) fail();
  const exact = row.value === undefined ? undefined : text(row.value);
  if (status === 'blocked' ? exact !== undefined : exact === undefined) fail();
  if (exact !== undefined) {
    const pattern = kind === 'gauge_change' ? /^-?(0|[1-9][0-9]*)$/ : kind === 'mean' ? /^(0|[1-9][0-9]*)\.[0-9]{3}$/ : /^(0|[1-9][0-9]*)$/;
    if (!pattern.test(exact)) fail();
    if (kind === 'counter_delta' && BigInt(exact) >= 1n << 128n) fail();
  }
  return { field: text(row.field), kind: kind as DiskWindowFeature['kind'], value: exact, unit: text(row.unit), status: status as DiskWindowFeature['status'], blockers: reasons(row.blockers), start_at: time(row.start_at), end_at: time(row.end_at) };
}
export function parseDiskHistoryWindow(value: unknown): DiskHistoryWindow {
  const row = object(value), window = text(row.window);
  if (!['1h', '24h', '7d'].includes(window)) fail();
  const common = { window: window as DiskHistoryWindow['window'], sample_count: count(row.sample_count), first_observed_at: time(row.first_observed_at), last_observed_at: time(row.last_observed_at), max_gap_seconds: row.max_gap_seconds == null ? undefined : count(row.max_gap_seconds) };
  if (row.contract_version !== DISK_HISTORY_WINDOW_CONTRACT) {
    return { ...common, coverage_status: 'unknown', blockers: [row.contract_version === undefined ? 'legacy_window_contract_unverified' : 'unsupported_window_contract'], features: [] };
  }
  const status = text(row.coverage_status), ratio = row.coverage_ratio;
  if (!['collecting', 'partial', 'complete'].includes(status) || typeof ratio !== 'number' || !Number.isFinite(ratio) || ratio < 0 || ratio > 1 || !Array.isArray(row.features) || row.features.length > 20) fail();
  const expected = count(row.expected_sample_count), occupied = count(row.occupied_sample_count);
  if (expected === 0 || occupied > expected) fail();
  return { ...common, contract_version: DISK_HISTORY_WINDOW_CONTRACT, coverage_status: status as DiskHistoryWindow['coverage_status'], expected_sample_count: expected, occupied_sample_count: occupied, coverage_ratio: ratio, covered_seconds: count(row.covered_seconds), window_start_at: time(row.window_start_at), window_end_at: time(row.window_end_at), excluded_future_count: count(row.excluded_future_count), excluded_invalid_time_count: count(row.excluded_invalid_time_count), duplicate_observation_count: count(row.duplicate_observation_count), blockers: reasons(row.blockers), features: row.features.map(feature) };
}
export function parseDiskHistoryPayload(value: unknown): DiskHistoryPayload {
  const payload = object(value), meta = object(payload.meta);
  if (!Array.isArray(payload.data) || !Array.isArray(payload.windows) || payload.windows.length > 3) fail();
  return { data: payload.data as HealthRow[], meta: { total: count(meta.total) }, windows: payload.windows.map(parseDiskHistoryWindow) };
}
