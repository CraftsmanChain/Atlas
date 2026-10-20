import contextlib
import copy
import datetime as dt
import importlib.util
import json
from pathlib import Path
import shutil
import tempfile
import unittest

spec = importlib.util.spec_from_file_location("disk_report", Path(__file__).with_name("disk_observation_report.py"))
report = importlib.util.module_from_spec(spec)
spec.loader.exec_module(report)


def write(path, obj):
    path.parent.mkdir(parents=True, exist_ok=True)
    raw = (json.dumps(obj, sort_keys=True) + "\n").encode()
    path.write_bytes(raw)
    return report.digest(raw)


class ReportTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.temp = tempfile.TemporaryDirectory()
        cls.root = Path(cls.temp.name)
        cls.start = dt.datetime(2026, 1, 1, tzinfo=dt.timezone.utc)
        cls.end = cls.start + dt.timedelta(hours=24)
        cls.as_of = cls.end + dt.timedelta(seconds=1)
        cls.devices = ["sda", "nvme0", "nvme1"]  # Dynamic node and three devices, never the former six.
        cls.ids = {d: report.digest(("entity-"+d).encode()) for d in cls.devices}
        cls.binding = dict(schema="disk-observation-binding-v1", node="10.80.16.41", scope_id="test-scope-41",
                           expected_roster_version="1", asset_identity_sha256="a"*64,
                           snapshot_scope="test-41", snapshot_generation="gen-41", entity_ids=sorted(cls.ids.values()))
        binding_sha = write(cls.root/"binding.json", cls.binding)
        cls.approval = dict(schema="disk-observation-approval-v2", run_id="test-run-41", node="10.80.16.41",
                            devices=cls.devices, issued_at=report.stamp(cls.start), expires_at=report.stamp(cls.end),
                            interval_seconds=1800, binding_sha256=binding_sha)
        for key in ("seed", "collector", "replay", "known_hosts", "smartctl", "drivedb", "expected_boot_id"):
            cls.approval[key+"_sha256"]="c"*64
        cls.approval["expected_boot_id_sha256"]=report.digest(("a"*36).encode())
        write(cls.root/"approval.json", cls.approval)
        labels = ["registry-check", "toolchain", "boot", "sda-power", "sda", "nvme0", "nvme1", "replay"]
        for slot in range(48):
            directory = cls.root/("run-%02d-test" % slot)
            directory.mkdir()
            scheduled = cls.start + dt.timedelta(seconds=1800*slot)
            begun = scheduled+dt.timedelta(seconds=1)
            observed = scheduled+dt.timedelta(seconds=3)
            available = scheduled+dt.timedelta(seconds=5)
            completed = scheduled+dt.timedelta(seconds=8)
            finished = scheduled+dt.timedelta(seconds=10)
            inputs=[];rows=[];entities=[]
            for index, device in enumerate(cls.devices):
                raw = json.dumps({"device":{"name":"/dev/"+device},"local_time":{"time_t":int(observed.timestamp())},
                                  "serial_number":"SYNTHETIC-PRIVATE-RAW-NEVER-REPORT", "smartctl":{"exit_status":0}}).encode()
                (directory/(device+".stdout")).write_bytes(raw)
                sha = report.digest(raw)
                inputs.append(dict(file=device+".stdout", sha256=sha, node="10.80.16.41",
                                   observed_at=report.stamp(observed), available_at=report.stamp(available),exit_code=0))
                rows.append(dict(entity_id=cls.ids[device],device="/dev/"+device,input_sha256=sha,
                                 observed_at=report.stamp(observed),available_at=report.stamp(available),
                                 assessment_status="assessed",trend_status="available"))
                entities.append(dict(entity_id=cls.ids[device],snapshot_id=str(slot*3+index+1),
                                     observed_at=report.stamp(observed),available_at=report.stamp(available),input_sha256=sha))
            manifest_sha=write(directory/"input.json",dict(schema="hardware-health-input-v1",inputs=inputs))
            report_sha=write(directory/"report/report.json",dict(input_manifest_sha256=manifest_sha,rows=rows))
            for row in entities:row["report_sha256"]=report_sha
            entities.sort(key=lambda x:x["entity_id"])
            receipt=dict(verified=True, completed_at=report.stamp(completed),binding_sha256=binding_sha,
                         scope_id=cls.binding["scope_id"],roster_version="1",entities=entities,report_sha256=report_sha,
                         snapshots_created=3,snapshots_duplicate=0)
            receipt_sha=write(directory/"persistence.json",receipt)
            commands=[];size=0
            for label in labels:
                output=directory/(label+".stdout")
                if not output.exists():
                    output.write_bytes(("a"*36).encode() if label=="boot" else
                                       (("c"*64+" /tool\n"+"c"*64+" /drivedb\n").encode() if label=="toolchain" else b"ok"))
                error=directory/(label+".stderr");error.write_bytes(b"")
                command=dict(label=label,status="completed",exit_code=0,started_at=report.stamp(begun),finished_at=report.stamp(available))
                for stream,path in (("stdout",output),("stderr",error)):
                    raw=path.read_bytes();command[stream+"_bytes"]=len(raw);command[stream+"_sha256"]=report.digest(raw);size+=len(raw)
                commands.append(command)
            audit=dict(schema="disk-observation-batch-v2",run_id=cls.approval["run_id"],node="10.80.16.41",
                       slot_index=slot,scheduled_at=report.stamp(scheduled),started_at=report.stamp(begun),finished_at=report.stamp(finished),
                       deadline_at=report.stamp(scheduled+dt.timedelta(seconds=120)),status="success",binding_sha256=binding_sha,
                       duration_seconds=9,output_bytes=size,commands=commands,input_sha256=manifest_sha,report_sha256=report_sha,
                       persistence=dict(receipt,receipt_sha256=receipt_sha))
            write(directory/"audit.json",audit);write(directory/"intent.json",dict(audit,status="running"))
        events=[dict(schema="disk-observation-event-v1",run_id=cls.approval["run_id"],kind="started",at=report.stamp(cls.start)),
                dict(schema="disk-observation-event-v1",run_id=cls.approval["run_id"],kind="expired",at=report.stamp(cls.end))]
        (cls.root/"events.jsonl").write_text("\n".join(json.dumps(x) for x in events)+"\n")
        (cls.root/"slo-evidence.json").write_text('{"source":"synthetic business metrics"}\n')
        cls.slo=dict(schema="disk-observation-slo-v1",run_id=cls.approval["run_id"],node="10.80.16.41",scope_id=cls.binding["scope_id"],
                     expected_roster_version="1",window_start=report.stamp(cls.start),window_end=report.stamp(cls.end),
                     valid=True,status="pass",load_comparable=True,source_evidence=[dict(path="slo-evidence.json",sha256=report.digest((cls.root/"slo-evidence.json").read_bytes()))],
                     metrics={"p95":{"baseline":100,"observation":102,"unit":"ms"},"p99":{"baseline":200,"observation":204,"unit":"ms"},
                              "throughput":{"baseline":100,"observation":99,"unit":"requests_per_second"}})
        write(cls.root/"slo.json",cls.slo)

    @classmethod
    def tearDownClass(cls):cls.temp.cleanup()

    def build(self, **kwargs):
        return report.build_report(self.root,self.root/"approval.json",kwargs.pop("as_of",self.as_of),
                                   kwargs.pop("slo",self.root/"slo.json"),now=kwargs.pop("now",self.as_of),**kwargs)

    @contextlib.contextmanager
    def changed(self,path,mutate):
        path=self.root/path;old=path.read_bytes()
        obj=json.loads(old);mutate(obj);write(path,obj)
        try:yield
        finally:path.write_bytes(old)

    def test_complete_dynamic_node_entities_and_exact_slo_boundaries(self):
        value=self.build();self.assertEqual(value["status"],"pass",value["blocking_reasons"])
        self.assertEqual(value["node"],"10.80.16.41");self.assertEqual(len(value["entities"]),3)
        self.assertTrue(all(x["occupied_slots"]==48 for x in value["entities"]))
        self.assertFalse(any(x in json.dumps(value) for x in ["SYNTHETIC-PRIVATE-RAW-NEVER-REPORT","serial_number","postgres://"]))
        self.assertEqual(self.build(),value)

    def test_window_has_to_elapse_and_future_as_of_rejected(self):
        value=self.build(as_of=self.end-dt.timedelta(seconds=1))
        self.assertIn("observation_window_not_elapsed",value["blocking_reasons"])
        with self.assertRaises(report.ReportError):self.build(as_of=self.as_of+dt.timedelta(seconds=1))

    def test_missing_and_duplicate_slots_never_pass(self):
        original=self.root/"run-01-test";moved=self.root/"saved-batch";original.rename(moved)
        try:self.assertIn("missing_slot_audit",self.build()["blocking_reasons"])
        finally:moved.rename(original)
        duplicate=self.root/"run-duplicate";shutil.copytree(original,duplicate)
        try:
            value=self.build();self.assertIn("duplicate_slot_audit",value["blocking_reasons"])
            self.assertTrue(all(x["occupied_slots"]==47 for x in value["entities"]))
        finally:shutil.rmtree(duplicate)

    def test_corrupt_report_receipt_raw_or_intent_block(self):
        for name in ["report/report.json","persistence.json","sda.stdout","intent.json"]:
            with self.subTest(name=name):
                path=self.root/"run-02-test"/name;old=path.read_bytes();path.write_bytes(old+b"corrupt")
                try:self.assertEqual(self.build()["status"],"blocked")
                finally:path.write_bytes(old)

    def test_paused_and_unfinished_intent_block(self):
        with self.changed(Path("run-03-test/audit.json"),lambda x:x.update(status="paused",category="persistence",reason_code="db_timeout")):
            value=self.build();self.assertEqual(value["pause_count"],1);self.assertEqual(value["failure_counts_by_category"]["persistence"],1)
        path=self.root/"run-03-test/audit.json";old=path.read_bytes();path.unlink()
        try:self.assertEqual(self.build()["status"],"blocked")
        finally:path.write_bytes(old)

    def test_late_or_overbudget_batch_cannot_backfill_a_slot(self):
        def late(x):x["finished_at"]=report.stamp(self.start+dt.timedelta(seconds=120*1+1800*4+1))
        for mutation in [late,lambda x:x.update(output_bytes=(1<<20)+1),lambda x:x.update(duration_seconds=121)]:
            with self.changed(Path("run-04-test/audit.json"),mutation):self.assertEqual(self.build()["status"],"blocked")

    def test_command_health_bits_are_not_transport_but_missing_command_is_invalid(self):
        with self.changed(Path("run-05-test/audit.json"),lambda x:x["commands"].pop()):self.assertEqual(self.build()["status"],"blocked")
        with self.changed(Path("run-05-test/audit.json"),lambda x:x["commands"][0].update(exit_code=255)):
            self.assertEqual(self.build()["status"],"blocked")

    def test_events_are_required_and_stopped_does_not_mean_accepted(self):
        path=self.root/"events.jsonl";old=path.read_bytes();path.unlink()
        try:self.assertIn("event_journal_missing",self.build()["blocking_reasons"])
        finally:path.write_bytes(old)
        path.write_bytes(old+json.dumps(dict(schema="disk-observation-event-v1",run_id=self.approval["run_id"],kind="stopped",at=report.stamp(self.end))).encode()+b"\n")
        try:self.assertIn("paused_or_stopped_event",self.build()["blocking_reasons"])
        finally:path.write_bytes(old)

    def test_no_slo_or_wrong_node_or_no_comparable_load_is_inconclusive(self):
        self.assertEqual(self.build(slo=None)["slo"]["status"],"inconclusive")
        for change in [{"node":"10.80.16.42"},{"load_comparable":False},{"scope_id":"wrong"},{"expected_roster_version":"2"},{"valid":False}]:
            with self.changed(Path("slo.json"),lambda x:x.update(change)):
                self.assertEqual(self.build()["slo"]["status"],"inconclusive")

    def test_slo_thresholds_and_nonfinite_zero_baseline(self):
        for name, observation in [("p95",102.001),("p99",204.001),("throughput",98.999)]:
            with self.changed(Path("slo.json"),lambda x:x.update(status="fail",metrics=dict(x["metrics"],**{name:dict(x["metrics"][name],observation=observation)}))):
                self.assertEqual(self.build()["slo"]["status"],"fail")
        for baseline in [0,"NaN","Infinity"]:
            with self.changed(Path("slo.json"),lambda x:x["metrics"]["p95"].update(baseline=baseline)):
                self.assertEqual(self.build()["slo"]["status"],"inconclusive")

    def test_slo_thresholds_preserve_decimal_digits_beyond_28_places(self):
        with self.changed(Path("slo.json"),lambda x:x.update(status="fail",metrics=dict(x["metrics"],p95=dict(x["metrics"]["p95"],baseline="1",observation="1.020000000000000000000000000000000000000001")))):
            self.assertEqual(self.build()["slo"]["status"],"fail")

    def test_unknown_health_profile_is_visible_without_failing_observation(self):
        path=self.root/"run-06-test/report/report.json";audit_path=self.root/"run-06-test/audit.json";receipt_path=self.root/"run-06-test/persistence.json"
        originals={p:p.read_bytes() for p in (path,audit_path,receipt_path)}
        try:
            value=json.loads(path.read_bytes());value["rows"][0]["assessment_status"]="insufficient_data"
            new_sha=write(path,value)
            receipt=json.loads(receipt_path.read_bytes())
            for row in receipt["entities"]:row["report_sha256"]=new_sha
            receipt["report_sha256"]=new_sha;receipt_sha=write(receipt_path,receipt)
            audit=json.loads(audit_path.read_bytes());audit["report_sha256"]=new_sha;audit["persistence"]=dict(receipt,receipt_sha256=receipt_sha);write(audit_path,audit)
            result=self.build();self.assertEqual(result["status"],"pass",result["blocking_reasons"])
            self.assertEqual(result["health_assessment_counts"]["insufficient_data"],1)
            self.assertTrue(result["health_coverage_limitations"])
        finally:
            for p,raw in originals.items():p.write_bytes(raw)

    def test_slo_sources_require_matching_sha_and_safe_path(self):
        for change in [{"path":"../private"},{"path":"/private/file"},{"sha256":"f"*64}]:
            with self.changed(Path("slo.json"),lambda x:x["source_evidence"][0].update(change)):
                self.assertEqual(self.build()["slo"]["status"],"inconclusive")

    def test_frozen_approval_and_binding_cannot_be_silently_changed(self):
        with self.changed(Path("binding.json"),lambda x:x.update(node="10.80.16.42")):
            with self.assertRaises(report.ReportError):self.build()
        with self.changed(Path("approval.json"),lambda x:x.update(devices=[])):
            with self.assertRaises(report.ReportError):self.build()

    def test_file_only_cli_does_not_overwrite_and_output_is_stable(self):
        out=self.root/"report-output"
        args=["--state-dir",str(self.root),"--approval",str(self.root/"approval.json"),"--as-of",report.stamp(self.as_of),"--slo",str(self.root/"slo.json"),"--out-dir",str(out)]
        self.assertEqual(report.run(args),0);data=(out/"report.json").read_bytes()
        self.assertEqual(report.run(args),1)
        self.assertEqual((out/"report.json").read_bytes(),data)
        self.assertIn("Status: pass",(out/"report.md").read_text())
        shutil.rmtree(out)


if __name__=="__main__":unittest.main()
