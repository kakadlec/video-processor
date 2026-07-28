## MODIFIED Requirements

### Requirement: SAST Gate
Every push to `main` and every pull request SHALL run a static application security testing scan (`gosec`) against the codebase in CI. The CI SAST job SHALL fail if the scan reports any finding. As of this change, the codebase has zero outstanding `gosec` findings; the SAST job is expected to be green.

#### Scenario: CI fails on a SAST finding
- **WHEN** `gosec` reports one or more findings against the code
- **THEN** the CI SAST job fails and is visibly reported

#### Scenario: CI passes when the scan is clean
- **WHEN** `gosec` reports zero findings against the code
- **THEN** the CI SAST job succeeds

#### Scenario: Findings are fixed or explicitly suppressed, never silenced globally
- **WHEN** a specific `gosec` finding is judged a false positive or an accepted risk
- **THEN** it SHALL be suppressed with an inline `#nosec` comment referencing the specific rule ID and a written justification, not by disabling the SAST job or excluding whole files/rules project-wide

## ADDED Requirements

### Requirement: Dependency Vulnerability Hygiene
The system's Go module dependencies SHALL NOT have open Dependabot vulnerability alerts. When a new alert is opened against a direct or transitive dependency, it SHALL be resolved by upgrading the implicated module (directly, or by upgrading the direct dependency that pulls it in transitively) to a patched version, not by ignoring or dismissing the alert.

#### Scenario: A dependency vulnerability alert is resolved by upgrading
- **WHEN** Dependabot opens an alert against a module in `go.mod`/`go.sum`
- **THEN** the module (or the direct dependency pulling it in transitively) is upgraded to a version that resolves the advisory, and `go.sum` no longer resolves to the vulnerable version

#### Scenario: Dependency upgrade preserves existing behavior
- **WHEN** a dependency is upgraded to resolve a vulnerability alert
- **THEN** `go test ./...` continues to pass without modification to test expectations
