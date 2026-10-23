import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readPlatformBrand, writePlatformBrand, fetchJSON } from '../src/progressiveLoading.ts';

test('public branding cache validates fields, excludes secrets and tolerates unavailable storage', () => {
  const fallback = { instance_name: 'Default', product_name: 'Atlas', product_tagline: '', environment: 'LOCAL' };
  let cached = '';
  const previous = Object.getOwnPropertyDescriptor(globalThis, 'localStorage');
  Object.defineProperty(globalThis, 'localStorage', { configurable: true, value: { getItem: () => cached, setItem: (_key: string, value: string) => { cached = value; } } });
  try {
    writePlatformBrand({ ...fallback, instance_name: 'Custom', secret: 'never-cache' } as typeof fallback);
    assert(!cached.includes('secret'));
    assert.equal(readPlatformBrand(fallback).instance_name, 'Custom');
    cached = '{broken'; assert.deepEqual(readPlatformBrand(fallback), fallback);
    cached = '{"instance_name":17}'; assert.deepEqual(readPlatformBrand(fallback), fallback);
    Object.defineProperty(globalThis, 'localStorage', { configurable: true, get() { throw new Error('disabled'); } });
    assert.deepEqual(readPlatformBrand(fallback), fallback);
    assert.doesNotThrow(() => writePlatformBrand(fallback));
  } finally {
    if (previous) Object.defineProperty(globalThis, 'localStorage', previous);
    else Reflect.deleteProperty(globalThis, 'localStorage');
  }
});

test('a pending or failed data request does not hold back branding', async () => {
  const previous = globalThis.fetch;
  let release!: (response: Response) => void;
  globalThis.fetch = async input => String(input).includes('slow') ? new Promise(resolve => { release = resolve; }) : Response.json({ name: 'Immediate' });
  try {
    const slow = fetchJSON('/slow');
    assert.deepEqual(await fetchJSON('/brand'), { name: 'Immediate' });
    release(new Response('', { status: 503 }));
    await assert.rejects(slow, /HTTP 503/);
  } finally { globalThis.fetch = previous; }
});
