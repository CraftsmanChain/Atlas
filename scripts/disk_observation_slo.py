#!/usr/bin/env python3
"""Evaluate frozen, SHA-bound business SLO evidence without contacting a node.

This consumes whole-window Prometheus query receipts, never host I/O averages.
Missing metrics, an unreviewed business mapping, incomplete query coverage or
unproven load comparability produce inconclusive, never a successful SLO gate.
All referenced artifacts must be inside the output directory's evidence tree.
"""
import argparse
import datetime as dt
from decimal import Decimal, InvalidOperation
from fractions import Fraction
import hashlib
import json
import os
from pathlib import Path
import re

METRICS = ("p95", "p99", "throughput")
HEX = re.compile(r"[0-9a-f]{64}\Z")
MAX_BYTES = 2 << 20


def digest(raw):
    return hashlib.sha256(raw).hexdigest()


def timestamp(value):
    if not isinstance(value, str):
        raise ValueError("timestamp_string_required")
    result = dt.datetime.fromisoformat(value.replace("Z", "+00:00"))
    if result.tzinfo is None:
        raise ValueError("timestamp_timezone_required")
    return result.astimezone(dt.timezone.utc)


def number(value):
    if isinstance(value, bool):
        raise ValueError("invalid_number")
    try:
        result = Decimal(str(value))
    except (InvalidOperation, ValueError, TypeError):
        raise ValueError("invalid_number") from None
    if not result.is_finite():
        raise ValueError("non_finite_number")
    if result and not -300 <= result.adjusted() <= 300:
        raise ValueError("number_outside_representable_range")
    return result


def object_pairs(items):
    result = {}
    for key, value in items:
        if key in result:
            raise ValueError("duplicate_json_key")
        result[key] = value
    return result


def read_json(path, expected=None):
    with path.open("rb") as source:
        raw = source.read(MAX_BYTES + 1)
    if len(raw) > MAX_BYTES:
        raise ValueError("artifact_too_large")
    if expected is not None and (not HEX.fullmatch(expected) or digest(raw) != expected):
        raise ValueError("artifact_sha256_mismatch")
    value = json.loads(raw, object_pairs_hook=object_pairs,
                       parse_constant=lambda _: (_ for _ in ()).throw(ValueError("non_finite_json")))
    if not isinstance(value, dict):
        raise ValueError("json_object_required")
    return value, digest(raw)


class Evidence:
    def __init__(self, root):
        self.root = root.resolve()
        self.sources = {}

    def read(self, reference, base):
        if not isinstance(reference, dict) or set(reference) != {"path", "sha256"}:
            raise ValueError("invalid_evidence_reference")
        if not isinstance(reference["sha256"], str) or not HEX.fullmatch(reference["sha256"]):
            raise ValueError("invalid_evidence_sha256")
        relative = Path(reference["path"])
        if relative.is_absolute() or ".." in relative.parts or not relative.parts:
            raise ValueError("unsafe_evidence_path")
        path = (base / relative).resolve()
        try:
            name = path.relative_to(self.root).as_posix()
        except ValueError:
            raise ValueError("evidence_outside_output_tree") from None
        data, sha = read_json(path, reference["sha256"])
        self.sources[name] = sha
        return data

    def add_input(self, path, expected):
        if not isinstance(expected, str) or not HEX.fullmatch(expected):
            raise ValueError("invalid_input_sha256")
        path = path.resolve()
        try:
            name = path.relative_to(self.root).as_posix()
        except ValueError:
            raise ValueError("input_outside_output_tree") from None
        data, sha = read_json(path, expected)
        self.sources[name] = sha
        return data, sha


def validate_plan(plan):
    required = {"schema", "run_id", "node", "scope_id", "expected_roster_version",
                "frozen_at", "window_start", "window_end", "baseline_start", "baseline_end",
                "workload_id", "node_selector", "queries", "business_binding"}
    if set(plan) != required or plan["schema"] != "disk-observation-slo-plan-v1":
        raise ValueError("invalid_slo_plan_schema")
    for key in ("run_id", "node", "scope_id"):
        if not isinstance(plan[key], str) or not plan[key].strip():
            raise ValueError("missing_slo_identity")
    if not isinstance(plan["expected_roster_version"], str) or not re.fullmatch(r"[1-9][0-9]{0,19}", plan["expected_roster_version"]):
        raise ValueError("invalid_roster_version")
    if int(plan["expected_roster_version"]) > 18446744073709551615:
        raise ValueError("invalid_roster_version")
    times = {k: timestamp(plan[k]) for k in ("frozen_at", "window_start", "window_end", "baseline_start", "baseline_end")}
    for start, end in (("window_start", "window_end"), ("baseline_start", "baseline_end")):
        if (times[end] - times[start]).total_seconds() != 86400:
            raise ValueError("slo_window_must_be_24h")
    if times["baseline_end"] > times["window_start"] or times["frozen_at"] > times["window_start"]:
        raise ValueError("slo_plan_not_frozen_before_observation")
    if not isinstance(plan["queries"], dict) or set(plan["queries"]) - set(METRICS):
        raise ValueError("invalid_query_set")
    return times


def validate_query(metric, definition, selector):
    if not isinstance(definition, dict) or set(definition) != {"promql", "coverage_promql", "unit"}:
        raise ValueError("invalid_query_definition")
    if not isinstance(selector, dict) or set(selector) != {"label", "value"}:
        raise ValueError("missing_node_selector")
    if not re.fullmatch(r"[A-Za-z_][A-Za-z0-9_]*", selector["label"]) or not isinstance(selector["value"], str):
        raise ValueError("invalid_node_selector")
    binding = selector["label"] + "=" + json.dumps(selector["value"])
    for key in ("promql", "coverage_promql"):
        query = definition[key]
        if not isinstance(query, str) or len(query) > 8192 or binding not in re.sub(r"\s+", "", query):
            raise ValueError("query_node_binding_missing")
        # These families measure infrastructure/exporters/ICMP, not this business.
        metric_text = re.sub(r"\{[^{}]*\}", "{}", query)
        if re.search(r"\b(?:node_|DCGM_|nvidia_|go_|process_|promhttp_|scrape_|ipmi_|smokeping_|rdma_|atlas_)[A-Za-z0-9_]*", metric_text):
            raise ValueError("infrastructure_metric_cannot_establish_business_slo")
        if "[24h]" not in query:
            raise ValueError("whole_window_query_required")
    query = re.sub(r"\s+", "", definition["promql"])
    if metric in ("p95", "p99"):
        quantile = "0.95" if metric == "p95" else "0.99"
        if not query.startswith("histogram_quantile(" + quantile + ",") or "increase(" not in query:
            raise ValueError("whole_window_histogram_quantile_required")
        if definition["unit"] not in ("seconds", "ms"):
            raise ValueError("invalid_latency_unit")
    elif "increase(" not in query or not query.endswith("/86400") or definition["unit"] != "requests_per_second":
        raise ValueError("whole_window_business_throughput_required")
    if "count_over_time(" not in definition["coverage_promql"]:
        raise ValueError("query_coverage_proof_required")


def receipt_value(receipt, query, end):
    if set(receipt) != {"query", "evaluation_time", "response"} or receipt["query"] != query or timestamp(receipt["evaluation_time"]) != end:
        raise ValueError("query_receipt_binding_mismatch")
    response = receipt["response"]
    if not isinstance(response, dict) or response.get("status") != "success" or response.get("warnings"):
        raise ValueError("query_not_successful")
    data = response.get("data", {})
    if not isinstance(data, dict):
        raise ValueError("invalid_query_data")
    rows = data.get("result", [])
    if data.get("resultType") != "vector" or not isinstance(rows, list) or len(rows) != 1 or not isinstance(rows[0], dict):
        raise ValueError("one_whole_window_result_required")
    pair = rows[0].get("value", [])
    if not isinstance(pair, list) or len(pair) != 2 or number(pair[0]) != number(end.timestamp()):
        raise ValueError("query_result_timestamp_mismatch")
    value = number(pair[1])
    if value < 0:
        raise ValueError("negative_slo_measurement")
    return value


def evaluate(plan_path, plan_sha, evidence_path, evidence_sha, root, as_of=None):
    sources = Evidence(root)
    plan, plan_sha = sources.add_input(plan_path, plan_sha)
    times = validate_plan(plan)
    now = as_of or dt.datetime.now(dt.timezone.utc)
    blockers = []
    metrics = {}
    result = {"schema": "disk-observation-slo-v1", "run_id": plan["run_id"], "node": plan["node"],
              "scope_id": plan["scope_id"], "expected_roster_version": plan["expected_roster_version"],
              "window_start": plan["window_start"], "window_end": plan["window_end"],
              "baseline_start": plan["baseline_start"], "baseline_end": plan["baseline_end"],
              "plan_sha256": plan_sha, "evaluated_at": now.isoformat(),
              "thresholds": {"latency_increase_max": 0.02, "throughput_drop_max": 0.01},
              "valid": False, "status": "inconclusive", "load_comparable": False}
    if now < times["window_end"]:
        blockers.append("observation_window_not_complete")
    try:
        mapping = sources.read(plan["business_binding"], plan_path.parent)
        query_sha = digest(json.dumps(plan["queries"], sort_keys=True, separators=(",", ":")).encode())
        if (mapping.get("schema") != "disk-observation-business-binding-v1" or mapping.get("confirmed") is not True
                or mapping.get("node") != plan["node"] or mapping.get("workload_id") != plan["workload_id"]
                or not plan["workload_id"] or mapping.get("queries_sha256") != query_sha
                or not mapping.get("reviewed_by") or not mapping.get("source")
                or timestamp(mapping["reviewed_at"]) > times["frozen_at"]):
            raise ValueError("business_metric_mapping_unconfirmed")
    except (ValueError, TypeError, KeyError, OSError):
        blockers.append("business_metric_mapping_unconfirmed")
    evidence = None
    if evidence_path is None:
        blockers.append("business_measurements_missing")
    else:
        evidence, evidence_sha = sources.add_input(evidence_path, evidence_sha)
        result["measurement_input_sha256"] = evidence_sha
        if (evidence.get("schema") != "disk-observation-slo-measurements-v1" or evidence.get("plan_sha256") != plan_sha):
            raise ValueError("slo_measurements_plan_mismatch")
        captured_at = timestamp(evidence["captured_at"])
        if captured_at < times["window_end"] or captured_at > now:
            blockers.append("measurement_capture_time_invalid")
        try:
            comparison = sources.read(evidence["load_comparison"], evidence_path.parent)
            if (comparison.get("schema") != "disk-observation-load-comparison-v1"
                    or comparison.get("plan_sha256") != plan_sha or comparison.get("load_comparable") is not True
                    or not comparison.get("method") or not comparison.get("reviewed_by")
                    or not comparison.get("source_evidence")):
                raise ValueError("load_comparability_unproven")
            for reference in comparison["source_evidence"]:
                sources.read(reference, evidence_path.parent)
            result["load_comparable"] = True
        except (ValueError, TypeError, KeyError, OSError):
            blockers.append("load_comparability_unproven")
    for metric in METRICS:
        try:
            definition = plan["queries"][metric]
            validate_query(metric, definition, plan["node_selector"])
            if evidence is None:
                raise ValueError("business_measurements_missing")
            values = {}
            coverage = {}
            for period, end_key in (("baseline", "baseline_end"), ("observation", "window_end")):
                binding = evidence["responses"][period][metric]
                receipt = sources.read(binding["value"], evidence_path.parent)
                values[period] = receipt_value(receipt, definition["promql"], times[end_key])
                receipt = sources.read(binding["coverage"], evidence_path.parent)
                coverage[period] = receipt_value(receipt, definition["coverage_promql"], times[end_key])
                if not Decimal("0.99") <= coverage[period] <= 1:
                    raise ValueError("business_metric_coverage_below_99_percent")
            if values["baseline"] <= 0:
                raise ValueError("positive_business_baseline_required")
            ratio = Fraction(values["observation"]) / Fraction(values["baseline"])
            degradation = (1 - ratio) if metric == "throughput" else (ratio - 1)
            limit = Fraction(1, 100) if metric == "throughput" else Fraction(2, 100)
            if abs(degradation) > Fraction(Decimal("1e300")):
                raise ValueError("ratio_outside_representable_range")
            # Preserve exact decimal inputs so an independent report verifier
            # cannot round a tiny threshold breach back into a passing value.
            metrics[metric] = {k: str(v) for k, v in values.items()}
            metrics[metric].update(unit=definition["unit"], degradation_ratio=float(degradation),
                                   within_threshold=degradation <= limit,
                                   coverage={k: float(v) for k, v in coverage.items()})
            if degradation > limit:
                blockers.append(metric + "_threshold_exceeded")
        except (ValueError, TypeError, KeyError, OSError) as error:
            code = str(error) if isinstance(error, ValueError) else "business_metric_evidence_missing"
            blockers.append(metric + ":" + code)
    data_blockers = [b for b in blockers if not b.endswith("_threshold_exceeded")]
    result["valid"] = not data_blockers and len(metrics) == 3
    if result["valid"]:
        result["status"] = "fail" if blockers else "pass"
    result["metrics"] = metrics
    result["blocking_reasons"] = sorted(set(blockers))
    result["source_evidence"] = [{"path": path, "sha256": sha} for path, sha in sorted(sources.sources.items())]
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--plan", type=Path, required=True)
    parser.add_argument("--plan-sha256", required=True)
    parser.add_argument("--evidence", type=Path)
    parser.add_argument("--evidence-sha256")
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    try:
        if bool(args.evidence) != bool(args.evidence_sha256):
            raise ValueError("evidence_and_sha256_required_together")
        if args.output.exists():
            raise ValueError("output_already_exists")
        result = evaluate(args.plan.resolve(), args.plan_sha256, args.evidence.resolve() if args.evidence else None,
                          args.evidence_sha256, args.output.parent)
        raw = (json.dumps(result, ensure_ascii=False, indent=2, allow_nan=False) + "\n").encode()
        with args.output.open("xb") as output:
            output.write(raw)
            output.flush()
            os.fsync(output.fileno())
        directory_fd = os.open(args.output.parent, os.O_RDONLY)
        try:
            os.fsync(directory_fd)
        finally:
            os.close(directory_fd)
        print(json.dumps({"status": result["status"], "valid": result["valid"],
                          "output_sha256": digest(raw), "blocking_reasons": result["blocking_reasons"]}))
    except (ValueError, KeyError, TypeError, OSError, json.JSONDecodeError):
        # Do not echo raw evidence, private paths or parsing exception payloads.
        parser.exit(2, "SLO evidence rejected: invalid contract, binding, hash, path or artifact\n")


if __name__ == "__main__":
    main()
