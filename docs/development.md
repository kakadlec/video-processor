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

By default it runs with identity disabled (video processing only, no auth). To exercise registration/login/bearer-protected routes locally, start PostgreSQL (`docker compose up -d postgres`) and set both `IDENTITY_POSTGRES_DSN` and `IDENTITY_JWT_SIGNING_KEY` before `go run .` — see [docs/operations.md](operations.md) for both variables.

## Running Tests

Tests are integration tests that drive the real Gin handlers via `httptest.NewServer`. They execute real `ffmpeg` commands and write real files. `ffmpeg` must be on `PATH`.

```bash
go test ./... -v
```

If `ffmpeg` is not available, the suite exits immediately with code 1:

```
FATAL: ffmpeg not found in PATH — integration tests require ffmpeg; see CLAUDE.md for the Docker fallback.
```

### Docker fallback (no local Go/ffmpeg required)

```bash
docker build -t video-processor . && docker run --rm video-processor go test ./... -v
```

This uses the image's bundled Go toolchain and ffmpeg. Use this if your local environment does not have Go 1.25 or ffmpeg installed.

### PostgreSQL (identity persistence tests)

`internal/identity/infrastructure/postgres`'s adapter tests only run against a real database — they skip (not fail) when `IDENTITY_POSTGRES_TEST_DSN` is unset, so they don't require PostgreSQL for the rest of the suite to run. To exercise them locally:

```bash
docker compose up -d postgres

export IDENTITY_POSTGRES_TEST_DSN="postgres://identity:identity@localhost:5432/identity?sslmode=disable"
go test ./... -v
```

`docker-compose.yml` at the repo root defines this service (`postgres:16-alpine`, matching the version CI provisions). The `identity`/`identity` credentials are fixed, non-secret local-only defaults — this service is never reachable from anywhere but your machine or CI.

```bash
docker compose down       # stop
docker compose down -v    # stop and drop the local data volume
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
# Build image
docker build -t video-processor .

# Run container
docker run -p 8080:8080 video-processor

# Access the UI by opening http://localhost:8080 in a browser
```

> The Dockerfile is intentionally a single-stage build without a non-root user (documented anti-pattern for study). Dockerfile hardening is planned for Phase 8.

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
2. **Implementation PR** — only application source and test files. It must not modify `tasks.md`, `README`, `docs/`, `CLAUDE.md`, `AGENTS.md`, configuration, CI, or any file under `openspec/`.
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
