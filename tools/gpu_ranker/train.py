"""Offline CPU MLP artifacts. No database, network, registry or operational actions."""
import argparse
import copy
import hashlib
import json
import math
import platform
from datetime import datetime, timedelta
from pathlib import Path

import torch
from torch import nn

from model import PROFILE, Ranker
from preprocess import NUMERIC, CATEGORIES, MAPS, fit, transform

VERSION = "gpu-host-mlp-artifact-v1"
SYNTHETIC = "synthetic_fixture_not_production_efficacy"


def read_pinned(path, expected):
    raw = Path(path).read_bytes()
    if len(raw) > 64 * 1024 * 1024 or hashlib.sha256(raw).hexdigest() != expected:
        raise ValueError("dataset bytes fingerprint or size mismatch")
    return json.loads(raw, parse_constant=lambda _: (_ for _ in ()).throw(ValueError("non-finite JSON")))


def validate(dataset, synthetic=False):
    if dataset["version"] != "gpu-host-history-dataset-v2" or dataset["feature_contract"] != "gpu-host-history-v2" or dataset["target"] != "confirmed_hardware_failure" or dataset["spec"]["profile"] != "paper_core":
        raise ValueError("unsupported dataset contract")
    marked = SYNTHETIC in dataset["implementation_choices"]
    if marked != synthetic:
        raise ValueError("synthetic artifacts require the separate self-test command")
    if not dataset["ready"] or not dataset["paper_core_complete"] or not dataset["exploration_ready"] or dataset["blockers"]:
        raise ValueError("waiting_for_data: dataset readiness blocked")
    expected = set(NUMERIC + CATEGORIES + MAPS)
    schema = dataset["schema"]
    if len(schema) != len(expected) or {f["name"] for f in schema} != expected or any(f["target"] != dataset["target"] or f["producer"] != dataset["feature_contract"] for f in schema):
        raise ValueError("feature manifest mismatch")
    for field in schema:
        expected_dtype = "float64" if field["name"] in ("weekday", "weekend") else "nullable_float64" if field["name"] in NUMERIC else "nullable_category" if field["name"] in CATEGORIES else "nullable_count_map"
        if field["dtype"] != expected_dtype:
            raise ValueError("feature dtype mismatch")
    rows = dataset["rows"]
    if not rows or len(rows) > 10000:
        raise ValueError("row budget exceeded")
    seen, components, episodes, groups = set(), {}, {}, {s: [] for s in ("train", "validation", "test")}
    boundaries = {s: datetime.fromisoformat(dataset["spec"][b].replace("Z", "+00:00")) for s, b in (("train", "train_end"), ("validation", "validation_end"), ("test", "test_end"))}
    for row in rows:
        key = (row["query_id"], row["node_id"])
        if key in seen or not row["entity_ref"]:
            raise ValueError("duplicate node or unknown identity")
        seen.add(key)
        if set(row["features"]["numeric"]) != set(NUMERIC) or set(row["features"]["categorical"]) != set(CATEGORIES):
            raise ValueError("unknown feature columns")
        for name in CATEGORIES:
            v = row["features"]["categorical"][name]
            if v is not None and (not isinstance(v, str) or len(v) > 256):
                raise ValueError("invalid category")
        for group in MAPS:
            counts = row["features"][group]
            if counts is not None and (not isinstance(counts, dict) or any(not isinstance(k, str) or len(k) > 256 or type(v) is not int or v < 0 for k, v in counts.items())):
                raise ValueError("invalid composition counts")
        if not row["eligible"]:
            continue
        split = row["split"]
        cutoff = datetime.fromisoformat(row["cutoff"].replace("Z", "+00:00"))
        if split not in groups or type(row["label"]) is not bool or row["blockers"] or row["features"]["blockers"] or not row["component"] or cutoff + timedelta(days=8) > boundaries[split]:
            raise ValueError("eligible row violates temporal/feature contract")
        if (split == "validation" and cutoff < boundaries["train"]) or (split == "test" and cutoff < boundaries["validation"]):
            raise ValueError("split time order violated")
        for identity in (row["component"], "entity:" + row["entity_ref"]):
            if components.setdefault(identity, split) != split:
                raise ValueError("entity split conflict")
        for episode in row["episode_keys"]:
            if episodes.setdefault(episode, split) != split:
                raise ValueError("episode split conflict")
        groups[split].append(row)
    for split, selected in groups.items():
        positive = {r["component"] for r in selected if r["label"]}
        controls = {r["component"] for r in selected if not r["label"]}
        ep = {e for r in selected if r["label"] for e in r["episode_keys"]}
        minimum = (30, 10, 60) if split == "train" else (10, 5, 20)
        if len(ep) < minimum[0] or len(positive) < minimum[1] or len(controls) < minimum[2] or len({r["label"] for r in selected}) != 2:
            raise ValueError("waiting_for_data: independent cohort gate")
    return groups


def train(dataset, dataset_sha, config, synthetic=False):
    groups = validate(dataset, synthetic)
    if not 1 <= config["epochs"] <= 100 or not 0 <= config["seed"] <= 2**31-1:
        raise ValueError("training config exceeds budget")
    torch.set_num_threads(1)
    torch.use_deterministic_algorithms(True)
    torch.manual_seed(config["seed"])
    prep = fit(groups["train"])
    x = torch.tensor(transform(prep, groups["train"]), dtype=torch.float32, device="cpu")
    vx = torch.tensor(transform(prep, groups["validation"]), dtype=torch.float32)
    y = torch.tensor([r["label"] for r in groups["train"]], dtype=torch.float32)
    vy = torch.tensor([r["label"] for r in groups["validation"]], dtype=torch.float32)
    model = Ranker(x.shape[1]).float().cpu()
    optimizer = torch.optim.Adam(model.parameters(), lr=1e-4)
    best, best_epoch, stale, checkpoint = math.inf, 0, 0, None
    # Full-batch CPU is a bounded implementation choice. All rows contribute
    # equally once per epoch, including zero-positive queries; no sampling.
    for epoch in range(config["epochs"]):
        model.train()
        optimizer.zero_grad()
        loss = nn.functional.binary_cross_entropy_with_logits(model(x), y, reduction="mean")
        if not torch.isfinite(loss):
            raise ValueError("non-finite training loss")
        loss.backward()
        optimizer.step()
        model.eval()
        with torch.no_grad():
            validation = nn.functional.binary_cross_entropy_with_logits(model(vx), vy, reduction="mean").item()
        if not math.isfinite(validation):
            raise ValueError("non-finite validation loss")
        if validation < best:
            best, best_epoch, stale = validation, epoch + 1, 0
            checkpoint = copy.deepcopy(model.state_dict())
        else:
            stale += 1
        if stale >= 10:
            break
    model.load_state_dict(checkpoint)
    model.eval()
    all_x = torch.tensor(transform(prep, dataset["rows"]), dtype=torch.float32)
    with torch.no_grad():
        logits = model(all_x).tolist()
    if not all(math.isfinite(z) for z in logits):
        raise ValueError("non-finite score")
    artifact = {"version": VERSION, "profile": PROFILE, "target": dataset["target"], "feature_contract": dataset["feature_contract"], "dataset_sha": dataset_sha, "dataset_manifest_sha": dataset["sha"], "feature_sha": dataset["feature_sha"], "split_sha": dataset["split_sha"], "purpose": SYNTHETIC if synthetic else "offline_exploration_not_promoted", "preprocessor": prep, "layers": model.export(), "config": {**config, "device": "cpu", "dtype": "float32", "optimizer": "Adam", "lr": 1e-4, "batch": "full_train_rows", "loss": "row_mean_BCEWithLogits", "patience": 10, "bn_eps": 1e-5, "bn_momentum": 0.1, "init": "torch_Linear_default_BN_gamma1_beta0", "dropout": 0}, "environment": {"python": platform.python_version(), "torch": torch.__version__, "platform": platform.platform(), "threads": 1, "requirements_sha": hashlib.sha256(Path(__file__).with_name("requirements.lock").read_bytes()).hexdigest(), "source_sha": {name: hashlib.sha256(Path(__file__).with_name(name).read_bytes()).hexdigest() for name in ("model.py", "preprocess.py", "train.py")}}, "selection": {"best_epoch": best_epoch, "validation_bce": best, "test_used_for_selection": False}, "probability_emitted": False, "no_action": True}
    return artifact, logits, model, all_x


def score_artifact(artifact, dataset, dataset_sha):
    if len(artifact["layers"]) != 4 or artifact["purpose"] not in (SYNTHETIC, "offline_exploration_not_promoted") or artifact["config"]["device"] != "cpu" or artifact["config"]["dtype"] != "float32" or artifact["config"]["loss"] != "row_mean_BCEWithLogits" or artifact["probability_emitted"] or not artifact["no_action"]:
        raise ValueError("unsupported checkpoint shape or purpose")
    if artifact["version"] != VERSION or artifact["profile"] != PROFILE or artifact["dataset_sha"] != dataset_sha or artifact["dataset_manifest_sha"] != dataset["sha"] or artifact["feature_sha"] != dataset["feature_sha"] or artifact["split_sha"] != dataset["split_sha"] or artifact["target"] != dataset["target"] or artifact["feature_contract"] != dataset["feature_contract"]:
        raise ValueError("model/dataset provenance mismatch")
    validate(dataset, artifact["purpose"] == SYNTHETIC)
    matrix = torch.tensor(transform(artifact["preprocessor"], dataset["rows"]), dtype=torch.float32)
    model = Ranker(matrix.shape[1]).float().cpu()
    with torch.no_grad():
        for position, layer in zip((0, 3, 6, 9), artifact["layers"]):
            model.layers[position].weight.copy_(torch.tensor(layer["weights"]))
            model.layers[position].bias.copy_(torch.tensor(layer["bias"]))
            if position < 9:
                bn = model.layers[position + 1]
                for target, key in ((bn.running_mean, "mean"), (bn.running_var, "variance"), (bn.weight, "gamma"), (bn.bias, "beta")):
                    target.copy_(torch.tensor(layer["bn"][key]))
                if layer["bn"]["eps"] != bn.eps:
                    raise ValueError("BN epsilon mismatch")
    model.eval()
    with torch.no_grad():
        scores = model(matrix).tolist()
    if not all(math.isfinite(z) for z in scores):
        raise ValueError("non-finite reloaded score")
    return scores


def score_packet(dataset, dataset_sha, artifact_sha, logits):
    return {"version": "gpu-host-mlp-scores-v1", "artifact_sha": artifact_sha, "dataset_sha": dataset_sha, "rows": [{"query_id": r["query_id"], "node_id": r["node_id"], "generation": r["generation"], "raw_score": z, "uncalibrated_score": (1 / (1 + math.exp(-z)) if z >= 0 else math.exp(z) / (1 + math.exp(z)))} for r, z in zip(dataset["rows"], logits)]}


def write_artifacts(output, dataset, dataset_sha, config, synthetic=False):
    artifact, logits, _, _ = train(dataset, dataset_sha, config, synthetic)
    reloaded = score_artifact(artifact, dataset, dataset_sha)
    if any(abs(a-b) > 1e-6 + 1e-5*abs(a) for a, b in zip(logits, reloaded)):
        raise ValueError("checkpoint reload parity failed")
    output = Path(output)
    output.mkdir(mode=0o700, parents=True, exist_ok=False)
    def write(name, value):
        raw = (json.dumps(value, ensure_ascii=True, separators=(",", ":"), allow_nan=False) + "\n").encode()
        (output / name).write_bytes(raw)
        (output / name).chmod(0o600)
        return hashlib.sha256(raw).hexdigest()
    artifact_sha = write("model.json", artifact)
    scores = score_packet(dataset, dataset_sha, artifact_sha, reloaded)
    scores_sha = write("scores.json", scores)
    write("manifest.json", {"version": VERSION, "purpose": artifact["purpose"], "files": {"model.json": artifact_sha, "scores.json": scores_sha}, "dataset_sha": dataset_sha, "probability_emitted": False, "no_action": True})
    return artifact_sha


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("command", choices=("train", "self-test", "score"))
    parser.add_argument("--dataset", required=True)
    parser.add_argument("--dataset-sha", required=True)
    parser.add_argument("--output", required=True)
    parser.add_argument("--artifact-file")
    parser.add_argument("--artifact-sha")
    parser.add_argument("--seed", type=int, default=17)
    parser.add_argument("--epochs", type=int, default=100)
    args = parser.parse_args()
    data = read_pinned(args.dataset, args.dataset_sha)
    if args.command == "score":
        if not args.artifact_file or not args.artifact_sha:
            parser.error("score requires pinned artifact-file and artifact-sha")
        artifact = read_pinned(args.artifact_file, args.artifact_sha)
        logits = score_artifact(artifact, data, args.dataset_sha)
        output = Path(args.output)
        output.mkdir(mode=0o700, parents=True, exist_ok=False)
        raw = (json.dumps(score_packet(data, args.dataset_sha, args.artifact_sha, logits), allow_nan=False, separators=(",", ":")) + "\n").encode()
        (output / "scores.json").write_bytes(raw)
        (output / "scores.json").chmod(0o600)
        print(json.dumps({"scores_sha": hashlib.sha256(raw).hexdigest(), "status": "offline_only"}))
        return
    sha = write_artifacts(args.output, data, args.dataset_sha, {"seed": args.seed, "epochs": args.epochs}, args.command == "self-test")
    print(json.dumps({"artifact_sha": sha, "status": "offline_only", "probability_emitted": False}))


if __name__ == "__main__":
    main()
