import copy
import hashlib
import json
import math
import os
import subprocess
import tempfile
import unittest
from pathlib import Path

import torch
from torch import nn

from model import Ranker
from preprocess import fit, transform
from train import read_pinned, train, validate, write_artifacts, score_artifact


class RankerTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.dataset = json.loads(Path(os.environ["ATLAS_GPU_RISK_MLP_FIXTURE"]).read_bytes())
        torch.set_num_threads(1)

    def test_backbone_bn_singleton_eval_and_extreme_bce(self):
        model = Ranker(3)
        self.assertEqual([m.out_features for m in model.modules() if isinstance(m, nn.Linear)], [512, 512, 256, 1])
        self.assertFalse(any(isinstance(m, nn.Dropout) for m in model.modules()))
        model.train()
        model(torch.ones(2, 3))
        model.eval()
        with torch.no_grad():
            batch = model(torch.ones(2, 3))[0]
            single = model(torch.ones(1, 3))[0]
        self.assertAlmostEqual(batch.item(), single.item(), places=6)
        self.assertTrue(torch.isfinite(nn.functional.binary_cross_entropy_with_logits(torch.tensor([1000., -1000.]), torch.tensor([0., 1.]))))

    def test_row_mean_not_query_mean(self):
        z = torch.tensor([0.] * 2 + [2.] * 8)
        y = torch.zeros(10)
        per_row = nn.functional.binary_cross_entropy_with_logits(z, y, reduction="none")
        expected = (2 * math.log(2) + 8 * math.log1p(math.exp(2))) / 10
        self.assertAlmostEqual(per_row.mean().item(), expected, places=6)
        self.assertGreater(abs(per_row.mean().item() - ((per_row[:2].mean() + per_row[2:].mean()) / 2).item()), .1)

    def test_train_only_vocab_statistics_missing_oov(self):
        groups = validate(self.dataset, synthetic=True)
        p = fit(groups["train"])
        row = copy.deepcopy(groups["test"][0])
        row["features"]["categorical"]["gpu_model"] = "TEST_ONLY_MODEL"
        row["features"]["numeric"]["episode_count"] = None
        row["features"]["by_fault_type"] = {"TEST_ONLY_FAULT": 7}
        vector = transform(p, [row])[0]
        self.assertEqual(vector[1], 1)
        self.assertNotIn("TEST_ONLY_MODEL", p["categories"][2]["vocab"])
        self.assertNotIn("TEST_ONLY_FAULT", p["numeric"][-1]["known_keys"])
        # OOV and absent are different one-hot positions.
        known = copy.deepcopy(row)
        known["features"]["categorical"]["gpu_model"] = None
        self.assertNotEqual(transform(p, [known]), [vector])
        row["features"]["numeric"]["episode_count"] = math.nan
        with self.assertRaises(ValueError):
            transform(p, [row])

    def test_gates_and_synthetic_separation(self):
        with self.assertRaises(ValueError):
            validate(self.dataset)
        for mutate in (
            lambda d: d.update(ready=False),
            lambda d: d["rows"].append(copy.deepcopy(d["rows"][0])),
            lambda d: d["rows"][-1].update(component=d["rows"][0]["component"]),
            lambda d: d["rows"][0]["features"]["numeric"].update(future_label=1),
            lambda d: [r.update(label=False) for r in d["rows"] if r["split"] == "train"],
        ):
            d = copy.deepcopy(self.dataset)
            mutate(d)
            with self.assertRaises(ValueError):
                validate(d, synthetic=True)

    def test_seed_determinism_test_labels_do_not_select_model(self):
        config = {"epochs": 2, "seed": 17}
        a, logits, model, x = train(self.dataset, "a" * 64, config, True)
        b, again, _, _ = train(self.dataset, "a" * 64, config, True)
        self.assertEqual(a, b)
        self.assertEqual(logits, again)
        self.assertEqual(logits, score_artifact(a, self.dataset, "a" * 64))
        changed = copy.deepcopy(self.dataset)
        for row in changed["rows"]:
            if row["split"] == "test":
                row["features"]["categorical"]["gpu_model"] = "TEST_ONLY"
        test_rows = [r for r in changed["rows"] if r["split"] == "test"]
        test_rows[0]["label"], test_rows[5]["label"] = test_rows[5]["label"], test_rows[0]["label"]
        test_rows[0]["episode_keys"], test_rows[5]["episode_keys"] = test_rows[5]["episode_keys"], test_rows[0]["episode_keys"]
        c, _, _, _ = train(changed, "b" * 64, config, True)
        self.assertEqual(a["layers"], c["layers"])
        with torch.no_grad():
            self.assertAlmostEqual(logits[0], model(x[:1]).item(), places=6)

    def test_cli_go_roundtrip_and_changed_scores_rejected(self):
        binary = os.environ.get("ATLAS_GPU_RISK_VERIFY_BIN")
        if not binary:
            self.skipTest("run make gpu-ranker-test for Go/Python integration")
        dataset_path = os.environ["ATLAS_GPU_RISK_MLP_FIXTURE"]
        dataset_sha = hashlib.sha256(Path(dataset_path).read_bytes()).hexdigest()
        with tempfile.TemporaryDirectory() as directory:
            output = str(Path(directory) / "model")
            write_artifacts(output, self.dataset, dataset_sha, {"seed": 17, "epochs": 3}, True)
            manifest = json.loads(Path(output, "manifest.json").read_bytes())
            args = [binary, "verify-model", "-dataset-file", dataset_path, "-dataset-sha", dataset_sha, "-artifact-file", str(Path(output, "model.json")), "-artifact-sha", manifest["files"]["model.json"], "-scores-file", str(Path(output, "scores.json")), "-scores-sha", manifest["files"]["scores.json"]]
            result = subprocess.run(args, capture_output=True, check=True)
            self.assertEqual(json.loads(result.stdout)["rows"], 120)
            scores = json.loads(Path(output, "scores.json").read_bytes())
            scores["rows"][0]["raw_score"] += 0.1
            raw = json.dumps(scores).encode()
            Path(output, "scores.json").write_bytes(raw)
            args[-1] = hashlib.sha256(raw).hexdigest()
            self.assertNotEqual(subprocess.run(args, capture_output=True).returncode, 0)


if __name__ == "__main__":
    unittest.main()
