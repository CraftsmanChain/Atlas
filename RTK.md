# ATLAS Repository Working Agreement

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
- Report environmental failures separately from code failures and leave the worktree clean after an authorized commit.

## Prediction delivery continuity

- For every hardware-failure or XID prediction task, first read the living roadmap at `../Atlas-Docs/docs/hardware-failure-prediction-capability-roadmap.md` and use its current capability boundary, execution queue, milestones, and daily workflow as the default context.
- Keep the real hardware-failure objective primary. Scoped XID, GPU-model, fault-family, anomaly, and ranking work is valid only when its contribution to the main objective or an independently useful production-shadow subtarget is explicit.
- Update the living roadmap during every prediction iteration with production facts, build IDs, immutable SHA evidence, metrics, failures, milestone status, and next work. Also update `../Atlas-Docs/docs/platform-capability-modules.md` and the product milestone UI when milestone state changes.
- After implementation, run the repository verification required above, review staged diffs, commit and push Atlas and Atlas-Docs separately, and leave both worktrees clean.
- Do not deploy production directly. Every completed release handoff must include `SKIP_GIT_PUSH=1 VERSION_NAME=vX.Y.Z bash scripts/deploy_remote_source.sh` with the actual continuous platform version, plus the commit SHA and post-deploy verification/experiment sequence.
- When the operator says the release is updated or asks to continue, verify `/api/v1/status` (`version`, `commit`, `build_time`) and `/health`, then continue the already-authorized read-only checks and offline/shadow artifact builds without asking them to restate this workflow.

## Git

- Review `git status` and the staged diff before committing.
- Do not rewrite published history or discard unrelated changes.
- Use concise commits that name the affected module and capability.
