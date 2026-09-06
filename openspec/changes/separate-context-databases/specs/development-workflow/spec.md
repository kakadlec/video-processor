## MODIFIED Requirements

### Requirement: Automated Test Gate
Every push to `main` and every pull request SHALL run the full test suite (`go test ./...`) in CI, with `ffmpeg` available in the CI environment and a PostgreSQL service available and reachable via each context's own test connection string — `IDENTITY_POSTGRES_TEST_DSN`, `VIDEO_POSTGRES_TEST_DSN`, and `NOTIFICATION_POSTGRES_TEST_DSN`, each naming a **different** database provisioned by CI. The CI test job SHALL fail if any test fails. Tests that depend on PostgreSQL SHALL NOT be allowed to silently skip in the CI environment.

Pointing the three at one database would leave a query crossing a context boundary passing on every pull request, since CI is where such a query would otherwise never be executed against a separated arrangement (`ddd-architecture`'s Context Storage Isolation Is Exhibited, Not Only Permitted).

#### Scenario: CI fails on a failing test
- **WHEN** a commit is pushed where a test fails
- **THEN** the CI test job fails and is visibly reported on the commit or pull request

#### Scenario: CI passes when all tests pass
- **WHEN** a commit is pushed where every test passes
- **THEN** the CI test job succeeds

#### Scenario: PostgreSQL-backed tests run for real in CI, not skip
- **WHEN** the CI test job runs `go test ./...`
- **THEN** `IDENTITY_POSTGRES_TEST_DSN` is set to a reachable PostgreSQL service provisioned by CI, and `internal/identity/infrastructure/postgres`'s adapter tests execute against it rather than skipping

#### Scenario: Each context's adapter suite runs against its own database
- **WHEN** the CI test job runs `go test ./...`
- **THEN** the three test connection strings name three different databases, so an adapter suite truncating its own tables cannot touch another context's rows and a query naming another context's table fails

### Requirement: Local PostgreSQL Development Service
The repository SHALL provide a `docker-compose.yml` at its root that starts a local PostgreSQL service matching the version used in CI, so any contributor can run the full test suite — including PostgreSQL-backed adapter tests — locally with a single documented command, without hand-provisioning a database and without manually exporting a database connection string.

That service SHALL provision one database per bounded context for runtime use and one per bounded context for the test suite, created on first initialization. Because the provisioning runs only when the data directory is empty, the repository SHALL document how a contributor with an existing volume creates the missing databases against a live container — the alternative, removing the project's volumes, would also destroy the object-storage and broker volumes the same file warns against losing.

#### Scenario: Contributor runs the full suite locally
- **WHEN** a contributor runs the documented `docker compose run --build --rm app-test go test ./... -v` command
- **THEN** the command runs `go test ./...` inside a container built from the repository's `Dockerfile`'s test stage (the only stage with both the Go toolchain and `ffmpeg`), against the compose-provisioned PostgreSQL service, with each context's test connection string already configured to its own database — exercising the PostgreSQL-backed adapter tests without the contributor exporting anything or installing Go/ffmpeg locally

#### Scenario: Local and CI databases stay aligned
- **WHEN** the PostgreSQL image version is changed in `docker-compose.yml`
- **THEN** the corresponding service image in `.github/workflows/ci.yml` is updated to match in the same change

#### Scenario: A contributor with an existing volume is told how to recover
- **GIVEN** a contributor whose PostgreSQL volume predates the per-context databases
- **WHEN** they start the stack and a process fails because its database does not exist
- **THEN** the repository's documentation names the statements that create the missing databases against the running container, without requiring the project's volumes to be removed
