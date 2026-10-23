import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';
import { levels, parseHealthBundle, selectRows, validateReport, type HealthReport } from '../src/hardwareHealthReport.ts';
import { parseDiskHistoryPayload, parseDiskHistoryWindow } from '../src/diskHistoryWindows.ts';
import { diskInstallationListURL, parseDiskInstallationPayload } from '../src/diskInstallationViews.ts';

const digest = async (value: string) => createHash('sha256').update(value, 'utf8').digest('hex');
const fixture = () => ({
  schema: 'hardware-health-report-v1', input_manifest_sha256: 'd'.repeat(64), rule_version: 'disk-health-v1.0.0', mode: 'offline_replay', as_of: '2026-10-09T00:00:00Z', alerts_emitted: false, actions_executed: false,
  summary: { total: 1, assessed: 1, unknown: 0, by_level: { clear_for_checked_signals: 1 } },
  rows: [{ reference: 'fixture-a', entity_id: 'a'.repeat(64), entity_type: 'nvme_controller', identity_status: 'bound', scope: 'fixture', node: 'node-a', generation: '1', boot_id: 'boot-a', serial: 'serial-a', model: 'fixture-model', firmware: '1', protocol: 'NVMe', device: '/dev/nvme0', tool_version: 'smartctl 7.4', source: 'fixture', source_kind: 'fixture', input_sha256: 'b'.repeat(64), observed_at: '2026-10-08T23:59:00Z', available_at: '2026-10-08T23:59:01Z', freshness: 'fresh_at_cutoff', support: 'fixture_verified', assessment_status: 'assessed', level: 'clear_for_checked_signals', exit_code: 0, media_delta: null, baseline_sha256: '', trend_status: 'unavailable', blockers: [], hits: [], evidence: [{ id: 'c'.repeat(64), field: 'nvme.media_errors', value: '340282366920938463463374607431768211455', unit: 'count' }] }],
});

const historyWindow = (status = 'complete') => ({
  contract_version: 'disk-history-window-v2', window: '1h', sample_count: 3,
  first_observed_at: '2026-10-15T00:00:00Z', last_observed_at: '2026-10-15T01:00:00Z', max_gap_seconds: 1800,
  window_start_at: '2026-10-15T00:00:00Z', window_end_at: '2026-10-15T01:00:00Z',
  coverage_status: status, expected_sample_count: 3, occupied_sample_count: 3, coverage_ratio: 1, covered_seconds: 3600,
  excluded_future_count: 0, excluded_invalid_time_count: 0, duplicate_observation_count: 0,
  blockers: [], features: [{ field: 'nvme.media_errors', kind: 'counter_delta', value: '340282366920938463463374607431768211455', unit: 'count', status: 'available', blockers: [], start_at: '2026-10-15T00:00:00Z', end_at: '2026-10-15T01:00:00Z' }],
});

const installationPayload = (view = 'active') => {
  const states = view === 'all' ? ['active', 'historical', 'unregistered'] : [view];
  return {
    data: states.map((state, index) => ({ ...fixture().rows[0], entity_id: String(index + 1).repeat(64), installation: state === 'unregistered' ? { state } : { state, installation_id: String(index + 1).repeat(64), scope_id: 'node-generation:synthetic', roster_version: '18446744073709551615', opened_at: '2026-10-15T00:00:00Z', ...(state === 'historical' ? { closed_at: '2026-10-16T00:00:00Z' } : {}) }, current: true })),
    summary: { contract_version: 'disk-installation-view-v1', view, active_count: 1, historical_count: 1, unregistered_count: 1, registered_scope_count: 1, total: states.length, assessed: states.length, unknown: 0, risk_count: 0, stale_count: 0, by_level: { clear_for_checked_signals: states.length } },
    meta: { total: states.length, generated_at: '2026-10-20T00:00:00Z' },
  };
};

test('installation views use explicit membership and preserve exact roster versions and counters', () => {
  for (const view of ['active', 'historical', 'unregistered', 'all']) {
    const parsed = parseDiskInstallationPayload(installationPayload(view), view);
    assert.equal(parsed.installation?.view, view);
    assert.equal(parsed.installation?.active_count, 1);
    assert.equal(parsed.data[0].installation?.state, view === 'all' ? 'active' : view);
    if (view !== 'unregistered') assert.equal(parsed.data[0].installation?.roster_version, '18446744073709551615');
    assert.equal(parsed.data[0].evidence[0].value, '340282366920938463463374607431768211455');
    assert(diskInstallationListURL(view).endsWith(`view=${view}`));
  }
  const historical = parseDiskInstallationPayload(installationPayload('historical'), 'historical');
  assert.equal(historical.data[0].installation?.state, 'historical', 'per-entity latest flag does not imply active membership');
});

test('zero active installations remain empty with unregistered evidence available separately', () => {
  const source = installationPayload();
  source.data = [];
  Object.assign(source.summary, { active_count: 0, historical_count: 0, unregistered_count: 6, registered_scope_count: 0, total: 0, assessed: 0, by_level: {} });
  source.meta.total = 0;
  const parsed = parseDiskInstallationPayload(source);
  assert.equal(parsed.data.length, 0);
  assert.equal(parsed.summary.total, 0);
  assert.equal(parsed.installation?.active_count, 0);
  assert.equal(parsed.installation?.unregistered_count, 6);
});

test('installation summaries permit a filtered page total below the view denominator', () => {
  const source = installationPayload('all');
  source.data = source.data.slice(0, 1);
  source.meta.total = 1;
  const parsed = parseDiskInstallationPayload(source, 'all');
  assert.equal(parsed.meta.total, 1);
  assert.equal(parsed.summary.total, 3);
  assert.equal(parsed.data.length, 1);
});

test('legacy online evidence has unverified membership even when rows claim active or current', () => {
  const source = installationPayload();
  const { contract_version: _contract, ...legacySummary } = source.summary;
  void _contract;
  const parsed = parseDiskInstallationPayload({ ...source, summary: legacySummary });
  assert.equal(parsed.installation, null);
  assert.equal(parsed.data[0].installation, undefined);
  assert.equal(source.data[0].installation.state, 'active', 'consumer must not mutate its input');
  const old = parseDiskInstallationPayload({ data: source.data });
  assert.equal(old.installation, null);
  assert.equal(old.summary.total, 1);
});

test('installation consumers reject invalid views, counts and membership conflicts', () => {
  for (const view of ['', 'invalid', 'active&view=all', null]) assert.throws(() => diskInstallationListURL(view));
  const changes: Array<(source: ReturnType<typeof installationPayload>) => void> = [
    source => { source.summary.view = 'invalid'; },
    source => { source.summary.view = 'all'; },
    source => { source.summary.active_count = -1; },
    source => { source.summary.historical_count = -1; },
    source => { source.summary.unregistered_count = -1; },
    source => { source.summary.registered_scope_count = -1; },
    source => { source.summary.active_count = 2; },
    source => { source.summary.assessed = 0; },
    source => { source.summary.by_level.clear_for_checked_signals = 0; },
    source => { Object.assign(source.summary.by_level, { clear_for_checked_signals: 0, warning: 1 }); source.summary.risk_count = 1; },
    source => { source.meta.total = 2; },
    source => { source.data[0].installation.state = 'historical'; },
    source => { source.data[0].installation.roster_version = '18446744073709551616'; },
    source => { source.summary.contract_version = 'disk-installation-view-v2'; },
  ];
  changes.forEach(change => { const source = installationPayload(); change(source); assert.throws(() => parseDiskInstallationPayload(source), /invalid_disk_installation_contract/); });
  const numeric = installationPayload();
  assert.throws(() => parseDiskInstallationPayload({ ...numeric, data: [{ ...numeric.data[0], installation: { ...numeric.data[0].installation, roster_version: 42 } }] }));
});

test('history windows preserve v2 completeness and independent exact feature values', () => {
  const values = ['9007199254740993', '9007199254740994', '340282366920938463463374607431768211455'];
  const windows = ['1h', '24h', '7d'].map((window, index) => ({ ...historyWindow(), window, features: [{ ...historyWindow().features[0], value: values[index] }] }));
  const parsed = parseDiskHistoryPayload({ data: [], meta: { total: 0 }, windows });
  assert.deepEqual(parsed.windows.map(window => window.features[0].value), values);
  assert.deepEqual(parsed.windows.map(window => window.coverage_status), ['complete', 'complete', 'complete']);
  assert.equal(parsed.meta.total, 0);
});

test('history legacy and unsupported contracts never imply completeness or window growth', () => {
  for (const contract_version of [undefined, 'disk-history-window-v99']) {
    const parsed = parseDiskHistoryWindow({ ...historyWindow(), contract_version });
    assert.equal(parsed.coverage_status, 'unknown');
    assert.equal(parsed.coverage_ratio, undefined);
    assert.deepEqual(parsed.features, []);
    assert.equal(parsed.blockers.length, 1);
  }
  const old = parseDiskHistoryWindow({ window: '24h', sample_count: 2, coverage_status: 'available', latest_media_delta: '123' });
  assert.equal(old.coverage_status, 'unknown');
  assert.equal(old.sample_count, 2);
  assert.deepEqual(old.features, []);
});

test('history partial values, signed gauges, means and missing blocked values stay distinct', () => {
  const row = historyWindow('partial');
  const partial = { ...row.features[0], status: 'partial', blockers: ['window_start_anchor_missing'] };
  const features = [partial, { ...partial, field: 'nvme.available_spare', kind: 'gauge_change', value: '-2' }, { ...partial, field: 'nvme.temperature', kind: 'mean', value: '30.333' }, { ...partial, field: 'nvme.percentage_used', kind: 'gauge_change', value: undefined, status: 'blocked', blockers: ['missing_field'] }];
  const parsed = parseDiskHistoryWindow({ ...row, features, blockers: ['window_start_anchor_missing'], occupied_sample_count: 2, coverage_ratio: 2 / 3 });
  assert.equal(parsed.coverage_status, 'partial');
  assert.deepEqual(parsed.features.map(feature => feature.value), [row.features[0].value, '-2', '30.333', undefined]);
  assert.equal(parsed.features[0].status, 'partial');
  assert.deepEqual(parsed.features[3].blockers, ['missing_field']);
  const collecting = parseDiskHistoryWindow({ ...row, coverage_status: 'collecting', sample_count: 1, features: [], occupied_sample_count: 1, coverage_ratio: 1 / 3, covered_seconds: 0, max_gap_seconds: undefined });
  assert.equal(collecting.coverage_status, 'collecting');
  assert.equal(collecting.max_gap_seconds, undefined);
});

test('history v2 rejects lossy counters, invalid metadata and misleading feature availability', () => {
  const cases = [
    { ...historyWindow(), coverage_status: 'available' },
    { ...historyWindow(), expected_sample_count: undefined },
    { ...historyWindow(), coverage_ratio: 1.1 },
    { ...historyWindow(), occupied_sample_count: 4 },
    { ...historyWindow(), sample_count: Number.MAX_SAFE_INTEGER + 1 },
    { ...historyWindow(), first_observed_at: 'invalid' },
    { ...historyWindow(), features: [{ ...historyWindow().features[0], value: 42 }] },
    { ...historyWindow(), features: [{ ...historyWindow().features[0], value: '340282366920938463463374607431768211456' }] },
    { ...historyWindow(), features: [{ ...historyWindow().features[0], value: '1e6' }] },
    { ...historyWindow(), features: [{ ...historyWindow().features[0], status: 'blocked' }] },
    { ...historyWindow(), features: [{ ...historyWindow().features[0], value: undefined }] },
  ];
  cases.forEach(value => assert.throws(() => parseDiskHistoryWindow(value), /invalid_disk_history_contract/));
});

test('frontend and framework release contracts stay aligned', () => {
  const app = readFileSync(new URL('../src/App.tsx', import.meta.url), 'utf8');
  const framework = readFileSync(new URL('../../internal/prediction/service.go', import.meta.url), 'utf8');
  const version = framework.match(/FrameworkVersion\s*=\s*"prediction-framework-(v[0-9.]+)"/)?.[1];
  assert(version);
  assert(app.includes(`version: '${version}'`));
  assert(app.includes(`${version} ·`));
  assert(app.includes('PLATFORM / v0.120.74'));
  assert(app.includes("id: 'component-health'"));
});

test('online age filters do not rewrite frozen report freshness', () => {
  const report = validateReport(fixture());
  const row = { ...report.rows[0], observation: { state: 'stale' as const, evaluated_at: '2026-10-15T00:00:00Z', max_age_seconds: 3600, age_seconds: 10000 } };
  assert.equal(selectRows([row], '', '', 'stale', 'risk').length, 1);
  assert.equal(row.freshness, 'fresh_at_cutoff');
  assert.equal(selectRows(report.rows, '', '', 'stale', 'risk').length, 0);
});

test('exact counters survive parsing without Number conversion', async () => {
  const reportJSON = JSON.stringify(fixture());
  const content = JSON.stringify({ schema: 'hardware-health-bundle-v1', report_json: reportJSON, report_sha256: await digest(reportJSON) });
  const result = await parseHealthBundle(content, 'fixture.json');
  assert.equal(result.report.rows[0].evidence[0].value, '340282366920938463463374607431768211455');
});

test('PM883 requires the exact versioned profile and preserves partial health coverage', () => {
  const report = fixture() as unknown as HealthReport;
  report.rule_version = 'hardware-health-v1.4.0';
  const row = report.rows[0];
  Object.assign(row, { rule_version: 'disk-health-v1.3.0', entity_type: 'disk', protocol: 'ATA', model: 'SAMSUNG MZ7LH480HAHQ-00005', firmware: 'HXT7B04Q' });
  row.evidence = ['ata.5.raw', 'ata.187.raw', 'ata.199.raw', 'ata.177.normalized', 'ata.temperature'].map((field, index) => ({ id: String(index + 1).repeat(64), field, value: index === 3 ? '99' : index === 4 ? '24' : '0', unit: index === 3 ? 'normalized' : index === 4 ? 'celsius' : 'count' }));
  row.disk = { media_type: 'SSD', profile: 'ata-samsung-pm883-v1', temperature_celsius: '24', wear: { field: 'ata.177.normalized', value: '99', unit: 'normalized', kind: 'normalized_index', evidence_id: row.evidence[3].id }, dimensions: [{ id: 'media', state: 'checked', level: 'clear_for_checked_signals' }, { id: 'endurance', state: 'insufficient_data', level: 'unknown' }, { id: 'temperature', state: 'insufficient_data', level: 'unknown' }, { id: 'path_power', state: 'checked', level: 'clear_for_checked_signals' }], counter_deltas: [] };
  const parsed = validateReport(report).rows[0];
  assert.equal(parsed.disk?.wear?.kind, 'normalized_index');
  assert.equal(parsed.disk?.dimensions[1].state, 'insufficient_data');
  for (const change of [
    (r: HealthReport) => { r.rows[0].firmware = 'unverified'; },
    (r: HealthReport) => { r.rows[0].model = 'SAMSUNG MZ7LH960HAJR-00005'; },
    (r: HealthReport) => { r.rule_version = 'hardware-health-v1.3.0'; r.rows[0].rule_version = 'disk-health-v1.2.0'; },
    (r: HealthReport) => { r.rows[0].disk!.wear!.field = 'ata.202.normalized'; },
    (r: HealthReport) => { r.rows[0].evidence = r.rows[0].evidence.filter(e => e.field !== 'ata.187.raw'); },
    (r: HealthReport) => { r.rows[0].disk!.dimensions[1] = { id: 'endurance', state: 'checked', level: 'clear_for_checked_signals' }; },
  ]) {
    const candidate = structuredClone(report); change(candidate); assert.throws(() => validateReport(candidate));
  }
});

test('reject tampering and unsupported safety contract', async () => {
  await assert.rejects(parseHealthBundle(JSON.stringify({ schema: 'hardware-health-bundle-v1', report_json: JSON.stringify(fixture()), report_sha256: '0'.repeat(64) }), 'bad.json'), /digest_mismatch/);
  for (const field of ['schema', 'rule_version', 'mode', 'alerts_emitted', 'actions_executed']) { const report = { ...fixture(), [field]: 'invalid' }; assert.throws(() => validateReport(report)); }
});

test('intranet HTTP imports preserve SHA-256 integrity without Web Crypto', async () => {
  const report = fixture();
  report.rows[0].node = '算力小镇-智元';
  const reportJSON = JSON.stringify(report);
  const expected = await digest(reportJSON);
  const content = JSON.stringify({ schema: 'hardware-health-bundle-v1', report_json: reportJSON, report_sha256: expected });
  const descriptor = Object.getOwnPropertyDescriptor(globalThis, 'crypto');
  Object.defineProperty(globalThis, 'crypto', { configurable: true, value: undefined });
  try {
    const result = await parseHealthBundle(content, 'http-report.json');
    assert.equal(result.sha256, expected);
    assert.equal(result.report.rows[0].node, report.rows[0].node);
    await assert.rejects(parseHealthBundle(content.replace('算力小镇-智元', 'changed'), 'tampered.json'), /digest_mismatch/);
  } finally {
    if (descriptor) Object.defineProperty(globalThis, 'crypto', descriptor);
    else Reflect.deleteProperty(globalThis, 'crypto');
  }
});

test('reject malformed rows, evidence links and inconsistent summaries', () => {
  const changes: Array<(report: ReturnType<typeof fixture>) => void> = [
    report => { report.summary.total = 10; },
    report => { report.rows[0].level = 'critical'; },
    report => { report.rows[0].evidence[0].value = '1e30'; },
    report => { report.rows[0].evidence[0].value = '340282366920938463463374607431768211456'; },
    report => { report.rows[0].node = '\u202ebad'; },
    report => { report.rows[0].entity_id = ''; },
    report => { report.rows[0].firmware = ''; },
    report => { report.rows[0].exit_code = 4; },
    report => { report.rows.push({ ...report.rows[0], reference: 'duplicate-identity' }); },
    report => { report.rows[0].available_at = '2027-01-01T00:00:00Z'; },
    report => { report.rows.push(report.rows[0]); },
    report => { report.rows[0].evidence.push(report.rows[0].evidence[0]); },
  ];
  changes.forEach(change => { const report = fixture(); change(report); assert.throws(() => validateReport(report)); });
});

test('known critical is retained separately from insufficient data', () => {
  const report = fixture() as unknown as HealthReport;
  report.rows[0].hits.push({ rule: 'media_readonly', level: 'critical', evidence_id: 'c'.repeat(64) });
  report.rows[0].level = 'critical'; report.rows[0].assessment_status = 'insufficient_data'; report.rows[0].blockers = ['missing_identity']; report.rows[0].identity_status = 'provisional';
  report.summary = { total: 1, assessed: 0, unknown: 1, by_level: { critical: 1 } };
  const value = validateReport(report); assert.equal(value.rows[0].level, 'critical'); assert.equal(value.summary.unknown, 1);
  report.rows[0].hits[0].evidence_id = 'd'.repeat(64); assert.throws(() => validateReport(report), /missing_hit_evidence/);
});

test('filtering is pure, respects data blockers and deterministic ordering', () => {
  const report = validateReport(fixture());
  const row = report.rows[0];
  const rows = levels.map((level, index) => ({ ...row, reference: `r-${index}`, level, assessment_status: (index === 0 ? 'insufficient_data' : 'assessed') as typeof row.assessment_status }));
  assert.equal(selectRows(rows, 'NODE-A', '', '', 'risk').length, 5);
  assert.deepEqual(selectRows(rows, '', '', '', 'risk').map(row => row.level), levels);
  assert.equal(selectRows(rows, '', '', 'insufficient_data', 'risk')[0].level, 'critical');
  assert.equal(selectRows(rows, 'absent', '', '', 'risk').length, 0);
  assert.equal(rows[0].reference, 'r-0');
});

test('Go CLI artifact passes browser contract', { skip: !process.env.ATLAS_HEALTH_TEST_BUNDLE }, async () => {
  const path = process.env.ATLAS_HEALTH_TEST_BUNDLE;
  if (!path) return;
  const parsed = await parseHealthBundle(readFileSync(path, 'utf8'), 'bundle.json');
  assert.equal(parsed.report.rows.length, 3);
  assert.equal(parsed.report.rows.find(row => row.protocol === 'ATA')?.support, 'fixture_verified');
});

test('D1 disk artifact preserves protocol-specific wear and four health dimensions', { skip: !process.env.ATLAS_DISK_TEST_BUNDLE }, async () => {
  const parsed = await parseHealthBundle(readFileSync(process.env.ATLAS_DISK_TEST_BUNDLE!, 'utf8'), 'disk-bundle.json');
  assert.equal(parsed.report.rule_version, 'hardware-health-v1.2.0');
  assert.equal(parsed.report.rows.length, 5);
  assert.equal(parsed.report.summary.assessed, 4);
  const ssd = parsed.report.rows.find(row => row.reference === 'public-sata-ssd')!;
  assert.equal(ssd.disk?.wear?.kind, 'normalized_index');
  assert.equal(ssd.disk?.wear?.value, '81');
  assert.equal(ssd.disk?.wear?.unit, 'normalized');
  const hdd = parsed.report.rows.find(row => row.reference === 'public-sata-hdd')!;
  assert.equal(hdd.disk?.dimensions.find(d => d.id === 'endurance')?.state, 'not_applicable');
  assert.equal(hdd.disk?.dimensions.find(d => d.id === 'temperature')?.state, 'insufficient_data');
  const unmatched = parsed.report.rows.find(row => row.reference === 'public-ata-unmatched')!;
  assert.equal(unmatched.assessment_status, 'insufficient_data');
  assert.equal(unmatched.level, 'unknown');
  assert.equal(parsed.report.rows.find(row => row.reference === 'public-nvme')?.disk?.wear?.kind, 'consumed_percent');
  for (const mutate of [
    (r: HealthReport) => { r.rows.find(row => row.reference === ssd.reference)!.disk!.wear!.unit = 'percent'; },
    (r: HealthReport) => { r.rows.find(row => row.reference === ssd.reference)!.firmware = 'unverified-fw'; },
    (r: HealthReport) => { r.rows.find(row => row.reference === ssd.reference)!.disk!.wear!.value = '99'; },
    (r: HealthReport) => { r.rows.find(row => row.reference === hdd.reference)!.disk!.dimensions[2].level = 'clear_for_checked_signals'; },
    (r: HealthReport) => { r.rows.find(row => row.reference === ssd.reference)!.disk!.dimensions.pop(); },
    (r: HealthReport) => { r.rows.find(row => row.reference === ssd.reference)!.disk!.profile = 'nvme-standard-v1'; },
    (r: HealthReport) => { r.rows.find(row => row.reference === ssd.reference)!.disk!.counter_deltas.push({field:'ata.199.raw', value:'1', unit:'count', evidence_id:'f'.repeat(64)}); },
  ]) { const candidate = structuredClone(parsed.report); mutate(candidate); assert.throws(() => validateReport(candidate)); }
});

test('field controller report is supported without weakening older rule-version checks', { skip: !process.env.ATLAS_FIELD_TEST_BUNDLE }, async () => {
  const parsed = await parseHealthBundle(readFileSync(process.env.ATLAS_FIELD_TEST_BUNDLE!, 'utf8'), 'field-bundle.json');
  assert.equal(parsed.report.rule_version, 'hardware-health-v1.2.1');
  assert.equal(parsed.report.rows.length, 6);
  assert.equal(parsed.report.summary.assessed, 4);
  assert.equal(parsed.report.summary.unknown, 2);
  assert.equal(parsed.report.rows.filter(row => row.entity_type === 'nvme_controller' && row.assessment_status === 'assessed').length, 4);
  const mismatch = structuredClone(parsed.report);
  mismatch.rows[0].rule_version = 'disk-health-v1.1.0';
  assert.throws(() => validateReport(mismatch), /invalid_disk_rule_version/);
  const previousContract = structuredClone(parsed.report);
  previousContract.rule_version = 'hardware-health-v1.2.0';
  assert.throws(() => validateReport(previousContract), /invalid_disk_rule_version/);
});

test('PM893 profile is versioned, field-bound and never a health percentage', () => {
  const report = fixture() as unknown as HealthReport;
  report.rule_version = 'hardware-health-v1.3.0';
  const evidence = [
    ['ata.5.raw', '0', 'count'], ['ata.187.raw', '0', 'count'], ['ata.199.raw', '0', 'count'], ['ata.197.raw', '0', 'sectors'],
    ['ata.177.normalized', '98', 'normalized'], ['ata.temperature', '30', 'celsius'],
  ].map(([field, value, unit], index) => ({ field, value, unit, id: String(index + 1).repeat(64) }));
  Object.assign(report.rows[0], {
    rule_version: 'disk-health-v1.2.0', entity_type: 'disk', protocol: 'ATA', model: 'SAMSUNG MZ7L3480HCHQ-00B7C', firmware: 'JXTC404Q', device: '/dev/sda', evidence,
    disk: { media_type: 'SSD', profile: 'ata-samsung-pm893-v1', temperature_celsius: '30',
      wear: { field: 'ata.177.normalized', value: '98', unit: 'normalized', kind: 'normalized_index', evidence_id: evidence[4].id },
      dimensions: [
        { id: 'media', state: 'checked', level: 'clear_for_checked_signals' }, { id: 'endurance', state: 'insufficient_data', level: 'unknown' },
        { id: 'temperature', state: 'insufficient_data', level: 'unknown' }, { id: 'path_power', state: 'checked', level: 'clear_for_checked_signals' },
      ], counter_deltas: [],
    },
  });
  assert.equal(validateReport(report).rows[0].disk?.wear?.value, '98');
  for (const mutate of [
    (r: HealthReport) => { r.rows[0].firmware = 'JXTC104Q'; },
    (r: HealthReport) => { r.rows[0].model = 'SAMSUNG MZ7L3480HCHQ-00A07'; },
    (r: HealthReport) => { r.rows[0].disk!.wear!.unit = 'percent'; },
    (r: HealthReport) => { r.rows[0].disk!.wear!.kind = 'consumed_percent'; },
    (r: HealthReport) => { r.rows[0].disk!.dimensions[1] = { id: 'endurance', state: 'checked', level: 'clear_for_checked_signals' }; },
    (r: HealthReport) => { r.rows[0].evidence = r.rows[0].evidence.filter(e => e.field !== 'ata.197.raw'); },
    (r: HealthReport) => { r.rows[0].evidence[3].unit = 'count'; },
    (r: HealthReport) => { r.rule_version = 'hardware-health-v1.2.1'; },
    (r: HealthReport) => { r.rule_version = 'disk-health-v1.0.0'; },
    (r: HealthReport) => { r.rule_version = 'hardware-health-v1.2.1'; r.rows[0].rule_version = 'disk-health-v1.1.1'; },
  ]) { const candidate = structuredClone(report); mutate(candidate); assert.throws(() => validateReport(candidate)); }
});

test('same real six-disk input now includes two limited Samsung assessments', { skip: !process.env.ATLAS_PM893_TEST_BUNDLE }, async () => {
  const parsed = await parseHealthBundle(readFileSync(process.env.ATLAS_PM893_TEST_BUNDLE!, 'utf8'), 'pm893-field-bundle.json');
  assert.equal(parsed.report.rule_version, 'hardware-health-v1.3.0');
  assert.equal(parsed.report.input_manifest_sha256, 'c435dde03f70ef12f1a7688d80d98408f4921e7879b0dfa64b35c32bf7b28639');
  assert.deepEqual([parsed.report.summary.total, parsed.report.summary.assessed, parsed.report.summary.unknown], [6, 6, 0]);
  const sata = parsed.report.rows.filter(row => row.protocol === 'ATA');
  assert.equal(sata.length, 2);
  for (const row of sata) {
    assert.equal(row.disk?.profile, 'ata-samsung-pm893-v1');
    assert.equal(row.disk?.wear?.kind, 'normalized_index');
    assert.equal(row.disk?.wear?.value, '98');
    assert.equal(row.trend_status, 'unavailable');
    assert.equal(row.disk?.dimensions.find(d => d.id === 'endurance')?.state, 'insufficient_data');
  }
});

test('controlled audit bundle retains actual execution provenance', { skip: !process.env.ATLAS_AUDIT_TEST_BUNDLE }, async () => {
  const parsed = await parseHealthBundle(readFileSync(process.env.ATLAS_AUDIT_TEST_BUNDLE!, 'utf8'), 'audit-bundle.json');
  assert.equal(parsed.report.rows.length, 1);
  const row = parsed.report.rows[0];
  assert.equal(row.source, 'hardware-evidence-acceptance-v1');
  assert.equal(row.source_kind, 'fixture');
  assert.equal(row.exit_code, 0);
  assert.equal(row.disk?.media_type, 'HDD');
  assert.notEqual(row.input_sha256, parsed.report.input_manifest_sha256);
});

test('controller-scoped memory contract preserves critical evidence and exact deltas', () => {
  const report = fixture() as unknown as HealthReport;
  report.rule_version = 'hardware-health-v1.1.0';
  Object.assign(report.rows[0], { rule_version: 'memory-health-v0.1.0', entity_type: 'memory_controller', protocol: 'EDAC', serial: '', firmware: '', tool_version: 'Linux EDAC sysfs v1', baseline_sha256: 'e'.repeat(64), trend_status: 'available', level: 'critical', memory: { controller: 'mc0', driver: 'synthetic-driver', kernel_release: 'synthetic-kernel', location_precision: 'controller', ce_delta: '0', ue_delta: '1' }, evidence: [{ id: 'c'.repeat(64), field: 'edac.ue_delta', value: '1', unit: 'count' }], hits: [{ rule: 'new_ue_errors', level: 'critical', evidence_id: 'c'.repeat(64) }] });
  report.summary.by_level = { critical: 1 };
  assert.equal(validateReport(report).rows[0].memory?.ue_delta, '1');
  for (const mutate of [
    (r: HealthReport) => { r.rows[0].memory!.ce_delta = null; },
    (r: HealthReport) => { r.rows[0].memory!.ue_delta = '-1'; },
    (r: HealthReport) => { r.rows[0].memory!.location_precision = 'dimm' as 'controller'; },
    (r: HealthReport) => { r.rows[0].memory!.driver = ''; },
    (r: HealthReport) => { r.rows[0].baseline_sha256 = ''; },
    (r: HealthReport) => { r.rows[0].media_delta = '1'; },
  ]) { const candidate = structuredClone(report); mutate(candidate); assert.throws(() => validateReport(candidate)); }
});

test('raw EDAC CLI artifact passes browser contract without claiming DIMM health', { skip: !process.env.ATLAS_MEMORY_TEST_BUNDLE }, async () => {
  const parsed = await parseHealthBundle(readFileSync(process.env.ATLAS_MEMORY_TEST_BUNDLE!, 'utf8'), 'memory-bundle.json');
  assert.equal(parsed.report.rows.length, 3);
  const incomplete = parsed.report.rows.find(row => row.reference === 'public-edac-incomplete')!;
  assert.equal(incomplete.assessment_status, 'insufficient_data');
  assert.equal(incomplete.level, 'attention');
  const critical = parsed.report.rows.find(row => row.reference === 'synthetic-memory-new-ue')!;
  assert.equal(critical.level, 'critical');
  assert.equal(critical.memory?.ue_delta, '1');
  assert.equal(critical.serial, '');
});
