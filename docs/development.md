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

## Code Quality Gates

The CI pipeline runs three checks — `Build & Test` (`go vet` + `go test`), `SAST` (`gosec`), `Vulnerability Scan` (`govulncheck`) — on every push and pull request, regardless of what the diff touches; that's the branch-protection gate, not a diff-conditional one.

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

Locally, only run `go vet`/`go test` when the diff includes a Go module input (`.go`/`go.mod`/`go.sum` — see "Change Completion Requires A Passing Test Run" below); `gosec`/`govulncheck` scan the whole codebase, so a docs/skill-only change rarely needs a fresh local run of those two, but running them costs little and CI catches anything missed regardless.

All three must pass in CI. The CI build fails on **any** `gosec` finding — deliberate policy, not a bug. `#nosec` is a last resort, not the default response to a finding: check the rule's own docs (e.g. `securego.io/docs/rules/g304.html` — lowercase, case-sensitive path) for a validation pattern gosec recognizes as safe, and test it (`gosec ./...`) before reaching for suppression — several findings that looked like they needed `#nosec` turned out to be fixable with a real containment check instead. Only suppress a finding that's genuinely a false positive or an accepted risk with no recognized fix pattern, using a bare inline `#nosec G<rule-id>` comment (no restated prose — that's what commit messages and PR descriptions are for). Never disable the SAST job or exclude whole files/rules to make it pass. `govulncheck` failures are resolved by upgrading the implicated dependency — generally by bumping the direct dependency that pulls it in transitively (see `go mod graph`) — then `go mod tidy`. Dependabot alerts should be resolved the same way, as soon as they're opened, not left to accumulate.

### Change Completion Requires A Passing Test Run

A change whose diff includes a Go module input file (`.go` source, `go.mod`, or `go.sum`) is not complete until `go test ./...` has been run and passes locally — this applies before reporting the change done, not just before pushing, and it applies to a dependency-only bump (`go.mod`/`go.sum` with no `.go` file touched) just as much as a source change, since that can still change compiled/runtime behavior. A change whose diff has no Go module input file (documentation, OpenSpec artifacts, agent/skill configuration) is exempt from this specific requirement — don't claim a test run that didn't happen.

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

### Releases

Versioning is **not** manual — nobody runs `git tag` by hand. On every push to `main`, `release-please` (`.github/workflows/release-please.yml`) maintains a single up-to-date "Release PR" aggregating unreleased Conventional Commits, showing the computed next version and changelog. Merging that PR is what actually cuts a release: it creates the git tag, publishes a GitHub Release with generated notes, and updates `CHANGELOG.md`. Until it's merged, nothing is tagged or released. Config: `release-please-config.json` (`release-type: simple` — this app has no package-manager manifest to version-bump) and `.release-please-manifest.json` (tracks the current released version per path).

### OpenSpec Workflow

For larger changes, this repo has often organized work through a spec-driven process ([OpenSpec](https://github.com/Fission-AI/OpenSpec)):

1. **Explore (when warranted):** `/opsx:explore` → for changes that are complex or ambiguous (cross-cutting impact, a new architectural pattern/dependency, security/performance/migration complexity, or open design questions), before proposing.
2. **Propose:** `/opsx:propose` → creates `openspec/changes/<name>/proposal.md`, `design.md`, `tasks.md`
3. **Implement:** `/opsx:apply` → work through `tasks.md`
4. **Archive:** `/opsx:archive` → folds the change into `openspec/specs/`

This documents a pattern the repo has used, not a gate this file enforces. Whether a given change warrants this process, a lighter version of it, or a single direct PR is the maintainer's call, made per change — not something this document or an automated review decides on the maintainer's behalf.

For Claude Code specifically, the `change-lifecycle` skill (`.claude/skills/change-lifecycle/SKILL.md`) and `repo-workflow` skill (`.claude/skills/repo-workflow/SKILL.md`) are where the actual scoping/sequencing judgment lives for Claude Code's own work — that's what decides how to approach a given change, deferring to the maintainer's explicit direction when given. Update those skills, not this section, if that judgment needs to change.

### PR Separation Rule

When the process above is in use for a given change, it has commonly used three PR roles, in this order:

1. **Propose PR** — the new `openspec/changes/<name>/` artifacts. Merges before implementation begins.
2. **Implementation PR** — the files that implement the change's declared scope.
3. **Finalization PR** — after implementation merges, marking completed tasks, promoting the delta into `openspec/specs/`, moving the change folder into `openspec/changes/archive/`, and updating any permanent documentation that needs to reflect the shipped change.

This keeps a spec-driven change's PRs individually reviewable; it isn't a requirement for every PR in this repo, and a single direct PR (a bugfix, a small CI/config change, anything the maintainer chooses to just ship) is a legitimate, normal way to contribute here.

Green CI does not authorize a merge. An agent may merge only when the user explicitly authorizes that specific PR in the current session; authorization for one PR does not extend to later PRs.

### Branch Protection

`main` is protected. All changes land via a feature branch and pull request. Required status checks: `Build & Test`, `SAST (gosec)`, `Vulnerability Scan (govulncheck)`. All review conversations must also be resolved before merge, including inline threads opened by GitHub Copilot. A PR is not mergeable until all three checks pass, the branch is up to date with `main`, and no review thread remains unresolved. This protection is enforced for administrators too.

```bash
git fetch origin
git checkout -b feat/short-description origin/main
git push -u origin feat/short-description
gh pr create --fill
```

Branch from freshly-fetched `origin/main` rather than from whatever is currently checked out. Changes here land as PR sequences, so the working tree is frequently on an earlier branch of the same sequence or on an unrelated open PR; branching from there carries those commits into the new PR's diff and breaks its declared file scope.

### PR Review Comments

This repository has a `copilot_code_review` branch ruleset that automatically requests a GitHub Copilot review the first time each pull request opens. `review_on_push` is off, so later commits pushed to an already-reviewed PR do **not** trigger a fresh automatic review — request one manually if a substantial follow-up change warrants a new pass.

Before reporting a PR-related task complete, check that PR for review comments (automatic and human), inspect the merge state, and address the findings that make sense:

```bash
gh pr view <n> --json reviews,mergeable,mergeStateStatus
gh api repos/{owner}/{repo}/pulls/{n}/comments
```

Fix genuine findings and resolve their threads (`resolveReviewThread` GraphQL mutation). If a finding doesn't warrant a code change, document why and still resolve the conversation rather than leaving it silently open. `gh pr checks` does not report unresolved conversations as a failed CI check; GitHub exposes the condition through the PR's blocked merge state and enforces it server-side when merging. Copilot's review can take a short while to post after a push — an empty check immediately after opening the PR doesn't mean there's nothing coming.

### Validation and Handoff

Before opening or handing off a PR:

```bash
git diff --check
npx --yes @fission-ai/openspec validate <change-id> --strict --no-interactive   # only if an OpenSpec change is involved
```

Before reporting implementation complete, run the repository's required tests and checks (see "Change Completion Requires A Passing Test Run" above). Report the PR number, URL, changed-file scope, and check results. Never direct-push to `main`.
