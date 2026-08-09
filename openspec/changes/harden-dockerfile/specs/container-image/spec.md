## ADDED Requirements

### Requirement: Multi-Stage Image Build
The repository's `Dockerfile` SHALL use a multi-stage build: a builder stage that compiles the application and a separate runtime stage that contains no Go toolchain or source tree. Dependency resolution in the builder SHALL verify against the committed `go.sum` (e.g. `go mod download`) rather than mutating it (e.g. `go mod tidy`), so a build fails closed on a checksum mismatch instead of silently drifting from what CI already validated.

#### Scenario: Runtime image contains no Go toolchain
- **WHEN** the runtime stage's image is built
- **THEN** it does not contain the `go` binary or the application's source tree — only the compiled binary, `ffmpeg`, and their runtime dependencies

#### Scenario: Build resolves dependencies deterministically
- **WHEN** the builder stage runs
- **THEN** it resolves modules by verifying against the committed `go.sum` and fails the build if a checksum doesn't match, rather than rewriting `go.mod`/`go.sum` from whatever the module proxy currently serves

### Requirement: Non-Root Runtime User
The runtime stage SHALL run the application process as a non-root user, not `root`.

#### Scenario: Container process runs unprivileged
- **WHEN** a container is started from the built image
- **THEN** the application process's effective user is a non-root user

### Requirement: Unchanged External Contract
Hardening the image SHALL NOT change its external contract: the application SHALL still listen on port 8080, require no environment variables to start, and create `uploads/`, `outputs/`, and `temp/` on first run — so `docker-compose.yml`'s `app` service and the deployment commands documented in `docs/operations.md` keep working without modification.

#### Scenario: Existing compose service keeps working
- **WHEN** `docker compose up --build` runs against the hardened image
- **THEN** the `app` service builds successfully and serves the application on port 8080 exactly as before, with no changes required to `docker-compose.yml`

#### Scenario: Existing deployment commands keep working
- **WHEN** a deployer runs the `docker build`/`docker run` commands documented in `docs/operations.md`
- **THEN** they succeed unmodified and the resulting container behaves identically to before hardening (same port, same required/optional environment variables, same first-run directory creation)
