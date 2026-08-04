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
go run main.go

# Build a binary
go build -o app .
./app
```

The server creates `uploads/`, `temp/`, and `outputs/` in the working directory on first run.

## Running Tests

Tests are integration tests that drive the real Gin handlers via `httptest.NewServer`. They execute real `ffmpeg` commands and write real files. `ffmpeg` must be on `PATH`.

```bash
go test ./... -v
```

If `ffmpeg` is not available, the test suite skips gracefully with a message:

```
SKIP: ffmpeg não encontrado no PATH — pulando testes de integração ...
```

### Docker fallback (no local Go/ffmpeg required)

```bash
docker build -t video-processor . && docker run --rm video-processor go test ./... -v
```

This uses the image's bundled Go toolchain and ffmpeg. Use this if your local environment does not have Go 1.25 or ffmpeg installed.

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

PRs must separate spec content from code content:

1. **Propose PR** — only `openspec/changes/<name>/` artifacts; no code
2. **Implement PR** — only the code diff (plus checking off `tasks.md`); no new spec content
3. **Archive PR** — folds specs, moves change folder to `archive/`; no application code

### Branch Protection

`main` is protected. All changes land via a feature branch and pull request. Required status checks: `Build & Test`, `SAST (gosec)`, `Vulnerability Scan (govulncheck)`. A PR is not mergeable until all three pass and the branch is up to date with `main`.

```bash
git checkout -b feat/short-description
git push -u origin feat/short-description
gh pr create --fill
```
