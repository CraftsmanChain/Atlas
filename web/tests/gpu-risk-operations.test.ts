import assert from 'node:assert/strict';
import { test } from 'node:test';
import { operationsView, loadOperations, count } from '../src/operationsView.ts';
const pause = () => new Promise(resolve => setTimeout(resolve, 0));
test('switches independent and missing values remain unknown', () => {
  for (const operations of [true, false]) for (const ranking of [true, false]) for (const outcomes of [true, false]) {
    assert.deepEqual(operationsView({ operations_enabled: operations, ranking_enabled: ranking, outcomes_enabled: outcomes }).flags, [operations, ranking, outcomes]);
  }
  assert.deepEqual(operationsView({}).flags, [null, null, null]);
  for (const x of [undefined, null, -1, '0', NaN, Infinity]) assert.equal(count(x), null);
  assert.equal(count(0), 0);
});
test('today never inherits yesterday completion; missing server date fails closed', () => {
  const payload = { current_day: '2026-11-11', records: [{ day: '2026-11-10', stage: 'ranking', status: 'completed' }] };
  assert.equal(operationsView(payload).stages[0].records.length, 0);
  assert.equal(operationsView(payload).records.length, 1);
  assert.equal(operationsView({ ...payload, current_day: undefined }).day, '');
  assert.equal(operationsView({}).recordsKnown, false);
});
test('failed and exhausted stages remain visible with their own query, coverage stays separate', () => {
  const view = operationsView({ current_day: '2026-11-11', records: [{ day: '2026-11-11', stage: 'ranking', query_id: 14, status: 'failed', exhausted: 2 }], coverage: { query_id: 15, members: 398, sources: [{ source: 'monitoring', check_status: 'stale_for_query', member_assertions_missing: 398 }] } });
  assert.equal(view.stages[0].records[0].status, 'failed');
  assert.equal(view.stages[0].records[0].exhausted, 2);
  assert.equal(view.stages[0].records[0].query_id, 14);
  assert.equal(view.coverage.query_id, 15);
  assert.equal(view.sources[0].row, undefined);
  assert.equal(view.sources[2].row?.member_assertions_missing, 398);
  assert.equal(count(view.coverage.eligible), null);
  for (const status of ['inspection_failed', 'no_completed_query', 'new_unknown_status']) assert.equal(operationsView({ coverage: { status } }).coverage.status, status);
});
test('GET-only loader exposes API failure without data and masks raw errors', async () => {
  const states: unknown[] = [];
  const cancel = loadOperations(s => states.push(s), (async (url, options) => {
    assert.equal(url, '/api/v1/gpu-risk/operations'); assert.equal(options?.method, 'GET');
    return new Response('private raw backend error', { status: 503 });
  }) as typeof fetch);
  await pause(); cancel();
  assert.deepEqual(states, [{ state: 'loading' }, { state: 'error', message: 'operations_unavailable' }]);
});
test('cancelled old request cannot overwrite refreshed response even if server ignores abort', async () => {
  const states: unknown[] = [];
  let resolveOld!: (r: Response) => void;
  let oldSignal: AbortSignal | null | undefined;
  const old = loadOperations(s => states.push(s), ((_url, options) => { oldSignal = options?.signal; return new Promise<Response>(resolve => { resolveOld = resolve; }); }) as typeof fetch);
  await pause(); old();
  const current = loadOperations(s => states.push(s), (async () => Response.json({ current_day: '2026-11-11' })) as typeof fetch);
  await pause(); resolveOld(Response.json({ current_day: '2026-11-10' })); await pause(); current();
  assert.equal(oldSignal?.aborted, true);
  assert.deepEqual(states, [{ state: 'loading' }, { state: 'loading' }, { state: 'loaded', data: { current_day: '2026-11-11' } }]);
});
