# container-image Specification

## Purpose

Define the build and runtime requirements for the repository's `Dockerfile`: a multi-stage build with deterministic, fail-closed dependency resolution, a non-root runtime user, and a minimal runtime image with no Go toolchain that carries every entrypoint (`cmd/identity-api`, `cmd/video-api`, `cmd/notification-api`, `cmd/worker` and `cmd/notifier`) so one image can be started as any of them — while preserving the container's external contract for `docker-compose.yml`'s local-development services and the deployment commands in `docs/operations.md`.
## Requirements
### Requirement: Multi-Stage Image Build
The repository's `Dockerfile` SHALL use a multi-stage build: a builder stage that compiles the application and a separate runtime stage that contains no Go toolchain or source tree. The builder SHALL compile **all five** entrypoints — `cmd/identity-api`, `cmd/video-api`, `cmd/notification-api`, `cmd/worker`, and `cmd/notifier` — and the runtime stage SHALL carry all five binaries and `ffmpeg`, so one image can be started as any of the five processes. `ffmpeg` is required by the worker rather than by any HTTP service or the notifier, and SHALL remain present for that reason. Dependency resolution in the builder SHALL run in a read-only mode (e.g. `go mod download` under `-mod=readonly`) that verifies against the committed `go.sum` and fails the build on any mismatch or missing entry, rather than a mode that can add to or rewrite `go.mod`/`go.sum` (e.g. `go mod tidy`, or bare `go mod download` without `-mod=readonly`).

The third binary joins the image rather than getting one of its own for the reason the second did: the three share every `internal/` package, and separate images would create a way for the halves of one deploy to be built from different commits of the same domain code.

#### Scenario: Runtime image contains no Go toolchain
- **WHEN** the runtime stage's image is built
- **THEN** it does not contain the `go` binary or the application's source tree — only the compiled binaries, `ffmpeg`, and their runtime dependencies

#### Scenario: All five entrypoints are built and present
- **WHEN** the runtime image is built
- **THEN** it contains an executable for each of `cmd/identity-api`, `cmd/video-api`, `cmd/notification-api`, `cmd/worker` and `cmd/notifier`, each runnable on its own, and none requires any of the others to be running in the same container

#### Scenario: Build resolves dependencies deterministically
- **WHEN** the builder stage runs
- **THEN** it resolves modules in read-only mode, verifying against the committed `go.sum`, and fails the build if a checksum doesn't match or an entry is missing — it does not add missing checksums or otherwise rewrite `go.mod`/`go.sum` to make the build succeed

#### Scenario: A Go- and ffmpeg-capable stage exists for running tests
- **WHEN** the Dockerfile is built targeting its test stage
- **THEN** the resulting image contains both the Go toolchain and `ffmpeg`, while the default/final image built without a target selection remains the Go-toolchain-free runtime stage

#### Scenario: The test stage alone is not sufficient to run the suite
- **WHEN** that image runs `go test ./...`
- **THEN** it additionally requires the reachable backing services the suite depends on — PostgreSQL, Redis, and a MinIO instance configured through `VIDEO_MINIO_*` — which `docker-compose.yml`'s `app-test` service supplies; the image contents alone do not satisfy `cmd/video-api`'s integration tests

### Requirement: Non-Root Runtime User

The runtime stage SHALL run the application process as a non-root user, not `root`, with a working directory and a `temp` subdirectory that user owns and can write to. Neither `outputs` nor `uploads` is among them: result artifacts and uploaded source videos both live in object storage, and the application no longer creates or writes either directory.

#### Scenario: Container process runs unprivileged

- **WHEN** a container is started from the built image
- **THEN** the application process's effective user is a non-root user

#### Scenario: Non-root user can create its runtime directory

- **WHEN** the application starts for the first time and creates `temp/` relative to its working directory
- **THEN** it succeeds, because the runtime stage pre-creates and owns that directory (and the working directory containing it) for the non-root user before switching to it

#### Scenario: The image pre-creates no storage-backed directories

- **WHEN** the built image is inspected
- **THEN** its working directory contains `temp` and neither `uploads` nor `outputs`

### Requirement: Unchanged External Contract

Hardening the image SHALL NOT change its external contract: the application SHALL still listen on port 8080 and create the runtime directories it needs on first run — so `docker-compose.yml`'s services and the deployment commands documented in `docs/operations.md` keep working.

The image now serves **five** processes, and that SHALL NOT change how any of them is configured or reached: each of the three HTTP services SHALL listen on port 8080 inside its own container, while the worker and the notifier SHALL each expose no port at all. Because the three HTTP services share that port number, they SHALL be distinguished by container rather than by port, and the compose stack SHALL publish exactly one host port — the ingress's. Adding a process SHALL NOT make it a prerequisite for any other to start, in either direction — each SHALL start, run, and fail independently.

The image carries five binaries and can default to only one, so its default command SHALL name a binary that exists. Removing or renaming the binary the default command names, without changing it, produces an image that builds and scans clean and exits immediately when run without an explicit command — a failure invisible to the compose stack, which names a command for every service, and visible only in the deployment commands `docs/operations.md` documents. Every process other than the default SHALL be started by naming its binary explicitly, and that SHALL be documented.

The ingress is the first service in the stack built from an image this repository does not produce. That SHALL NOT change what this image contains: the ingress SHALL be the stock upstream image plus a mounted configuration file, and no application binary, key, or credential SHALL be added to it.

Each process SHALL require only the environment configuration it uses, and SHALL fail fast with a clear error when it is missing rather than starting in a degraded mode. The surfaces are deliberately different: the Identity service requires no object-storage, broker, cache, or `ffmpeg` configuration; the worker requires no identity configuration; the notifier requires neither identity nor object-storage configuration nor `ffmpeg`; and the two non-Identity HTTP services require a public key but SHALL be given no private key. Requiring any of the absent ones would misrepresent what the process does. Which variables are required is specified by the capabilities that own them, not by this one.

#### Scenario: Each HTTP service starts from the same image

- **WHEN** `docker compose up --build` runs
- **THEN** the Identity, Video Processing, and Notification HTTP services each start from the same image, running their own binary with the environment the compose file supplies, each listening on port 8080 inside its own container and publishing none

#### Scenario: The documented deployment commands match what the image now contains

- **GIVEN** that the HTTP surface is served by three processes behind an ingress rather than by one
- **WHEN** `docs/operations.md`'s `docker build`/`docker run` commands are followed as written, supplying every environment variable those docs list as required for the process being started
- **THEN** each command succeeds and the container behaves as documented: the same host port reaches the system through the ingress, the same first-run directory creation happens in the process that needs it, and configuration is still fail-fast. The documentation SHALL be updated as part of the change that renames the binaries, rather than promising that commands naming a binary the image no longer contains still work

#### Scenario: The image's default command names a binary it contains

- **GIVEN** the built image
- **WHEN** it is run with no command argument
- **THEN** the process named by the image's default command starts and behaves as documented, rather than failing because the binary it names is not present

#### Scenario: The worker service starts from the same image

- **WHEN** `docker compose up --build` runs
- **THEN** the `worker` service starts from the same image as the HTTP services, running the worker binary with the environment the compose file supplies, and exposes no port

#### Scenario: The notifier service starts from the same image

- **WHEN** `docker compose up --build` runs
- **THEN** the `notifier` service starts from the same image as the others, running the notifier binary with the environment the compose file supplies, and exposes no port

#### Scenario: The ingress carries no application code

- **GIVEN** the ingress service in the compose stack
- **WHEN** its image and mounts are inspected
- **THEN** it is the stock upstream image with a configuration file mounted read-only, carrying no binary built from this repository and no key material

#### Scenario: Each process starts without the others

- **GIVEN** the built image
- **WHEN** any one of the five binaries is started on its own with its own required configuration
- **THEN** it starts and operates, without requiring any of the others to be running

#### Scenario: Missing configuration fails fast rather than degrading

- **WHEN** a container is started without the environment variables the process it runs requires
- **THEN** it exits with an error naming what is missing, rather than starting and failing at request time

