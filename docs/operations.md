# Operations

## Current Deployment

The application is a single Go binary (or `go run ./cmd/api`) behind a Docker container. There is no orchestration. External services are a required PostgreSQL instance, used by both the identity module (Phase 2) and, as of Phase 3's `wire-videojob-http-endpoints`, the video job module, and (as of Phase 4's `add-upload-idempotency-keys`) a required Redis instance backing `POST /upload`'s idempotency keys — everything else still runs with no environment-specific configuration beyond the port.

### Docker

```bash
# Build
docker build -t video-processor .

# Run (identity, video, and Redis configuration are all required — see Environment Variables below)
docker run -p 8080:8080 \
  -e IDENTITY_POSTGRES_DSN="postgres://user:pass@host:5432/identity?sslmode=disable" \
  -e IDENTITY_JWT_SIGNING_KEY="change-me" \
  -e VIDEO_POSTGRES_DSN="postgres://user:pass@host:5432/identity?sslmode=disable" \
  -e REDIS_ADDR="host:6379" \
  video-processor
```

The Dockerfile is a multi-stage build. The default (final) stage — used by the command above — compiles a static binary in a `golang:1.26-alpine` builder stage (dependencies resolved read-only from the committed `go.sum`), then ships only that binary and `ffmpeg` in a minimal `alpine` runtime stage with no Go toolchain or source tree, running as a fixed non-root user (UID 1000). See [docs/development.md](development.md) for the additional `test` stage used to run the suite via Docker.

### Environment Variables

| Variable | Default | Description |
|---|---|---|
| `PORT` | `8080` | Listening port (hardcoded in `cmd/api/main.go` as `:8080`; no env var read currently — listed here for future use) |
| `GIN_MODE` | `debug` | Set to `release` to suppress Gin debug output |
| `IDENTITY_POSTGRES_DSN` | unset | PostgreSQL connection string for the Identity module (e.g. `postgres://user:pass@host:5432/identity?sslmode=disable`). Required at startup. |
| `IDENTITY_JWT_SIGNING_KEY` | unset | Symmetric key used to sign/verify access tokens (HMAC-SHA256). Required at startup. There is no default signing key — startup fails clearly rather than falling back to one. |
| `VIDEO_POSTGRES_DSN` | unset | PostgreSQL connection string for the Video Processing module's `VideoJob` repository (e.g. `postgres://user:pass@host:5432/identity?sslmode=disable` — same instance/database as `IDENTITY_POSTGRES_DSN` by design, not a separate one). Required at startup as of Phase 3's `wire-videojob-http-endpoints`, which wires `cmd/api/video.go`'s `setupVideo` into `main()`. |
| `REDIS_ADDR` | unset | Address (`host:port`) of the Redis instance backing `POST /upload`'s idempotency keys (e.g. `redis:6379`). Required at startup as of Phase 4's `add-upload-idempotency-keys`, which wires `internal/platform/redis` and `internal/video/infrastructure/idempotency.RedisStore` into `setupVideo`. |

`IDENTITY_POSTGRES_DSN`, `IDENTITY_JWT_SIGNING_KEY`, `VIDEO_POSTGRES_DSN`, and `REDIS_ADDR` are all required to be *set*: the process exits at startup with a clear configuration error if any is empty, rather than running with unsafe defaults or an unauthenticated fallback (see [openspec/specs/identity-authentication/spec.md](../openspec/specs/identity-authentication/spec.md), [openspec/specs/videojob-http-api/spec.md](../openspec/specs/videojob-http-api/spec.md), and [openspec/specs/upload-idempotency/spec.md](../openspec/specs/upload-idempotency/spec.md)). Startup validation depth differs by dependency, though: both PostgreSQL DSNs are also *connectivity*-checked at startup (`db.PingContext`), so an unreachable or malformed database fails fast. `REDIS_ADDR` is not — `platformredis.Open` only constructs the client, and a malformed address or unreachable Redis surfaces later, at the first `POST /upload` request that needs it, not at startup.

## Runtime Directory Structure

The application creates and uses three directories relative to its working directory:

```
./
  uploads/    Transient input: uploaded video files
  temp/       Per-request scratch: extracted PNG frames
  outputs/    Durable results: processed ZIP files
```

| Directory | Created by | Contents | Cleaned by |
|---|---|---|---|
| `uploads/` | `createDirs()` at startup | Uploaded video (named `<timestamp>_<original>`) | Deleted on successful processing |
| `temp/` | Per request in `processVideo` | PNG frames from `ffmpeg` | Always deleted by `defer os.RemoveAll` |
| `outputs/` | `createDirs()` at startup | `frames_<timestamp>.zip` files | Never (manual cleanup required) |

> **Known gap:** If processing fails, the file in `uploads/` is NOT deleted. This is documented behavior in `main_test.go` (`TestProcessing_Failure_LeavesUploadedFileBehind`).

The `outputs/` directory accumulates ZIPs indefinitely. There is no expiry, no cleanup job, and no size limit — disk space must be monitored manually in the current deployment.

## CI / CD

Three required checks run on every push and pull request:

| Check | Tool | What it does |
|---|---|---|
| `Build & Test` | `go vet` + `go test ./... -v` | Compiles the application and runs integration tests (with `ffmpeg` installed on the runner) |
| `SAST (gosec)` | [`gosec`](https://github.com/securego/gosec) | Static security analysis; fails the build on any finding |
| `Vulnerability Scan (govulncheck)` | [`govulncheck`](https://go.dev/security/vuln) | Fails only when a known vulnerability is reachable from code actually called by this project |

Releases are automated via `release-please`. On every push to `main`, it maintains a "Release PR" showing the next version computed from Conventional Commits. Merging that PR creates the git tag, publishes a GitHub Release, and updates `CHANGELOG.md`.

---

## Implemented Infrastructure

### PostgreSQL — Implemented (Phase 2 for identity, Phase 3 for video), required

Authoritative state store for users (`User` aggregate) and `VideoJob`s, configured via `IDENTITY_POSTGRES_DSN` and `VIDEO_POSTGRES_DSN` respectively — by design the same PostgreSQL instance and database, not two separate ones. Schema/migrations for both are applied automatically at startup (`postgres.Migrate`). The video processing schema (`video_jobs` and the transactional-outbox `video_job_outbox` table) was added by Phase 3's `add-videojob-infrastructure`; `cmd/api/video.go`'s `setupVideo` (added by `wire-videojob-http-endpoints`) is what actually instantiates and migrates it at startup — `VIDEO_POSTGRES_DSN` is required exactly like `IDENTITY_POSTGRES_DSN`.

- **Local/CI service:** `docker-compose.yml` at the repo root starts a matching `postgres:16-alpine` instance (`docker compose up -d postgres`) for running identity-dependent tests locally; CI provisions the same image as a service container. See [docs/development.md](development.md).
- **Local/CI credentials** (`identity`/`identity`) are fixed, non-secret defaults — never used outside a developer's machine or CI.

### Redis — Connection adapter and idempotency keys implemented (Phase 4), rate limiting/status cache Planned

`internal/platform/redis` (`add-redis-infrastructure`) provides connection plumbing — `Config`/`LoadConfigFromEnv`, `Open`, `Ping`, `Close`. It is wired into `cmd/api`'s `setupVideo` as of `add-upload-idempotency-keys`, which requires `REDIS_ADDR` at startup like the PostgreSQL DSNs above. Of Redis's three planned feature responsibilities (all additive to PostgreSQL, not a replacement), one is now implemented and two remain planned:

1. **Idempotency keys** — **Implemented.** `internal/video/infrastructure/idempotency.RedisStore` deduplicates `POST /upload` requests by content hash + `UserID`: a `Reserve`/`Finalize`/`Clear`/`Lookup` protocol backs the "prevent duplicate job creation from client retries" goal. See [docs/architecture.md](architecture.md)'s Request pipeline section and `openspec/specs/upload-idempotency/spec.md`.
2. **Rate limiting** — Planned. Sliding-window counter per user at the API layer.
3. **Status cache** — Planned. Short-TTL cache for `GET /jobs/{id}/status` to reduce PostgreSQL read pressure.

---

## Planned Infrastructure (Not Yet Implemented)

> The components below are planned for future phases and do not exist in the current deployment. Each is labeled with the phase that introduces it.

### MinIO — Planned (Phase 5)

S3-compatible object storage for uploaded video files and processed ZIP results. Replaces the current local `uploads/` and `outputs/` directories. Enables multiple API and worker instances to share the same storage backend. Presigned URLs are used for result downloads, removing the need to proxy ZIP content through the API server.

### RabbitMQ — Planned (Phase 6)

Durable async message broker for dispatching `VideoJob` processing tasks to the worker. Key properties: per-message acknowledgement, dead-letter queues, durable queues that survive broker restarts. The API publishes a job message after `CreateVideoJob`; the worker (`cmd/worker`) dequeues, runs `ffmpeg`, and calls `CompleteJob` or `FailJob`. The transactional outbox table in PostgreSQL ensures no messages are lost if the API crashes between the DB write and the broker publish.

A fourth Redis-backed responsibility — a **distributed lock**, belt-and-suspenders alongside RabbitMQ acknowledgement to prevent concurrent worker pickup of the same job — is also planned here rather than in Phase 4: there is no `cmd/worker` to contend over job pickup until this phase.

### Email / Webhook delivery — Planned (Phase 7)

Notification infrastructure for `VideoJobCompleted` and `VideoJobFailed` events. Owned by the Notification bounded context. Delivery methods and preferences are per-user. Webhook delivery includes retry logic and HMAC signature verification.

### Observability — Planned (Phase 8)

Structured logging (zerolog or slog), Prometheus metrics at `/metrics`, health endpoint at `/health`, readiness endpoint at `/ready`. Also in Phase 8: `docker-compose.yml` for the full local development stack (API, worker, PostgreSQL, Redis, RabbitMQ, MinIO).
