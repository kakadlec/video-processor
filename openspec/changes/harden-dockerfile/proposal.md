## Why

The repository's only `Dockerfile` is a deliberately bad example (see its own header comment): single-stage, runs as root, resolves dependencies with `go mod tidy` against whatever the module proxy serves at build time (not the committed `go.sum`), and ships the full Go toolchain in the final image because there's no separate runtime stage. It's now the build source for `docker-compose.yml`'s `app` service (`add-docker-compose-app-service`, archived) and for the deployment path documented in `docs/operations.md`. Both now depend on an image that has no hardening applied. This was pulled forward from Phase 8 at the user's explicit request rather than left bundled with that phase's observability work.

## What Changes

- Replace the single-stage `Dockerfile` with a multi-stage build:
  - A **builder stage** (`golang:1.26-alpine`) that runs `go mod download` (resolving from the committed `go.sum`, not `go mod tidy`) and compiles a static binary (`CGO_ENABLED=0`).
  - A **runtime stage** on a minimal base with `ffmpeg` installed and no Go toolchain, running as a non-root user, that copies in only the compiled binary.
- Add a `.dockerignore` so the build context (and therefore `COPY . .` in the builder stage) excludes local runtime directories (`uploads/`, `outputs/`, `temp/`) and VCS metadata.
- Keep the image's runtime contract unchanged: still listens on `:8080`, still creates `uploads/`/`outputs/`/`temp/` on first run, still takes no required environment variables — so `docker-compose.yml`'s `app` service (`build: .`) and `docs/operations.md`'s `docker build`/`docker run` commands keep working unmodified.
- Update the documentation that currently describes the Dockerfile as an intentional anti-pattern (`docs/development.md`, `docs/operations.md`, `CLAUDE.md`) to describe the hardened build instead.

**BREAKING**: none at the container's external interface (port, env vars, CMD behavior all unchanged) — this is an internal image-build change.

## Capabilities

### New Capabilities
- `container-image`: build and runtime requirements for the repository's `Dockerfile` — multi-stage build, deterministic dependency resolution, non-root runtime user, minimal runtime image contents.

### Modified Capabilities
(none — `development-workflow`'s existing requirements about `docker-compose.yml` being the sole local-dev entry point are unaffected; the `app` service keeps building from the same `Dockerfile` path)

## Impact

- `Dockerfile` (rewritten)
- `.dockerignore` (new)
- `docs/development.md`, `docs/operations.md`, `CLAUDE.md` (documentation updates reflecting the hardened image, follow-on docs PR)
- No `main.go`/`main_test.go` changes — application code and its runtime contract are untouched
