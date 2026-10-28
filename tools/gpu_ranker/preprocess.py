"""Train-only vocabulary/statistics; missing and OOV are separate channels."""
import math
import statistics

NUMERIC = ["episode_count", "episode_count_30d", "mean_interval_days", "frequency_per_day", "recent_ratio", "unresolved_count", "weekday", "weekend"]
CATEGORIES = ["host_id", "host_type", "gpu_model", "manufacturer", "main_board", "quota_group", "data_center", "rack"]
MAPS = ["by_fault_type", "by_severity"]


def value(features, column):
    if column["group"] == "numeric":
        v = features["numeric"][column["name"]]
    else:
        counts = features[column["group"]]
        if counts is None:
            return None
        if column["oov"]:
            v = sum(n for key, n in counts.items() if key not in column["known_keys"])
        else:
            v = counts.get(column["name"], 0)
    if v is not None and (isinstance(v, bool) or not isinstance(v, (float, int)) or not math.isfinite(v)):
        raise ValueError("non-finite or invalid feature")
    return v


def fit(rows):
    columns = [{"group": "numeric", "name": n, "oov": False, "known_keys": []} for n in NUMERIC]
    for group in MAPS:
        keys = sorted({k for r in rows for k in (r["features"][group] or {})})
        if len(keys) > 128:
            raise ValueError("count-map vocabulary budget exceeded")
        columns.extend({"group": group, "name": k, "oov": False, "known_keys": keys} for k in keys)
        columns.append({"group": group, "name": "", "oov": True, "known_keys": keys})
    for column in columns:
        values = [v for r in rows if (v := value(r["features"], column)) is not None]
        column["mean"] = statistics.fmean(values) if values else 0.0
        column["scale"] = statistics.pstdev(values) if len(values) > 1 else 1.0
        if column["scale"] < 1e-12:
            column["scale"] = 1.0
    categories = []
    for name in CATEGORIES:
        vocab = sorted({v for r in rows if (v := r["features"]["categorical"][name]) is not None})
        if len(vocab) > 1024:
            raise ValueError("category vocabulary budget exceeded")
        categories.append({"name": name, "vocab": vocab})
    return {"numeric": columns, "categories": categories}


def transform(preprocessor, rows):
    result = []
    for r in rows:
        features, vector = r["features"], []
        for column in preprocessor["numeric"]:
            v = value(features, column)
            vector.extend((0.0 if v is None else (v - column["mean"]) / column["scale"], float(v is None)))
        for column in preprocessor["categories"]:
            v = features["categorical"][column["name"]]
            index = 0 if v is None else (column["vocab"].index(v) + 2 if v in column["vocab"] else 1)
            vector.extend(float(i == index) for i in range(len(column["vocab"]) + 2))
        if not all(math.isfinite(v) and abs(v) < 1e30 for v in vector):
            raise ValueError("transformed feature out of float32 budget")
        if len(vector) > 4096:
            raise ValueError("input dimension budget exceeded")
        result.append(vector)
    if result and len(result) * len(result[0]) > 2_000_000:
        raise ValueError("transformed cell budget exceeded")
    return result
