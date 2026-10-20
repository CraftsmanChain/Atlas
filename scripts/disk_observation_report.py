#!/usr/bin/env python3
"""Verify a frozen 24-hour disk observation from private files only.

This program never invokes a device, network request, database or subprocess.
Reports contain evidence hashes and identifiers, never raw SMART or DSNs.
"""
import argparse
from collections import Counter
import datetime as dt
from decimal import Decimal, InvalidOperation
from fractions import Fraction
import hashlib
import ipaddress
import json
from pathlib import Path
import re
import sys

UTC = dt.timezone.utc
CATEGORIES = {"identity", "execution", "persistence", "quality", "scope", "budget", "audit"}
SHA = re.compile(r"^[0-9a-f]{64}$")
CODE = re.compile(r"^[A-Za-z0-9_.:-]{1,128}$")


class ReportError(ValueError):
    pass


def instant(value):
    if not isinstance(value, str):
        raise ReportError("invalid_timestamp")
    try:
        result = dt.datetime.fromisoformat(value.replace("Z", "+00:00"))
    except ValueError as exc:
        raise ReportError("invalid_timestamp") from exc
    if result.tzinfo is None or result.utcoffset() is None:
        raise ReportError("timestamp_requires_timezone")
    return result.astimezone(UTC)


def stamp(value):
    return value.astimezone(UTC).isoformat().replace("+00:00", "Z")


def number(value):
    if isinstance(value, bool) or not isinstance(value, (str, int, float, Decimal)):
        raise ReportError("invalid_number")
    try:
        result = Decimal(str(value))
    except InvalidOperation as exc:
        raise ReportError("invalid_number") from exc
    if not result.is_finite():
        raise ReportError("nonfinite_number")
    return result


def digest(raw):
    return hashlib.sha256(raw).hexdigest()


def strict_object(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ReportError("duplicate_json_field")
        result[key] = value
    return result


class Evidence:
    def __init__(self, state_dir):
        self.root = Path(state_dir).resolve()
        self.files = {}

    def read(self, path, limit=4 << 20, external=False, label=None):
        path = Path(path)
        if path.is_symlink() or not path.is_file():
            raise ReportError("missing_or_symlink_evidence")
        resolved = path.resolve()
        if not external and not resolved.is_relative_to(self.root):
            raise ReportError("evidence_path_escape")
        raw = path.read_bytes()
        if len(raw) > limit:
            raise ReportError("evidence_too_large")
        name = label or "state/" + str(resolved.relative_to(self.root))
        self.files[name] = {"path": name, "sha256": digest(raw), "bytes": len(raw)}
        return raw

    def obj(self, path, **kwargs):
        raw = self.read(path, **kwargs)
        try:
            obj = json.loads(raw, object_pairs_hook=strict_object,
                             parse_constant=lambda _: (_ for _ in ()).throw(ReportError("nonfinite_json")))
        except (ValueError, UnicodeError) as exc:
            raise ReportError("invalid_json_evidence") from exc
        if not isinstance(obj, dict):
            raise ReportError("object_required")
        return obj, raw


def checked_sha(value, raw):
    if not isinstance(value, str) or not SHA.fullmatch(value) or value != digest(raw):
        raise ReportError("evidence_sha_mismatch")


def frozen_approval(evidence, path):
    approval, raw = evidence.obj(path, external=True, label="approval.json")
    if approval.get("schema") != "disk-observation-approval-v2":
        raise ReportError("unsupported_approval_schema")
    start, end = instant(approval["issued_at"]), instant(approval["expires_at"])
    try:
        ipaddress.ip_address(approval["node"])
    except (ValueError, TypeError, KeyError) as exc:
        raise ReportError("invalid_approved_node") from exc
    devices = approval.get("devices")
    if (not isinstance(devices, list) or not 1 <= len(devices) <= 200
            or not all(isinstance(x, str) and re.fullmatch(r"sd[a-z]+|nvme[0-9]+", x) for x in devices)
            or len(devices) != len(set(devices))):
        raise ReportError("invalid_approved_devices")
    if (end - start != dt.timedelta(hours=24) or type(approval.get("interval_seconds")) is not int or approval["interval_seconds"] != 1800
            or not CODE.fullmatch(approval.get("run_id", ""))):
        raise ReportError("invalid_frozen_approval")
    binding, binding_raw = evidence.obj(evidence.root / "binding.json")
    checked_sha(approval.get("binding_sha256"), binding_raw)
    ids = binding.get("entity_ids")
    if (binding.get("schema") != "disk-observation-binding-v1" or binding.get("node") != approval["node"]
            or not isinstance(ids, list) or len(ids) != len(devices) or len(set(ids)) != len(devices)
            or ids != sorted(ids) or not all(isinstance(x, str) and SHA.fullmatch(x) for x in ids)
            or not isinstance(binding.get("expected_roster_version"), str)
            or not re.fullmatch(r"[0-9]+", binding["expected_roster_version"])
            or not binding.get("scope_id") or not SHA.fullmatch(binding.get("asset_identity_sha256", ""))):
        raise ReportError("invalid_frozen_binding")
    if "expected_entity_ids" in approval and approval["expected_entity_ids"] != ids:
        raise ReportError("approval_binding_entities_differ")
    for field in ("seed_sha256", "collector_sha256", "replay_sha256", "known_hosts_sha256",
                  "smartctl_sha256", "drivedb_sha256", "expected_boot_id_sha256"):
        if not isinstance(approval.get(field), str) or not SHA.fullmatch(approval[field]):
            raise ReportError("approval_toolchain_hash_missing")
    frozen = evidence.root / "approval.json"
    if not frozen.exists() or evidence.read(frozen) != raw:
        raise ReportError("frozen_approval_mismatch")
    return approval, binding, start, end, digest(raw)


def verify_batch(evidence, directory, audit, approval, binding, start, end, as_of):
    slot = audit.get("slot_index")
    if type(slot) is not int or not 0 <= slot < 48:
        raise ReportError("invalid_slot_index")
    if (audit.get("schema") != "disk-observation-batch-v2"
            or audit.get("node") != approval["node"]
            or audit.get("run_id") != approval["run_id"]
            or audit.get("binding_sha256") != approval["binding_sha256"]):
        raise ReportError("batch_binding_mismatch")
    scheduled = start + dt.timedelta(seconds=1800 * slot)
    begun, finished = instant(audit["started_at"]), instant(audit["finished_at"])
    if (instant(audit["scheduled_at"]) != scheduled or not scheduled <= begun < scheduled + dt.timedelta(minutes=30)
            or not begun <= finished <= min(end, as_of, scheduled+dt.timedelta(seconds=120))
            or finished - begun > dt.timedelta(seconds=120)):
        raise ReportError("batch_time_or_budget_invalid")
    duration = number(audit["duration_seconds"])
    if duration < 0 or duration > 120 or abs(duration - number((finished-begun).total_seconds())) > Decimal("1"):
        raise ReportError("batch_duration_invalid")
    output_bytes = audit.get("output_bytes")
    if type(output_bytes) is not int or not 0 <= output_bytes <= 1 << 20:
        raise ReportError("batch_output_budget_invalid")
    commands = audit.get("commands")
    labels = ["registry-check", "toolchain", "boot"]
    for device in approval["devices"]:
        if device.startswith("sd"):
            labels.append(device+"-power")
        labels.append(device)
    labels.append("replay")
    if (not isinstance(commands, list) or not all(isinstance(c, dict) for c in commands)
            or [c.get("label") for c in commands] != labels):
        raise ReportError("command_audit_missing")
    command_bytes = 0
    for command in commands:
        cs, cf = instant(command["started_at"]), instant(command["finished_at"])
        code = command.get("exit_code")
        if (command.get("status") != "completed" or type(code) is not int or code < 0 or code > 255
                or code & 7 or code in (124, 137, 255) or not begun <= cs <= cf <= finished
                or cf-cs > dt.timedelta(seconds=60 if command["label"] == "replay" else 16)):
            raise ReportError("command_execution_invalid")
        for stream in ("stdout", "stderr"):
            size = command.get(stream + "_bytes")
            if type(size) is not int or size < 0:
                raise ReportError("command_byte_audit_invalid")
            raw = evidence.read(directory / (command["label"] + "." + stream), limit=256 << 10)
            checked_sha(command.get(stream + "_sha256"), raw)
            if len(raw) != size:
                raise ReportError("command_stream_size_mismatch")
            command_bytes += size
    if command_bytes != output_bytes:
        raise ReportError("command_bytes_total_mismatch")
    boot = evidence.read(directory / "boot.stdout").decode().strip()
    if digest(boot.encode()) != approval["expected_boot_id_sha256"]:
        raise ReportError("boot_identity_changed")
    tool_lines = evidence.read(directory / "toolchain.stdout").decode().splitlines()
    if (len(tool_lines) != 2 or not all(len(line.split()) == 2 for line in tool_lines)
            or [line.split()[0] for line in tool_lines] != [approval["smartctl_sha256"], approval["drivedb_sha256"]]):
        raise ReportError("remote_toolchain_changed")
    manifest, manifest_raw = evidence.obj(directory / "input.json")
    report, report_raw = evidence.obj(directory / "report/report.json")
    receipt, receipt_raw = evidence.obj(directory / "persistence.json")
    checked_sha(audit.get("input_sha256"), manifest_raw)
    checked_sha(audit.get("report_sha256"), report_raw)
    persistence = audit.get("persistence")
    if not isinstance(persistence, dict):
        raise ReportError("persistence_audit_missing")
    checked_sha(persistence.get("receipt_sha256"), receipt_raw)
    for value in (receipt, persistence):
        if value.get("verified") is not True:
            raise ReportError("persistence_not_verified")
    committed = instant(receipt["completed_at"])
    if (instant(persistence["completed_at"]) != committed or not begun <= committed <= finished
            or committed-begun > dt.timedelta(seconds=120)
            or receipt.get("binding_sha256") != approval["binding_sha256"]
            or receipt.get("scope_id") != binding["scope_id"]
            or str(receipt.get("roster_version")) != binding["expected_roster_version"]):
        raise ReportError("persistence_binding_or_time_invalid")
    entities = receipt.get("entities")
    if (not isinstance(entities, list) or not all(isinstance(x, dict) for x in entities)
            or len(entities) != len(binding["entity_ids"]) or persistence.get("entities") != entities):
        raise ReportError("persistence_entities_mismatch")
    expected_ids = set(binding["entity_ids"])
    if {row.get("entity_id") for row in entities} != expected_ids:
        raise ReportError("persistence_entities_mismatch")
    if len({str(row.get("snapshot_id")) for row in entities}) != len(entities):
        raise ReportError("duplicate_persisted_snapshot")
    inputs, rows = manifest.get("inputs"), report.get("rows")
    if (manifest.get("schema") != "hardware-health-input-v1" or not isinstance(inputs, list)
            or not all(isinstance(x, dict) for x in inputs) or len(inputs) != len(binding["entity_ids"])
            or not isinstance(rows, list) or not all(isinstance(x, dict) for x in rows) or len(rows) != len(binding["entity_ids"])
            or report.get("input_manifest_sha256") != audit["input_sha256"]
            or {row.get("entity_id") for row in rows} != expected_ids):
        raise ReportError("report_or_manifest_entities_invalid")
    raw_by_device = {}
    smart_by_device = {}
    for item in inputs:
        candidate = directory / item["file"]
        raw = evidence.read(candidate, limit=256 << 10)
        checked_sha(item.get("sha256"), raw)
        # Validate identity through report+receipt digests, without serial output.
        device = Path(item["file"]).name.split(".", 1)[0]
        if device not in approval["devices"] or device in raw_by_device or item.get("node") != approval["node"]:
            raise ReportError("input_devices_invalid")
        obj = json.loads(raw, object_pairs_hook=strict_object)
        if not isinstance(obj, dict) or obj.get("device", {}).get("name") != "/dev/" + device:
            raise ReportError("raw_device_mismatch")
        smart_by_device[device] = obj
        raw_by_device[device] = item
        if isinstance(item.get("baseline"), dict):
            baseline = item["baseline"]
            checked_sha(baseline.get("sha256"), evidence.read(directory / baseline["file"], limit=256 << 10))
    times = {}
    receipt_by_id = {item["entity_id"]: item for item in entities}
    for row in rows:
        device = str(row.get("device", "")).removeprefix("/dev/")
        if device not in raw_by_device:
            raise ReportError("report_device_mapping_invalid")
        item, persisted = raw_by_device[device], receipt_by_id[row["entity_id"]]
        observed, available = instant(row["observed_at"]), instant(row["available_at"])
        raw_time = smart_by_device[device].get("local_time", {}).get("time_t")
        if type(raw_time) is not int or dt.datetime.fromtimestamp(raw_time, UTC) != observed:
            raise ReportError("raw_observation_time_mismatch")
        if (not scheduled <= observed <= available <= committed <= min(end, as_of)
                or observed < begun-dt.timedelta(seconds=60)
                or row.get("input_sha256") != item["sha256"]
                or row.get("trend_status") in ("counter_reset", "invalid_baseline")):
            raise ReportError("report_quality_or_observation_invalid")
        if (persisted.get("input_sha256") != item["sha256"]
                or persisted.get("report_sha256") != audit["report_sha256"]
                or instant(persisted["observed_at"]) != observed or instant(persisted["available_at"]) != available
                or instant(item["observed_at"]) != observed or instant(item["available_at"]) != available
                or not re.fullmatch(r"[1-9][0-9]*", str(persisted.get("snapshot_id", "")))):
            raise ReportError("persisted_snapshot_mismatch")
        times[row["entity_id"]] = observed
    if set(times) != expected_ids:
        raise ReportError("duplicate_report_device")
    assessment_counts = Counter(row.get("assessment_status", "unknown") for row in rows)
    return slot, times, duration, output_bytes, assessment_counts


def evaluate_slo(evidence, path, approval, binding, start, end):
    if path is None:
        return {"status": "inconclusive", "blocking_reasons": ["business_slo_evidence_missing"]}
    try:
        slo, _ = evidence.obj(path, external=True, label="slo.json")
        if (slo.get("schema") != "disk-observation-slo-v1" or slo.get("run_id") != approval["run_id"]
                or instant(slo["window_start"]) != start or instant(slo["window_end"]) != end
                or slo.get("valid") is not True or slo.get("node") != approval["node"]
                or slo.get("scope_id") != binding["scope_id"]
                or str(slo.get("expected_roster_version")) != binding["expected_roster_version"]
                or slo.get("load_comparable") is not True or slo.get("status") not in ("pass", "fail")):
            raise ReportError("slo_evidence_invalid_or_incomparable")
        sources = slo.get("source_evidence")
        if not isinstance(sources, list) or not sources:
            raise ReportError("slo_primary_evidence_missing")
        for source in sources:
            relative = Path(source["path"])
            if relative.is_absolute() or ".." in relative.parts:
                raise ReportError("slo_evidence_path_escape")
            source_path = Path(path).parent / relative
            if not source_path.resolve().is_relative_to(Path(path).parent.resolve()):
                raise ReportError("slo_evidence_path_escape")
            raw = evidence.read(source_path, external=True, label="slo-evidence/" + str(relative))
            checked_sha(source.get("sha256"), raw)
        outcomes = {}
        for name in ("p95", "p99", "throughput"):
            metric = slo["metrics"][name]
            baseline, observed = number(metric["baseline"]), number(metric["observation"])
            units = ("requests_per_second",) if name == "throughput" else ("seconds", "ms")
            if baseline <= 0 or observed < 0 or metric.get("unit") not in units:
                raise ReportError("slo_metric_invalid")
            passed = (Fraction(observed) >= Fraction(baseline) * Fraction(99, 100) if name == "throughput"
                      else Fraction(observed) <= Fraction(baseline) * Fraction(102, 100))
            outcomes[name] = {"baseline": str(baseline), "observation": str(observed), "unit": metric["unit"], "passed": passed}
        blocked = ["slo_threshold_exceeded:" + name for name, value in outcomes.items() if not value["passed"]]
        status = "fail" if blocked else "pass"
        if status != slo["status"]:
            raise ReportError("slo_status_inconsistent")
        return {"status": status, "blocking_reasons": blocked, "metrics": outcomes}
    except (ReportError, KeyError, TypeError, ValueError, OSError):
        return {"status": "inconclusive", "blocking_reasons": ["slo_evidence_invalid_or_incomplete"]}


def build_report(state_dir, approval_path, as_of, slo_path=None, now=None):
    now = now or dt.datetime.now(UTC)
    as_of = instant(as_of) if isinstance(as_of, str) else as_of.astimezone(UTC)
    if as_of > now.astimezone(UTC):
        raise ReportError("future_as_of_rejected")
    evidence = Evidence(state_dir)
    approval, binding, start, end, approval_sha = frozen_approval(evidence, approval_path)
    blockers, audits_by_slot, categories = set(), {}, Counter()
    pause_count = anomaly_count = unavailable_cost = 0
    total_duration, total_bytes = Decimal(0), 0
    attempted_duration, attempted_bytes = Decimal(0), 0
    anomaly_codes, process_events = Counter(), Counter()
    assessment_counts = Counter()
    good_times = {entity: {} for entity in binding["entity_ids"]}
    for directory in sorted(evidence.root.glob("run-*")):
        if not directory.is_dir() or directory.is_symlink():
            blockers.add("invalid_batch_directory")
            continue
        try:
            intent, _ = evidence.obj(directory / "intent.json")
            audit, _ = evidence.obj(directory / "audit.json")
            if any(intent.get(key) != audit.get(key) for key in
                   ("schema", "run_id", "node", "slot_index", "scheduled_at", "started_at", "binding_sha256")):
                raise ReportError("intent_audit_mismatch")
            slot = audit.get("slot_index")
            if type(slot) is not int or not 0 <= slot < 48:
                raise ReportError("invalid_slot_index")
            audits_by_slot.setdefault(slot, []).append(directory.name)
            try:
                cost_seconds, cost_bytes = number(audit["duration_seconds"]), audit["output_bytes"]
                if cost_seconds < 0 or type(cost_bytes) is not int or cost_bytes < 0:
                    raise ReportError("invalid_cost")
                attempted_duration += cost_seconds
                attempted_bytes += cost_bytes
            except (ReportError, KeyError, TypeError, ValueError):
                unavailable_cost += 1
            if audit.get("status") == "paused":
                pause_count += 1
                category = audit.get("category", "audit")
                categories[category if category in CATEGORIES else "audit"] += 1
                blockers.add("paused_batch")
                continue
            if audit.get("status") != "success":
                raise ReportError("nonterminal_batch")
            slot, times, duration, output_bytes, health_counts = verify_batch(evidence, directory, audit, approval, binding, start, end, as_of)
            assessment_counts.update(health_counts)
            total_duration += duration
            total_bytes += output_bytes
            for entity, observed in times.items():
                good_times[entity][slot] = observed
        except (ReportError, KeyError, TypeError, ValueError, OverflowError, OSError) as error:
            categories["audit"] += 1
            anomaly_count += 1
            anomaly_codes[str(error) if isinstance(error, ReportError) else "malformed_batch_evidence"] += 1
            blockers.add("invalid_or_unverified_batch:" + directory.name)
    duplicate_slots = sorted(slot for slot, runs in audits_by_slot.items() if len(runs) > 1)
    missing_slots = sorted(set(range(48)) - set(audits_by_slot))
    if duplicate_slots:
        blockers.add("duplicate_slot_audit")
        for per_entity in good_times.values():
            for slot in duplicate_slots:
                per_entity.pop(slot, None)
    if missing_slots:
        blockers.add("missing_slot_audit")
    # Process events are separate from each batch's durable intent/audit pair.
    events_path = evidence.root / "events.jsonl"
    if not events_path.exists():
        blockers.add("event_journal_missing")
    else:
        try:
            raw = evidence.read(events_path)
            started_seen = expired_seen = False
            for line in raw.splitlines():
                if not line.strip():
                    continue
                event = json.loads(line, object_pairs_hook=strict_object)
                at = instant(event["at"])
                if (event.get("schema") != "disk-observation-event-v1"
                        or event.get("run_id") != approval["run_id"] or at > as_of):
                    raise ReportError("invalid_process_event")
                kind = event.get("kind")
                process_events[kind if kind in ("started", "expired", "paused", "stopped") else "invalid"] += 1
                if kind == "started":
                    if not start <= at < end:
                        raise ReportError("invalid_process_start")
                    started_seen = True
                elif kind == "expired":
                    if at < end:
                        raise ReportError("early_expiry_event")
                    expired_seen = True
                elif kind in ("paused", "stopped"):
                    blockers.add("paused_or_stopped_event")
                else:
                    raise ReportError("unknown_event_kind")
            if not started_seen:
                blockers.add("process_start_event_missing")
            if as_of >= end and not expired_seen:
                blockers.add("process_expiry_event_missing")
        except (ReportError, ValueError, TypeError, KeyError, OSError):
            blockers.add("invalid_event_journal")
    state_path = evidence.root / "state.json"
    if state_path.exists():
        try:
            state, _ = evidence.obj(state_path)
            if state.get("paused") is True or state.get("in_progress") is True:
                blockers.add("paused_or_in_progress_state")
        except (ReportError, ValueError, TypeError, OSError):
            blockers.add("invalid_state_journal")
    entities = []
    for entity, slots in good_times.items():
        ordered = sorted(slots.values())
        gaps = [(right-left).total_seconds() for left, right in zip([start]+ordered, ordered+[end])]
        max_gap = max(gaps)
        covered = len(slots)
        if covered * 5 < 48 * 4:
            blockers.add("coverage_below_80_percent:" + entity)
        if max_gap > 3600:
            blockers.add("maximum_gap_exceeds_one_hour:" + entity)
        entities.append({"entity_id": entity, "expected_slots": 48, "occupied_slots": covered,
                         "coverage_ratio": str(Decimal(covered)/Decimal(48)), "max_gap_seconds": str(number(max_gap)),
                         "first_observed_at": stamp(ordered[0]) if ordered else None,
                         "last_observed_at": stamp(ordered[-1]) if ordered else None})
    if as_of < end or now.astimezone(UTC) < end:
        blockers.add("observation_window_not_elapsed")
    slo = evaluate_slo(evidence, slo_path, approval, binding, start, end)
    if slo["status"] != "pass":
        blockers.update(slo["blocking_reasons"])
    return {"schema": "disk-observation-report-v1", "run_id": approval["run_id"], "as_of": stamp(as_of),
            "window_start": stamp(start), "window_end": stamp(end), "approval_sha256": approval_sha,
            "binding_sha256": approval["binding_sha256"], "node": approval["node"],
            "scope_id": binding["scope_id"], "expected_roster_version": binding["expected_roster_version"],
            "entity_count": len(binding["entity_ids"]), "minimum_occupied_slots": 39,
            "status": "blocked" if blockers else "pass", "expected_slots": 48,
            "observed_slot_audits": len(audits_by_slot), "missing_slots": missing_slots, "duplicate_slots": duplicate_slots,
            "entities": entities, "failure_counts_by_category": dict(sorted(categories.items())),
            "pause_count": pause_count, "anomaly_count": anomaly_count,
            "anomaly_counts_by_code": dict(sorted(anomaly_codes.items())), "process_event_counts": dict(sorted(process_events.items())),
            "health_assessment_counts": dict(sorted(assessment_counts.items())),
            "health_coverage_limitations": (["unsupported_or_insufficient_health_assessment"]
                                            if any(key != "assessed" and count for key, count in assessment_counts.items()) else []),
            "cost": {"successful_batch_duration_seconds": str(total_duration), "successful_batch_output_bytes": total_bytes,
                     "attempted_batch_duration_seconds": str(attempted_duration), "attempted_batch_output_bytes": attempted_bytes,
                     "unavailable_cost_count": unavailable_cost},
            "slo": slo, "blocking_reasons": sorted(blockers),
            "input_files": sorted(evidence.files.values(), key=lambda item: item["path"]),
            "device_reads": False, "database_writes": False, "notifications": False}


def markdown(report):
    lines = ["# Disk observation acceptance", "", "Status: " + report["status"],
             "Run: " + report["run_id"], "Window: " + report["window_start"] + " → " + report["window_end"],
             "", "| Entity | Occupied / 48 | Coverage | Maximum gap (s) |", "| --- | ---: | ---: | ---: |"]
    for row in report["entities"]:
        lines.append("| {entity_id} | {occupied_slots} | {coverage_ratio} | {max_gap_seconds} |".format(**row))
    lines += ["", "SLO: " + report["slo"]["status"], "", "Blocking reasons:"]
    lines += ["- " + value for value in report["blocking_reasons"]] or ["- none"]
    lines += ["", "Paused batch count: " + str(report["pause_count"]),
              "Invalid evidence count: " + str(report["anomaly_count"]), "", "Cost:"]
    lines += ["- " + key + ": " + str(value) for key, value in sorted(report["cost"].items())]
    lines += ["", "Failure classifications:"]
    lines += ["- " + key + ": " + str(value) for key, value in report["failure_counts_by_category"].items()] or ["- none"]
    lines += ["", "Health assessment coverage:"]
    lines += ["- " + key + ": " + str(value) for key, value in report["health_assessment_counts"].items()]
    lines += ["- limitation: " + value for value in report["health_coverage_limitations"]]
    lines += ["", "| Input file | SHA256 | Bytes |", "| --- | --- | ---: |"]
    lines += ["| {path} | {sha256} | {bytes} |".format(**value) for value in report["input_files"]]
    return "\n".join(lines) + "\n"


def run(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--state-dir", type=Path, required=True)
    parser.add_argument("--approval", type=Path, required=True)
    parser.add_argument("--as-of", required=True)
    parser.add_argument("--slo", type=Path)
    parser.add_argument("--out-dir", type=Path, required=True)
    args = parser.parse_args(argv)
    try:
        report = build_report(args.state_dir, args.approval, args.as_of, args.slo)
        args.out_dir.mkdir(mode=0o700, parents=True, exist_ok=False)
        raw = (json.dumps(report, sort_keys=True, indent=2, allow_nan=False) + "\n").encode()
        (args.out_dir / "report.json").write_bytes(raw)
        (args.out_dir / "report.md").write_text(markdown(report))
        print(json.dumps({"status": report["status"], "report_sha256": digest(raw)}))
        return 0
    except (ReportError, ValueError, OSError, KeyError, TypeError):
        print('{"status":"error","reason_code":"invalid_report_evidence_or_output"}', file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(run())
