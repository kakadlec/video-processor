## MODIFIED Requirements

### Requirement: Merge Requires Passing Status Checks

A pull request against `main` SHALL NOT be mergeable unless `Build & Test`, `SAST (gosec)`, and `Vulnerability Scan (govulncheck)` all report success. The PR branch SHALL NOT be required to be up to date with `main` before merging.

#### Scenario: Merge blocked by a failing required check

- **WHEN** a PR has `Build & Test`, `SAST (gosec)`, or `Vulnerability Scan (govulncheck)` failing
- **THEN** GitHub blocks the merge button/API for that PR

#### Scenario: Merge allowed with successful checks on a stale branch

- **WHEN** a PR has all three required checks passing and `main` advances after those checks run
- **THEN** GitHub allows the PR to merge without requiring the branch to be updated and re-tested against the new `main`

#### Scenario: Automated release PRs are subject to the same gate

- **WHEN** `release-please` opens its own release PR against `main`
- **THEN** that PR is mergeable only under the same conditions as any other PR — no special bypass

### Requirement: Pull Request Branches Are Not Automatically Updated

The repository SHALL NOT run a GitHub Actions workflow that automatically updates out-of-date pull request branches.

#### Scenario: Main advances while a pull request is open

- **WHEN** a commit is merged into `main` while another PR remains open
- **THEN** GitHub Actions does not invoke the pull-request update-branch API for that PR
