## Why

All changes so far have been pushed directly to `main`. That doesn't scale as a real practice — nothing stops a broken or insecure change from landing straight on the branch everything else builds on. We want every change to go through a PR, and `main` protected so a merge can only happen once CI (both the test gate and the SAST gate) is green.

## What Changes

- Enable GitHub branch protection on `main`:
  - Require a pull request before merging (no direct pushes — including for repo admins; no bypass).
  - Require status checks `Build & Test` and `SAST (gosec)` to pass before merging.
  - Require the PR branch to be up to date with `main` before merging.
  - No required approval count (solo repo — nothing to require a second reviewer for).
- **BREAKING (process, not code)**: this immediately blocks all merges to `main`, including `release-please`'s own release PR, until the 9 pre-existing `gosec` findings are triaged (fixed or suppressed with justified `#nosec` comments) — a deliberate, informed decision, not an oversight. Tracked as follow-up work, not fixed by this change.
- Update `CLAUDE.md`: document that changes now go through a feature branch + PR (`gh pr create`), never a direct push to `main`.

## Capabilities

### Modified Capabilities
- `development-workflow`: adds a requirement that changes land via PR with required status checks, superseding the earlier `add-ci-testing-and-sast` design's explicit non-goal of "no branch protection, solo repo, direct pushes."

## Impact

- GitHub repository setting change (branch protection rule on `main`), not a code change.
- `CLAUDE.md`: new process section.
- Every future change (including this one, going forward) needs a branch + PR instead of a direct push to `main`.
- `main` is unmergeable until the 9 `gosec` findings are addressed — explicitly accepted, not silently worked around.
