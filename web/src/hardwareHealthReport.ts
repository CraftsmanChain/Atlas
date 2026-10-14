import { sha256 } from '@noble/hashes/sha2.js';

export type HealthLevel = 'critical' | 'warning' | 'attention' | 'unknown' | 'clear_for_checked_signals';
export type HealthEvidence = { id: string; field: string; value: string; unit: string };
export type HealthHit = { rule: string; level: 'critical' | 'warning' | 'attention'; evidence_id: string };
export type DiskDimension = { id: 'media' | 'endurance' | 'temperature' | 'path_power'; level: HealthLevel; state: 'checked' | 'insufficient_data' | 'not_applicable' };
export type DiskDetails = {
  media_type: 'HDD' | 'SSD' | 'unknown'; profile: string; temperature_celsius: string | null;
  wear: { field: string; value: string; unit: string; kind: 'consumed_percent' | 'normalized_index'; evidence_id: string } | null;
  dimensions: DiskDimension[]; counter_deltas: { field: string; value: string; unit: string; evidence_id: string }[];
};
export type MemoryDetails = { controller: string; driver: string; kernel_release: string; location_precision: 'controller'; ce_delta: string | null; ue_delta: string | null };
export type HealthRow = {
  rule_version?: string; memory?: MemoryDetails; disk?: DiskDetails;
  reference: string; entity_id: string; entity_type: 'disk' | 'nvme_controller' | 'memory_controller'; identity_status: 'bound' | 'provisional' | 'conflict';
  scope: string; node: string; generation: string; boot_id: string; serial: string; model: string; firmware: string; protocol: string; device: string;
  tool_version: string; source: string; source_kind: 'fixture' | 'operator_snapshot' | ''; input_sha256: string;
  observed_at: string; available_at: string; freshness: 'unknown' | 'future' | 'stale' | 'fresh_at_cutoff';
  support: 'unsupported' | 'fixture_verified'; assessment_status: 'assessed' | 'insufficient_data'; level: HealthLevel;
  exit_code: number | null; media_delta: string | null; baseline_sha256: string; trend_status: 'unavailable' | 'available' | 'invalid_baseline' | 'counter_reset';
  blockers: string[]; hits: HealthHit[]; evidence: HealthEvidence[];
};
export type HealthReport = {
  schema: 'hardware-health-report-v1'; input_manifest_sha256: string; rule_version: 'disk-health-v1.0.0' | 'hardware-health-v1.1.0' | 'hardware-health-v1.2.0' | 'hardware-health-v1.2.1' | 'hardware-health-v1.3.0'; mode: 'offline_replay' | 'online_cache'; as_of: string;
  alerts_emitted: false; actions_executed: false;
  summary: { total: number; assessed: number; unknown: number; by_level: Record<string, number> }; rows: HealthRow[];
};
export type ImportedHealthReport = { report: HealthReport; sha256: string; fileName: string };
export const MAX_BUNDLE_BYTES = 4 * 1024 * 1024;
export const levels: HealthLevel[] = ['critical', 'warning', 'attention', 'unknown', 'clear_for_checked_signals'];

function fail(reason: string): never { throw new Error(reason); }
function object(value: unknown): Record<string, unknown> {
  if (!value || typeof value !== 'object' || Array.isArray(value)) fail('expected_object');
  return value as Record<string, unknown>;
}
function text(value: unknown, max = 512): string {
  if (typeof value !== 'string' || value.length > max) fail('invalid_text');
  if (Array.from(value).some(character => { const code = character.charCodeAt(0); return code < 32 || code === 127 || (code >= 0x202a && code <= 0x202e) || (code >= 0x2066 && code <= 0x2069); })) fail('invalid_text');
  return value;
}
function list(value: unknown, max: number): unknown[] { if (!Array.isArray(value) || value.length > max) fail('invalid_array'); return value; }
function choice(value: unknown, choices: readonly string[]): string { const result = text(value); if (!choices.includes(result)) fail('unsupported_value'); return result; }
function hash(value: unknown, optional = false): string { const result = text(value, 64); if (!(optional && result === '') && !/^[a-f0-9]{64}$/.test(result)) fail('invalid_sha256'); return result; }
function date(value: unknown): string { const result = text(value, 40); if (!/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d{1,9})?(Z|[+-]\d{2}:\d{2})$/.test(result) || !Number.isFinite(Date.parse(result))) fail('invalid_time'); return result; }
function decimal(value: unknown): string { const result = text(value, 39); if (!/^(0|[1-9][0-9]*)$/.test(result) || BigInt(result) >= (1n << 128n)) fail('invalid_decimal_counter'); return result; }

export function diskDimensionForRule(rule: string): DiskDimension['id'] {
  if (rule.includes('temperature')) return 'temperature';
  if (['endurance', 'spare', 'wear'].some(term => rule.includes(term))) return 'endurance';
  if (['crc', 'shutdown', 'backup', 'pmr'].some(term => rule.includes(term))) return 'path_power';
  return 'media';
}

function validateDisk(row: Record<string, unknown>, evidence: HealthEvidence[], hits: HealthHit[], reportRule: string): DiskDetails {
  const disk = object(row.disk);
  choice(disk.media_type, ['HDD', 'SSD', 'unknown']);
  choice(disk.profile, ['', 'nvme-standard-v1', 'ata-wdc-hdd-v1', 'ata-samsung-860-evo-v1', 'ata-samsung-pm893-v1']);
  if ((disk.profile === 'nvme-standard-v1' && (row.protocol !== 'NVMe' || disk.media_type !== 'SSD')) ||
      (disk.profile === 'ata-wdc-hdd-v1' && (row.protocol !== 'ATA' || disk.media_type !== 'HDD' || row.model !== 'WDC WD140EDFZ-11A0VA0' || row.firmware !== '81.00A81')) ||
      (disk.profile === 'ata-samsung-860-evo-v1' && (row.protocol !== 'ATA' || disk.media_type !== 'SSD' || row.model !== 'Samsung SSD 860 EVO 500GB' || row.firmware !== 'RVT02B6Q')) ||
      (disk.profile === 'ata-samsung-pm893-v1' && (reportRule !== 'hardware-health-v1.3.0' || row.rule_version !== 'disk-health-v1.2.0' || row.protocol !== 'ATA' || disk.media_type !== 'SSD' || row.model !== 'SAMSUNG MZ7L3480HCHQ-00B7C' || row.firmware !== 'JXTC404Q'))) fail('invalid_disk_profile');
  const linked = (field: unknown, value: unknown, unit: unknown, id?: unknown) => {
    text(field, 160); decimal(value); text(unit, 40);
    if (!evidence.some(item => item.field === field && item.value === value && item.unit === unit && (id === undefined || item.id === hash(id)))) fail('missing_disk_evidence');
  };
  if (disk.temperature_celsius !== null) linked(row.protocol === 'NVMe' ? 'nvme.temperature' : 'ata.temperature', disk.temperature_celsius, 'celsius');
  if (disk.wear !== null) {
    const wear = object(disk.wear);
    linked(wear.field, wear.value, wear.unit, wear.evidence_id);
    if (BigInt(String(wear.value)) > 255n || !((disk.profile === 'nvme-standard-v1' && wear.field === 'nvme.percentage_used' && wear.kind === 'consumed_percent' && wear.unit === 'percent') ||
      (['ata-samsung-860-evo-v1', 'ata-samsung-pm893-v1'].includes(String(disk.profile)) && wear.field === 'ata.177.normalized' && wear.kind === 'normalized_index' && wear.unit === 'normalized'))) fail('invalid_wear_semantics');
  }
  const severity = { critical: 3, warning: 2, attention: 1, unknown: 0, clear_for_checked_signals: 0 };
  const dimensionIDs = new Set<string>();
  const dimensions = list(disk.dimensions, 4).map(value => {
    const dimension = object(value);
    const id = choice(dimension.id, ['media', 'endurance', 'temperature', 'path_power']);
    if (dimensionIDs.has(id)) fail('duplicate_disk_dimension'); dimensionIDs.add(id);
    choice(dimension.level, levels);
    let state = row.assessment_status === 'assessed' ? 'checked' : 'insufficient_data';
    if (id === 'endurance' && disk.media_type === 'HDD') state = 'not_applicable';
    if (id === 'endurance' && row.protocol === 'ATA' && disk.media_type !== 'HDD') state = 'insufficient_data';
    if (id === 'temperature' && row.protocol === 'ATA') state = 'insufficient_data';
    const max = hits.filter(hit => diskDimensionForRule(hit.rule) === id).reduce((value, hit) => Math.max(value, severity[hit.level]), 0);
    const expectedLevel = max ? ['unknown', 'attention', 'warning', 'critical'][max] : state === 'checked' ? 'clear_for_checked_signals' : 'unknown';
    if (dimension.state !== state || dimension.level !== expectedLevel) fail('disk_dimension_inconsistent');
    return dimension as DiskDimension;
  });
  if (dimensions.length !== 4) fail('missing_disk_dimensions');
  const fields = new Set<string>();
  const deltas = list(disk.counter_deltas, 3).map(value => {
    const delta = object(value); const field = text(delta.field, 160);
    if (!['ata.5.raw', 'ata.199.raw', 'ata.187.raw'].includes(field) || fields.has(field) || row.protocol !== 'ATA' || (field === 'ata.187.raw' && disk.media_type !== 'SSD')) fail('invalid_disk_delta');
    fields.add(field);
    linked(`${field}_delta`, delta.value, delta.unit, delta.evidence_id);
    if (!evidence.some(item => item.field === field && item.unit === delta.unit) || BigInt(String(delta.value)) >= 1n << 64n) fail('invalid_disk_delta_unit');
    return delta as DiskDetails['counter_deltas'][number];
  });
  if (row.protocol === 'ATA' && ((row.trend_status === 'available') !== (deltas.length > 0) || row.media_delta !== null || (deltas.length > 0 && (deltas.length !== (disk.media_type === 'SSD' ? 3 : 2) || !row.baseline_sha256)))) fail('invalid_ata_delta_state');
  if (row.assessment_status === 'assessed' && (!disk.profile || disk.temperature_celsius === null || (disk.media_type === 'SSD' && disk.wear === null))) fail('missing_assessed_disk_details');
  if (disk.profile === 'ata-samsung-pm893-v1' && row.assessment_status === 'assessed' && ![5, 187, 199, 197].every(id =>
    evidence.some(item => item.field === `ata.${id}.raw` && item.unit === (id === 197 ? 'sectors' : 'count')))) fail('missing_pm893_checked_evidence');
  return { ...disk, dimensions, counter_deltas: deltas } as DiskDetails;
}

export function validateReport(value: unknown): HealthReport {
  const report = object(value);
  if (report.schema !== 'hardware-health-report-v1' || !['disk-health-v1.0.0', 'hardware-health-v1.1.0', 'hardware-health-v1.2.0', 'hardware-health-v1.2.1', 'hardware-health-v1.3.0'].includes(String(report.rule_version)) || report.mode !== 'offline_replay' || report.alerts_emitted !== false || report.actions_executed !== false) fail('unsupported_report_contract');
  const dimensionalReport = ['hardware-health-v1.2.0', 'hardware-health-v1.2.1', 'hardware-health-v1.3.0'].includes(String(report.rule_version));
  date(report.as_of);
  hash(report.input_manifest_sha256);
  const refs = new Set<string>();
  const boundIdentities = new Set<string>();
  const rows = list(report.rows, 200).map(value => {
    const row = object(value);
    for (const field of ['reference', 'scope', 'node', 'generation', 'boot_id', 'serial', 'model', 'firmware', 'protocol', 'device', 'tool_version', 'source']) text(row[field]);
    if (!row.reference || refs.has(String(row.reference))) fail('duplicate_or_missing_reference');
    refs.add(String(row.reference));
    hash(row.entity_id); hash(row.input_sha256); hash(row.baseline_sha256, true);
    date(row.observed_at); date(row.available_at);
    choice(row.entity_type, ['disk', 'nvme_controller', 'memory_controller']); choice(row.identity_status, ['bound', 'provisional', 'conflict']);
    const isMemory = row.entity_type === 'memory_controller';
    if (isMemory) {
      if (!['hardware-health-v1.1.0', 'hardware-health-v1.2.0', 'hardware-health-v1.2.1', 'hardware-health-v1.3.0'].includes(String(report.rule_version)) || row.rule_version !== 'memory-health-v0.1.0' || row.protocol !== 'EDAC' || row.media_delta !== null || row.disk !== undefined) fail('invalid_memory_contract');
      const memory = object(row.memory);
      for (const field of ['controller', 'driver', 'kernel_release']) text(memory[field], 160);
      if (memory.location_precision !== 'controller') fail('invalid_memory_location');
      if (memory.ce_delta !== null) decimal(memory.ce_delta);
      if (memory.ue_delta !== null) decimal(memory.ue_delta);
      if ((row.trend_status === 'available') !== (memory.ce_delta !== null && memory.ue_delta !== null) || (memory.ce_delta === null) !== (memory.ue_delta === null)) fail('invalid_memory_delta');
      if (row.trend_status === 'available' && !row.baseline_sha256) fail('missing_memory_baseline');
    } else if (row.memory !== undefined) fail('unexpected_memory_details');
    if (row.identity_status === 'bound') { if (boundIdentities.has(String(row.entity_id))) fail('duplicate_bound_identity'); boundIdentities.add(String(row.entity_id)); }
    choice(row.source_kind, ['fixture', 'operator_snapshot', '']); choice(row.support, ['unsupported', 'fixture_verified']);
    choice(row.freshness, ['unknown', 'future', 'stale', 'fresh_at_cutoff']); choice(row.assessment_status, ['assessed', 'insufficient_data']); choice(row.level, levels);
    choice(row.trend_status, ['unavailable', 'available', 'invalid_baseline', 'counter_reset']);
    if (row.exit_code !== null && (!Number.isInteger(row.exit_code) || Number(row.exit_code) < 0 || Number(row.exit_code) > 255)) fail('invalid_exit_code');
    if (row.media_delta !== null) decimal(row.media_delta);
    const blockers = list(row.blockers, 64).map(value => text(value, 160));
    const evidenceIDs = new Set<string>();
    const evidence = list(row.evidence, 64).map(value => {
      const item = object(value); const id = hash(item.id);
      if (evidenceIDs.has(id)) fail('duplicate_evidence'); evidenceIDs.add(id);
      text(item.field, 160); text(item.unit, 40); text(item.value);
      if (String(item.field).startsWith('nvme.') || String(item.field).startsWith('edac.')) decimal(item.value);
      if (/^ata\.(\d+\.(raw|raw_delta|normalized)|temperature)$/.test(String(item.field))) { decimal(item.value); if (BigInt(String(item.value)) >= 1n << 64n) fail('invalid_ata_counter'); }
      return item as HealthEvidence;
    });
    const hits = list(row.hits, 32).map(value => {
      const hit = object(value); text(hit.rule, 160); choice(hit.level, ['critical', 'warning', 'attention']);
      if (!evidenceIDs.has(hash(hit.evidence_id))) fail('missing_hit_evidence');
      return hit as HealthHit;
    });
    const severity = { critical: 3, warning: 2, attention: 1, unknown: 0, clear_for_checked_signals: 0 };
    const max = hits.reduce((value, hit) => Math.max(value, severity[hit.level]), 0);
    if (severity[row.level as HealthLevel] !== max) fail('severity_inconsistent');
    if (row.assessment_status === 'assessed') {
      if (blockers.length || row.identity_status !== 'bound' || row.freshness !== 'fresh_at_cutoff' || row.support !== 'fixture_verified' || row.level === 'unknown') fail('invalid_assessed_state');
      if (isMemory) {
        const memory = object(row.memory);
        if (['scope', 'node', 'generation', 'boot_id', 'model', 'source'].some(field => !String(row[field]).trim()) || !row.source_kind || !String(memory.driver).trim() || !String(memory.kernel_release).trim() || !/^mc[0-9]{1,4}$/.test(String(memory.controller)) || row.exit_code !== 0 || row.tool_version !== 'Linux EDAC sysfs v1') fail('invalid_memory_metadata');
      } else if (['scope', 'generation', 'serial', 'model', 'firmware', 'source', 'tool_version'].some(field => !String(row[field]).trim()) || !row.source_kind ||
        !((row.protocol === 'NVMe' && row.entity_type === 'nvme_controller') || (dimensionalReport && row.protocol === 'ATA' && row.entity_type === 'disk')) || row.exit_code === null || (Number(row.exit_code) & 7) !== 0) fail('invalid_assessed_metadata');
    } else if (!blockers.length || (max === 0 && row.level !== 'unknown')) fail('invalid_insufficient_state');
    if (row.freshness === 'fresh_at_cutoff' && (Date.parse(String(row.available_at)) > Date.parse(String(report.as_of)) || Date.parse(String(row.observed_at)) > Date.parse(String(row.available_at)))) fail('cutoff_violation');
    const diskRule = report.rule_version === 'hardware-health-v1.3.0' ? 'disk-health-v1.2.0' : report.rule_version === 'hardware-health-v1.2.1' ? 'disk-health-v1.1.1' : 'disk-health-v1.1.0';
    if (!isMemory && dimensionalReport && row.rule_version !== diskRule) fail('invalid_disk_rule_version');
    if (!isMemory && row.disk !== undefined) row.disk = validateDisk(row, evidence, hits, String(report.rule_version));
    if (!isMemory && dimensionalReport && row.assessment_status === 'assessed' && row.disk === undefined) fail('missing_disk_details');
    if (!isMemory && (row.protocol !== 'ATA' || row.disk === undefined) && (row.trend_status === 'available') !== (row.media_delta !== null)) fail('invalid_delta_state');
    return { ...row, blockers, evidence, hits } as HealthRow;
  });
  const summary = object(report.summary), byLevel = object(summary.by_level);
  const assessed = rows.filter(row => row.assessment_status === 'assessed').length;
  if (summary.total !== rows.length || summary.assessed !== assessed || summary.unknown !== rows.length - assessed) fail('summary_mismatch');
  const counts: Record<string, number> = {};
  for (const row of rows) counts[row.level] = (counts[row.level] || 0) + 1;
  if (Object.keys(byLevel).some(level => !levels.includes(level as HealthLevel)) || levels.some(level => (byLevel[level] || 0) !== (counts[level] || 0))) fail('level_count_mismatch');
  return { ...report, rows } as HealthReport;
}

export async function parseHealthBundle(content: string, fileName: string): Promise<ImportedHealthReport> {
  if (new TextEncoder().encode(content).byteLength > MAX_BUNDLE_BYTES) fail('bundle_too_large');
  const bundle = object(JSON.parse(content));
  if (bundle.schema !== 'hardware-health-bundle-v1' || typeof bundle.report_json !== 'string' || bundle.report_json.length > 2 * 1024 * 1024) fail('unsupported_bundle_contract');
  const expected = hash(bundle.report_sha256);
  const bytes = new TextEncoder().encode(bundle.report_json);
  // Offline reports also open on intranet HTTP, where SubtleCrypto is unavailable.
  const actual = Array.from(sha256(bytes), value => value.toString(16).padStart(2, '0')).join('');
  if (actual !== expected) fail('report_digest_mismatch');
  return { report: validateReport(JSON.parse(bundle.report_json)), sha256: actual, fileName };
}

export function selectRows(rows: HealthRow[], query: string, level: string, quality: string, order: string): HealthRow[] {
  const needle = query.trim().toLocaleLowerCase();
  const result = rows.filter(row => (!needle || [row.reference, row.node, row.serial, row.model, row.device, row.scope].some(value => value.toLocaleLowerCase().includes(needle))) && (!level || row.level === level) && (!quality || (quality === 'stale' ? row.freshness === 'stale' : row.assessment_status === quality)));
  const priority = { critical: 4, warning: 3, attention: 2, unknown: 1, clear_for_checked_signals: 0 };
  return result.sort((a, b) => (order === 'observed' ? Date.parse(b.observed_at) - Date.parse(a.observed_at) : priority[b.level] - priority[a.level]) || a.reference.localeCompare(b.reference));
}
