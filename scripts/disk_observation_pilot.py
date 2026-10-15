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

NODE = "10.80.16.42"
DEVICES = ("sda", "sdb", "nvme0", "nvme1", "nvme2", "nvme3")
TOOL_ROOT = "/opt/atlas-tools/smartmontools-7.2-1ubuntu0.1"
SMARTCTL = TOOL_ROOT + "/usr/sbin/smartctl"
DRIVEDB = TOOL_ROOT + "/usr/share/smartmontools/drivedb.h"
INTERVAL = 1800
MAX_BYTES = 1 << 20


def utcnow():
    return dt.datetime.now(dt.timezone.utc)


def timestamp(value):
    return dt.datetime.fromisoformat(value.replace("Z", "+00:00"))


def digest(raw):
    return hashlib.sha256(raw).hexdigest()


def save(path, value):
    raw = json.dumps(value, indent=2).encode() + b"\n"
    with open(path, "xb") as out:
        out.write(raw)


def replace_state(path, value):
    temporary = path.with_suffix(".new")
    with open(temporary, "w") as out:
        json.dump(value, out)
        out.flush()
        os.fsync(out.fileno())
    os.replace(temporary, path)


def validate_approval(approval, now):
    issued, expires = timestamp(approval["issued_at"]), timestamp(approval["expires_at"])
    if (approval["node"] != NODE or approval["devices"] != list(DEVICES)
            or approval["interval_seconds"] != INTERVAL
            or not issued <= now < expires
            or not dt.timedelta(0) < expires - issued <= dt.timedelta(hours=24)):
        raise ValueError("approval_scope_or_time_invalid")
    return expires


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


def collect(args, state):
    approval = json.loads(args.approval.read_text())
    expires = validate_approval(approval, utcnow())
    seeds = read_seed(Path(state.get("manifest", args.seed)))
    run_dir = args.state_dir / utcnow().strftime("run-%Y%m%dT%H%M%S.%fZ")
    run_dir.mkdir(mode=0o700)
    state.update(in_progress=True, last_run=str(run_dir))
    replace_state(args.state_dir / "state.json", state)
    save(run_dir / "approval.json", approval)
    audit = {"node": NODE, "started_at": utcnow().isoformat(), "commands": [], "status": "running"}
    deadline = time.monotonic() + min(120, (expires - utcnow()).total_seconds())
    budget = MAX_BYTES

    def remote(label, argv):
        nonlocal budget
        validate_approval(approval, utcnow())
        remaining = deadline - time.monotonic()
        if remaining < 16 or budget <= 0:
            raise ValueError("batch_budget_exhausted")
        ssh = ["ssh", "-F", "/dev/null", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes",
               "-o", "ConnectTimeout=5", "-o", "ConnectionAttempts=1", "-o", "ServerAliveInterval=3",
               "-o", "ServerAliveCountMax=1", "-o", "UserKnownHostsFile=" + str(args.known_hosts),
               "root@" + NODE, shlex.join(["timeout", "-k", "2s", "10s"] + argv)]
        started = utcnow()
        result = bounded_process(ssh, 16, min(256 << 10, budget))
        finished = utcnow()
        budget -= len(result["stdout"]) + len(result["stderr"])
        entry = {k: v for k, v in result.items() if k not in ("stdout", "stderr")}
        entry.update(label=label, argv=argv, started_at=started.isoformat(), finished_at=finished.isoformat())
        for stream in ("stdout", "stderr"):
            data = result[stream]
            (run_dir / (label + "." + stream)).write_bytes(data)
            entry[stream + "_sha256"] = digest(data)
            entry[stream + "_bytes"] = len(data)
        audit["commands"].append(entry)
        if result["status"] != "completed" or result["exit_code"] in (124, 137, 255) or result["stderr"]:
            raise ValueError("command_failed_or_termination_unknown")
        return result, finished

    try:
        boot, _ = remote("boot", ["cat", "/proc/sys/kernel/random/boot_id"])
        boot_id = boot["stdout"].decode().strip()
        if boot["exit_code"] != 0 or not re.fullmatch(r"[0-9a-f-]{36}", boot_id):
            raise ValueError("boot_identity_invalid")
        if state.get("boot_id") and state["boot_id"] != boot_id:
            raise ValueError("boot_changed_review_required")
        inputs = []
        for device, previous, baseline_raw, expected in seeds:
            if device.startswith("sd"):
                power, _ = remote(device + "-power", command(device, True))
                power_obj = json.loads(power["stdout"])
                if not ata_active(power_obj, power["exit_code"]):
                    raise ValueError("ata_not_confirmed_active")
            result, finished = remote(device, command(device))
            obj = validated_smart(result["stdout"], result["exit_code"], expected, device)
            observed = dt.datetime.fromtimestamp(obj["local_time"]["time_t"], dt.timezone.utc)
            if not 0 <= (finished - observed).total_seconds() <= 60:
                raise ValueError("device_clock_or_stale_output")
            item = {k: v for k, v in previous.items() if k != "baseline"}
            item.update(file=device + ".stdout", sha256=digest(result["stdout"]), boot_id=boot_id,
                        observed_at=observed.isoformat(), available_at=finished.isoformat(),
                        max_age_seconds=3600, exit_code=result["exit_code"], truncated=False)
            if previous["boot_id"] == boot_id:
                baseline = {k: v for k, v in previous.items() if k != "baseline"}
                baseline["file"] = device + ".baseline"
                (run_dir / baseline["file"]).write_bytes(baseline_raw)
                item["baseline"] = baseline
            inputs.append(item)
        manifest = run_dir / "input.json"
        save(manifest, {"schema": "hardware-health-input-v1", "inputs": inputs})
        replay = [str(args.replay), "--manifest", str(manifest), "--as-of", utcnow().isoformat(),
                  "--out-dir", str(run_dir / "report"), "--db-driver", "postgres", "--db-dsn-env", "ATLAS_DATABASE_DSN"]
        result = bounded_process(replay, 60, MAX_BYTES)
        # Logs may contain database diagnostics: private only, never journaled.
        for stream in ("stdout", "stderr"):
            (run_dir / ("replay." + stream)).write_bytes(result[stream])
        if result["status"] != "completed" or result["exit_code"] != 0:
            raise ValueError("replay_or_persistence_failed")
        report = json.loads((run_dir / "report/report.json").read_text())
        if len(report["rows"]) != 6 or any(row["assessment_status"] != "assessed" or row["trend_status"] in ("counter_reset", "invalid_baseline") for row in report["rows"]):
            raise ValueError("report_quality_or_counter_reset_review_required")
        audit.update(status="success", finished_at=utcnow().isoformat(), input_sha256=digest(manifest.read_bytes()),
                     report_sha256=digest((run_dir / "report/report.json").read_bytes()))
        state.update(manifest=str(manifest), boot_id=boot_id, last_success=utcnow().isoformat(), last_run=str(run_dir), in_progress=False)
        return state
    except Exception as error:
        audit.update(status="paused", reason=type(error).__name__, finished_at=utcnow().isoformat())
        state.update(paused=True, last_run=str(run_dir), reason=str(error) if isinstance(error, ValueError) else type(error).__name__)
        raise
    finally:
        save(run_dir / "audit.json", audit)
        replace_state(args.state_dir / "state.json", state)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for field in ("approval", "seed", "state-dir", "known-hosts", "replay"):
        parser.add_argument("--" + field, type=Path, required=True)
    parser.add_argument("--execute", action="store_true")
    parser.add_argument("--run", action="store_true", help="Stay active until the fixed approval expiry; never renew it")
    args = parser.parse_args()
    os.umask(0o077)
    approval = json.loads(args.approval.read_text())
    expires = validate_approval(approval, utcnow())
    read_seed(args.seed)
    if not args.execute:
        print(json.dumps({"mode": "plan_only", "node": NODE, "devices": DEVICES, "expires_at": expires.isoformat()}))
        return
    if not os.environ.get("ATLAS_DATABASE_DSN") or not args.known_hosts.is_file() or not args.replay.is_file():
        raise ValueError("execution_prerequisites_missing")
    args.state_dir.mkdir(mode=0o700, parents=True, exist_ok=True)
    with open(args.state_dir / "pilot.lock", "a") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        while utcnow() < expires:
            path = args.state_dir / "state.json"
            state = json.loads(path.read_text()) if path.exists() else {}
            if state.get("paused") or state.get("in_progress"):
                raise ValueError("pilot_paused_requires_review")
            delay = max(0, INTERVAL - (utcnow() - timestamp(state["last_success"])).total_seconds()) if state.get("last_success") else 0
            if delay == 0:
                state = collect(args, state)
                print(json.dumps({"status": "success", "last_success": state["last_success"], "disks": 6}), flush=True)
            if not args.run:
                return
            time.sleep(max(0, min(delay or INTERVAL, (expires - utcnow()).total_seconds())))
        print('{"status":"expired","no_further_collection":true}', flush=True)


if __name__ == "__main__":
    try:
        main()
    except Exception as exc:
        print(json.dumps({"status": "stopped", "error_type": type(exc).__name__}))
        raise SystemExit(1) from None
