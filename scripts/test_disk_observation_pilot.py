import copy
import datetime as dt
import importlib.util
import json
from pathlib import Path
import sys
import tempfile
import unittest
from types import SimpleNamespace
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("pilot", Path(__file__).with_name("disk_observation_pilot.py"))
pilot = importlib.util.module_from_spec(spec)
spec.loader.exec_module(pilot)


class PilotTests(unittest.TestCase):
    def approval(self):
        now = pilot.utcnow()
        return dict(node=pilot.NODE, devices=list(pilot.DEVICES), interval_seconds=1800,
                    issued_at=(now-dt.timedelta(seconds=1)).isoformat(), expires_at=(now+dt.timedelta(hours=23)).isoformat())

    def test_approval_limits(self):
        good = self.approval()
        pilot.validate_approval(good, pilot.utcnow())
        for change in [dict(node="10.80.16.43"), dict(devices=["sda"]), dict(interval_seconds=60),
                       dict(expires_at=(pilot.utcnow()+dt.timedelta(days=2)).isoformat()),
                       dict(expires_at=(pilot.utcnow()-dt.timedelta(seconds=1)).isoformat())]:
            with self.assertRaises(ValueError):
                pilot.validate_approval(dict(good, **change), pilot.utcnow())

    def test_commands_have_no_self_test_or_scan(self):
        for device in pilot.DEVICES:
            command = pilot.command(device)
            self.assertNotIn("-t", command)
            self.assertNotIn("--scan", command)
            self.assertEqual(command[-1], "/dev/"+device)
            self.assertIn("standby,3" if device.startswith("sd") else "nvme,0xffffffff", command)
        with self.assertRaises(ValueError):
            pilot.command("sda;reboot")

    def test_bounded_process_limits_and_separate_streams(self):
        result = pilot.bounded_process([sys.executable, "-c", "import sys;print('ok');sys.stderr.write('error')"], 2, 1024)
        self.assertEqual(result["status"], "completed")
        self.assertEqual(result["stderr"], b"error")
        result = pilot.bounded_process([sys.executable, "-c", "print('x'*10000)"], 2, 100)
        self.assertIn("output_limit", result["status"])
        self.assertLessEqual(len(result["stdout"])+len(result["stderr"]), 100)
        result = pilot.bounded_process([sys.executable, "-c", "import time;time.sleep(2)"], .05, 100)
        self.assertIn("timeout", result["status"])

    def test_identity_and_health_exit_bits(self):
        obj = dict(serial_number="serial", model_name="model", firmware_version="fw", device=dict(name="/dev/sda", protocol="ATA"), smartctl=dict(exit_status=8))
        pilot.validated_smart(json.dumps(obj).encode(), 8, obj, "sda")
        for code in (1, 2, 4, 255, -1):
            with self.assertRaises(ValueError):
                pilot.validated_smart(json.dumps(obj).encode(), code, obj, "sda")
        changed = dict(obj, serial_number="replacement")
        with self.assertRaises(ValueError):
            pilot.validated_smart(json.dumps(changed).encode(), 8, obj, "sda")

    def test_power_mode_requires_positive_evidence(self):
        old = {"smartctl": {"exit_status": 0, "output": ["Device is in ACTIVE or IDLE mode"]}}
        self.assertTrue(pilot.ata_active(old, 0))
        for obj, code in [({}, 0), (old, 3), ({"smartctl":{"exit_status":0,"output":["Device is in STANDBY mode"]}}, 0), (dict(old, power_mode="STANDBY"), 0)]:
            self.assertFalse(pilot.ata_active(obj, code))

    def setup_seed(self, root):
        seed_dir = root / "seed"
        seed_dir.mkdir()
        inputs = []
        for device in pilot.DEVICES:
            raw = json.dumps(dict(serial_number=device, model_name="model", firmware_version="fw", device=dict(name="/dev/"+device, protocol="ATA" if device.startswith("sd") else "NVMe"), smartctl=dict(exit_status=0))).encode()
            (seed_dir / (device+".json")).write_bytes(raw)
            inputs.append(dict(file=device+".json", sha256=pilot.digest(raw), node=pilot.NODE, boot_id="a"*36, reference=device))
        seed = seed_dir / "input.json"
        pilot.save(seed, dict(schema="hardware-health-input-v1", inputs=inputs))
        approval = root / "approval.json"
        pilot.save(approval, self.approval())
        state_dir = root / "state"
        state_dir.mkdir()
        return SimpleNamespace(seed=seed, approval=approval, state_dir=state_dir, replay=Path("/replay"), known_hosts=Path("/known_hosts"))

    def test_complete_batch_and_failed_batch_preserves_previous(self):
        for failure in (None, "standby", "replacement", "timeout"):
            with self.subTest(failure=failure), tempfile.TemporaryDirectory() as temp:
                args = self.setup_seed(Path(temp))
                calls = []
                def fake(argv, timeout, limit):
                    calls.append(argv)
                    if argv[0] == "/replay":
                        directory = Path(argv[argv.index("--out-dir")+1]); directory.mkdir()
                        pilot.save(directory/"report.json", {"rows":[{"assessment_status":"assessed", "trend_status":"available"}]*6})
                        return dict(status="completed", exit_code=0, stdout=b"", stderr=b"")
                    line = argv[-1]
                    obj = {}
                    if "boot_id" in line:
                        raw = b"a"*36
                    elif " -a " not in line:
                        raw = json.dumps(dict(power_mode="STANDBY" if failure == "standby" else "ACTIVE or IDLE", smartctl=dict(exit_status=0))).encode()
                    else:
                        device = line.split("/dev/")[-1]
                        raw = (args.seed.parent / (device+".json")).read_bytes()
                        obj = json.loads(raw)
                        obj["local_time"] = dict(time_t=int(pilot.utcnow().timestamp()))
                        if failure == "replacement": obj["serial_number"] = "changed"
                        raw = json.dumps(obj).encode()
                    return dict(status="timeout_remote_termination_unknown" if failure == "timeout" else "completed", exit_code=0, stdout=raw, stderr=b"")
                state = {}
                with patch.object(pilot, "bounded_process", side_effect=fake):
                    if failure:
                        with self.assertRaises(ValueError): pilot.collect(args, state)
                        self.assertTrue(state["paused"])
                        self.assertNotIn("manifest", state)
                        self.assertFalse(any(call[0] == "/replay" for call in calls))
                    else:
                        pilot.collect(args, state)
                        self.assertFalse(state["in_progress"])
                        manifest = json.loads(Path(state["manifest"]).read_text())
                        self.assertEqual(len(manifest["inputs"]), 6)
                        self.assertTrue(all("baseline" in item for item in manifest["inputs"]))
                        self.assertEqual(len(calls), 10)
                        self.assertIn("--db-dsn-env", calls[-1])


if __name__ == "__main__":
    unittest.main()
