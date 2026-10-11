import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';
import { levels, parseHealthBundle, selectRows, validateReport, type HealthReport } from '../src/hardwareHealthReport.ts';

const digest = async (value: string) => createHash('sha256').update(value, 'utf8').digest('hex');
const fixture = () => ({
  schema: 'hardware-health-report-v1', input_manifest_sha256: 'd'.repeat(64), rule_version: 'disk-health-v1.0.0', mode: 'offline_replay', as_of: '2026-10-09T00:00:00Z', alerts_emitted: false, actions_executed: false,
  summary: { total: 1, assessed: 1, unknown: 0, by_level: { clear_for_checked_signals: 1 } },
  rows: [{ reference: 'fixture-a', entity_id: 'a'.repeat(64), entity_type: 'nvme_controller', identity_status: 'bound', scope: 'fixture', node: 'node-a', generation: '1', boot_id: 'boot-a', serial: 'serial-a', model: 'fixture-model', firmware: '1', protocol: 'NVMe', device: '/dev/nvme0', tool_version: 'smartctl 7.4', source: 'fixture', source_kind: 'fixture', input_sha256: 'b'.repeat(64), observed_at: '2026-10-08T23:59:00Z', available_at: '2026-10-08T23:59:01Z', freshness: 'fresh_at_cutoff', support: 'fixture_verified', assessment_status: 'assessed', level: 'clear_for_checked_signals', exit_code: 0, media_delta: null, baseline_sha256: '', trend_status: 'unavailable', blockers: [], hits: [], evidence: [{ id: 'c'.repeat(64), field: 'nvme.media_errors', value: '340282366920938463463374607431768211455', unit: 'count' }] }],
});

test('frontend and framework release contracts stay aligned', () => {
  const app = readFileSync(new URL('../src/App.tsx', import.meta.url), 'utf8');
  const framework = readFileSync(new URL('../../internal/prediction/service.go', import.meta.url), 'utf8');
  const version = framework.match(/FrameworkVersion\s*=\s*"prediction-framework-(v[0-9.]+)"/)?.[1];
  assert(version);
  assert(app.includes(`version: '${version}'`));
  assert(app.includes(`${version} ·`));
  assert(app.includes('PLATFORM / v0.120.61'));
  assert(app.includes("id: 'component-health'"));
});

test('exact counters survive parsing without Number conversion', async () => {
  const reportJSON = JSON.stringify(fixture());
  const content = JSON.stringify({ schema: 'hardware-health-bundle-v1', report_json: reportJSON, report_sha256: await digest(reportJSON) });
  const result = await parseHealthBundle(content, 'fixture.json');
  assert.equal(result.report.rows[0].evidence[0].value, '340282366920938463463374607431768211455');
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
