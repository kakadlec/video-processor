## ADDED Requirements

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
