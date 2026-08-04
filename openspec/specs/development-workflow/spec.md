# development-workflow Specification

## Purpose
Defines how changes land on `main` for this repository: required CI gates (tests, SAST, dependency vulnerability scanning), the PR-only branch protection workflow, Conventional Commit conventions, and the automated release process. Contributors and AI agents working in this repo should treat these as binding constraints, not suggestions.
## Requirements
### Requirement: Automated Test Gate
Every push to `main` and every pull request SHALL run the full test suite (`go test ./...`) in CI, with `ffmpeg` available in the CI environment. The CI test job SHALL fail if any test fails.

#### Scenario: CI fails on a failing test
- **WHEN** a commit is pushed where a test fails
- **THEN** the CI test job fails and is visibly reported on the commit or pull request

#### Scenario: CI passes when all tests pass
- **WHEN** a commit is pushed where every test passes
- **THEN** the CI test job succeeds

### Requirement: SAST Gate
Every push to `main` and every pull request SHALL run a static application security testing scan (`gosec`) against the codebase in CI. The CI SAST job SHALL fail if the scan reports any finding. As of this change, the codebase has zero outstanding `gosec` findings; the SAST job is expected to be green.

#### Scenario: CI fails on a SAST finding
- **WHEN** `gosec` reports one or more findings against the code
- **THEN** the CI SAST job fails and is visibly reported

#### Scenario: CI passes when the scan is clean
- **WHEN** `gosec` reports zero findings against the code
- **THEN** the CI SAST job succeeds

#### Scenario: Suppression is a last resort, checked against the rule's documented fix pattern first
- **WHEN** a `gosec` finding is reported
- **THEN** the rule's own documentation SHALL be checked for a validation pattern gosec recognizes as resolving the finding, and that pattern SHALL be applied and verified (re-running `gosec`) before falling back to suppression

#### Scenario: Findings that remain after that are fixed or explicitly suppressed, never silenced globally
- **WHEN** a specific `gosec` finding is judged a false positive or an accepted risk with no recognized fix pattern
- **THEN** it SHALL be suppressed with a bare inline `#nosec G<rule-id>` comment (no restated prose in-line; rationale belongs in the commit message/PR description), not by disabling the SAST job or excluding whole files/rules project-wide

### Requirement: Dependency Vulnerability Hygiene
The system's Go module dependencies SHALL NOT have open Dependabot vulnerability alerts. When a new alert is opened against a direct or transitive dependency, it SHALL be resolved by upgrading the implicated module (directly, or by upgrading the direct dependency that pulls it in transitively) to a patched version, not by ignoring or dismissing the alert.

#### Scenario: A dependency vulnerability alert is resolved by upgrading
- **WHEN** Dependabot opens an alert against a module in `go.mod`/`go.sum`
- **THEN** the module (or the direct dependency pulling it in transitively) is upgraded to a version that resolves the advisory, and `go.sum` no longer resolves to the vulnerable version

#### Scenario: Dependency upgrade preserves existing behavior
- **WHEN** a dependency is upgraded to resolve a vulnerability alert
- **THEN** `go test ./...` continues to pass without modification to test expectations

### Requirement: Change Completion Requires A Passing Test Run
A code change SHALL NOT be considered complete until `go test ./...` has been run and passes, in addition to any applicable CI checks.

#### Scenario: A change is not marked done without a passing local test run
- **WHEN** a code change affecting application behavior is made
- **THEN** `go test ./...` is run and passes before the change is reported as complete

### Requirement: Conventional Commit Messages
Commit messages on this project SHALL follow the [Conventional Commits](https://www.conventionalcommits.org/) format (`type: description`, e.g. `feat:`, `fix:`, `chore:`, `docs:`, `ci:`, `test:`, `refactor:`), with a `!` after the type or a `BREAKING CHANGE:` footer for breaking changes. This is the signal the automated release process uses to compute the next version.

#### Scenario: Feature commit triggers a minor version bump
- **WHEN** a commit with type `feat:` lands on `main`
- **THEN** the next proposed release version increments the minor component

#### Scenario: Fix commit triggers a patch version bump
- **WHEN** a commit with type `fix:` lands on `main`
- **THEN** the next proposed release version increments the patch component

#### Scenario: Breaking change triggers a major version bump
- **WHEN** a commit includes `!` after its type or a `BREAKING CHANGE:` footer
- **THEN** the next proposed release version increments the major component

### Requirement: Automated Release Pull Request
On every push to `main`, the system SHALL maintain a single up-to-date "Release PR" that aggregates unreleased Conventional Commits and shows the computed next version and changelog. No tag or GitHub Release SHALL be created until this PR is merged by a human.

#### Scenario: Release PR updates on new commits
- **WHEN** a new Conventional Commit is pushed to `main` while a Release PR is already open
- **THEN** the existing Release PR is updated in place to include the new commit and any resulting version change, rather than opening a duplicate PR

#### Scenario: Merging the Release PR cuts a release
- **WHEN** the Release PR is merged
- **THEN** a git tag matching the computed version is created, a GitHub Release with generated release notes is published, and `CHANGELOG.md` is updated

### Requirement: Pull Request Required For All Changes
No commit SHALL land on `main` except through a merged pull request. Direct pushes to `main` SHALL be rejected, including for repository administrators.

#### Scenario: Direct push to main is rejected
- **WHEN** a direct push to `main` is attempted, by any account including an admin
- **THEN** GitHub rejects the push due to branch protection

#### Scenario: Change lands via a merged PR
- **WHEN** a change is ready
- **THEN** it is pushed to a feature branch, opened as a PR against `main`, and merged only once required checks pass

### Requirement: Merge Requires Passing Status Checks
A pull request against `main` SHALL NOT be mergeable unless `Build & Test`, `SAST (gosec)`, and `Vulnerability Scan (govulncheck)` all report success, and the PR branch is up to date with `main`.

#### Scenario: Merge blocked by a failing required check
- **WHEN** a PR has `Build & Test`, `SAST (gosec)`, or `Vulnerability Scan (govulncheck)` failing
- **THEN** GitHub blocks the merge button/API for that PR

#### Scenario: Merge allowed once all checks pass
- **WHEN** a PR has `Build & Test`, `SAST (gosec)`, and `Vulnerability Scan (govulncheck)` all passing and is up to date with `main`
- **THEN** the PR is mergeable

#### Scenario: Automated release PRs are subject to the same gate
- **WHEN** `release-please` opens its own release PR against `main`
- **THEN** that PR is mergeable only under the same conditions as any other PR — no special bypass

### Requirement: Vulnerability Scan Gate
Every push to `main` and every pull request SHALL run [`govulncheck`](https://go.dev/security/vulncheck) against the module, its dependencies, and the Go toolchain/stdlib. The CI job SHALL fail if any reachable, known vulnerability is found. Where branch protection requires status checks for merging (see the pull-request-workflow requirements), this check SHALL be included alongside the test and SAST gates.

#### Scenario: CI fails on a reachable vulnerability
- **WHEN** `govulncheck` reports a vulnerability that the code actually reaches
- **THEN** the CI vulnerability-scan job fails and is visibly reported

#### Scenario: CI passes when no reachable vulnerability is found
- **WHEN** `govulncheck` finds no vulnerability reachable from the code, even if unreachable vulnerabilities exist elsewhere in the dependency graph
- **THEN** the CI vulnerability-scan job succeeds

### Requirement: Automated Dependency Update Proposals
The repository SHALL use Dependabot to automatically propose updates for Go modules, GitHub Actions, and the Docker base image on a recurring schedule, and SHALL have Dependabot security alerts and automated security updates enabled for vulnerability-driven updates outside that schedule.

#### Scenario: Scheduled dependency update PR
- **WHEN** a newer version of a tracked Go module, GitHub Action, or Docker base image is available
- **THEN** Dependabot opens a pull request proposing the update on its configured schedule

#### Scenario: Security update PR outside the schedule
- **WHEN** a dependency has a known security vulnerability
- **THEN** Dependabot opens a pull request proposing the fix independent of the regular schedule

#### Scenario: Dependency update PRs are subject to the same merge gate
- **WHEN** Dependabot opens a pull request against `main`
- **THEN** that pull request is mergeable only once it satisfies the same required status checks as any other pull request

### Requirement: Missing Test Prerequisites Must Cause a Non-Zero Exit
When a hard runtime prerequisite for the integration test suite is absent from the environment, `go test ./...` SHALL exit with a non-zero exit code and a clear, actionable error message identifying the missing prerequisite and referring to the documented fallback. Exiting with code 0 when no tests ran is not acceptable — code 0 is indistinguishable from all tests passing and constitutes a false green.

#### Scenario: ffmpeg absent causes go test to exit non-zero
- **WHEN** `go test ./...` is run in an environment where `ffmpeg` is not on `PATH`
- **THEN** the process exits with a non-zero exit code and prints an English message identifying the missing prerequisite and pointing to the Docker fallback documented in `CLAUDE.md`

#### Scenario: ffmpeg present leaves test behavior unchanged
- **WHEN** `go test ./...` is run in an environment where `ffmpeg` is on `PATH`
- **THEN** the suite runs and exits with the same outcome as before this change — this requirement adds no new test cases and changes no passing behavior

