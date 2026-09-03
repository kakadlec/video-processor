# FIAP X — Video Frame Processor

A Go service that accepts a video upload, extracts frames at 1 fps via `ffmpeg`, packages them into a ZIP, and hands the client a time-limited URL to download it from object storage. Processing is asynchronous: an HTTP API (`cmd/api`) accepts and reports, and a worker (`cmd/worker`) does the extraction off a RabbitMQ queue. Built as the code deliverable for a POSTECH/FIAP hackathon.

## Prerequisites

| Dependency | Version | Notes |
|---|---|---|
| Go | 1.27+ | Required; see `go.mod` |
| ffmpeg | any recent | Must be on `PATH`; `cmd/worker` shells out to it |
| Docker | any recent | Optional for local dev; required if Go/ffmpeg are not installed |

## Quickstart

The API requires identity, video, Redis, MinIO, and broker configuration (`IDENTITY_POSTGRES_DSN`, `IDENTITY_JWT_SIGNING_KEY`, `VIDEO_POSTGRES_DSN`, `REDIS_ADDR`, `VIDEO_MINIO_ENDPOINT`/`_ACCESS_KEY`/`_SECRET_KEY`/`_BUCKET`, `RABBITMQ_URL`) to start — `RABBITMQ_URL` only has to be *set*, since neither process dials the broker from a request path. **The worker is a second process** (`go run ./cmd/worker`) with a smaller surface: the same variables minus the `IDENTITY_*` ones. Without it, uploads are accepted and never processed. See [docs/development.md](docs/development.md) for running both directly. The fastest path with no manual wiring is Docker:

```bash
# 1. Clone and enter the repo
git clone https://github.com/kakadlec/video-processor.git
cd video-processor

# 2. Run the full stack (app + worker + PostgreSQL + Redis + MinIO +
#    RabbitMQ, all already configured)
docker compose up --build
# Server starts on http://127.0.0.1:8080, with PostgreSQL-backed identity
# already wired in — /api/auth/register and /api/auth/login are live.
# The `worker` service runs from the same image with its command overridden.

# 3. Open http://127.0.0.1:8080 in your browser
# Register/log in, then upload a video file. The upload returns immediately
# and the page polls the job's status until it completes, then shows a
# Download button: clicking it asks the API for a 5-minute URL and the
# browser fetches the ZIP from MinIO directly.
```

`docker-compose.yml` is the only supported way to run the application via Docker **for local development** — there is no separate plain `docker build`/`docker run` workflow documented for that purpose. (Container deployment is a different concern; see [docs/operations.md](docs/operations.md).) See [docs/development.md](docs/development.md) for running the test suite the same way.

## Current Limitations

Processing is asynchronous as of Phase 6, but the system is not yet complete:

- **A worker must be running for anything to be processed.** `POST /upload` answers `202` whether or not one is; with the API alone, jobs sit in `queued` indefinitely. Scale by running more worker processes — each holds exactly one job at a time by design.
- **Frame extraction still needs local scratch** — `ffmpeg` reads and writes files, so the worker downloads each source into its own `temp/`, extracts frames there, and builds the zip there, removing all of it before the job finishes. Nothing durable lives on local disk (Phase 5): uploaded source videos go to MinIO too, as **transient** objects whose owner deletes them — the processed ZIP is the one durable artifact, so a result survives its container and any instance can serve it.
- **A source object can leak.** A job never dispatched, a dispatch dead-lettered before any claim, or a worker interrupted after a terminal commit but before best-effort cleanup can leave its source in the bucket. Mid-extraction crashes are recovered by the worker sweeper. Configure the `uploads/`-prefix expiration lifecycle rule; it remains the only exhaustive guarantee. See [docs/operations.md](docs/operations.md).
- **No notifications** — users must stay on the page (which polls the job's status URL) or poll `GET /api/status` to find out when processing completes. Phase 7.
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

## Tech Stack

- **Language:** Go 1.27
- **HTTP framework:** [Gin](https://github.com/gin-gonic/gin) v1.12
- **Frame extraction:** `ffmpeg` (via `exec.CommandContext`, in `cmd/worker`)
- **Identity and job persistence:** PostgreSQL (via `pgx`), including a transactional outbox
- **Job dispatch:** RabbitMQ (via [`amqp091-go`](https://github.com/rabbitmq/amqp091-go)) — outbox relay in `cmd/api`, consumer in `cmd/worker`
- **Object storage:** MinIO / S3-compatible (via [`minio-go`](https://github.com/minio/minio-go)) for source videos and ZIP results
- **Idempotency, rate limiting, status cache, worker leases:** Redis (via [`go-redis`](https://github.com/redis/go-redis))
- **Password hashing:** bcrypt
- **Access tokens:** JWT ([`golang-jwt/jwt`](https://github.com/golang-jwt/jwt))
- **CI:** GitHub Actions — `go vet`, `go test`, [gosec](https://github.com/securego/gosec), [govulncheck](https://go.dev/security/vuln)
