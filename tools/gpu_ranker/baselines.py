"""File-attested baseline inputs, never a live scope or identity certification."""
import math
from collections import Counter
from datetime import datetime, timedelta
from metrics import ranking_metrics


def at(value):
    if not isinstance(value, str):
        raise ValueError('invalid timestamp')
    t = datetime.fromisoformat(value.replace('Z', '+00:00'))
    if t.tzinfo is None:
        raise ValueError('timezone required')
    return t


def integer(value, minimum=0):
    if type(value) is not int or value < minimum:
        raise ValueError('invalid integer')


def sha(value):
    if not isinstance(value, str) or len(value) != 64 or any(c not in '0123456789abcdef' for c in value):
        raise ValueError('invalid SHA')


def binding(value, dataset, dataset_sha, version):
    base = {'version','dataset_sha','target','profile','feature_contract'}
    extra = {'rows'} if version == 'gpu-ranking-health-sidecar-v1' else {'dimension','formula','selection_policy','test_used_for_selection','frozen_at','weights'}
    if not isinstance(value, dict) or set(value) != base | extra:
        raise ValueError('closed baseline schema')
    if (value['version'], value['dataset_sha'], value['target'], value['profile'], value['feature_contract']) != (version, dataset_sha, dataset['target'], dataset['spec']['profile'], dataset['feature_contract']):
        raise ValueError('baseline binding mismatch')


def key(row):
    return row['query_id'], row['node_id'], row['generation']


def health_inputs(value, dataset, dataset_sha):
    binding(value, dataset, dataset_sha, 'gpu-ranking-health-sidecar-v1')
    if not isinstance(value['rows'], list) or len(value['rows']) > 10000:
        raise ValueError('health row budget')
    known = {key(r): r for r in dataset['rows']}
    result, query_refs, uuids, assets = {}, {}, set(), set()
    for row in value['rows']:
        for field in ('query_id', 'node_id'):
            integer(row[field], 1)
        integer(row['generation'])
        k = key(row)
        if k not in known or k in result or row['cutoff'] != known[k]['cutoff']:
            raise ValueError('health row identity mismatch')
        for field in ('scope_sha', 'result_sha'):
            sha(row[field])
        refs = row['scope_sha'], row['result_sha']
        if query_refs.setdefault(k[0], refs) != refs:
            raise ValueError('inconsistent query provenance')
        if set(row) != {'query_id','node_id','generation','cutoff','scope_sha','result_sha','node_available_at','identity_changed_at','expected','observed','cards'}:
            raise ValueError('closed health row schema')
        cutoff = at(row['cutoff'])
        changed = at(row['identity_changed_at'])
        if not at(row['node_available_at']) < cutoff or not changed < cutoff:
            raise ValueError('node time leakage')
        integer(row['expected']); integer(row['observed'])
        if not isinstance(row['cards'], list) or len(row['cards']) > 64:
            raise ValueError('card budget')
        scores = []
        for card in row['cards']:
            if set(card) != {'asset_id','uuid','first_seen_at','state','health_id','run_id','rule','score','evaluated_at','available_at'}:
                raise ValueError('closed card schema')
            integer(card['asset_id'], 1)
            if not isinstance(card['uuid'], str) or not card['uuid']:
                raise ValueError('missing UUID')
            if (k[0], card['uuid']) in uuids or (k[0], card['asset_id']) in assets:
                raise ValueError('query-wide card conflict')
            uuids.add((k[0], card['uuid'])); assets.add((k[0], card['asset_id']))
            first = at(card['first_seen_at'])
            if not first < cutoff:
                raise ValueError('installation time leakage')
            if card['score'] is None:
                continue
            integer(card['score'])
            integer(card['health_id'], 1); integer(card['run_id'], 1)
            if card['score'] > 100 or card['state'] != 'active' or not isinstance(card['rule'], str) or not card['rule']:
                raise ValueError('invalid health provenance')
            evaluated, available = at(card['evaluated_at']), at(card['available_at'])
            if not max(cutoff-timedelta(hours=24), first, changed) <= evaluated < cutoff or not available < cutoff:
                raise ValueError('health time leakage or stale score')
            scores.append(100-card['score'])
        denominator = max(row['expected'], row['observed'], len(row['cards']))
        state = 'complete' if denominator > 0 and len(scores) == denominator else 'partial' if scores else 'no_signal'
        result[k] = {'score': max(scores) if state == 'complete' else None, 'state': state}
    return result


def weight_inputs(value, dataset, dataset_sha, selected):
    binding(value, dataset, dataset_sha, 'gpu-ranking-severity-weights-v1')
    if value['dimension'] != 'severity' or value['formula'] != 'sum(count*weight)' or value['selection_policy'] != 'predeclared_no_outcome_selection' or value['test_used_for_selection'] is not False:
        raise ValueError('weight policy rejected')
    frozen = at(value['frozen_at'])
    if frozen > at(dataset['spec']['train_end']) or any(frozen >= at(r['cutoff']) for r in selected):
        raise ValueError('weight freeze time rejected')
    weights = value['weights']
    if not isinstance(weights, dict) or not weights or len(weights) > 256:
        raise ValueError('invalid weights')
    for name, weight in weights.items():
        if not isinstance(name, str) or not name or type(weight) not in (int, float) or weight < 0 or weight > 1e100 or not math.isfinite(weight):
            raise ValueError('invalid weight')
    result = {}
    for row in dataset['rows']:
        counts = row['features']['by_severity']
        total = row['features']['numeric']['episode_count']
        reason = None
        if row['features']['history_unknown'] or counts is None or total is None:
            reason = 'unknown_history_or_severity'
        elif sum(counts.values()) != total:
            reason = 'severity_total_mismatch'
        elif any(n > 0 and s not in weights for s, n in counts.items()):
            reason = 'unmapped_severity'
        if not reason and any(n > 10**100 for n in counts.values()):
            raise ValueError('count budget')
        score = None if reason else sum(n*weights.get(s, 0) for s, n in counts.items())
        if score is not None and not math.isfinite(score):
            raise ValueError('weighted score overflow')
        result[key(row)] = {'score': score, 'state': reason or 'complete'}
    return result


def comparison(group, common, inputs, logits, indexed, digest, ready):
    joint = [r for r in common if ready and all(v.get(key(r), {}).get('score') is not None for v in inputs.values())]
    cohort_sha = digest([key(r) for r in joint])
    methods = {}
    names = ['uniform', 'episode_count'] + (['mlp'] if logits is not None else []) + list(inputs)
    for name in names:
        if name == 'uniform': scores = [0 for _ in joint]
        elif name == 'episode_count': scores = [r['features']['numeric']['episode_count'] for r in joint]
        elif name == 'mlp': scores = [logits[indexed[id(r)]] for r in joint]
        else: scores = [inputs[name][key(r)]['score'] for r in joint]
        methods[name] = {'cohort_sha': cohort_sha, 'status': 'computed' if ready and joint else 'pending', 'metrics': ranking_metrics(scores, [r['label'] for r in joint]) if ready and joint else None}
    diagnostics = {}
    for name, values in inputs.items():
        states = Counter(values.get(key(r), {'state': 'missing_input'})['state'] for r in group)
        diagnostics[name] = {'available': sum(values.get(key(r), {}).get('score') is not None for r in group), 'no_signal': sum(values.get(key(r), {}).get('score') is None for r in group), 'states': dict(sorted(states.items()))}
    return {'N': len(group), 'base_eligible': len(common), 'joint_eligible': len(joint), 'joint_dropped': len(common)-len(joint), 'status': 'computed' if ready and joint else 'pending', 'diagnostics': diagnostics, 'methods': methods, 'cohort_sha': cohort_sha}
