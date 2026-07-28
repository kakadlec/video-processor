## MODIFIED Requirements

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

## ADDED Requirements

### Requirement: Dependency Vulnerability Hygiene
The system's Go module dependencies SHALL NOT have open Dependabot vulnerability alerts. When a new alert is opened against a direct or transitive dependency, it SHALL be resolved by upgrading the implicated module (directly, or by upgrading the direct dependency that pulls it in transitively) to a patched version, not by ignoring or dismissing the alert.

#### Scenario: A dependency vulnerability alert is resolved by upgrading
- **WHEN** Dependabot opens an alert against a module in `go.mod`/`go.sum`
- **THEN** the module (or the direct dependency pulling it in transitively) is upgraded to a version that resolves the advisory, and `go.sum` no longer resolves to the vulnerable version

#### Scenario: Dependency upgrade preserves existing behavior
- **WHEN** a dependency is upgraded to resolve a vulnerability alert
- **THEN** `go test ./...` continues to pass without modification to test expectations

### Requirement: Vulnerability Scan Gate
Every push to `main` and every pull request SHALL run [`govulncheck`](https://go.dev/security/vuln) against the module in CI. The CI job SHALL fail if `govulncheck` reports a vulnerability reachable from code the project actually calls.

#### Scenario: CI fails on a reachable vulnerability
- **WHEN** `govulncheck` reports a vulnerability in a symbol reachable from this project's code
- **THEN** the CI `Vulnerability Scan (govulncheck)` job fails and is visibly reported

#### Scenario: CI passes when no reachable vulnerability is found
- **WHEN** `govulncheck` reports zero vulnerabilities reachable from this project's code (vulnerabilities present only in unused symbols of a required module do not count)
- **THEN** the CI `Vulnerability Scan (govulncheck)` job succeeds
