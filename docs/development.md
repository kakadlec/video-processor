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

```bash
# Download dependencies
go mod download

# Start the server (listens on :8080)
go run .

# Build a binary
go build -o app .
./app
```

The server creates `uploads/`, `temp/`, and `outputs/` in the working directory on first run.

By default it runs with identity disabled (video processing only, no auth). To exercise registration/login/bearer-protected routes locally, start PostgreSQL (`docker compose up -d postgres`) and set both `IDENTITY_POSTGRES_DSN` and `IDENTITY_JWT_SIGNING_KEY` before `go run .` — see [docs/operations.md](operations.md) for both variables. Or skip the manual wiring entirely with `docker compose up --build`, which runs the whole application inside Docker with identity already configured — see "Docker Workflow" below.

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
docker compose run --build --rm app go test ./... -v
```

This builds the image from the repository's `Dockerfile` and runs `go test` inside it — no local Go or ffmpeg install required. `internal/identity/infrastructure/postgres`'s adapter tests, which otherwise skip (not fail) when `IDENTITY_POSTGRES_TEST_DSN` is unset, run automatically here: `docker-compose.yml`'s `postgres` service creates an isolated `identity_test` database on first init (see `docker/postgres-init/create-test-db.sql`), and `IDENTITY_POSTGRES_TEST_DSN` is already pointed at it — no manual export needed.

That database is separate from the runtime `identity` database `IDENTITY_POSTGRES_DSN` uses, so this is safe to run even while `docker compose up --build` is serving real registered users — the test run's `TRUNCATE` only touches `identity_test`.

`docker-compose.yml` is the sole documented way to run the application or its tests via Docker in this repository — there is no separate plain `docker build`/`docker run` fallback. The `identity`/`identity` Postgres credentials and the app's JWT signing key are fixed, non-secret local-only defaults — this stack is never reachable from anywhere but your machine or CI.

```bash
docker compose down       # stop
docker compose down -v    # stop and drop the local data volume(s)
```

## Code Quality Gates

The CI pipeline runs three checks on every push and pull request. Run them locally before pushing:

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

All three must pass. The CI build fails on any `gosec` finding — `#nosec` is a last resort, not the default response. See `CLAUDE.md` for the full policy.

## Docker Workflow

```bash
docker compose up --build
# Access the UI by opening http://127.0.0.1:8080 in a browser —
# identity is already configured (PostgreSQL + JWT signing key)
```

`docker-compose.yml` is the sole documented way to build and run the application via Docker in this repository (see "Running the full suite via Docker" above for the equivalent test command). It builds from the same `Dockerfile` used for deployment.

> The Dockerfile is intentionally a single-stage build without a non-root user (documented anti-pattern for study). Dockerfile hardening is tracked in `docs/roadmap.md`'s Change Backlog (`harden-dockerfile`).

## Dependency Management

```bash
# Sync go.mod and go.sum after any dependency change
go mod tidy
```

## Contribution Conventions

### Commit Messages

Follow [Conventional Commits](https://www.conventionalcommits.org/):

```
feat: short description of new capability
fix: short description of bug fix
chore: dependency bump or tooling change
docs: documentation only
ci: CI workflow change
test: test additions or changes
refactor: internal restructuring, no behavior change
```

Use `!` after the type (e.g. `feat!:`) or a `BREAKING CHANGE:` footer for breaking changes. Commit messages drive automated versioning via `release-please` — do not version manually.

### OpenSpec Workflow

Non-trivial changes (new features, behavior changes, bug fixes with real design decisions, refactors) follow the spec-driven process:

1. **Propose:** `/opsx:propose` → creates `openspec/changes/<name>/proposal.md`, `design.md`, `tasks.md`
2. **Implement:** `/opsx:apply` → work through `tasks.md`
3. **Archive:** `/opsx:archive` → folds the change into `openspec/specs/`

Skip this flow only for trivial changes (typo fixes, comment tweaks, dependency bumps).

### PR Separation Rule

Non-trivial changes use three PR roles, in this order:

1. **Propose PR** — only the new `openspec/changes/<name>/` artifacts; no application code, tests, docs, agent instructions, configuration, CI, or canonical specs. This PR must merge before implementation begins.
2. **Implementation PR** — only the files that implement the change's declared proposal scope: application source and test files for a feature/behavior change, or the specific configuration/CI/infrastructure files named in the proposal for a change whose own subject is configuration, infrastructure, or CI. It must not modify `tasks.md`, `README`, `docs/`, `CLAUDE.md`, `AGENTS.md`, configuration or CI files unrelated to that scope, or any file under `openspec/`.
3. **Finalization/archive PR** — after implementation merges, mark the completed tasks, promote the delta into `openspec/specs/`, and move the change folder into `openspec/changes/archive/`. It must not contain application source or tests.

Permanent documentation or agent-instruction changes belong in a separate docs PR and must never be bundled into the implementation PR. `tasks.md` checkoffs belong in the finalization/archive PR, not in the implementation PR.

Green CI does not authorize a merge. An agent may merge only when the user explicitly authorizes that specific PR in the current session; authorization for one PR does not extend to later PRs.
### Branch Protection

`main` is protected. All changes land via a feature branch and pull request. Required status checks: `Build & Test`, `SAST (gosec)`, `Vulnerability Scan (govulncheck)`. A PR is not mergeable until all three pass and the branch is up to date with `main`.

```bash
git checkout -b feat/short-description
git push -u origin feat/short-description
gh pr create --fill
```
