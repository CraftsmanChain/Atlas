# ATLAS Repository Working Agreement

## Bootstrap protocol

- Treat this file as the stable execution contract. Do not turn it into a progress diary.
- At the start of every task, inspect both repository worktrees before editing and preserve unrelated operator changes.
- Read `../Atlas-Docs/docs/llm-wiki.md` first. Use its routing table to load only the task capsule and source documents relevant to the request; do not read every historical planning document by default.
- For hardware-failure or XID prediction work, also read `../Atlas-Docs/docs/hardware-failure-prediction-capability-roadmap.md` completely.
- Runtime APIs, immutable artifacts, source code and tests outrank prose. If the Wiki conflicts with runtime evidence, follow runtime evidence and repair the Wiki in the same iteration.
- Dynamic facts in the Wiki must include `verified_at` and evidence. Re-query production when the fact is older than 24 hours or the operator says a deployment/data update occurred.
- Never place passwords, tokens, cookies, private keys, full connection strings or unredacted incident payloads in prompts, logs, commits or the Wiki.

## Knowledge architecture

- `AGENTS.md`: minimal entry point; it imports this contract and must not contain volatile facts.
- `RTK.md`: stable safety, workflow, verification and delivery rules; change only when the engineering process changes.
- `../Atlas-Docs/docs/llm-wiki.md`: compact machine-first project memory, current verified facts, code/API map, active experiment and decision index; update every material iteration.
- `../Atlas-Docs/docs/hardware-failure-prediction-capability-roadmap.md`: authoritative capability boundary, technical route, milestones, experiment ledger and next queue for prediction.
- `../Atlas-Docs/docs/platform-capability-modules.md`: product/module capability catalog and public version history.
- Code, tests, database state and immutable SHA-bound reports remain the source of truth for implementation and measured results.

Use explicit evidence labels in knowledge documents:

- `FACT`: directly verified from code, tests, API, database or immutable artifact.
- `DECISION`: accepted engineering choice with reason and supersession rule.
- `HYPOTHESIS`: testable explanation that is not yet established.
- `PLAN`: intended future work, never described as delivered capability.

## Scope

- Keep changes focused on the requested ATLAS capability and preserve unrelated user work.
- Treat production data, remote hosts, releases, and operator workflows as read-only unless the user explicitly authorizes a mutation.
- Prediction features must remain point-in-time safe: no feature, label, or evidence may cross its prediction cutoff.

## Prediction safety

- A risk ranking is not a calibrated failure probability.
- New prediction capabilities default to read-only shadow mode.
- No prediction release may emit operational alerts or execute isolation, restart, maintenance, scheduling, or workload actions unless that capability is separately designed, approved, and versioned.
- Promotion decisions must bind immutable evidence versions and SHA256 fingerprints and must expose blocking reasons instead of silently relaxing gates.
- Training, validation, calibration, test, and prospective cohorts preserve time order and GPU UUID isolation.

## Versioning

- Platform and prediction-framework versions are updated together when a prediction capability changes the user-visible platform.
- Framework governance releases use `prediction-framework-vX.Y.Z` and the matching UI `vX.Y.Z` history.
- Historical training and shadow-runtime milestones use the explicit `PIPELINE vX.Y.Z` namespace so their numbers cannot collide with framework governance versions.
- Every version bump includes a bilingual release-history entry and tests for the changed contract.

## Verification

- Run `make test` for Go changes.
- Run `npm run lint` and `npm run build` in `web` for frontend changes.
- Run `make release-scripts-check` when release scripts or a platform release version changes.
- Run `make agent-context-check` when `AGENTS.md`, `RTK.md`, the LLM Wiki, roadmap links, or their validation script changes.
- Report environmental failures separately from code failures and leave the worktree clean after an authorized commit.

Verification must be proportional to the change:

- Documentation-only: validate links/paths, `git diff --check`, and repository status; do not bump or deploy the platform unless runtime behavior changed.
- Go/API/data contract: targeted tests during development, then `make test` before delivery.
- Frontend: `npm run lint` and `npm run build` in `web`.
- Platform version or release scripts: `make release-scripts-check` in addition to the relevant tests.
- Production acceptance: verify `/api/v1/status` and `/health`, then exercise only the read-only or explicitly authorized workflow and record build IDs, metrics and SHA evidence.

## Prediction delivery continuity

- For every hardware-failure or XID prediction task, first read the living roadmap at `../Atlas-Docs/docs/hardware-failure-prediction-capability-roadmap.md` and use its current capability boundary, execution queue, milestones, and daily workflow as the default context.
- Keep the real hardware-failure objective primary. Scoped XID, GPU-model, fault-family, anomaly, and ranking work is valid only when its contribution to the main objective or an independently useful production-shadow subtarget is explicit.
- Update the living roadmap during every prediction iteration with production facts, build IDs, immutable SHA evidence, metrics, failures, milestone status, and next work. Also update `../Atlas-Docs/docs/platform-capability-modules.md` and the product milestone UI when milestone state changes.
- After implementation, run the repository verification required above, review staged diffs, commit and push Atlas and Atlas-Docs separately, and leave both worktrees clean.
- Do not deploy production directly. Every completed release handoff must include `SKIP_GIT_PUSH=1 VERSION_NAME=vX.Y.Z bash scripts/deploy_remote_source.sh` with the actual continuous platform version, plus the commit SHA and post-deploy verification/experiment sequence.
- When the operator says the release is updated or asks to continue, verify `/api/v1/status` (`version`, `commit`, `build_time`) and `/health`, then continue the already-authorized read-only checks and offline/shadow artifact builds without asking them to restate this workflow.
- Treat failed experiments as useful evidence. Record them, compare them on the identical cohort, and switch to a materially different hypothesis instead of repeatedly tuning the same model family.
- Never choose “latest build” as a semantic baseline when multiple algorithms share a build table. Bind comparisons and follow-up runs to explicit build IDs, matrix IDs, versions and SHA256 values.

## Efficient execution loop

1. **Recover context:** read the Wiki capsule, relevant source document and current worktree status.
2. **Verify freshness:** after an operator update, query production status/health and the exact APIs needed for this task. Prefer `scripts/export_llm_context.sh` when its allowlisted runtime/matrix/model snapshot covers the question; it is read-only and must never be expanded to secrets, raw incidents or feature values.
3. **State the delta:** identify the current fact, desired outcome, safety boundary and measurable exit criterion.
4. **Inspect before editing:** trace the real API/data/runtime path and search tests and consumers, not just the first matching file.
5. **Implement the smallest complete vertical slice:** contract, implementation, safety guard, UI/consumer and tests when applicable.
6. **Prove the result:** run targeted then full checks; for online experiments capture build ID, input SHA, output SHA, metrics and blocking reasons.
7. **Write back knowledge:** update the Wiki current state and decision index; update roadmap/module docs only where their authority applies.
8. **Deliver cleanly:** review staged diffs, commit/push each repository separately, confirm clean worktrees, and provide the next executable action.

Avoid efficiency traps:

- Do not re-run expensive reports when an immutable build/report ID already answers the question.
- Prefer narrow API projections or `jq` summaries over loading large manifests into the conversation.
- Prefer `rg` and targeted source ranges over broad repository dumps.
- Do not copy volatile facts into multiple documents; link to the authoritative document and keep a compact pointer in the Wiki.
- Do not create a new platform version for documentation-only process improvements.

## Git

- Review `git status` and the staged diff before committing.
- Do not rewrite published history or discard unrelated changes.
- Use concise commits that name the affected module and capability.
