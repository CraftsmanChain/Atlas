# Offline GPU host MLP

This CPU-only package consumes caller-pinned `gpu-host-history-dataset-v2` files from `gpu-risk dataset`. It implements `hearank-paper-spec-v1`: Linear(d,512) → BN → ReLU → Linear(512,512) → BN → ReLU → Linear(512,256) → BN → ReLU → Linear(256,1). Outputs are logits and uncalibrated sigmoid scores; no registry, live ranking writes, probability, notification or operational action is provided.

Use a dedicated Python 3.12 environment, install the exact dependencies, and run the software tests:

```bash
python3.12 -m venv /tmp/atlas-ranker-venv
/tmp/atlas-ranker-venv/bin/python -m pip install -r tools/gpu_ranker/requirements.lock
make gpu-ranker-test RANKER_PYTHON=/tmp/atlas-ranker-venv/bin/python
```

The test target generates a synthetic Go dataset, trains a CPU fixture, checks Python/Go logits for all nodes and rejects changed scores. It does not access a database. Synthetic fixture artifacts require the separate `self-test` command and carry `synthetic_fixture_not_production_efficacy` throughout; `train` rejects them.

For a real offline dataset, first pin its **exact file bytes SHA256** after the trusted Go export. `train` requires `ready`, `paper_core_complete`, and `exploration_ready`, and independently checks both classes, train/validation/test episode/component counts, schema, duplicate nodes, entity/episode separation, and time purge. These checks do not authenticate source material; preserve the trusted export and its four fingerprints. Never hand-edit readiness flags.

```bash
umask 077
/tmp/atlas-ranker-venv/bin/python tools/gpu_ranker/train.py train \
  --dataset /private/history-v2.json --dataset-sha <sha256-of-file> \
  --output /private/ranker-seed17 --seed 17 --epochs 100
```

Output directories must not already exist. They contain JSON inference weights/BN running statistics, frozen preprocessing, config/environment/provenance, scores and a file-hash manifest. They are not pickle files. JSON weight exports support inference/reloading, not optimizer-state training resumption. Preserve complete artifacts privately; do not commit real identities, labels or checkpoints.

Preprocessing fits only eligible training rows: numeric/map counts use train mean/standard deviation plus explicit missing indicators. An entirely missing numeric column uses mean=0/scale=1 and its missing channel; this is an explicit preprocessing choice, not a claim the missing source value is zero. Raw count maps use train-only keys plus an aggregated OOV count. Each categorical field gets separate missing/OOV slots plus the sorted train vocabulary. Ordered numeric columns (value, missing pairs), followed by ordered category slots, define the complete feature order. No balancing, sampling or log transform is applied.

The bounded initial trainer uses all training rows as one CPU batch (equal row BCE), Adam lr=1e-4, float32, 100 epochs maximum, validation BCE early stopping with patience=10, default seed=17; freeze independent runs for seeds 17/23/41. BN eps=1e-5/momentum=0.1, torch Linear defaults, gamma=1/beta=0, no dropout. These are documented implementation choices where the paper is unspecified. Validation/test must satisfy the independent E1 gate; test labels never select a model. B0–B6 and production promotion are separate future work.

Budgets: at most 10,000 rows, 4096 transformed columns, 2 million transformed cells, 128 count-map keys per group, 1024 categories per field. There is no production-scale performance certification. Exceeding a budget rejects the run rather than sampling away rows. CPU output is deterministic within the pinned software environment; cross-platform bitwise identity is not guaranteed.

Reload and rescore the **same pinned cohort** independently:

```bash
/tmp/atlas-ranker-venv/bin/python tools/gpu_ranker/train.py score \
  --dataset /private/history-v2.json --dataset-sha <dataset-file-sha> \
  --artifact-file /private/ranker-seed17/model.json --artifact-sha <model-file-sha> \
  --output /private/reloaded-scores
```

Verify using the existing Go CLI; `verify-model` operates on local files before any DSN/database initialization:

```bash
gpu-risk verify-model \
  -dataset-file /private/history-v2.json -dataset-sha <dataset-file-sha> \
  -artifact-file /private/ranker-seed17/model.json -artifact-sha <model-file-sha> \
  -scores-file /private/reloaded-scores/scores.json -scores-sha <scores-file-sha>
```

The verifier checks all pinned bytes, Go dataset digest/schema, target/profile, model shape, preprocessing, BN statistics, finite values and complete unique query/node/generation coverage, then computes every logit in Go. It enforces abs=1e-6/rel=1e-5 and reports adjacent near ties separately; no arbitrary claim of identical ranks across tolerance-sized ties. This is an offline parity check and does not prove how a checkpoint was trained or promote it. New-query serving, registry admission, repeatable failure/evaluation ledgers and B0–B6 remain pending. The deployment script does not install Python dependencies on monitored machines.

Implementation references: [PyTorch 2.9 BatchNorm1d](https://docs.pytorch.org/docs/2.9/generated/torch.nn.BatchNorm1d.html) and [BCEWithLogitsLoss](https://docs.pytorch.org/docs/2.9/generated/torch.nn.BCEWithLogitsLoss.html).
