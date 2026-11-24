import { test } from 'node:test';
import assert from 'node:assert/strict';
import { parseRoute } from '../src/routes.ts';

test('current routes parse page and section', () => {
  assert.deepEqual(parseRoute('#/gpu/ranking'), ['gpu', 'ranking']);
  assert.deepEqual(parseRoute('#/memory'), ['memory', undefined]);
  assert.deepEqual(parseRoute(''), ['overview', undefined]);
  assert.deepEqual(parseRoute('#/nope'), ['overview', undefined]);
});

test('pre-consolidation hashes map to their new home', () => {
  assert.deepEqual(parseRoute('#/hardware'), ['memory', undefined]);
  assert.deepEqual(parseRoute('#/models'), ['gpu', 'ranking']);
  assert.deepEqual(parseRoute('#/incidents'), ['gpu', 'events']);
  assert.deepEqual(parseRoute('#/quality'), ['data', 'targets']);
  assert.deepEqual(parseRoute('#/about'), ['settings', 'platform']);
});
