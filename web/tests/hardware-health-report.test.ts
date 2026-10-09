import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';
import { levels, parseHealthBundle, selectRows, validateReport, type HealthReport } from '../src/hardwareHealthReport.ts';

const digest = async (value: string) => Array.from(new Uint8Array(await crypto.subtle.digest('SHA-256', new TextEncoder().encode(value))), value => value.toString(16).padStart(2, '0')).join('');
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
  assert(app.includes('PLATFORM / v0.120.57'));
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
  assert.equal(parsed.report.rows.find(row => row.protocol === 'ATA')?.support, 'unsupported');
});
