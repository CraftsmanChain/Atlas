"""Pinned, descriptive period strata. No fitting, live source certification or promotion."""
import argparse
import hashlib
import json
from datetime import datetime, timezone
from pathlib import Path
from evaluate import evaluate, read_file, digest
from metrics import ranking_metrics

STRATA = ('history_unknown', 'zero_observed_failures', 'observed_history')


def timestamp(value):
    if not isinstance(value, str):
        raise ValueError('timestamp required')
    t = datetime.fromisoformat(value.replace('Z', '+00:00'))
    if t.tzinfo is None:
        raise ValueError('timezone required')
    return t.astimezone(timezone.utc)


def stratum(row):
    f = row['features']
    count = f['numeric']['episode_count']
    return STRATA[0] if f['history_unknown'] or count is None else STRATA[1] if count == 0 else STRATA[2]


def build(dataset, dataset_sha, start, end, split='validation', synthetic=False):
    start, end = timestamp(start), timestamp(end)
    if start >= end:
        raise ValueError('invalid period')
    original = evaluate(dataset, dataset_sha, split=split, synthetic=synthetic)
    ready = original['status'] == 'offline_artifact_comparison'
    selected = [r for r in dataset['rows'] if r['split'] == split]
    window = [r for r in selected if start <= timestamp(r['cutoff']) < end]
    original_queries = {q['query_id']: q for q in original['queries']}
    queries = []
    for query_id in sorted({r['query_id'] for r in window}):
        rows = [r for r in window if r['query_id'] == query_id]
        rejected = {(r['node_id'], r['generation']) for r in original_queries[query_id]['row_rejections']}
        groups = {}
        for name in STRATA:
            group = [r for r in rows if stratum(r) == name]
            common = [r for r in group if (r['node_id'], r['generation']) not in rejected]
            methods = {}
            for method in ('uniform', 'episode_count'):
                scores = [0 if method == 'uniform' else r['features']['numeric']['episode_count'] for r in common]
                methods[method] = {'status': 'computed' if ready else 'pending', 'metrics': ranking_metrics(scores, [r['label'] for r in common]) if ready else None}
            groups[name] = {'N': len(group), 'eligible': len(common), 'excluded': sum(not r['eligible'] or bool(r['blockers']) for r in group), 'unknown': sum(r['label'] is None or stratum(r) == 'history_unknown' or bool(r['features']['blockers']) for r in group), 'no_signal': sum(stratum(r) == 'history_unknown' for r in group), 'cohort_sha': digest([(r['query_id'], r['node_id'], r['generation']) for r in common]), 'methods': methods}
        queries.append({'query_id': query_id, 'cutoff': timestamp(rows[0]['cutoff']).isoformat(), 'N': len(rows), 'original_query_cohort_sha': original_queries[query_id]['cohort_sha'], 'strata': groups})
    summary = {}
    for name in STRATA:
        summary[name] = {'N': sum(q['strata'][name]['N'] for q in queries), 'methods': {}}
        for method in ('uniform', 'episode_count'):
            values = [q['strata'][name]['methods'][method]['metrics']['auc'] for q in queries if q['strata'][name]['methods'][method]['metrics'] is not None and q['strata'][name]['methods'][method]['metrics']['auc'] is not None]
            mean = sum(values)/len(values) if values else None
            summary[name]['methods'][method] = {'query_count': len(values), 'auc_mean': mean, 'auc_population_variance': sum((v-mean)**2 for v in values)/len(values) if values else None}
    report = {'version': 'gpu-ranking-period-strata-v1', 'status': original['status'], 'purpose': original['purpose'], 'input': original['input'], 'original_report_sha': original['sha'], 'source_sha': {**original['source_sha'], 'period_report.py': hashlib.sha256(Path(__file__).read_bytes()).hexdigest()}, 'period': {'start': start.isoformat(), 'end': end.isoformat(), 'boundary': 'start_inclusive_end_exclusive_UTC'}, 'split': split, 'counts': {'split_rows': len(selected), 'N': len(window), 'outside_period': len(selected)-len(window), 'unique_node_generations': len({(r['node_id'], r['generation']) for r in window}), 'positive_declared_episode_keys': len({e for r in window if r['label'] is True for e in r['episode_keys']})}, 'stratum_rules': {'history_unknown': 'history_unknown flag or null episode_count', 'zero_observed_failures': 'known episode_count equals zero; not proof of complete history or first failure', 'observed_history': 'known episode_count greater than zero'}, 'count_semantics': 'strata N disjoint; excluded/unknown/no_signal flags overlap; eligible is original candidate cohort, not E1 certification; episode keys are declarations, not certified independent incidents', 'ranking_semantics': 'descriptive reranking within each query stratum, not full fleet Top-5% capture', 'aggregation_semantics': 'non-null query AUC macro mean and population variance; repeated queries not independent samples', 'queries': queries, 'summary': summary, 'pending': ['mlp_health_weighted_methods_not_loaded_in_this_entry', 'new_entity_OOV', 'first_failure_before_after', 'independent_episode_capture', 'nonoverlapping_week_blocks', 'confidence_intervals', 'real_drift_and_efficacy', 'complete_B5_B6'], 'blockers': original['blockers'], 'input_provenance': original['input_provenance'], 'probability_emitted': False, 'no_action': True, 'promoted': False, 'sha': ''}
    report['sha'] = digest(report)
    return report


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('command', choices=['evaluate', 'self-test'])
    for name in ('dataset', 'dataset-sha', 'period-start', 'period-end', 'output'):
        p.add_argument('--'+name, required=True)
    p.add_argument('--split', choices=['validation', 'test'], default='validation')
    args = p.parse_args()
    try:
        d = read_file(args.dataset, args.dataset_sha)
        report = build(d, args.dataset_sha, args.period_start, args.period_end, args.split, args.command == 'self-test')
        out = Path(args.output)
        out.mkdir(mode=0o700, exist_ok=False, parents=True)
        target = out/'report.json'
        target.write_text(json.dumps(report, sort_keys=True, separators=(',', ':'), allow_nan=False)+'\n')
        target.chmod(0o600)
        print(json.dumps({'status': report['status'], 'report_sha': report['sha'], 'no_action': True}))
    except (ValueError, KeyError, TypeError, OSError, OverflowError, AttributeError):
        p.exit(2, 'period report rejected: invalid pinned input or period\n')


if __name__ == '__main__':
    main()
