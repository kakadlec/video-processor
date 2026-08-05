## MODIFIED Requirements

### Requirement: Automated Test Gate
Every push to `main` and every pull request SHALL run the full test suite (`go test ./...`) in CI, with `ffmpeg` available in the CI environment and a PostgreSQL service available and reachable via `IDENTITY_POSTGRES_TEST_DSN`. The CI test job SHALL fail if any test fails. Tests that depend on PostgreSQL SHALL NOT be allowed to silently skip in the CI environment.

#### Scenario: CI fails on a failing test
- **WHEN** a commit is pushed where a test fails
- **THEN** the CI test job fails and is visibly reported on the commit or pull request

#### Scenario: CI passes when all tests pass
- **WHEN** a commit is pushed where every test passes
- **THEN** the CI test job succeeds

#### Scenario: PostgreSQL-backed tests run for real in CI, not skip
- **WHEN** the CI test job runs `go test ./...`
- **THEN** `IDENTITY_POSTGRES_TEST_DSN` is set to a reachable PostgreSQL service provisioned by CI, and `internal/identity/infrastructure/postgres`'s adapter tests execute against it rather than skipping

## ADDED Requirements

### Requirement: Local PostgreSQL Development Service
The repository SHALL provide a `docker-compose.yml` at its root that starts a local PostgreSQL service matching the version used in CI, so any contributor can run the full test suite — including PostgreSQL-backed adapter tests — locally with a single documented command, without hand-provisioning a database.

#### Scenario: Contributor runs the full suite locally
- **WHEN** a contributor starts the PostgreSQL service via the documented `docker-compose` command and sets `IDENTITY_POSTGRES_TEST_DSN` per `docs/development.md`
- **THEN** `go test ./...` runs the PostgreSQL-backed adapter tests against that local service instead of skipping them

#### Scenario: Local and CI databases stay aligned
- **WHEN** the PostgreSQL image version is changed in `docker-compose.yml`
- **THEN** the corresponding service image in `.github/workflows/ci.yml` is updated to match in the same change
