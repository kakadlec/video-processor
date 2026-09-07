# Development Guide

## Prerequisites

| Tool | Version | Purpose |
|---|---|---|
| Go | 1.27+ | Build and test the application |
| ffmpeg | any recent | Frame extraction; must be on `PATH` |
| MinIO | any recent | Source and result storage; `cmd/video-api` and `cmd/worker` require `VIDEO_MINIO_*` at startup, and the Video API's tests require it too |
| RabbitMQ | any recent | `RABBITMQ_URL` must be **set** to run the app or the suite, but the broker does not have to be reachable — the outbox relay owns the connection and retries in its own goroutine. Needed for real coverage of `internal/platform/rabbitmq` and `internal/video/infrastructure/messaging`, whose tests use `RABBITMQ_TEST_URL` and skip cleanly when it is unset |
| Docker | any recent | Alternative if Go/ffmpeg/MinIO/RabbitMQ are not installed locally — `docker compose` provides all of them |
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

There are **five** `go run` targets, one per composition root, and each requires only the configuration it uses. Every one of them fails to start when something it needs is missing rather than degrading:

| Target | Requires | Serves |
|---|---|---|
| `go run ./cmd/identity-api` | `IDENTITY_POSTGRES_DSN`, `IDENTITY_JWT_PRIVATE_KEY`, `IDENTITY_JWT_KEY_ID`, `IDENTITY_JWT_PUBLIC_KEYS` | `POST /api/auth/register`, `POST /api/auth/login` on `:8080` |
| `go run ./cmd/video-api` | `IDENTITY_JWT_PUBLIC_KEYS`, `VIDEO_POSTGRES_DSN`, `REDIS_ADDR`, the four `VIDEO_MINIO_*`, `RABBITMQ_URL` | the frontend, `POST /upload`, `GET /download/:filename`, `GET /api/status`, `/api/video-jobs` on `:8080` |
| `go run ./cmd/notification-api` | `IDENTITY_JWT_PUBLIC_KEYS`, `NOTIFICATION_POSTGRES_DSN`, `REDIS_ADDR` | `GET`/`PUT /api/notification-preferences` on `:8080` |
| `go run ./cmd/worker` | `VIDEO_POSTGRES_DSN`, `REDIS_ADDR`, the four `VIDEO_MINIO_*`, `RABBITMQ_URL` | nothing — no HTTP, no port |
| `go run ./cmd/notifier` | `NOTIFICATION_POSTGRES_DSN`, `RABBITMQ_URL` | nothing — no HTTP, no port |

**All three HTTP services listen on `:8080`**, so running more than one of them directly on a host means giving each its own port or its own container. In the compose stack each has its own container and the gateway is the only thing publishing a port; locally, the simplest path is `docker compose up --build` rather than five `go run`s (see "Docker Workflow" below). What follows is the manual route, run one service at a time.

`RABBITMQ_URL` is the odd one out among the required variables: it must be *set*, but the broker behind it does not have to be up — the outbox relay and the consumers each dial in their own goroutine and retry, so a service starts and serves every route regardless.

**Running one service is not enough to process an upload.** `POST /upload` answers `202` and the job waits on the queue for `cmd/worker`; without a worker, jobs stay `queued` forever and the status endpoint reports exactly that. Without `cmd/notifier`, jobs still complete normally and only the announcement is missing. And without `cmd/identity-api` there is no way to obtain a token in the first place — though a token already issued keeps working while it is down, which is the point of distributing the public key by configuration.

```bash
# Download dependencies
go mod download

# Once per machine: generate the RSA key pair access tokens are signed and
# verified with, into a git-ignored .env. Every compose command reads it, so
# this comes first even when only the dependencies are being started.
make dev-keys

# Start PostgreSQL, Redis, MinIO, and RabbitMQ for the identity, video,
# notification, idempotency-key, storage, outbox-relay, and webhook-delivery
# modules
docker compose up -d postgres redis minio rabbitmq

# Set required identity, video, notification, Redis, MinIO, and broker
# configuration. The three DSNs name three different databases on the same
# server: each bounded context owns its own pool, its own tables, and its own
# database, so a query reaching into another context's tables fails as an
# unknown relation. Sharing one server is a deployment choice; sharing one
# database is not one this project makes.
export IDENTITY_POSTGRES_DSN="postgres://identity:identity@localhost:5432/identity?sslmode=disable"
# Tokens are RS256-signed, so the key material is a pair: the private half and
# the active key id belong to whatever process mints tokens, the public set to
# every process that verifies. `make dev-keys` wrote all three into .env.
set -a && . ./.env && set +a
export VIDEO_POSTGRES_DSN="postgres://identity:identity@localhost:5432/video?sslmode=disable"
export NOTIFICATION_POSTGRES_DSN="postgres://identity:identity@localhost:5432/notification?sslmode=disable"
export REDIS_ADDR="localhost:6379"
export VIDEO_MINIO_ENDPOINT="localhost:9000"
export VIDEO_MINIO_ACCESS_KEY="minioadmin"
export VIDEO_MINIO_SECRET_KEY="minioadmin"
export VIDEO_MINIO_BUCKET="video-results"
export RABBITMQ_URL="amqp://video:video@localhost:5672/"
# Read by cmd/notification-api as well as cmd/notifier — the destination
# policy is one variable with two readers, and a local receiver is always an
# http:// or private address. Without it the Notification API refuses every
# destination at registration, so the notifier never gets one to deliver.
# NEVER set it in production.
export NOTIFICATION_ALLOW_INSECURE_DESTINATIONS="true"
# VIDEO_MINIO_USE_SSL, VIDEO_MINIO_PUBLIC_ENDPOINT, and
# VIDEO_MINIO_PUBLIC_USE_SSL are optional and correct unset for this setup:
# the browser reaches MinIO at the same localhost:9000 the server does, so the
# presigned URLs GET /download/:filename issues are already followable. That
# is not true inside Docker Compose, where the server uses minio:9000 and the
# compose file sets VIDEO_MINIO_PUBLIC_ENDPOINT to the published port instead.

# Start the Video API (listens on :8080) — the frontend and the upload flow
go run ./cmd/video-api

# In a second shell, the Identity API. It also listens on :8080, so give it
# its own port or run it on another host; without it nothing can obtain a
# token. It reads the IDENTITY_* exports and nothing else.
go run ./cmd/identity-api

# In a third shell, the Notification API — the preference routes. Also :8080.
# It reads IDENTITY_JWT_PUBLIC_KEYS, NOTIFICATION_POSTGRES_DSN and REDIS_ADDR.
go run ./cmd/notification-api

# In a fourth shell, with the same exports minus the IDENTITY_* and
# NOTIFICATION_* ones, start the worker. It serves no HTTP and exposes no port.
go run ./cmd/worker

# In a fifth shell, start the notifier. It needs only three of the exports
# above — NOTIFICATION_POSTGRES_DSN, RABBITMQ_URL, and the destination
# relaxation already exported with them. It serves no HTTP and exposes no
# port.
go run ./cmd/notifier

# Build binaries
go build -o identity-api ./cmd/identity-api
go build -o video-api ./cmd/video-api
go build -o notification-api ./cmd/notification-api
go build -o worker ./cmd/worker
go build -o notifier ./cmd/notifier
```

`cmd/worker` reads a deliberately smaller configuration surface: `RABBITMQ_URL`, `VIDEO_POSTGRES_DSN`, `REDIS_ADDR`, and the four required `VIDEO_MINIO_*` variables. It reads **no** `IDENTITY_*` and **no** `NOTIFICATION_*` variables — it makes no access-control decision and resolves no delivery preference, so exporting them anyway is harmless.

`cmd/notifier` reads the smallest surface of the five: `RABBITMQ_URL` and `NOTIFICATION_POSTGRES_DSN` required, and optionally `NOTIFICATION_ALLOW_INSECURE_DESTINATIONS`, `NOTIFICATION_WEBHOOK_MAX_ATTEMPTS`, `NOTIFICATION_WEBHOOK_TIMEOUT_SECONDS`, and `NOTIFICATION_DELIVERY_RECLAIM_SECONDS`. It reads **no** `IDENTITY_*`, **no** `VIDEO_*` (MinIO included), and **no** `REDIS_ADDR`. Two local-development notes: without `NOTIFICATION_ALLOW_INSECURE_DESTINATIONS=true` no `http` or private-address destination can be registered *or* dialled, which is every destination a local receiver could have — and `cmd/notification-api` needs that same variable, since the policy is applied at registration too. The last three are validated against one another at startup: a reclaim bound below twice the attempt budget is a fatal configuration error naming both values, not a warning. See [docs/operations.md](operations.md) for the arithmetic. `VIDEO_MINIO_PUBLIC_ENDPOINT`/`_USE_SSL` are a different case: the worker never presigns, but `setupWorker` goes through the same MinIO loader and builds the presign client anyway, so `ResultStorage` is fully constructed rather than holding a nil that would panic the day something calls the other half of its interface. They are therefore *read* by the worker even though nothing signs with them, and a malformed value can fail worker startup. Leaving them unset is the normal case — each falls back to its internal counterpart.

`cmd/worker` creates `temp/` in its working directory at startup and exits if it cannot. **No other process creates a directory at all**: extraction lives in the worker, so nothing else touches the filesystem. Neither uploaded source videos nor processed ZIP results are written to disk — both go to the MinIO bucket named by `VIDEO_MINIO_BUCKET`, which `cmd/video-api` and `cmd/worker` both require at startup and the Video API creates if absent. `temp/` holds per-job scratch only: the source copy downloaded for `ffmpeg`, the extracted frames, and the zip built from them, all removed before the job finishes. Running several processes from the same working directory is fine — only the worker uses it.

To skip the manual wiring entirely, use `docker compose up --build`, which starts all five services plus the gateway inside Docker with everything already configured — see "Docker Workflow" below.

## Running Tests

Tests are integration tests that drive the real Gin handlers via `httptest.NewServer`. They execute real `ffmpeg` commands, write real files, and store real objects. `ffmpeg` must be on `PATH`, the `VIDEO_MINIO_*` variables must point at a reachable MinIO, and `RABBITMQ_URL` must be **set** — `cmd/video-api`'s `TestMain` requires the variable because `setupVideo` does, but deliberately does not require a live broker, because `cmd/video-api` does not either. That `TestMain` is the only one of the five that gates on anything: the Identity and Notification suites drive their routers over in-memory repositories and would be refusing to run a suite that needs nothing.

`cmd/worker`'s own suite is the one place a **reachable** broker changes whether real coverage runs rather than only how much: its end-to-end dispatch tests need `RABBITMQ_TEST_URL` alongside PostgreSQL, Redis, and MinIO, and skip cleanly without it, exactly like the messaging package's. Run it through Docker (below) or CI to exercise them.

A *reachable* broker is a prerequisite for **full coverage**, not for a passing run: `internal/platform/rabbitmq`'s and `internal/video/infrastructure/messaging`'s tests skip with a clear message when `RABBITMQ_TEST_URL` is unset, the same way the Redis and MinIO adapter suites do. A local run without a broker passes while exercising none of those two packages — which between them cover the publisher, the relay, and its reconnect and shutdown paths — so exercise them through the Docker command below (or CI, which always provides one) before trusting a green result for a change that touches them.

```bash
go test ./... -v
```

If either prerequisite is missing, the suite exits immediately with code 1 rather than skipping — a skipped result-storage suite would report green while covering none of `POST /upload`'s actual behavior:

```
FATAL: ffmpeg not found in PATH — integration tests require ffmpeg; see CLAUDE.md for the Docker fallback.
FATAL: video: VIDEO_MINIO_ENDPOINT environment variable is required — integration tests require MinIO; see CLAUDE.md for the Docker fallback.
```

### Running the full suite via Docker, including PostgreSQL-backed tests

```bash
docker compose run --build --rm app-test go test ./... -v
```

`app-test` builds from the `Dockerfile`'s `test` stage (Go toolchain + `ffmpeg`) and runs `go test` inside it, against the compose-provided PostgreSQL, Redis, and MinIO — no local Go, ffmpeg, or MinIO install required. With result storage now in MinIO, this is the path of least resistance for anyone not already running one. It's a separate service from `app` because `app`'s image (the hardened, deployed build) deliberately has no Go toolchain; see "Docker Workflow" below. `app-test` is gated behind Compose's `test` profile so it never starts as part of a plain `docker compose up`/`up --build` — `docker compose run` targets it explicitly regardless, so the command above needs no extra flag.

The three PostgreSQL adapter suites — `internal/identity/infrastructure/postgres`, `internal/video/infrastructure/postgres`, and `internal/notification/infrastructure/postgres` — otherwise skip (not fail) when their own `IDENTITY_POSTGRES_TEST_DSN` / `VIDEO_POSTGRES_TEST_DSN` / `NOTIFICATION_POSTGRES_TEST_DSN` is unset. All three run automatically here: `docker-compose.yml`'s `postgres` service creates an isolated test database per bounded context on first init — `identity_test`, `video_test`, `notification_test` (see `docker/postgres-init/create-context-databases.sql`) — and each variable is already pointed at its own one, no manual export needed.

Notification's is the one most worth reaching for deliberately, because the rule it covers has no in-memory equivalent: "creating a preference without a signing secret is refused" is decided by whether the adapter's `UPDATE … RETURNING` affected a row, so a skipped run exercises none of it while still reporting green. `NOTIFICATION_POSTGRES_DSN` — the *runtime* variable — is a separate thing and is **not** needed to run the suite: no test in `cmd/notification-api` calls `setupNotification` (nor does any in `cmd/identity-api` call `setupIdentity`, or any in `cmd/video-api` call `setupVideo`); every one builds its modules by hand, which is why no `TestMain`'s startup gate names any of the three DSNs.

Each test database is separate from the runtime database its context uses, so this is safe to run even while `docker compose up --build` is serving real registered users — a `TRUNCATE` in the identity suite touches `identity_test` and no other database, and the same holds for the other two. The separation is per context rather than global for the same reason: a Video adapter test truncating `video_jobs` has no business reaching Notification's rows either.

> **If you already have a `postgres_data` volume from before the per-context split:** the init script only runs against a fresh, empty PostgreSQL data directory. An existing volume holds `identity` and `identity_test` alone, so the API and the notifier fail to start and the adapter suites fail rather than run. Create the four missing databases against the running container instead:
>
> ```bash
> docker compose up -d postgres
> for db in video video_test notification notification_test; do
>   docker compose exec -T postgres \
>     psql -U identity -d identity -c "CREATE DATABASE $db"
> done
> ```
>
> Not `docker compose down -v` — that would drop `minio_data` and `rabbitmq_data` along with the Postgres volume, taking every stored result and every queued message with them. The statements above leave existing data alone. A database that already exists makes its own statement print `ERROR: database "video" already exists` and exit non-zero without changing anything, so run the loop under a shell without `set -e` (the default, and what the snippet above assumes) and ignore those errors.

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
# Once per machine: generate the RSA key pair the stack signs and verifies
# access tokens with, into a git-ignored .env that Compose reads. No key
# material lives in the repository, so `docker compose up` fails with this
# instruction until it has been run. `make dev-keys FORCE=1` replaces it.
make dev-keys

docker compose up --build
# Access the UI by opening http://127.0.0.1:8080 in a browser. That port
# belongs to the `gateway` service, which is the only one that publishes a
# host port; it routes /api/auth/ to identity-api, /api/notification-
# preferences to notification-api, and everything else to video-api, so the
# split is invisible from the browser. Identity, video, notification, Redis,
# MinIO, and RabbitMQ are already configured, and `worker` and `notifier`
# are started from the same image, so uploads are actually processed and
# finished jobs are actually announced.
#
# Three workers start by default (docker-compose.yml's `deploy.replicas`),
# so this stack processes several videos at the same time. Prefetch is 1, so
# a worker holds exactly one job at a time and concurrent processing is
# worker count — this is the only knob. Nothing else changes: the workers
# compete for one queue, each claim is an atomic conditional UPDATE, and a
# lost claim is rejected rather than run twice.

docker compose up --build --scale worker=1
# The same stack with a different number of workers. `--scale` overrides the
# default in both directions, so this is how you get a single worker (one log
# stream, a serial trace) and `--scale worker=5` is how you go higher.
# `--scale notifier=N` works the same way; deliveries are claimed per
# (user, event, channel, job) so two notifiers do not double-send.
```

`docker-compose.yml` is the sole documented way to build and run the application via Docker **for local development** (see "Running the full suite via Docker" above for the equivalent test command). It builds from the same `Dockerfile` used for deployment — see [docs/operations.md](operations.md) for the deployment-focused Docker commands, which are a separate concern from this local dev workflow.

> The `Dockerfile` is a multi-stage build: a `builder` stage compiles a static binary (dependencies resolved read-only from the committed `go.sum` — the build fails rather than silently patching it), a `test` stage adds `ffmpeg` on top of `builder` for running the suite (see `app-test` above), and the default `runtime` stage — the one `app`, `worker`, `notifier`, and deployment all use — ships **all three** compiled binaries (`/app/app`, `/app/worker`, `/app/notifier`) plus `ffmpeg`, no Go toolchain or source tree, running as a non-root user (fixed UID 1000). `ffmpeg` is there for the worker rather than for the API or the notifier. The `worker` and `notifier` services are the same image with their command overridden to `/app/worker` and `/app/notifier`. The `notifier` service also carries `stop_grace_period: 90s`, longer than its shutdown drain — Compose's 10-second default would `SIGKILL` a delivery still in flight and leave a claim unresolved on every `docker compose stop`.
>
> **Bind-mount permissions:** there is no longer a bind-mounted working directory to get wrong. `./uploads` and `./outputs` were both removed once their artifacts moved into MinIO, so the non-root user (UID 1000) writes only to `temp/` inside the container, which the image creates and owns. If you still have a root-owned `uploads/` or `outputs/` in your clone from an older checkout, it is inert — delete it.

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

### Optional OpenSpec Workflow

OpenSpec is an optional, explicitly selected methodology. A direct implementation request does not activate it, regardless of change size or complexity. The OpenSpec lifecycle begins only when a developer asks to use OpenSpec, invokes an `/opsx:*` command, or explicitly continues a named active OpenSpec change.

Once selected, the full lifecycle applies:

1. **Explore (when warranted):** `/opsx:explore` for complex or ambiguous opted-in changes.
2. **Propose:** `/opsx:propose` creates the change artifacts.
3. **Implement:** after the proposal PR merges, `/opsx:apply` works through `tasks.md`.
4. **Archive:** after implementation merges and strict validation passes, `/opsx:archive` promotes the specs and archives the change.

The `change-lifecycle` skill encodes this optional sequence. It does not activate for task lookup, backlog lookup, direct implementation, or perceived complexity. The `repo-workflow` skill is separate and applies to every requested or agent-created PR without selecting OpenSpec.

### OpenSpec PR Separation Rule

Only changes that explicitly opt into OpenSpec use three PR roles:

1. **Propose PR** — only the new `openspec/changes/<name>/` artifacts; it must merge before implementation begins.
2. **Implementation PR** — only files in the proposal's declared implementation scope; no task checkoffs, permanent documentation, agent instructions, or files under `openspec/`.
3. **Finalization PR** — after implementation merges, bundles task checkoffs, promoted canonical specs, archive movement, required permanent documentation/agent instructions, and an applicable roadmap update. It contains no application source or tests.

Direct work and its PRs are not assigned OpenSpec roles and do not require proposal or finalization PRs. Green CI never authorizes a merge; each PR still requires explicit authorization for that specific PR in the current session.

### Branch Protection

`main` is protected. All changes land via a feature branch and pull request. Required status checks: `Build & Test`, `SAST (gosec)`, `Vulnerability Scan (govulncheck)`. All review conversations must also be resolved before merge, including inline threads opened by GitHub Copilot. A PR is not mergeable until all three checks pass and no review thread remains unresolved, but its branch does not need to be up to date with `main`. This protection is enforced for administrators too.

```bash
git fetch origin
git checkout -b feat/short-description origin/main
git push -u origin feat/short-description
gh pr create --fill
```

Branch from freshly-fetched `origin/main` rather than from whatever is currently checked out. Branching from stale or unrelated work can carry unrelated commits into the new PR's diff.

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
