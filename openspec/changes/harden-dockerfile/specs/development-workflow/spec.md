## MODIFIED Requirements

### Requirement: Local PostgreSQL Development Service
The repository SHALL provide a `docker-compose.yml` at its root that starts a local PostgreSQL service matching the version used in CI, so any contributor can run the full test suite — including PostgreSQL-backed adapter tests — locally with a single documented command, without hand-provisioning a database and without manually exporting a database connection string.

#### Scenario: Contributor runs the full suite locally
- **WHEN** a contributor runs the documented `docker compose run --build --rm app-test go test ./... -v` command
- **THEN** the command runs `go test ./...` inside a container built from the repository's `Dockerfile`'s test stage (the only stage with both the Go toolchain and `ffmpeg`), against the compose-provisioned PostgreSQL service, with `IDENTITY_POSTGRES_TEST_DSN` already configured — exercising the PostgreSQL-backed adapter tests without the contributor exporting anything or installing Go/ffmpeg locally

#### Scenario: Local and CI databases stay aligned
- **WHEN** the PostgreSQL image version is changed in `docker-compose.yml`
- **THEN** the corresponding service image in `.github/workflows/ci.yml` is updated to match in the same change
