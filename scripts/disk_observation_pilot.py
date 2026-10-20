#!/usr/bin/env python3
"""Bounded six-disk development pilot. No discovery, installation or self-tests.

Runs on the approved gateway with its existing SSH key/known_hosts. The approval
file is an operator declaration, not a cryptographic authorization mechanism.
"""
import argparse
import datetime as dt
import fcntl
import hashlib
import json
import os
from pathlib import Path
import re
import selectors
import shlex
import subprocess
import time

NODE = "10.80.16.41"
DEVICES = ("sda", "sdb", "nvme0", "nvme1", "nvme2", "nvme3")
TOOL_ROOT = "/opt/atlas-tools/smartmontools-7.2-1ubuntu0.1"
SMARTCTL = TOOL_ROOT + "/usr/sbin/smartctl"
DRIVEDB = TOOL_ROOT + "/usr/share/smartmontools/drivedb.h"
INTERVAL = 1800
MAX_BYTES = 1 << 20
BATCH_SECONDS = 120
SCHEMA = "disk-observation-approval-v2"


def utcnow():
    return dt.datetime.now(dt.timezone.utc)


def timestamp(value):
    result = dt.datetime.fromisoformat(value.replace("Z", "+00:00"))
    if result.tzinfo is None:
        raise ValueError("timestamp_timezone_required")
    return result.astimezone(dt.timezone.utc)


def digest(raw):
    return hashlib.sha256(raw).hexdigest()


def save(path, value):
    raw = json.dumps(value, indent=2).encode() + b"\n"
    with open(path, "xb") as out:
        out.write(raw)
        out.flush()
        os.fsync(out.fileno())
    sync_directory(path.parent)


def sync_directory(path):
    fd = os.open(path, os.O_RDONLY)
    try:
        os.fsync(fd)
    finally:
        os.close(fd)


def save_bytes(path, raw):
    with open(path, "xb") as out:
        out.write(raw)
        out.flush()
        os.fsync(out.fileno())
    sync_directory(path.parent)


def replace_state(path, value):
    temporary = path.with_suffix(".new")
    with open(temporary, "w") as out:
        json.dump(value, out)
        out.flush()
        os.fsync(out.fileno())
    os.replace(temporary, path)
    sync_directory(path.parent)


def event(args, kind, **fields):
    record = dict(schema="disk-observation-event-v1", at=utcnow().isoformat(), kind=kind, **fields)
    with open(args.state_dir / "events.jsonl", "ab") as out:
        out.write((json.dumps(record, sort_keys=True) + "\n").encode())
        out.flush()
        os.fsync(out.fileno())
    sync_directory(args.state_dir)


def validate_approval(approval, now):
    issued, expires = timestamp(approval["issued_at"]), timestamp(approval["expires_at"])
    hashes = ("binding_sha256", "seed_sha256", "collector_sha256", "replay_sha256",
              "known_hosts_sha256", "smartctl_sha256", "drivedb_sha256", "expected_boot_id_sha256")
    if (approval.get("schema") != SCHEMA
            or not re.fullmatch(r"[a-z0-9][a-z0-9-]{0,95}", approval.get("run_id", ""))
            or any(not re.fullmatch(r"[0-9a-f]{64}", approval.get(k, "")) for k in hashes)
            or approval["node"] != NODE or approval["devices"] != list(DEVICES)
            or approval["interval_seconds"] != INTERVAL
            or not issued <= now < expires
            or expires - issued != dt.timedelta(hours=24)):
        raise ValueError("approval_scope_or_time_invalid")
    return expires


def frozen_files(args, approval):
    paths = {"binding": args.state_dir / "binding.json", "seed": args.seed,
             "collector": Path(__file__), "replay": args.replay, "known_hosts": args.known_hosts}
    for name, path in paths.items():
        if digest(path.read_bytes()) != approval[name + "_sha256"]:
            raise ValueError("frozen_" + name + "_changed")
    frozen = args.state_dir / "approval.json"
    if frozen.read_bytes() != args.approval.read_bytes():
        raise ValueError("frozen_approval_changed")
    binding = json.loads(paths["binding"].read_bytes())
    if (binding.get("schema") != "disk-observation-binding-v1" or binding.get("node") != NODE
            or len(binding.get("entity_ids", [])) != 6
            or len(set(binding["entity_ids"])) != 6):
        raise ValueError("frozen_binding_invalid")
    return binding


def classify(reason):
    if any(word in reason for word in ("identity", "boot_", "replacement")):
        return "identity"
    if any(word in reason for word in ("persistence", "receipt", "replay_")):
        return "persistence"
    if any(word in reason for word in ("scope", "binding", "frozen", "approval")):
        return "scope"
    if any(word in reason for word in ("budget", "deadline")):
        return "budget"
    if any(word in reason for word in ("quality", "reset", "baseline", "stale")):
        return "quality"
    return "execution"


def command(device, power=False):
    if device not in DEVICES:
        raise ValueError("device_not_approved")
    if device.startswith("sd"):
        args = [SMARTCTL, "-B", DRIVEDB, "-d", "ata", "-n", "standby,3", "--json=o"]
        if not power:
            args += ["-a"]
    else:
        if power:
            raise ValueError("power_check_only_for_ata")
        args = [SMARTCTL, "-d", "nvme,0xffffffff", "--json=o", "-a"]
    return args + ["/dev/" + device]


def bounded_process(argv, timeout, limit):
    """Bound combined stdout/stderr while draining both pipes without deadlock."""
    proc = subprocess.Popen(argv, stdout=subprocess.PIPE, stderr=subprocess.PIPE, start_new_session=True)
    buffers = [bytearray(), bytearray()]
    status = "completed"
    end = time.monotonic() + timeout
    try:
        with selectors.DefaultSelector() as selector:
            for index, stream in enumerate((proc.stdout, proc.stderr)):
                selector.register(stream, selectors.EVENT_READ, index)
            while selector.get_map():
                remaining = end - time.monotonic()
                if remaining <= 0:
                    status = "timeout_remote_termination_unknown"
                    break
                for key, _ in selector.select(min(remaining, 0.2)):
                    data = os.read(key.fileobj.fileno(), 8192)
                    if not data:
                        selector.unregister(key.fileobj)
                        continue
                    room = max(0, limit - sum(map(len, buffers)))
                    buffers[key.data].extend(data[:room])
                    if len(data) > room:
                        status = "output_limit_remote_termination_unknown"
                        break
                if status != "completed":
                    break
        if status == "completed":
            try:
                proc.wait(timeout=max(0.01, end - time.monotonic()))
            except subprocess.TimeoutExpired:
                status = "timeout_remote_termination_unknown"
        if status != "completed":
            os.killpg(proc.pid, 9)
            proc.wait()
        return {"status": status, "exit_code": proc.returncode,
                "stdout": bytes(buffers[0]), "stderr": bytes(buffers[1])}
    finally:
        if proc.poll() is None:
            os.killpg(proc.pid, 9)
            proc.wait()
        proc.stdout.close()
        proc.stderr.close()


def validated_smart(raw, code, expected, device):
    obj = json.loads(raw)
    if (code < 0 or code > 255 or code & 7 or obj["smartctl"]["exit_status"] != code
            or obj["device"]["name"] != "/dev/" + device
            or obj["device"]["protocol"] != expected["device"]["protocol"]
            or any(not obj.get(k) or obj[k] != expected[k] for k in ("serial_number", "model_name", "firmware_version"))):
        raise ValueError("smart_command_or_identity_changed")
    # SMART health bits are evidence, not transport failures; preserve them.
    return obj


def ata_active(obj, code):
    if code != 0 or obj.get("smartctl", {}).get("exit_status") != 0:
        return False
    mode = obj.get("power_mode")
    if mode is not None:
        return mode in ("ACTIVE", "IDLE", "ACTIVE or IDLE")
    # smartctl 7.2 exposes this check only through --json=o text output.
    lines = obj.get("smartctl", {}).get("output", [])
    modes = [line for line in lines if re.fullmatch(r"Device is in .+ mode", line)]
    return modes == ["Device is in ACTIVE or IDLE mode"]


def read_seed(path):
    manifest = json.loads(path.read_text())
    inputs = manifest["inputs"]
    if manifest["schema"] != "hardware-health-input-v1" or len(inputs) != 6:
        raise ValueError("seed_scope_invalid")
    result = []
    for device, item in zip(DEVICES, inputs):
        raw_path = (path.parent / item["file"]).resolve()
        if not raw_path.is_relative_to(path.parent.resolve()):
            raise ValueError("seed_path_escape")
        raw = raw_path.read_bytes()
        if len(raw) > 256 << 10 or digest(raw) != item["sha256"] or item["node"] != NODE:
            raise ValueError("seed_integrity_invalid")
        obj = json.loads(raw)
        if obj["device"]["name"] != "/dev/" + device:
            raise ValueError("seed_device_order_invalid")
        result.append((device, item, raw, obj))
    return result


def collect(args, state, slot_index=None):
    approval = json.loads(args.approval.read_text())
    expires = validate_approval(approval, utcnow())
    issued = timestamp(approval["issued_at"])
    binding = frozen_files(args, approval)
    current_slot = int((utcnow() - issued).total_seconds() // INTERVAL)
    slot_index = current_slot if slot_index is None else slot_index
    if slot_index != current_slot or not 0 <= slot_index < 48 or slot_index <= state.get("last_slot", -1):
        raise ValueError("scheduled_slot_invalid")
    scheduled = issued + dt.timedelta(seconds=slot_index * INTERVAL)
    if utcnow() >= scheduled + dt.timedelta(seconds=BATCH_SECONDS):
        raise ValueError("scheduled_slot_missed")
    original = read_seed(args.seed)
    seeds = read_seed(Path(state.get("manifest", args.seed)))
    for (_, first, _, expected), (_, item, _, obj) in zip(original, seeds):
        if (any(obj.get(k) != expected.get(k) for k in ("serial_number", "model_name", "firmware_version"))
                or item.get("scope") != binding["snapshot_scope"]
                or item.get("generation") != binding["snapshot_generation"]
                or item.get("boot_id") != first.get("boot_id")):
            raise ValueError("seed_identity_or_scope_changed")
    run_dir = args.state_dir / ("run-%02d-" % slot_index + utcnow().strftime("%Y%m%dT%H%M%S.%fZ"))
    run_dir.mkdir(mode=0o700)
    started = utcnow()
    monotonic_start = time.monotonic()
    deadline_at = min(expires, scheduled + dt.timedelta(seconds=BATCH_SECONDS),
                      started + dt.timedelta(seconds=BATCH_SECONDS))
    deadline = monotonic_start + (deadline_at - started).total_seconds()
    audit = dict(schema="disk-observation-batch-v2", run_id=approval["run_id"], node=NODE,
                 slot_index=slot_index, scheduled_at=scheduled.isoformat(), started_at=started.isoformat(),
                 deadline_at=deadline_at.isoformat(), binding_sha256=approval["binding_sha256"],
                 commands=[], status="running", output_bytes=0)
    replace_state(run_dir / "intent.json", audit)
    state.update(in_progress=True, last_run=str(run_dir), last_slot=slot_index)
    replace_state(args.state_dir / "state.json", state)
    budget = MAX_BYTES

    def execute(label, argv, timeout, remote_command=False):
        nonlocal budget
        validate_approval(approval, utcnow())
        remaining = deadline - time.monotonic()
        if remaining <= 0 or budget <= 0:
            raise ValueError("batch_budget_exhausted")
        entry = dict(label=label, status="in_progress", started_at=utcnow().isoformat())
        audit["commands"].append(entry)
        replace_state(run_dir / "intent.json", audit)
        result = bounded_process(argv, min(timeout, remaining), min(256 << 10, budget))
        finished = utcnow()
        budget -= len(result["stdout"]) + len(result["stderr"])
        entry.update({k: v for k, v in result.items() if k not in ("stdout", "stderr")})
        entry["finished_at"] = finished.isoformat()
        for stream in ("stdout", "stderr"):
            data = result[stream]
            save_bytes(run_dir / (label + "." + stream), data)
            entry[stream + "_sha256"] = digest(data)
            entry[stream + "_bytes"] = len(data)
        audit["output_bytes"] = MAX_BYTES - budget
        replace_state(run_dir / "intent.json", audit)
        if time.monotonic() > deadline or utcnow() > deadline_at:
            raise ValueError("batch_deadline_exceeded_" + ("persistence_result_unknown" if label == "replay" else "execution"))
        if result["status"] != "completed" or result["exit_code"] in (124, 137, 255):
            raise ValueError("persistence_result_unknown" if label == "replay" else "command_failed_or_termination_unknown")
        if remote_command and result["stderr"]:
            raise ValueError("command_stderr_review_required")
        return result, finished

    def remote(label, argv):
        if deadline - time.monotonic() < 16:
            raise ValueError("batch_budget_exhausted")
        ssh = ["ssh", "-F", "/dev/null", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes",
               "-o", "ConnectTimeout=5", "-o", "ConnectionAttempts=1", "-o", "ServerAliveInterval=3",
               "-o", "ServerAliveCountMax=1", "-o", "UserKnownHostsFile=" + str(args.known_hosts),
               "root@" + NODE, shlex.join(["timeout", "-k", "2s", "10s"] + argv)]
        return execute(label, ssh, 16, True)

    cli = [str(args.replay), "--installation-binding", str(args.state_dir / "binding.json"),
           "--db-driver", "postgres", "--db-dsn-env", "ATLAS_DATABASE_DSN",
           "--deadline", deadline_at.isoformat()]
    try:
        check, _ = execute("registry-check", cli + ["--check-installation-binding"], 15)
        if check["exit_code"] != 0:
            raise ValueError("registry_scope_or_expected_version_changed")
        tool, _ = remote("toolchain", ["sha256sum", SMARTCTL, DRIVEDB])
        if tool["exit_code"] != 0:
            raise ValueError("frozen_remote_toolchain_unavailable")
        hashes = tool["stdout"].decode().splitlines()
        if (len(hashes) != 2 or hashes[0].split() != [approval["smartctl_sha256"], SMARTCTL]
                or hashes[1].split() != [approval["drivedb_sha256"], DRIVEDB]):
            raise ValueError("frozen_remote_toolchain_changed")
        boot, _ = remote("boot", ["cat", "/proc/sys/kernel/random/boot_id"])
        boot_id = boot["stdout"].decode().strip()
        if (boot["exit_code"] != 0 or not re.fullmatch(r"[0-9a-f-]{36}", boot_id)
                or digest(boot_id.encode()) != approval["expected_boot_id_sha256"]):
            raise ValueError("boot_identity_changed_review_required")
        inputs = []
        for device, previous, baseline_raw, expected in seeds:
            if device.startswith("sd"):
                power, _ = remote(device + "-power", command(device, True))
                if not ata_active(json.loads(power["stdout"]), power["exit_code"]):
                    raise ValueError("ata_not_confirmed_active")
            result, finished = remote(device, command(device))
            obj = validated_smart(result["stdout"], result["exit_code"], expected, device)
            observed = dt.datetime.fromtimestamp(obj["local_time"]["time_t"], dt.timezone.utc)
            if not 0 <= (finished - observed).total_seconds() <= 60 or observed < scheduled:
                raise ValueError("device_clock_or_stale_output")
            item = {k: v for k, v in previous.items() if k != "baseline"}
            item.update(file=device + ".stdout", sha256=digest(result["stdout"]), boot_id=boot_id,
                        observed_at=observed.isoformat(), available_at=finished.isoformat(),
                        max_age_seconds=3600, exit_code=result["exit_code"], truncated=False)
            if previous["boot_id"] == boot_id:
                baseline = {k: v for k, v in previous.items() if k != "baseline"}
                baseline["file"] = device + ".baseline"
                save_bytes(run_dir / baseline["file"], baseline_raw)
                item["baseline"] = baseline
            inputs.append(item)
        manifest = run_dir / "input.json"
        save(manifest, {"schema": "hardware-health-input-v1", "inputs": inputs})
        audit["input_sha256"] = digest(manifest.read_bytes())
        receipt_path = run_dir / "persistence.json"
        replay = cli + ["--manifest", str(manifest), "--as-of", utcnow().isoformat(),
                        "--out-dir", str(run_dir / "report"), "--persistence-receipt", str(receipt_path)]
        result, _ = execute("replay", replay, 60)
        if result["exit_code"] != 0:
            raise ValueError("replay_or_persistence_failed")
        receipt = json.loads(receipt_path.read_bytes())
        audit["persistence"] = dict(receipt, receipt_sha256=digest(receipt_path.read_bytes()))
        if (receipt.get("verified") is not True or receipt.get("binding_sha256") != approval["binding_sha256"]
                or receipt.get("scope_id") != binding["scope_id"]
                or str(receipt.get("roster_version")) != binding["expected_roster_version"]
                or sorted(r["entity_id"] for r in receipt.get("entities", [])) != sorted(binding["entity_ids"])
                or timestamp(receipt["completed_at"]) > deadline_at):
            raise ValueError("persistence_receipt_invalid")
        report = json.loads((run_dir / "report/report.json").read_bytes())
        audit["report_sha256"] = digest((run_dir / "report/report.json").read_bytes())
        if (receipt.get("report_sha256") != audit["report_sha256"]
                or not started <= timestamp(receipt["completed_at"]) <= utcnow()
                or len({r["snapshot_id"] for r in receipt["entities"]}) != 6
                or any(not r["snapshot_id"] or not scheduled <= timestamp(r["observed_at"])
                       <= timestamp(r["available_at"]) <= timestamp(receipt["completed_at"])
                       for r in receipt["entities"])):
            raise ValueError("persistence_receipt_evidence_invalid")
        if (len(report["rows"]) != 6 or {r["entity_id"] for r in report["rows"]} != set(binding["entity_ids"])
                or any(row["trend_status"] in ("counter_reset", "invalid_baseline") for row in report["rows"])):
            raise ValueError("report_quality_or_counter_reset_review_required")
        audit["assessment_counts"] = {status: sum(r["assessment_status"] == status for r in report["rows"])
                                      for status in sorted({r["assessment_status"] for r in report["rows"]})}
        if time.monotonic() > deadline or utcnow() > deadline_at:
            raise ValueError("batch_deadline_exceeded_after_persistence")
        audit.update(status="success", finished_at=utcnow().isoformat())
        state.update(manifest=str(manifest), last_success=audit["finished_at"], in_progress=False)
        return state
    except Exception as error:
        reason = str(error) if isinstance(error, ValueError) and re.fullmatch(r"[a-z_]+", str(error)) else type(error).__name__
        audit.update(status="paused", reason_code=reason, category=classify(reason), finished_at=utcnow().isoformat())
        state.update(paused=True, in_progress=False, reason=reason)
        event(args, "paused", run_id=approval["run_id"], slot_index=slot_index, reason_code=reason, category=classify(reason))
        raise
    finally:
        audit["duration_seconds"] = round(time.monotonic() - monotonic_start, 6)
        audit["output_bytes"] = MAX_BYTES - budget
        save(run_dir / "audit.json", audit)
        replace_state(args.state_dir / "state.json", state)
        if audit["status"] == "success" and (time.monotonic() > deadline or utcnow() > deadline_at):
            audit.update(status="paused", reason_code="batch_deadline_exceeded_after_audit", category="budget", finished_at=utcnow().isoformat())
            state.update(paused=True, reason=audit["reason_code"])
            replace_state(run_dir / "audit.json", audit)
            replace_state(args.state_dir / "state.json", state)
            event(args, "paused", run_id=approval["run_id"], slot_index=slot_index, reason_code=audit["reason_code"], category="budget")
            raise ValueError(audit["reason_code"])

def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for field in ("approval", "seed", "state-dir", "known-hosts", "replay"):
        parser.add_argument("--" + field, type=Path, required=True)
    parser.add_argument("--execute", action="store_true")
    parser.add_argument("--run", action="store_true", help="Stay active until the fixed approval expiry; never renew it")
    args = parser.parse_args()
    os.umask(0o077)
    if not args.execute:
        approval = json.loads(args.approval.read_text())
        expires = validate_approval(approval, utcnow())
        read_seed(args.seed)
        print(json.dumps({"mode": "plan_only", "node": NODE, "devices": DEVICES, "expires_at": expires.isoformat()}))
        return
    args.state_dir.mkdir(mode=0o700, parents=True, exist_ok=True)
    with open(args.state_dir / "pilot.lock", "a") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        path = args.state_dir / "state.json"
        state = json.loads(path.read_text()) if path.exists() else {}
        approval = {}
        try:
            approval = json.loads(args.approval.read_text())
            expires = validate_approval(approval, utcnow())
            read_seed(args.seed)
            if not os.environ.get("ATLAS_DATABASE_DSN") or not args.known_hosts.is_file() or not args.replay.is_file():
                raise ValueError("execution_prerequisites_missing")
            frozen = args.state_dir / "approval.json"
            if not frozen.exists():
                save_bytes(frozen, args.approval.read_bytes())
            frozen_files(args, approval)
            if state.get("paused") or state.get("in_progress"):
                raise ValueError("pilot_paused_requires_review")
            event(args, "started", run_id=approval["run_id"], approval_sha256=digest(frozen.read_bytes()))
            issued = timestamp(approval["issued_at"])
            while utcnow() < expires:
                next_slot = state.get("last_slot", -1) + 1
                due = issued + dt.timedelta(seconds=next_slot * INTERVAL)
                if next_slot >= 48:
                    due = expires
                delay = (due - utcnow()).total_seconds()
                if delay > 0:
                    if not args.run:
                        return
                    time.sleep(min(delay, 30))
                    continue
                if next_slot >= 48:
                    break
                state = collect(args, state, next_slot)
                print(json.dumps({"status": "success", "slot_index": next_slot,
                                  "last_success": state["last_success"], "disks": 6}), flush=True)
                if not args.run:
                    return
            state.update(expired=True, expired_at=utcnow().isoformat(), in_progress=False)
            replace_state(path, state)
            event(args, "expired", run_id=approval["run_id"], no_further_collection=True)
            print('{"status":"expired","no_further_collection":true}', flush=True)
        except Exception as exc:
            reason = str(exc) if isinstance(exc, ValueError) and re.fullmatch(r"[a-z_]+", str(exc)) else type(exc).__name__
            state.update(paused=True, reason=reason)
            replace_state(path, state)
            event(args, "stopped", run_id=approval.get("run_id", "unknown"), reason_code=reason, category=classify(reason))
            raise


if __name__ == "__main__":
    try:
        main()
    except Exception as exc:
        print(json.dumps({"status": "stopped", "error_type": type(exc).__name__}))
        raise SystemExit(1) from None
