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

## Git

- Review `git status` and the staged diff before committing.
- Do not rewrite published history or discard unrelated changes.
- Use concise commits that name the affected module and capability.
