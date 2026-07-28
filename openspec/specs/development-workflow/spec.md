# development-workflow Specification

## Purpose
TBD - created by archiving change add-ci-testing-and-sast. Update Purpose after archive.
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
Every push to `main` and every pull request SHALL run a static application security testing scan (`gosec`) against the codebase in CI. The CI SAST job SHALL fail if the scan reports any finding.

#### Scenario: CI fails on a SAST finding
- **WHEN** `gosec` reports one or more findings against the code
- **THEN** the CI SAST job fails and is visibly reported

#### Scenario: Findings are fixed or explicitly suppressed, never silenced globally
- **WHEN** a specific `gosec` finding is judged a false positive or an accepted risk
- **THEN** it SHALL be suppressed with an inline `#nosec` comment referencing the specific rule ID and a written justification, not by disabling the SAST job or excluding whole files/rules project-wide

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

