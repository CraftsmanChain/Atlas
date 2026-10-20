import copy
import datetime as dt
import hashlib
import importlib.util
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

SPEC = importlib.util.spec_from_file_location("disk_observation_slo", Path(__file__).with_name("disk_observation_slo.py"))
slo = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(slo)


class SLOTest(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.root = Path(self.directory.name)
        self.now = dt.datetime(2026, 10, 21, 1, tzinfo=dt.timezone.utc)
        selector = 'node_ip="10.80.16.41"'
        self.plan = {
            "schema": "disk-observation-slo-plan-v1", "run_id": "single-node-41",
            "node": "10.80.16.41", "scope_id": "scope-41", "expected_roster_version": "1",
            "frozen_at": "2026-10-19T23:00:00Z", "window_start": "2026-10-20T00:00:00Z",
            "window_end": "2026-10-21T00:00:00Z", "baseline_start": "2026-10-19T00:00:00Z",
            "baseline_end": "2026-10-20T00:00:00Z", "workload_id": "synthetic-business",
            "node_selector": {"label": "node_ip", "value": "10.80.16.41"}, "queries": {},
            "business_binding": None,
        }
        for metric, quantile in (("p95", "0.95"), ("p99", "0.99")):
            self.plan["queries"][metric] = {
                "promql": f'histogram_quantile({quantile},sum(increase(business_duration_seconds_bucket{{{selector}}}[24h]))by(le))',
                "coverage_promql": f'min(count_over_time(business_duration_seconds_bucket{{{selector}}}[24h]))/1440',
                "unit": "seconds",
            }
        self.plan["queries"]["throughput"] = {
            "promql": f'sum(increase(business_requests_total{{{selector}}}[24h]))/86400',
            "coverage_promql": f'min(count_over_time(business_requests_total{{{selector}}}[24h]))/1440',
            "unit": "requests_per_second",
        }
        binding = {
            "schema": "disk-observation-business-binding-v1", "node": self.plan["node"],
            "workload_id": self.plan["workload_id"], "confirmed": True,
            "reviewed_at": self.plan["frozen_at"], "reviewed_by": "synthetic-reviewer",
            "source": "synthetic workload-to-node mapping",
            "queries_sha256": slo.digest(json.dumps(self.plan["queries"], sort_keys=True, separators=(",", ":")).encode()),
        }
        self.plan["business_binding"] = self.write("binding.json", binding)
        self.plan_ref = self.write("plan.json", self.plan)
        proof = self.write("load-proof.json", {"synthetic_load_background": [1, 1]})
        comparison = {
            "schema": "disk-observation-load-comparison-v1", "plan_sha256": self.plan_ref["sha256"],
            "load_comparable": True, "reviewed_by": "synthetic-reviewer", "method": "matched workload mix and offered load",
            "source_evidence": [proof],
        }
        self.evidence = {"schema": "disk-observation-slo-measurements-v1", "plan_sha256": self.plan_ref["sha256"],
                         "captured_at": "2026-10-21T00:01:00Z", "load_comparison": self.write("comparison.json", comparison),
                         "responses": {}}
        for period, end in (("baseline", self.plan["baseline_end"]), ("observation", self.plan["window_end"])):
            self.evidence["responses"][period] = {}
            for metric, definition in self.plan["queries"].items():
                measurement = "100" if metric == "throughput" else "1"
                self.evidence["responses"][period][metric] = {
                    "value": self.write(f"{period}-{metric}.json", self.receipt(definition["promql"], end, measurement)),
                    "coverage": self.write(f"{period}-{metric}-coverage.json", self.receipt(definition["coverage_promql"], end, "1")),
                }

    def write(self, name, value):
        raw = (json.dumps(value, sort_keys=True) + "\n").encode()
        (self.root / name).write_bytes(raw)
        return {"path": name, "sha256": hashlib.sha256(raw).hexdigest()}

    def receipt(self, query, end, value):
        return {"query": query, "evaluation_time": end, "response": {"status": "success", "data": {
            "resultType": "vector", "result": [{"metric": {}, "value": [slo.timestamp(end).timestamp(), value]}]}}}

    def result(self, evidence=True, now=None):
        reference = self.write("evidence.json", self.evidence) if evidence else None
        return slo.evaluate(self.root / "plan.json", self.plan_ref["sha256"],
                            self.root / "evidence.json" if evidence else None,
                            reference["sha256"] if evidence else None, self.root, now or self.now)

    def measurement(self, metric, value, coverage=None):
        definition = self.plan["queries"][metric]
        self.evidence["responses"]["observation"][metric]["value"] = self.write(
            f"observation-{metric}.json", self.receipt(definition["promql"], self.plan["window_end"], value))
        if coverage is not None:
            self.evidence["responses"]["observation"][metric]["coverage"] = self.write(
                f"observation-{metric}-coverage.json", self.receipt(definition["coverage_promql"], self.plan["window_end"], coverage))

    def test_complete_business_evidence_passes(self):
        result = self.result()
        self.assertEqual((result["status"], result["valid"], result["load_comparable"]), ("pass", True, True))
        self.assertEqual(len(result["metrics"]), 3)
        self.assertGreater(len(result["source_evidence"]), 12)
        self.assertEqual(result["blocking_reasons"], [])

    def test_exact_hard_thresholds_pass_without_float_relaxation(self):
        self.measurement("p95", "1.02")
        self.measurement("p99", "1.02")
        self.measurement("throughput", "99")
        self.assertEqual(self.result()["status"], "pass")
        self.measurement("p99", "1.020000001")
        result = self.result()
        self.assertEqual(result["status"], "fail")
        self.assertIn("p99_threshold_exceeded", result["blocking_reasons"])
        self.measurement("p99", "1.020000000000000000000000000000000000000001")
        self.assertIn("p99_threshold_exceeded", self.result()["blocking_reasons"])

    def test_throughput_hard_limit(self):
        self.measurement("throughput", "98.999999")
        self.assertIn("throughput_threshold_exceeded", self.result()["blocking_reasons"])

    def test_missing_metrics_is_inconclusive(self):
        result = self.result(evidence=False)
        self.assertEqual((result["status"], result["valid"]), ("inconclusive", False))
        del self.evidence["responses"]["observation"]["p99"]
        self.assertEqual(self.result()["status"], "inconclusive")

    def test_preliminary_window_cannot_pass(self):
        result = self.result(now=slo.timestamp("2026-10-20T12:00:00Z"))
        self.assertEqual(result["status"], "inconclusive")
        self.assertIn("observation_window_not_complete", result["blocking_reasons"])

    def test_non_finite_idle_and_missing_coverage_are_not_success(self):
        for bad in ("NaN", "Inf", "-1"):
            with self.subTest(bad=bad):
                self.measurement("p95", bad)
                self.assertEqual(self.result()["status"], "inconclusive")
        self.measurement("p95", "1", coverage="0.989999")
        self.assertEqual(self.result()["status"], "inconclusive")
        self.measurement("p95", "1", coverage="1")
        ref = self.evidence["responses"]["baseline"]["throughput"]["value"]
        receipt = json.loads((self.root / ref["path"]).read_text())
        receipt["response"]["data"]["result"][0]["value"][1] = "0"
        self.evidence["responses"]["baseline"]["throughput"]["value"] = self.write(ref["path"], receipt)
        self.assertEqual(self.result()["status"], "inconclusive")

    def test_wrong_node_or_infrastructure_metrics_are_rejected(self):
        definition = copy.deepcopy(self.plan["queries"]["p95"])
        definition["promql"] = definition["promql"].replace("10.80.16.41", "10.80.16.42")
        with self.assertRaisesRegex(ValueError, "node_binding"):
            slo.validate_query("p95", definition, self.plan["node_selector"])
        definition = copy.deepcopy(self.plan["queries"]["p95"])
        definition["promql"] = definition["promql"].replace("business_duration_seconds_bucket", "node_disk_read_time_seconds_total")
        with self.assertRaisesRegex(ValueError, "infrastructure_metric"):
            slo.validate_query("p95", definition, self.plan["node_selector"])

    def test_mean_of_quantiles_is_rejected(self):
        definition = copy.deepcopy(self.plan["queries"]["p95"])
        definition["promql"] = "avg_over_time(" + definition["promql"] + "[24h:5m])"
        with self.assertRaisesRegex(ValueError, "whole_window_histogram"):
            slo.validate_query("p95", definition, self.plan["node_selector"])

    def test_query_time_result_cardinality_and_sha_bindings(self):
        reference = self.evidence["responses"]["observation"]["p95"]["value"]
        receipt = json.loads((self.root / reference["path"]).read_text())
        receipt["evaluation_time"] = self.plan["baseline_end"]
        self.evidence["responses"]["observation"]["p95"]["value"] = self.write(reference["path"], receipt)
        self.assertEqual(self.result()["status"], "inconclusive")
        self.evidence["responses"]["observation"]["p95"]["value"]["sha256"] = "0" * 64
        self.assertEqual(self.result()["status"], "inconclusive")
        with self.assertRaisesRegex(ValueError, "sha256"):
            slo.evaluate(self.root / "plan.json", "0" * 64, None, None, self.root, self.now)

    def test_plan_cannot_be_retroactively_frozen_or_shortened(self):
        plan = copy.deepcopy(self.plan)
        plan["frozen_at"] = "2026-10-20T00:01:00Z"
        with self.assertRaisesRegex(ValueError, "not_frozen"):
            slo.validate_plan(plan)
        plan = copy.deepcopy(self.plan)
        plan["window_end"] = "2026-10-20T01:00:00Z"
        with self.assertRaisesRegex(ValueError, "24h"):
            slo.validate_plan(plan)

    def test_unreviewed_load_and_identity_proof_are_inconclusive(self):
        comparison = json.loads((self.root / "comparison.json").read_text())
        comparison["load_comparable"] = False
        self.evidence["load_comparison"] = self.write("comparison.json", comparison)
        self.assertIn("load_comparability_unproven", self.result()["blocking_reasons"])
        (self.root / "binding.json").write_text("{}")
        self.assertIn("business_metric_mapping_unconfirmed", self.result()["blocking_reasons"])

    def test_paths_symlinks_and_duplicate_json_are_rejected(self):
        sources = slo.Evidence(self.root)
        for path in ("../private.json", "/private.json"):
            with self.assertRaisesRegex(ValueError, "unsafe"):
                sources.read({"path": path, "sha256": "0" * 64}, self.root)
        (self.root / "escape").symlink_to(self.root.parent)
        with self.assertRaisesRegex(ValueError, "outside"):
            sources.read({"path": "escape/private.json", "sha256": "0" * 64}, self.root)
        (self.root / "duplicate.json").write_text('{"schema":1,"schema":2}')
        with self.assertRaisesRegex(ValueError, "duplicate"):
            slo.read_json(self.root / "duplicate.json")

    def test_null_hash_cannot_bypass_evidence_integrity(self):
        with self.assertRaisesRegex(ValueError, "sha256"):
            slo.Evidence(self.root).read({"path": "binding.json", "sha256": None}, self.root)
        with self.assertRaisesRegex(ValueError, "sha256"):
            slo.evaluate(self.root / "plan.json", None, None, None, self.root, self.now)

    def test_invalid_response_shapes_are_inconclusive(self):
        reference = self.evidence["responses"]["observation"]["p95"]["value"]
        receipt = json.loads((self.root / reference["path"]).read_text())
        for bad in (None, [], {"status": "success", "data": []},
                    {"status": "success", "data": {"resultType": "vector", "result": [None]}}):
            with self.subTest(bad=bad):
                receipt["response"] = bad
                self.evidence["responses"]["observation"]["p95"]["value"] = self.write(reference["path"], receipt)
                self.assertEqual(self.result()["status"], "inconclusive")

    def test_cli_missing_data_writes_bound_inconclusive_and_never_overwrites(self):
        output = self.root / "slo.json"
        command = [sys.executable, str(Path(slo.__file__)), "--plan", str(self.root / "plan.json"),
                   "--plan-sha256", self.plan_ref["sha256"], "--output", str(output)]
        run = subprocess.run(command, capture_output=True, text=True)
        self.assertEqual(run.returncode, 0, run.stderr)
        self.assertEqual(json.loads(output.read_text())["status"], "inconclusive")
        self.assertEqual(json.loads(run.stdout)["output_sha256"], slo.digest(output.read_bytes()))
        run = subprocess.run(command, capture_output=True, text=True)
        self.assertEqual(run.returncode, 2)


if __name__ == "__main__":
    unittest.main()
