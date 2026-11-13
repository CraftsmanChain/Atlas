"""B3 loss primitives only: no training, probability, admission or actions.

ListNet is an explicit local definition: softmax(binary labels) cross entropy
averaged equally over queries. It is not an exact-paper reproduction claim.
"""
import torch
from torch.nn import functional as F

VERSION = 'gpu-ranking-objectives-v1'
MAX_ROWS = 10000
MAX_PAIRS = 100000


def objective(method, logits, labels, query_ids):
    """Return differentiable loss (or None), with denominator diagnostics.

    Pointwise is a row mean. ListNet is a query mean, including single-class
    queries. Pairwise is a mean of per-query positive/negative pair means,
    excluding queries without pairs. Pair budgets reject, never subsample.
    """
    if method not in ('pointwise', 'listnet', 'pairwise'):
        raise ValueError('unknown objective')
    if not isinstance(logits, torch.Tensor) or logits.device.type != 'cpu' or logits.dtype not in (torch.float32, torch.float64) or logits.ndim != 1 or not 0 < logits.numel() <= MAX_ROWS:
        raise ValueError('invalid logits')
    if not isinstance(labels, torch.Tensor) or labels.device.type != 'cpu' or labels.ndim != 1 or labels.shape != logits.shape or labels.dtype not in (torch.bool, torch.float32, torch.float64, torch.int32, torch.int64):
        raise ValueError('invalid labels')
    if not torch.isfinite(logits).all() or not torch.isfinite(labels).all() or not ((labels == 0) | (labels == 1)).all():
        raise ValueError('nonfinite or nonbinary input')
    if not isinstance(query_ids, (list, tuple)) or len(query_ids) != logits.numel() or any(type(q) is not int or q <= 0 for q in query_ids):
        raise ValueError('query IDs must be positive integers')
    groups = {}
    for i, q in enumerate(query_ids):
        groups.setdefault(q, []).append(i)
    groups = [groups[q] for q in sorted(groups)]
    y = labels.to(dtype=logits.dtype)
    counts = []
    for indices in groups:
        positives = int(labels[indices].sum().item())
        counts.append(positives * (len(indices)-positives))
    pair_count = sum(counts)
    if method == 'pairwise' and pair_count > MAX_PAIRS:
        raise ValueError('pair budget exceeded before pair allocation')
    valid_pairs = sum(n > 0 for n in counts)
    result = dict(version=VERSION, method=method, rows=logits.numel(), queries=len(groups), pairs=pair_count, queries_with_pairs=valid_pairs, queries_without_pairs=len(groups)-valid_pairs, loss=None, reason=None)
    if method == 'pointwise':
        loss = F.binary_cross_entropy_with_logits(logits, y, reduction='mean')
    elif method == 'listnet':
        loss = torch.stack([-(torch.softmax(y[ix], dim=0) * torch.log_softmax(logits[ix], dim=0)).sum() for ix in groups]).mean()
    elif not valid_pairs:
        result['reason'] = 'no_positive_negative_pairs'
        return result
    else:
        losses = []
        for indices, count in zip(groups, counts):
            if not count:
                continue
            z, target = logits[indices], y[indices]
            positive, negative = z[target == 1], z[target == 0]
            # Global pair count was checked before any Cartesian tensor exists.
            losses.append(F.softplus(-(positive[:, None]-negative[None, :])).mean())
        loss = torch.stack(losses).mean()
    if not torch.isfinite(loss):
        raise ValueError('nonfinite objective')
    result['loss'] = loss
    return result
