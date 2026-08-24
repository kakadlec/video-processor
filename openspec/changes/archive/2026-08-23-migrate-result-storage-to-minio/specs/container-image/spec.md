## MODIFIED Requirements

### Requirement: Multi-Stage Image Build
The repository's `Dockerfile` SHALL use a multi-stage build: a builder stage that compiles the application and a separate runtime stage that contains no Go toolchain or source tree. Dependency resolution in the builder SHALL run in a read-only mode (e.g. `go mod download` under `-mod=readonly`) that verifies against the committed `go.sum` and fails the build on any mismatch or missing entry, rather than a mode that can add to or rewrite `go.mod`/`go.sum` (e.g. `go mod tidy`, or bare `go mod download` without `-mod=readonly`).

#### Scenario: Runtime image contains no Go toolchain
- **WHEN** the runtime stage's image is built
- **THEN** it does not contain the `go` binary or the application's source tree — only the compiled binary, `ffmpeg`, and their runtime dependencies

#### Scenario: Build resolves dependencies deterministically
- **WHEN** the builder stage runs
- **THEN** it resolves modules in read-only mode, verifying against the committed `go.sum`, and fails the build if a checksum doesn't match or an entry is missing — it does not add missing checksums or otherwise rewrite `go.mod`/`go.sum` to make the build succeed

#### Scenario: A Go- and ffmpeg-capable stage exists for running tests
- **WHEN** the Dockerfile is built targeting its test stage
- **THEN** the resulting image contains both the Go toolchain and `ffmpeg`, while the default/final image built without a target selection remains the Go-toolchain-free runtime stage

#### Scenario: The test stage alone is not sufficient to run the suite
- **WHEN** that image runs `go test ./...`
- **THEN** it additionally requires the reachable backing services the suite depends on — PostgreSQL, Redis, and a MinIO instance configured through `VIDEO_MINIO_*` — which `docker-compose.yml`'s `app-test` service supplies; the image contents alone do not satisfy `cmd/api`'s integration tests

### Requirement: Non-Root Runtime User

The runtime stage SHALL run the application process as a non-root user, not `root`, with a working directory and `uploads`/`temp` subdirectories that user owns and can write to. `outputs` is no longer among them: result artifacts live in object storage, and the application no longer creates or writes that directory.

#### Scenario: Container process runs unprivileged

- **WHEN** a container is started from the built image
- **THEN** the application process's effective user is a non-root user

#### Scenario: Non-root user can create its runtime directories

- **WHEN** the application starts for the first time and creates `uploads/` and `temp/` relative to its working directory
- **THEN** it succeeds, because the runtime stage pre-creates and owns these directories (and the working directory containing them) for the non-root user before switching to it

### Requirement: Unchanged External Contract

Hardening the image SHALL NOT change its external contract: the application SHALL still listen on port 8080 and create the runtime directories it needs on first run — so `docker-compose.yml`'s `app` service and the deployment commands documented in `docs/operations.md` keep working.

The application requires environment configuration to start, and SHALL fail fast with a clear error when it is missing rather than starting in a degraded mode. This requirement previously asserted that the application needed no environment variables at all; that has been false since Phase 2 made `IDENTITY_POSTGRES_DSN`/`IDENTITY_JWT_SIGNING_KEY` mandatory, and it is corrected here rather than left standing as a known-false normative claim. Which variables are required is specified by the capabilities that own them, not by this one.

#### Scenario: Existing compose service keeps working

- **WHEN** `docker compose up --build` runs
- **THEN** the `app` service builds successfully and serves the application on port 8080, with the environment the compose file supplies

#### Scenario: Existing deployment commands keep working

- **WHEN** a deployer runs the `docker build`/`docker run` commands documented in `docs/operations.md`, supplying every environment variable those docs list as required
- **THEN** they succeed unmodified and the container behaves as documented: same port, same first-run directory creation, and the same fail-fast behavior for missing configuration

#### Scenario: Missing configuration fails fast rather than degrading

- **WHEN** a container is started without the environment variables the application requires
- **THEN** it exits with an error naming what is missing, rather than starting and failing at request time
