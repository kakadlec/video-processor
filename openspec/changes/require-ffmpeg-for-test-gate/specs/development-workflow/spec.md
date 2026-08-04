## ADDED Requirements

### Requirement: Missing Test Prerequisites Must Cause a Non-Zero Exit
When a hard runtime prerequisite for the integration test suite is absent from the environment, `go test ./...` SHALL exit with a non-zero exit code and a clear, actionable error message identifying the missing prerequisite and referring to the documented fallback. Exiting with code 0 when no tests ran is not acceptable — code 0 is indistinguishable from all tests passing and constitutes a false green.

#### Scenario: ffmpeg absent causes go test to exit non-zero
- **WHEN** `go test ./...` is run in an environment where `ffmpeg` is not on `PATH`
- **THEN** the process exits with a non-zero exit code and prints an English message identifying the missing prerequisite and pointing to the Docker fallback documented in `CLAUDE.md`

#### Scenario: ffmpeg present leaves test behavior unchanged
- **WHEN** `go test ./...` is run in an environment where `ffmpeg` is on `PATH`
- **THEN** the suite runs and exits with the same outcome as before this change — this requirement adds no new test cases and changes no passing behavior
