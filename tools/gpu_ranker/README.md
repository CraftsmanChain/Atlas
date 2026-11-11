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

The verifier checks all pinned bytes, Go dataset digest/schema, target/profile, model shape, preprocessing, BN statistics, finite values and complete unique query/node/generation coverage, then computes every logit in Go. It enforces abs=1e-6/rel=1e-5 and reports adjacent near ties separately; no arbitrary claim of identical ranks across tolerance-sized ties. This is an offline parity check and does not prove how a checkpoint was trained or promote it. The R4-B CLI below adds candidate admission and new-query score records; automatic serving, promotion, full evaluation ledgers and B0–B6 remain pending. The deployment script does not install Python dependencies on monitored machines.

Implementation references: [PyTorch 2.9 BatchNorm1d](https://docs.pytorch.org/docs/2.9/generated/torch.nn.BatchNorm1d.html) and [BCEWithLogitsLoss](https://docs.pytorch.org/docs/2.9/generated/torch.nn.BCEWithLogitsLoss.html).

## Candidate admission and new query scoring (R4-B)

`register-model` is an explicit CLI write, not an HTTP upload or automatic promotion. It first verifies the pinned original dataset/model/Python-score files, rejects synthetic markers, and then rebuilds that dataset from this database using its fixed batch IDs. A forged `ready` flag or a checkpoint detached from the authoritative dataset is rejected. A successful record is only `offline_candidate`; it does not certify predictive performance. The model JSON and training identity set are private database fields, omitted from API projections.

```bash
gpu-risk register-model -dataset-file /private/history-v2.json -dataset-sha <dataset-file-sha> \
  -artifact-file /private/seed17/model.json -artifact-sha <model-file-sha> \
  -scores-file /private/seed17/scores.json -scores-sha <scores-file-sha>
# After a later R1 query has been frozen (model availability must precede cutoff):
umask 077
gpu-risk model-request -query <query-id> -model <model-id> > /private/model-request.json
# Explicit controlled write, only when authorized for that database:
gpu-risk model-record -input /private/model-request.json
```

`model-request` is read-only and consumes no future outcome labels. It binds query/scope/ranking SHA, model/preprocessor SHA, schema, cutoff and full-node historical features. The model registration time and training test-end must both precede query cutoff. A cold-start node may use missing/OOV channels; source conflicts, missing physical identity, or overlap with the training dataset's entities/physical GPUs are explicitly unranked. This conservative prospective cohort does not evaluate recurrence on entities already present in the training/validation/test dataset.

`model-record` rebuilds and compares the entire request against authoritative rows, scores in Go under `gpu-host-mlp-go-f64-v1`, and atomically saves an independent immutable batch. Original R1 scores remain untouched. All nodes remain in the denominator; warnings describe source unknowns separately from blocking reasons. Exact Go logit ties use competition rank. This scorer is a pinned float64 implementation; PyTorch float32 tolerance evidence does not promise identical ordering near numerical ties. A numerical failure clears the whole candidate batch's scores and records `failed`, rather than publishing a partial ranking. Repeating the same request returns the same batch.

GET `/api/v1/gpu-risk/models`, `/queries/{id}/model-batches` and `/queries/{id}/model-scores?batch_id=...` are read-only. Scores require an explicit batch ID; paginated reads stay pinned to that batch and carry ETag/integrity checks. The UI separately lists candidates and asks the reader to select a fixed batch, without a latest-model default. No registry activation, model training worker, promotion, calibration, operational alert or automatic scoring worker is introduced. R5 evaluation and actual source/runtime accumulation remain separate gates.

Historical R4-B blocker: the original `Through >= cutoff` requirement conflicted with `Through <= AvailableAt < cutoff`. The later versioned as-known watermark contract superseded that requirement; do not treat this original blocker as current. The latest production verification on 2026-11-11 (v0.120.104 / aa1dd2a) still leaves E1 blocked by stale three-source checks, missing member assertions, required history/episode-state evidence and independent dataset readiness. Configuration binding fingerprints do not certify coverage. Consult `Atlas-Docs/docs/llm-wiki.md` and `gpu-health-warning-progress-and-plan-2026-10-30.md` for dated evidence and `gpu-health-warning-remaining-plan-2026-11-11.md` for remaining work. Synthetic tests prove software behavior, not production data readiness or ranking efficacy.

## Pinned comparison first slice

`evaluate.py evaluate --dataset DATASET --dataset-sha SHA --split validation --output NEW_DIR` compares uniform and historical episode count on the same eligible query cohort. Supply `--artifact-file MODEL --artifact-sha SHA` to score an existing MLP checkpoint without fitting. Test reporting requires explicit `--split test`; never select configurations from these test metrics. File provenance does not certify live E1 or training history.

Non-ready data produces a deterministic waiting report. Synthetic input is rejected by evaluate and accepted only by the separate self-test command. Exact score ties use expected hits/NDCG and capture bounds, not node-ID ordering. Missing historical health scores and unfrozen weights remain pending; independent episode capture and remaining B0–B6 are not implemented in this slice. Reports preserve full query denominators and separately describe overlapping rejection/unknown/missing-score flags. No probabilities, promotion or actions.

Run `make gpu-ranking-eval-test RANKER_PYTHON=/tmp/atlas-ranker-venv/bin/python` with the existing isolated runtime. The evaluator never installs dependencies or accesses a database.

### Historical health and explicit severity weights

Optional `--health-file FILE --health-sha SHA` and `--weights-file FILE --weights-sha SHA` enable a separate `baseline_comparison` intersection. Original uniform/count/MLP denominators remain intact. Requested methods share the joint cohort and disclose missing, partial and unknown rows; an empty intersection stays pending. Blocked datasets do not load these files. No default weights, fitting, source certification, probabilities or promotion are provided.

Both JSON inputs use a closed tuple: `version`, `dataset_sha` (actual dataset bytes), `target`, `profile`, `feature_contract`. Health version is `gpu-ranking-health-sidecar-v1`, with `rows`. Each row has `query_id,node_id,generation,cutoff,scope_sha,result_sha,node_available_at,identity_changed_at,expected,observed,cards`. Cards contain `asset_id,uuid,first_seen_at,state,health_id,run_id,rule,score,evaluated_at,available_at`; score is nullable integer 0–100. Source score times must precede cutoff, with evaluation within 24h and after installation/identity change. Health inverse is max(100-score); only full card coverage is comparable, partial remains diagnostic. Query-wide UUID/asset conflicts in supplied rows reject input. `scope_sha/result_sha` are caller attestations, not independently recomputed scope proof; conflicts outside supplied rows, source truth and source completeness are not certified. Source availability is the original health created/run-finished maximum, never query completion time. Caller must construct inputs from immutable historical scope, never current Capture.

Weights version is `gpu-ranking-severity-weights-v1` with `dimension:"severity"`, `formula:"sum(count*weight)"`, `selection_policy:"predeclared_no_outcome_selection"`, `test_used_for_selection:false`, `frozen_at`, `weights` (explicit nonnegative finite map). Freeze must be no later than train_end and strictly before selected cutoffs. These declarations cannot independently prove historical selection behavior. Unmapped severity, unknown history/map or inconsistent count totals remain no_signal. A known zero with empty known map scores zero. Weights are not inferred from labels or selected by this tool. Test output must never drive configuration selection.

### Fixed period strata first slice

`period_report.py evaluate --dataset FILE --dataset-sha SHA --split validation --period-start ISO --period-end ISO --output NEW_DIR` validates the complete dataset through the existing evaluator before selecting a timezone-aware UTC half-open period. `self-test` is the only command accepting synthetic fixtures. Known zero, unknown history and observed history form disjoint full-scope strata; zero is not proof of complete history or a first-failure boundary. Uniform/count metrics rerank within each query stratum, not the full fleet. Non-ready data retains denominators with null metrics. Query AUC means/population variances do not imply independent cohorts or confidence intervals. MLP/health/weights loading, OOV/first-failure analysis, independent episode capture, nonoverlapping weeks, intervals and drift remain pending in this entry. No fit, promotion, probability or action. Run `make gpu-period-report-test RANKER_PYTHON=/tmp/atlas-ranker-venv/bin/python`.
