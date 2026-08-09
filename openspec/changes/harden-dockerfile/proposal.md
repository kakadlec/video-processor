## Why

The repository's only `Dockerfile` is a deliberately bad example (see its own header comment): single-stage, runs as root, resolves dependencies with `go mod tidy` against whatever the module proxy serves at build time (not the committed `go.sum`), and ships the full Go toolchain in the final image because there's no separate runtime stage. It's now the build source for `docker-compose.yml`'s `app` service (`add-docker-compose-app-service`, archived) and for the deployment path documented in `docs/operations.md`. Both now depend on an image that has no hardening applied. This was pulled forward from Phase 8 at the user's explicit request rather than left bundled with that phase's observability work.

## What Changes

- Replace the single-stage `Dockerfile` with a three-stage build:
  - A **builder stage** (`golang:1.26-alpine`) that runs `go mod download` under `-mod=readonly` (resolving from the committed `go.sum` and failing the build on any mismatch, not `go mod tidy`, which can silently patch `go.sum`) and compiles a static binary (`CGO_ENABLED=0`).
  - A **test stage**, `FROM builder`, that additionally installs `ffmpeg` — used only to run `go test ./...` (which needs both the Go toolchain and `ffmpeg` for the integration tests in `main_test.go`); never shipped as the final image.
  - A **runtime stage** on a minimal, currently-supported base with `ffmpeg` installed and no Go toolchain, running as a non-root user, `WORKDIR /app` with `uploads/`/`outputs/`/`temp/` pre-created and owned by that user, that copies in only the compiled binary.
- Add a `.dockerignore` so the build context (and therefore `COPY . .` in the builder stage) excludes local runtime directories (`uploads/`, `outputs/`, `temp/`) and VCS metadata.
- Keep the running application's contract unchanged: still listens on `:8080`, still creates `uploads/`/`outputs/`/`temp/` on first run, still takes no required environment variables — so `docs/operations.md`'s `docker build`/`docker run` commands (which build the default/final stage) keep working unmodified.
- Add a second `docker-compose.yml` service, `app-test`, built from the new `test` stage (same environment/`depends_on` as `app`), so the documented test-running command has a Go+ffmpeg image to run against — the hardened `app` service's final image has neither. This changes the exact command `docs/development.md` documents for running the suite via Docker (from `docker compose run --build --rm app go test ./... -v` to the `app-test` service).
- Update the documentation that currently describes the Dockerfile as an intentional anti-pattern (`docs/development.md`, `docs/operations.md`, `CLAUDE.md`) to describe the hardened build instead.

**BREAKING**: the container's external interface (port, env vars, `CMD` behavior of the `app` service/deployment image) is unchanged. The documented **test-running command changes** (new `app-test` service) because the hardened runtime image no longer contains a Go toolchain.

## Capabilities

### New Capabilities
- `container-image`: build and runtime requirements for the repository's `Dockerfile` — multi-stage build, deterministic dependency resolution, non-root runtime user, minimal runtime image contents.

### Modified Capabilities
- `development-workflow`: the "Contributor runs the full suite locally" scenario's documented command changes from `docker compose run --build --rm app go test ./... -v` to target the new `app-test` service, since the hardened `app` service's image no longer contains a Go toolchain.

## Impact

- `Dockerfile` (rewritten, three stages)
- `.dockerignore` (new)
- `docker-compose.yml` (new `app-test` service; `app` service unchanged)
- `docs/development.md`, `docs/operations.md`, `CLAUDE.md` (documentation updates reflecting the hardened image and the new test command, follow-on docs PR)
- No `main.go`/`main_test.go` changes — application code and its runtime contract are untouched
