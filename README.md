# FIAP X — Video Frame Processor

A Go service that accepts a video upload, extracts frames at 1 fps via `ffmpeg`, packages them into a ZIP, and hands the client a time-limited URL to download it from object storage. Processing is asynchronous across three processes: an HTTP API (`cmd/api`) accepts and reports, a worker (`cmd/worker`) does the extraction off a RabbitMQ queue, and a notifier (`cmd/notifier`) announces each outcome to whatever webhook its owner registered. Built as the code deliverable for a POSTECH/FIAP hackathon.

## Prerequisites

| Dependency | Version | Notes |
|---|---|---|
| Go | 1.27+ | Required; see `go.mod` |
| ffmpeg | any recent | Must be on `PATH`; `cmd/worker` shells out to it |
| Docker | any recent | Optional for local dev; required if Go/ffmpeg are not installed |

## Quickstart

The API requires identity, video, notification, Redis, MinIO, and broker configuration (`IDENTITY_POSTGRES_DSN`, `IDENTITY_JWT_SIGNING_KEY`, `VIDEO_POSTGRES_DSN`, `NOTIFICATION_POSTGRES_DSN`, `REDIS_ADDR`, `VIDEO_MINIO_ENDPOINT`/`_ACCESS_KEY`/`_SECRET_KEY`/`_BUCKET`, `RABBITMQ_URL`) to start — `RABBITMQ_URL` only has to be *set*, since no process dials the broker from a request path. **The worker is a second process** (`go run ./cmd/worker`) with a smaller surface: the same variables minus the `IDENTITY_*` and `NOTIFICATION_*` ones. Without it, uploads are accepted and never processed. **The notifier is a third** (`go run ./cmd/notifier`), with the narrowest surface of the three — `NOTIFICATION_POSTGRES_DSN` and `RABBITMQ_URL`. Without it, jobs still finish and nothing is announced. See [docs/development.md](docs/development.md) for running all three directly. The fastest path with no manual wiring is Docker:

```bash
# 1. Clone and enter the repo
git clone https://github.com/kakadlec/video-processor.git
cd video-processor

# 2. Generate the local token key pair (once per machine, into a
#    git-ignored .env — no key material is kept in the repository)
make dev-keys

# 3. Run the full stack (app + three workers + notifier + PostgreSQL +
#    Redis + MinIO + RabbitMQ, all already configured)
docker compose up --build
# Server starts on http://127.0.0.1:8080, with PostgreSQL-backed identity
# already wired in — /api/auth/register and /api/auth/login are live.
# The `worker` and `notifier` services run from the same image with their
# commands overridden. Three workers start by default, so several videos
# are processed at the same time: each worker holds exactly one job at a
# time by design (prefetch 1), so concurrency is worker count.

# 2b. ALTERNATIVE to step 2 (stop it first, or run this instead): to pick a
#     different number of workers — including one, for a single log stream
#     or a serial trace:
docker compose up --build --scale worker=1

# 4. Open http://127.0.0.1:8080 in your browser
# Register/log in, then upload a video file. The upload returns immediately
# and the page polls the job's status until it completes, then shows a
# Download button: clicking it asks the API for a 5-minute URL and the
# browser fetches the ZIP from MinIO directly.
```

`docker-compose.yml` is the only supported way to run the application via Docker **for local development** — there is no separate plain `docker build`/`docker run` workflow documented for that purpose. (Container deployment is a different concern; see [docs/operations.md](docs/operations.md).) See [docs/development.md](docs/development.md) for running the test suite the same way.

## Current Limitations

Processing is asynchronous as of Phase 6, but the system is not yet complete:

- **A worker must be running for anything to be processed.** `POST /upload` answers `202` whether or not one is; with the API alone, jobs sit in `queued` indefinitely. Concurrency is worker count: each worker holds exactly one job at a time by design, so processing several videos at once means running several worker processes. The default stack starts **three**, so that is what `docker compose up` already does; `--scale worker=<n>` picks a different number in either direction, and running the binary directly means more `go run ./cmd/worker` shells.
- **Frame extraction still needs local scratch** — `ffmpeg` reads and writes files, so the worker downloads each source into its own `temp/`, extracts frames there, and builds the zip there, removing all of it before the job finishes. Nothing durable lives on local disk (Phase 5): uploaded source videos go to MinIO too, as **transient** objects whose owner deletes them — the processed ZIP is the one durable artifact, so a result survives its container and any instance can serve it.
- **A source object can leak.** A job never dispatched, a dispatch dead-lettered before any claim, or a worker interrupted after a terminal commit but before best-effort cleanup can leave its source in the bucket. Mid-extraction crashes are recovered by the worker sweeper. Configure the `uploads/`-prefix expiration lifecycle rule; it remains the only exhaustive guarantee. See [docs/operations.md](docs/operations.md).
- **Webhooks are the only notification channel.** A user who registers a webhook preference through `PUT /api/notification-preferences` is notified when a job completes or fails — `cmd/notifier` consumes the terminal-event queue and delivers a signed request per subscribed outcome. Email is not implemented (Phase 7's `add-notification-email-delivery`), and `channel: "email"` is refused rather than stored. A user who registers nothing still has the page (which polls the job's status URL) and `GET /api/status`; absence of a preference means *not subscribed*, and there is no implicit default.
- **A notifier must be running for anything to be delivered.** Same shape as the worker above: jobs still finish, and `video.jobs.terminal.events.v1` accumulates until a notifier reads it.
- **Crash recovery is bounded, not immediate.** A worker renews an epoch-scoped Redis lease while extracting. After the lease expires, the sweeper requires two successful missing-lease observations before requeueing; after three recoveries, it fails the job rather than loop forever. Redis outages delay takeover instead of authorizing it.

These limitations are addressed in the [architecture roadmap](docs/roadmap.md).

## Documentation

| Document | Contents |
|---|---|
| [docs/architecture.md](docs/architecture.md) | Current implementation, target DDD structure, roadmap summary |
| [docs/domain-model.md](docs/domain-model.md) | Bounded contexts, `VideoJob` aggregate, state machine, domain events |
| [docs/flows.md](docs/flows.md) | The asynchronous upload/poll/download flow, the worker's own sequence, frontend interaction sequences |
| [docs/development.md](docs/development.md) | Local setup, test execution, Docker workflow, contribution conventions |
| [docs/operations.md](docs/operations.md) | Deployment, runtime directories, environment variables, planned infrastructure |
| [docs/roadmap.md](docs/roadmap.md) | 8-phase evolution roadmap (summary) |

For the full project requirements see [docs/project-requirements.pdf](docs/project-requirements.pdf).

## Database Schema and Infrastructure Resources

Every resource this system needs is created by the processes themselves at startup — there is no runbook step to forget and no ordering between the three binaries to get right. The one exception is the PostgreSQL databases the DSNs name, which have to exist before a process can migrate into one. `docker compose up` creates them on the Postgres volume's **first** initialization; a volume that predates the per-context split keeps whatever it already had, and [docs/development.md](docs/development.md) names the statements that add the rest.

| Resource | Database | DDL / declaration | Applied by |
|---|---|---|---|
| `identity_users` | `identity` | [`internal/identity/infrastructure/postgres/schema.sql`](internal/identity/infrastructure/postgres/schema.sql) | `cmd/api` (`setupIdentity`) |
| `video_jobs`, `video_job_outbox` | `video` | [`internal/video/infrastructure/postgres/schema.sql`](internal/video/infrastructure/postgres/schema.sql) | `cmd/api` (`setupVideo`), `cmd/worker` |
| `notification_preferences`, `notification_deliveries` | `notification` | [`internal/notification/infrastructure/postgres/schema.sql`](internal/notification/infrastructure/postgres/schema.sql) | `cmd/api` (`setupNotification`), `cmd/notifier` |
| MinIO bucket (`VIDEO_MINIO_BUCKET`) | — | `storage.EnsureBucket` | `cmd/api`, `cmd/worker` |
| RabbitMQ exchanges, queues, bindings, DLQs | — | `messaging.JobDispatchTopology()`, `TerminalEventsTopology()` | Every producer and consumer, redeclared on **every** dial |

The three `schema.sql` files are plain DDL, embedded with `go:embed` and applied idempotently (`CREATE TABLE IF NOT EXISTS`) by each context's `Migrate` — so they can also be run by hand against a database (`psql -f …`) if you want the schema without starting the application. Each bounded context owns its own pool, its own tables, and its own **database**; pointing all three DSNs at one server, as `docker-compose.yml` does, is a deployment choice, and the databases named above are what keeps the boundary enforced by the engine — PostgreSQL has no cross-database query without an extension, so a query reaching from one context into another's tables fails as an unknown relation. The databases themselves are the one thing the processes do *not* create: `docker/postgres-init/create-context-databases.sql` creates them (plus a test counterpart each) on the Compose volume's first init, and is unrelated to the runtime schema — it creates databases, never tables.

## API

| Method | Path | Description |
|---|---|---|
| `GET` | `/` | Web upload UI (inline HTML/CSS/JS); always public |
| `POST` | `/api/auth/register` | Create a user account |
| `POST` | `/api/auth/login` | Authenticate and receive a bearer access token |
| `POST` | `/upload` | Upload a video file (multipart `video` field); returns `202 {"job_id", "status", "status_url"}` — the work is accepted, not done; requires `Authorization: Bearer <token>` |
| `GET` | `/api/video-jobs/:id` | Poll a job's status (`queued` → `processing` → `completed`/`failed`); this is what `status_url` names. Owner-only |
| `GET` | `/download/:filename` | Issue a 5-minute presigned URL for a processed ZIP: `200 {"url", "expires_at"}`, not the archive itself. Owner-only; follow the returned URL (no `Authorization` header) to fetch the bytes from MinIO |
| `GET` | `/api/status` | List processed ZIPs with metadata; scoped to the caller's own uploads |
| `GET` | `/api/notification-preferences` | List the caller's own delivery preferences; `has_secret` instead of the signing secret, which no route ever returns. An empty set is `200` with an empty array |
| `PUT` | `/api/notification-preferences` | Register or update one preference, named by `event_type` + `channel` in the body. `secret` is required to create one and optional to update one. Owner-only; a `user_id` in the body is ignored. `cmd/notifier` resolves each terminal event against these; a destination it could never dial is refused here rather than stored |

## Tech Stack

- **Language:** Go 1.27
- **HTTP framework:** [Gin](https://github.com/gin-gonic/gin) v1.12
- **Frame extraction:** `ffmpeg` (via `exec.CommandContext`, in `cmd/worker`)
- **Identity, job, and notification-preference persistence:** PostgreSQL (via `pgx`), including a transactional outbox
- **Job dispatch and terminal events:** RabbitMQ (via [`amqp091-go`](https://github.com/rabbitmq/amqp091-go)) — dispatch outbox relay in `cmd/api`, consumer in `cmd/worker`, a second outbox relay in `cmd/worker` publishing each job's `completed`/`failed` outcome, and the consumer for that terminal stream in `cmd/notifier`
- **Webhook delivery:** HMAC-SHA256 signatures over `<timestamp>.<body>`, per-delivery claim records in PostgreSQL, and a destination policy applied both at registration and at dial time (`cmd/notifier`)
- **Object storage:** MinIO / S3-compatible (via [`minio-go`](https://github.com/minio/minio-go)) for source videos and ZIP results
- **Idempotency, rate limiting, status cache, worker leases:** Redis (via [`go-redis`](https://github.com/redis/go-redis))
- **Password hashing:** bcrypt
- **Access tokens:** JWT ([`golang-jwt/jwt`](https://github.com/golang-jwt/jwt))
- **CI:** GitHub Actions — `go vet`, `go test`, [gosec](https://github.com/securego/gosec), [govulncheck](https://go.dev/security/vuln)
