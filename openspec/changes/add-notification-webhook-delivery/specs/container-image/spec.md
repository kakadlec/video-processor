## MODIFIED Requirements

### Requirement: Multi-Stage Image Build
The repository's `Dockerfile` SHALL use a multi-stage build: a builder stage that compiles the application and a separate runtime stage that contains no Go toolchain or source tree. The builder SHALL compile **all three** entrypoints — `cmd/api`, `cmd/worker`, and `cmd/notifier` — and the runtime stage SHALL carry all three binaries and `ffmpeg`, so one image can be started as any of the three processes. `ffmpeg` is required by the worker rather than by the API or the notifier, and SHALL remain present for that reason. Dependency resolution in the builder SHALL run in a read-only mode (e.g. `go mod download` under `-mod=readonly`) that verifies against the committed `go.sum` and fails the build on any mismatch or missing entry, rather than a mode that can add to or rewrite `go.mod`/`go.sum` (e.g. `go mod tidy`, or bare `go mod download` without `-mod=readonly`).

The third binary joins the image rather than getting one of its own for the reason the second did: the three share every `internal/` package, and separate images would create a way for the halves of one deploy to be built from different commits of the same domain code.

#### Scenario: Runtime image contains no Go toolchain
- **WHEN** the runtime stage's image is built
- **THEN** it does not contain the `go` binary or the application's source tree — only the compiled binaries, `ffmpeg`, and their runtime dependencies

#### Scenario: All three entrypoints are built and present
- **WHEN** the runtime image is built
- **THEN** it contains an executable for `cmd/api`, one for `cmd/worker`, and one for `cmd/notifier`, each runnable on its own, and none requires either of the others to be running in the same container

#### Scenario: Build resolves dependencies deterministically
- **WHEN** the builder stage runs
- **THEN** it resolves modules in read-only mode, verifying against the committed `go.sum`, and fails the build if a checksum doesn't match or an entry is missing — it does not add missing checksums or otherwise rewrite `go.mod`/`go.sum` to make the build succeed

#### Scenario: A Go- and ffmpeg-capable stage exists for running tests
- **WHEN** the Dockerfile is built targeting its test stage
- **THEN** the resulting image contains both the Go toolchain and `ffmpeg`, while the default/final image built without a target selection remains the Go-toolchain-free runtime stage

#### Scenario: The test stage alone is not sufficient to run the suite
- **WHEN** that image runs `go test ./...`
- **THEN** it additionally requires the reachable backing services the suite depends on — PostgreSQL, Redis, and a MinIO instance configured through `VIDEO_MINIO_*` — which `docker-compose.yml`'s `app-test` service supplies; the image contents alone do not satisfy `cmd/api`'s integration tests

### Requirement: Unchanged External Contract

Hardening the image SHALL NOT change its external contract: the application SHALL still listen on port 8080 and create the runtime directories it needs on first run — so `docker-compose.yml`'s `app` service and the deployment commands documented in `docs/operations.md` keep working.

The image now serves three processes, and that SHALL NOT change how any of them is configured or reached: the API SHALL still listen on port 8080 and the compose service that runs it SHALL keep working unmodified, while the worker and the notifier SHALL each expose no port at all. Adding a process SHALL NOT make it a prerequisite for any other to start, in either direction — each SHALL start, run, and fail independently.

Each process SHALL require only the environment configuration it uses, and SHALL fail fast with a clear error when it is missing rather than starting in a degraded mode. The surfaces are deliberately different: the worker requires no identity configuration, and the notifier requires neither identity nor object-storage configuration nor `ffmpeg` — requiring any of them would misrepresent what the process does. This requirement previously asserted that the application needed no environment variables at all; that has been false since Phase 2 made `IDENTITY_POSTGRES_DSN`/`IDENTITY_JWT_SIGNING_KEY` mandatory, and it is corrected here rather than left standing as a known-false normative claim. Which variables are required is specified by the capabilities that own them, not by this one.

#### Scenario: Existing compose service keeps working

- **WHEN** `docker compose up --build` runs
- **THEN** the `app` service builds successfully and serves the application on port 8080, with the environment the compose file supplies

#### Scenario: Existing deployment commands keep working

- **WHEN** a deployer runs the `docker build`/`docker run` commands documented in `docs/operations.md`, supplying every environment variable those docs list as required
- **THEN** they succeed unmodified and the container behaves as documented: same port, same first-run directory creation, and the same fail-fast behavior for missing configuration

#### Scenario: The worker service starts from the same image

- **WHEN** `docker compose up --build` runs
- **THEN** the `worker` service starts from the same image as the `app` service, running the worker binary with the environment the compose file supplies, and exposes no port

#### Scenario: The notifier service starts from the same image

- **WHEN** `docker compose up --build` runs
- **THEN** the `notifier` service starts from the same image as the `app` and `worker` services, running the notifier binary with the environment the compose file supplies, and exposes no port

#### Scenario: Each process starts without the others

- **GIVEN** the built image
- **WHEN** any one of the three binaries is started on its own with its own required configuration
- **THEN** it starts and operates, without requiring either of the others to be running

#### Scenario: Missing configuration fails fast rather than degrading

- **WHEN** a container is started without the environment variables the process it runs requires
- **THEN** it exits with an error naming what is missing, rather than starting and failing at request time
