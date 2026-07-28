## ADDED Requirements

### Requirement: Pull Request Required For All Changes
No commit SHALL land on `main` except through a merged pull request. Direct pushes to `main` SHALL be rejected, including for repository administrators.

#### Scenario: Direct push to main is rejected
- **WHEN** a direct push to `main` is attempted, by any account including an admin
- **THEN** GitHub rejects the push due to branch protection

#### Scenario: Change lands via a merged PR
- **WHEN** a change is ready
- **THEN** it is pushed to a feature branch, opened as a PR against `main`, and merged only once required checks pass

### Requirement: Merge Requires Passing Status Checks
A pull request against `main` SHALL NOT be mergeable unless both the `Build & Test` and `SAST (gosec)` CI checks report success, and the PR branch is up to date with `main`.

#### Scenario: Merge blocked by a failing required check
- **WHEN** a PR has `Build & Test` or `SAST (gosec)` failing
- **THEN** GitHub blocks the merge button/API for that PR

#### Scenario: Merge allowed once both checks pass
- **WHEN** a PR has both `Build & Test` and `SAST (gosec)` passing and is up to date with `main`
- **THEN** the PR is mergeable

#### Scenario: Automated release PRs are subject to the same gate
- **WHEN** `release-please` opens its own release PR against `main`
- **THEN** that PR is mergeable only under the same conditions as any other PR — no special bypass
