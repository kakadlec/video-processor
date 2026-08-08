## ADDED Requirements

### Requirement: Pinned And Cached CI Tool Versions
The `gosec` and `govulncheck` tools used by the SAST and vulnerability-scan CI jobs SHALL be installed at specific pinned released versions (not `@latest`), and the CI workflow SHALL cache each tool's installed binary keyed on the tool's pinned version so that unchanged versions do not require reinstalling the tool from source on every run.

#### Scenario: Tool install is skipped on an unchanged pinned version
- **WHEN** the `sast` or `vulncheck` CI job runs and the cache for the currently pinned tool version already exists
- **THEN** the job restores the cached tool binary instead of rerunning `go install`, and still runs the tool's scan (`gosec ./...` / `govulncheck ./...`) exactly as before

#### Scenario: A version bump invalidates the cache
- **WHEN** the pinned version of `gosec` or `govulncheck` in the workflow is changed
- **THEN** the cache key changes accordingly, causing a fresh `go install` of the new pinned version on the next run
