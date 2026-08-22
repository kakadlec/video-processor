# Development Guide

## Prerequisites

| Tool | Version | Purpose |
|---|---|---|
| Go | 1.25+ | Build and test the application |
| ffmpeg | any recent | Frame extraction; must be on `PATH` |
| Docker | any recent | Alternative if Go/ffmpeg are not installed locally |
| git | any | Source control |

### Installing ffmpeg

```bash
# Ubuntu / Debian
sudo apt-get install ffmpeg

# macOS (Homebrew)
brew install ffmpeg

# Alpine Linux (Docker)
apk add --no-cache ffmpeg
```

## Running Locally

Identity, Video Processing, and Redis configuration are all required at startup — the server refuses to start unless `IDENTITY_POSTGRES_DSN`, `IDENTITY_JWT_SIGNING_KEY`, `VIDEO_POSTGRES_DSN`, and `REDIS_ADDR` are all set (see [docs/operations.md](operations.md) for all four variables). Start PostgreSQL and Redis (`docker compose up -d postgres redis`) and export all four before `go run ./cmd/api`:

```bash
# Download dependencies
go mod download

# Start PostgreSQL and Redis for the identity, video, and idempotency-key modules
docker compose up -d postgres redis

# Set required identity, video, and Redis configuration
export IDENTITY_POSTGRES_DSN="postgres://identity:identity@localhost:5432/identity?sslmode=disable"
export IDENTITY_JWT_SIGNING_KEY="dev-signing-key"
export VIDEO_POSTGRES_DSN="postgres://identity:identity@localhost:5432/identity?sslmode=disable"
export REDIS_ADDR="localhost:6379"

# Start the server (listens on :8080)
go run ./cmd/api

# Build a binary
go build -o app ./cmd/api
./app
```

The server creates `uploads/`, `temp/`, and `outputs/` in the working directory on first run.

To skip the manual wiring entirely, use `docker compose up --build`, which runs the whole application inside Docker with identity already configured — see "Docker Workflow" below.

## Running Tests

Tests are integration tests that drive the real Gin handlers via `httptest.NewServer`. They execute real `ffmpeg` commands and write real files. `ffmpeg` must be on `PATH`.

```bash
go test ./... -v
```

If `ffmpeg` is not available, the suite exits immediately with code 1:

```
FATAL: ffmpeg not found in PATH — integration tests require ffmpeg; see CLAUDE.md for the Docker fallback.
```

### Running the full suite via Docker, including PostgreSQL-backed tests

```bash
docker compose run --build --rm app-test go test ./... -v
```

`app-test` builds from the `Dockerfile`'s `test` stage (Go toolchain + `ffmpeg`) and runs `go test` inside it — no local Go or ffmpeg install required. It's a separate service from `app` because `app`'s image (the hardened, deployed build) deliberately has no Go toolchain; see "Docker Workflow" below. `app-test` is gated behind Compose's `test` profile so it never starts as part of a plain `docker compose up`/`up --build` — `docker compose run` targets it explicitly regardless, so the command above needs no extra flag.

`internal/identity/infrastructure/postgres`'s adapter tests, which otherwise skip (not fail) when `IDENTITY_POSTGRES_TEST_DSN` is unset, run automatically here: `docker-compose.yml`'s `postgres` service creates an isolated `identity_test` database on first init (see `docker/postgres-init/create-test-db.sql`), and `IDENTITY_POSTGRES_TEST_DSN` is already pointed at it — no manual export needed.

That database is separate from the runtime `identity` database `IDENTITY_POSTGRES_DSN` uses, so this is safe to run even while `docker compose up --build` is serving real registered users — the test run's `TRUNCATE` only touches `identity_test`.

> **If you already have a `postgres_data` volume from before this change:** the init script that creates `identity_test` only runs against a fresh, empty PostgreSQL data directory. An existing volume won't get the new database, and the command above will fail rather than run the adapter tests. Run `docker compose down -v` once to drop the old volume (this destroys any local Postgres data you had), then `docker compose up --build` recreates it with `identity_test` included.

`docker-compose.yml` is the sole documented way to run the application or its tests via Docker **for local development** (container deployment is a separate concern; see [docs/operations.md](operations.md)) — there is no separate plain `docker build`/`docker run` fallback documented for local dev. The `identity`/`identity` Postgres credentials and the app's JWT signing key are fixed, non-secret local-only defaults. `app`'s port is published loopback-only (`127.0.0.1:8080:8080`); note that `postgres`'s port (`5432:5432`, unqualified, matching the pre-existing test-infrastructure setup) is not similarly restricted and is reachable from other machines on the same network unless firewalled.

```bash
docker compose down       # stop
docker compose down -v    # stop and drop the local data volume(s)
```

## CI checks

The CI pipeline runs three checks — `Build & Test` (`go vet` + `go test`), `SAST` (`gosec`), and `Vulnerability Scan` (`govulncheck`) — on every push and pull request.

```bash
# Static analysis
go vet ./...

# SAST (requires gosec)
go install github.com/securego/gosec/v2/cmd/gosec@latest
gosec ./...

# Dependency vulnerability scan (requires govulncheck)
go install golang.org/x/vuln/cmd/govulncheck@latest
govulncheck ./...
```

These commands can also be run locally. `gosec` and `govulncheck` scan the full codebase; CI reports their results for every push and pull request. The SAST job fails when `gosec` reports a finding, and the vulnerability scan fails for reachable known vulnerabilities.

## Docker Workflow

```bash
docker compose up --build
# Access the UI by opening http://127.0.0.1:8080 in a browser —
# identity, video, and Redis are already configured (PostgreSQL, JWT signing key, REDIS_ADDR)
```

`docker-compose.yml` is the sole documented way to build and run the application via Docker **for local development** (see "Running the full suite via Docker" above for the equivalent test command). It builds from the same `Dockerfile` used for deployment — see [docs/operations.md](operations.md) for the deployment-focused Docker commands, which are a separate concern from this local dev workflow.

> The `Dockerfile` is a multi-stage build: a `builder` stage compiles a static binary (dependencies resolved read-only from the committed `go.sum` — the build fails rather than silently patching it), a `test` stage adds `ffmpeg` on top of `builder` for running the suite (see `app-test` above), and the default `runtime` stage — the one `app` and deployment both use — ships only the compiled binary and `ffmpeg`, no Go toolchain or source tree, running as a non-root user (fixed UID 1000).
>
> **If the app fails to write to `./uploads`/`./outputs` on a fresh clone:** those directories are bind-mounted into the container, and the non-root user (UID 1000) needs write access to them. `chown -R 1000:1000 uploads outputs` once, or `chmod` them, fixes it — deleting and letting Docker recreate the directories does **not** help, since Docker creates a missing bind-mount source as root-owned regardless of which user runs `docker compose`.

## Dependency Management

```bash
# Sync go.mod and go.sum after any dependency change
go mod tidy
```

## Optional workflow skills

The user chooses whether a change uses a workflow. Do not impose OpenSpec, tests, branches, pull requests, commits, or review procedures unless the user explicitly requests that workflow.

- Invoke `.claude/skills/change-lifecycle/SKILL.md` for the full OpenSpec lifecycle.
- Invoke `.claude/skills/repo-workflow/SKILL.md` for the repository PR, validation, commit, and release procedure.
- The `openspec-explore`, `openspec-propose`, `openspec-apply-change`, and `openspec-archive-change` skills remain available individually.

CI runs `Build & Test`, `SAST (gosec)`, and `Vulnerability Scan (govulncheck)` for pushes and pull requests. `release-please` maintains a release PR from commits on `main`; merging it publishes the release and updates `CHANGELOG.md`.
