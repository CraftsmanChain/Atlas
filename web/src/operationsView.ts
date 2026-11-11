export type ObjectRow = Record<string, unknown>;
export const objectRow = (value: unknown): ObjectRow => value !== null && typeof value === 'object' && !Array.isArray(value) ? value as ObjectRow : {};
export const count = (value: unknown): number | null => typeof value === 'number' && Number.isSafeInteger(value) && value >= 0 ? value : null;
export const text = (value: unknown): string => typeof value === 'string' && value.length > 0 ? value : '';
export function operationsView(value: unknown) {
  const raw = objectRow(value);
  const day = /^\d{4}-\d{2}-\d{2}$/.test(text(raw.current_day)) ? text(raw.current_day) : '';
  const records = Array.isArray(raw.records) ? raw.records.map(objectRow) : [];
  const coverage = objectRow(raw.coverage);
  return {
    day, flags: [raw.operations_enabled, raw.ranking_enabled, raw.outcomes_enabled].map(v => typeof v === 'boolean' ? v : null),
    stages: ['ranking', 'outcomes'].map(stage => ({ stage, records: day ? records.filter(r => r.day === day && r.stage === stage) : [] })),
    records, recordsKnown: Array.isArray(raw.records), coverage,
    sources: ['hardware_fault_ledger', 'identity', 'monitoring'].map(source => ({ source, row: Array.isArray(coverage.sources) ? coverage.sources.map(objectRow).find(r => r.source === source) : undefined })),
  };
}
export type OperationsLoad = { state: 'loading' } | { state: 'error'; message: string } | { state: 'loaded'; data: unknown };
export function loadOperations(emit: (state: OperationsLoad) => void, request: typeof fetch = fetch) {
  const controller = new AbortController();
  let active = true;
  void Promise.resolve().then(async () => {
    if (!active) return;
    emit({ state: 'loading' });
    try {
      const response = await request('/api/v1/gpu-risk/operations', { method: 'GET', signal: controller.signal });
      if (!response.ok) throw new Error(`HTTP ${response.status}`);
      const data: unknown = await response.json();
      if (active) emit({ state: 'loaded', data });
    } catch {
      if (active) emit({ state: 'error', message: 'operations_unavailable' });
    }
  });
  return () => { active = false; controller.abort(); };
}
