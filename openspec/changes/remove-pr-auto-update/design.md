## Context

`main` currently has strict required status checks, which makes every PR become stale when another PR merges. `.github/workflows/auto-update-pr-branches.yml` reacts to pushes by calling GitHub's update-branch API for every stale PR. GitHub records the follow-up `pull_request` runs as triggered by `github-actions[bot]`; under this public repository's `first_time_contributors` approval policy, those CI runs require manual approval.

## Goals / Non-Goals

**Goals:**
- Stop generating CI runs that require approval solely because an automation updated a PR branch.
- Preserve the existing PR-only merge gate and the three required CI checks.
- Let independently green PRs merge without a branch-refresh cycle.

**Non-Goals:**
- Disable GitHub Actions workflow approval for actual external contributors.
- Remove required CI checks, PR protection, administrator enforcement, or conversation resolution.
- Change CI jobs or their permissions.

## Decisions

**Delete the automatic updater rather than changing its trigger or token.**

The workflow's only purpose is satisfying strict branch protection. Retaining it after removing strictness would create needless writes. Replacing its token or trigger would retain the same complexity and would not improve merge safety under the chosen policy.

**Set required-status-check strictness to false.**

GitHub will continue to require `Build & Test`, `SAST (gosec)`, and `Vulnerability Scan (govulncheck)` to succeed, but it will accept those successful checks from the PR head even if `main` has advanced since they ran. This is the explicit trade-off requested: faster independent PR merges over re-testing each PR against every intervening merge.

**Keep workflow approval policy unchanged.**

The root cause for internal PRs is removed by stopping bot-driven branch updates. The repository remains public, so retaining `first_time_contributors` preserves GitHub's protection for genuinely untrusted contributions.

## Risks / Trade-offs

- A PR can merge after `main` changes without CI having tested that exact combined revision. Required checks still validate the PR head; merge conflicts must still be resolved by GitHub before merge.
- Maintainers must manually update a branch if they specifically want to test it against the current `main` before merging.

## Migration Plan

1. Delete `.github/workflows/auto-update-pr-branches.yml`.
2. Set `main` branch protection's required-status-check `strict` value to `false`, retaining all other current protection settings.
3. Verify a PR can remain mergeable after `main` advances while its three required checks remain successful.
