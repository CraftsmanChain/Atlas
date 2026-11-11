"""Deterministic binary ranking metrics; exact ties are averaged, never ID-broken."""
import math


def ranking_metrics(scores, labels):
    if len(scores) != len(labels) or any(type(y) is not bool for y in labels) or any(type(s) not in (int, float) or not math.isfinite(s) for s in scores):
        raise ValueError('invalid metric rows')
    n, positives = len(scores), sum(labels)
    groups = {}
    for s, y in zip(scores, labels):
        groups.setdefault(s, []).append(y)
    ordered = [groups[s] for s in sorted(groups, reverse=True)]
    k = math.ceil(n * .05)
    expected = minimum = maximum = 0
    position = 0
    dcg = {v: 0.0 for v in (5, 10, 20)}
    wins, negatives_below = 0.0, n - positives
    for ys in ordered:
        size, p = len(ys), sum(ys)
        negatives_below -= size-p
        wins += p * (negatives_below + (size-p)/2)
        slots = max(0, min(size, k-position))
        expected += slots*p/size
        minimum += max(0, slots-(size-p))
        maximum += min(slots, p)
        for cutoff in dcg:
            dcg[cutoff] += p/size * sum(1/math.log2(i+2) for i in range(position, min(position+size, cutoff)))
        position += size
    ndcg = {str(v): {'requested_k': v, 'effective_k': min(v,n), 'value': dcg[v]/sum(1/math.log2(i+2) for i in range(min(v,positives))) if positives else None} for v in dcg}
    return {'N': n, 'positive_node_rows': positives, 'auc': wins/(positives*(n-positives)) if 0<positives<n else None,
            'auc_reason': None if 0<positives<n else 'single_class_or_empty', 'ndcg': ndcg,
            'top_five_percent': {'k': k, 'expected_hits': expected, 'min_hits': minimum, 'max_hits': maximum, 'capture': expected/positives if positives else None, 'min_capture': minimum/positives if positives else None, 'max_capture': maximum/positives if positives else None, 'reason': None if positives else 'no_positive_labels'},
            'tie_policy': 'expected_uniform_selection_within_exact_score_ties'}
