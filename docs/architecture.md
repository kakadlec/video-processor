# Architecture

## Current Implementation

The video-processing HTTP surface lives in `cmd/api` (package `main`, split across `main.go`, `identity.go`, and `video.go`) — Phase 3's `extract-cmd-api-entrypoint` moved it there from the repo root. Phase 2 added the first real internal package: `internal/identity`, an explicit DDD slice (domain/application/infrastructure) wired into `cmd/api`'s composition root rather than a package of its own. Phase 3's `wire-videojob-http-endpoints` wired `internal/video` in the same way, behind a preview `/api/video-jobs` HTTP surface, and `migrate-ffmpeg-execution-to-videojob-application` then cut `POST /upload`'s own `ffmpeg` execution over to run through that same `internal/video` application layer — see the Routes table below.

```
video-processor/
  cmd/
    api/
      main.go          # HTTP server, router, static file serving, /download+/api/status, ownership helpers
      identity.go       # Identity composition root: wires internal/identity into main.go's router
      video.go          # Video Processing composition root + handlers: wires internal/video into main.go's router, handles POST /upload and /api/video-jobs
      main_test.go     # Integration tests (drive the real handlers via httptest)
      identity_test.go # Identity HTTP/middleware/ownership tests
      video_test.go     # Video job HTTP route/setup tests
      web/
        index.html   # Upload form + login/register panel
        styles.css
        app.js
  internal/
    identity/
      domain/         # User, UserID, ports (repository, password hasher, token issuer/verifier)
      application/    # RegisterUser, AuthenticateUser use cases
      infrastructure/ # PostgreSQL adapter, bcrypt adapter, JWT adapter, UUID generator
    video/
      domain/         # VideoJob aggregate + transition methods, value objects, repository/FrameExtractor ports
      application/    # CreateVideoJob, GetJobStatus, ListUserJobs, EnqueueVideoJob, StartProcessing, CompleteJob, FailJob, ProcessVideoJob use cases
      infrastructure/ # PostgreSQL adapter, UUID generator, ffmpeg-backed FrameExtractor adapter
  go.mod / go.sum  # Module definition: gin, pgx, golang-jwt, bcrypt, google/uuid
  Dockerfile       # Multi-stage build: builder -> test -> runtime (non-root)
  docker-compose.yml # Local dev stack: postgres, app, and the app-test service used to run the suite
  .github/
    workflows/
      ci.yml                      # Build & Test, SAST (gosec), Vulnerability Scan
      release-please.yml          # Automated release management
  docs/            # Project documentation (this directory)
  openspec/        # Spec-driven change governance artifacts
```

### Request pipeline (current)

All processing happens synchronously inside the HTTP handler, but the actual work now runs through `internal/video`'s application layer rather than an inline `exec.Command` call in `cmd/api`:

```
Browser / client
  │
  └─► POST /upload (multipart)
        │
        ├─ Validate extension (.mp4, .avi, .mov, .mkv, .wmv, .flv, .webm)
        ├─ Save to uploads/<uploadID>_<filename>, hashing the stream (SHA-256) in the same pass
        ├─ Reserve(userID + content hash)          (Redis, idempotency)
        │    ├─ error (Redis down/erroring) → log, proceed without a
        │    │    reservation (fail-open); Finalize/Clear below are
        │    │    skipped for this request — see fail-open-upload-idempotency
        │    ├─ reserved=false → poll Lookup up to a bounded 2s window; a
        │    │    resolved duplicate returns the existing job's status
        │    │    (200), an unresolved one returns 409 — no CreateVideoJob call
        │    └─ reserved=true  → proceed below
        ├─ CreateVideoJob                          (application, status: pending)
        │    ├─ on failure: Clear the idempotency reservation if one was
        │    │    obtained, return 500
        │    └─ on success: Finalize the reservation if one was obtained
        │         → real VideoJobID, 24h TTL (non-fatal if this write
        │         itself fails — see below)
        ├─ ProcessVideoJob:
        │    ├─ EnqueueVideoJob                     (pending → queued)
        │    ├─ StartProcessing                      (queued → processing)
        │    ├─ FrameExtractor.ExtractFrames (infrastructure/ffmpeg):
        │    │    ├─ exec.CommandContext ffmpeg -i <video> -vf fps=1 temp/<jobID>/frame_%04d.png
        │    │    ├─ Glob PNGs from temp/<jobID>/
        │    │    ├─ Write temp/<jobID>.zip          (beside the frame dir, so
        │    │    │    the defer below can't delete it)
        │    │    └─ Remove temp/<jobID>/            (defer, always)
        │    ├─ ResultStorage.Put (infrastructure/storage):
        │    │    ├─ FPutObject → bucket/frames_<jobID>.zip
        │    │    └─ Remove temp/<jobID>.zip         (defer, always)
        │    └─ FailJob if extraction OR storage failed (processing → failed)
        │         → also clears the idempotency reservation
        │         (on success, job stays "processing" — see below)
        ├─ Remove uploads/<file>                    (on success only)
        ├─ CompleteJob (processing → completed)
        │    → the result is already durable in the bucket by this point
        └─ Return JSON { success, message, zip_path, frame_count, images }
```

`ProcessVideoJob` deliberately doesn't call `CompleteJob` itself on extraction success: the job stays `processing` until `handleVideoUpload`'s own remaining step (recording who owns the output artifact) has also succeeded. This matters because `completed → failed` isn't a valid transition — calling `CompleteJob` any earlier would make a later failure in that step unrecoverable (the job would stay `completed` forever, pointing at a `StorageKey` for an artifact that got deleted).

**Idempotency (Phase 4, `add-upload-idempotency-keys`; fail-open correction, `fail-open-upload-idempotency`):** `POST /upload` deduplicates by content, not just by request. The upload's SHA-256 hash plus the authenticated `UserID` form a Redis-backed `IdempotencyKey` (`internal/video/domain`, implemented by `internal/video/infrastructure/idempotency.RedisStore`). A `Reserve` call wins the race for a given key with a short-lived sentinel; the loser polls `Lookup` for up to a bounded window and returns the winner's job status instead of starting a second `ffmpeg` run. The reservation is `Finalize`d (24h TTL) once `CreateVideoJob` succeeds, or `Clear`ed immediately if `CreateVideoJob` fails or the job later fails — either way freeing an immediate retry. Two different users uploading identical content each get their own key and their own job (the key includes `UserID`). If `Reserve` itself errors (Redis unreachable/erroring, as opposed to a normal reservation conflict), `handleVideoUpload` fails open: it logs the error and proceeds to `CreateVideoJob` without a reservation, rather than blocking the upload — the same posture already used by rate limiting and the status cache, so a Redis outage degrades deduplication instead of the critical upload path itself. See `openspec/specs/upload-idempotency/spec.md` for the full contract.

**Rate limiting (Phase 4, `add-rate-limiting-middleware`):** every route gated by `identity.requireBearerAuth()` is also gated by a per-user, Redis-backed fixed-window rate limiter (`internal/platform/ratelimit.Limiter`, mounted as `cmd/api/ratelimit.go`'s `rateLimitMiddleware` on the `videoRoutes` group, immediately after the auth middleware). A request that crosses the configured limit within the current window is rejected with `429` and a `Retry-After` header before any handler runs, keyed independently per authenticated `UserID`. Thresholds are configurable via optional `RATE_LIMIT_MAX_REQUESTS`/`RATE_LIMIT_WINDOW_SECONDS` (defaults 60 requests / 60 seconds) — unlike `REDIS_ADDR`, neither is required at startup. If the Redis check itself fails or exceeds a short internal timeout, the middleware fails open (allows the request, logs the error) rather than blocking otherwise-healthy traffic on an infrastructure hiccup. That timeout only actually bounds the real Redis call because `internal/platform/redis.Open` sets `ContextTimeoutEnabled: true` on the shared client — go-redis v9 otherwise silently discards a passed context's deadline on every command, a subtlety this change had to fix and cover with a real-client test (`internal/platform/redis/client_test.go`) after an initial fix attempt turned out not to work. Unauthenticated routes (`/api/auth/register`, `/api/auth/login`, static assets) are out of scope. See `openspec/specs/rate-limiting/spec.md` for the full contract.

**Status cache (Phase 4, `add-videojob-status-cache`):** `internal/video/infrastructure/cache.CachedVideoJobRepository` decorates the PostgreSQL `VideoJobRepository` with a Redis-backed cache for `FindByID` lookups, wired into `cmd/api/video.go`'s `setupVideo` so every use case that touches a `VideoJob` — including `GetJobStatus`, which backs `GET /api/video-jobs/:id`'s repeated polling reads — shares one cached repository instance. Reads are cache-aside (a hit skips PostgreSQL; a miss, Redis error, or corrupted entry falls back to PostgreSQL, always unconditionally — the fallback read never itself fails); the four state-transition use cases' writes are write-through (`Update` writes PostgreSQL first, then overwrites the cache entry with the confirmed new state). Miss-repopulation uses `SET NX`, not a plain `SET`, so a slow reader that read a pre-transition value can never clobber a fresher write-through entry a concurrent transition already wrote — a race caught during review and covered by a dedicated concurrency test. A malformed-but-string cache entry is removed via a compare-and-delete Lua script (mirroring the idempotency store's own atomicity pattern) before repopulation, so it's guaranteed to self-heal without reopening that same race — but a generic Redis error on the read (e.g. `WRONGTYPE` from a key holding an incompatible value) only gets a best-effort repopulation attempt, since `SET NX` can't replace an existing key of the wrong type; PostgreSQL correctness is unaffected either way, only that job's caching benefit is what's at risk. Entries carry a fixed 5-minute TTL as a safety net, not the source of correctness. `FindByUserID` (the `GET /api/video-jobs` list endpoint) and `Create` are intentionally left uncached. This closes out all three of Phase 4's planned Redis responsibilities. See `openspec/specs/videojob-status-cache/spec.md` for the full contract.

The handler returns only after the full sequence completes and the ZIP is written. There is still no queue, no worker, and no async signalling — see `openspec/specs/videojob-execution/spec.md` for the full contract. The `VideoJob` created for the upload is queryable afterward via `GET /api/video-jobs/:id`/`GET /api/video-jobs` (see the preview API note below).

### State (current)

Uploaded source videos still live in the local filesystem; results live in MinIO and user accounts in PostgreSQL:

| Store | Purpose | Durability |
|---|---|---|
| `uploads/` | Transient input; deleted on successful processing | Lost on failure |
| `temp/` | Per-request scratch — extracted frames and the zip built from them; always cleaned up (defer) | Ephemeral |
| MinIO bucket (`VIDEO_MINIO_BUCKET`) | Durable ZIP results, keyed `frames_<jobID>.zip`; streamed by `/download/:filename` | Persistent, external to the container |
| `uploads/*.owner` | Sidecar files recording which authenticated `UserID` owns an uploaded video. Results have no sidecar — their owner is the `VideoJob` row. Retired for uploads too by `migrate-upload-storage-to-minio` | Same lifecycle as the upload they accompany |
| PostgreSQL `users` table | User accounts (normalized email, password hash) | Persistent, external to the container |
| PostgreSQL `video_jobs` table | `VideoJob` rows — created both by `POST /upload` (this pipeline) and by `POST /api/video-jobs` (the preview API below); the two share the same repository and owner-scoping | Persistent, external to the container |

No message broker. Redis backs idempotency keys, rate limiting, and (as of `add-videojob-status-cache`) a non-authoritative `VideoJob` status cache — PostgreSQL remains the source of truth on any cache miss.

### Routes (current)

| Route | Handler | Description |
|---|---|---|
| `GET /` | inline | Serves embedded `cmd/api/web/index.html` (via `go:embed`); always public |
| `POST /api/auth/register` | `handleRegister` | Create a user account |
| `POST /api/auth/login` | `handleLogin` | Authenticate and issue a bearer JWT |
| `POST /upload` | `handleVideoUpload` | Accept multipart video, process synchronously; requires a bearer token |
| `GET /download/:filename` | `(*videoModule).handleDownload` | Stream a ZIP out of the MinIO bucket; owner-only, entitlement decided from the `VideoJob` row. Every rejection returns a byte-identical 404 |
| `GET /api/status` | `(*videoModule).handleStatus` | JSON list of the caller's `completed` jobs' results; size and timestamp come from the stored object |
| `GET /uploads/*` | `gin.Static` | Static file serving of `uploads/`; owner-only; `.owner` sidecar files are never served regardless |
| `POST /api/video-jobs` | `handleCreateVideoJob` | Create a `VideoJob` record (JSON `original_filename`, no file content); requires a bearer token; preview API, see below |
| `GET /api/video-jobs/:id` | `handleGetVideoJobStatus` | Get a `VideoJob`'s status; owner-only (non-owner and nonexistent both 404) |
| `GET /api/video-jobs` | `handleListVideoJobs` | Paginated list of the caller's own `VideoJob`s |

`IDENTITY_POSTGRES_DSN`, `IDENTITY_JWT_SIGNING_KEY`, `VIDEO_POSTGRES_DSN`, (as of `add-upload-idempotency-keys`) `REDIS_ADDR`, and (as of `migrate-result-storage-to-minio`) `VIDEO_MINIO_ENDPOINT`/`VIDEO_MINIO_ACCESS_KEY`/`VIDEO_MINIO_SECRET_KEY`/`VIDEO_MINIO_BUCKET` are all required at startup — the API composition root fails to start otherwise. There is no unauthenticated fallback mode. `RATE_LIMIT_MAX_REQUESTS`/`RATE_LIMIT_WINDOW_SECONDS` (as of `add-rate-limiting-middleware`) are optional, with defaults. See [docs/operations.md](operations.md) for configuration.

**`/api/video-jobs` is a preview API, not the async processing flow.** It was wired by Phase 3's `wire-videojob-http-endpoints`, alongside (not replacing) the legacy synchronous `/upload` flow above. A `VideoJob` created *through this API* has no processing trigger — `handleCreateVideoJob` never calls `EnqueueVideoJob`/`StartProcessing`/`CompleteJob`/`FailJob` — and none is currently planned for it: `migrate-ffmpeg-execution-to-videojob-application` gave those four use cases a real caller, but it's `POST /upload`, not `POST /api/video-jobs`, so a job created via this API still stays `pending` indefinitely. Giving this preview API its own trigger would need a separate, not-yet-proposed future change. The frontend does not consume these routes. It is deliberately not named `/jobs`: see [docs/flows.md](flows.md) for why that path is reserved for the real Phase 6 async endpoint, and `openspec/specs/videojob-http-api/spec.md` for the full contract.

**`GET /api/video-jobs`/`GET /api/video-jobs/:id` do show non-`pending` jobs, though** — `POST /upload` also calls `CreateVideoJob`, so a user who has uploaded via `/upload` will see those jobs (real `completed`/`failed` status, real `frame_count`/`storage_key`) alongside any still-`pending` jobs they created directly via `POST /api/video-jobs`. Same aggregate, same repository, same owner-scoping — not a bug, see `openspec/specs/videojob-execution/spec.md` for how `/upload` drives that state machine.

CORS headers (`Access-Control-Allow-Origin: *`) are applied globally.

### Frontend (current)

The web UI lives in `cmd/api/web/index.html`, `cmd/api/web/styles.css`, and `cmd/api/web/app.js`, embedded into the binary via `go:embed` and served at `GET /`, `GET /styles.css`, and `GET /app.js` respectively. It contains:

- Plain HTML form for file selection, plus a login/register panel (Phase 2)
- CSS in `cmd/api/web/styles.css`
- Vanilla JavaScript in `cmd/api/web/app.js` using `fetch` to call `POST /upload`, `GET /api/status`, `POST /api/auth/register`, and `POST /api/auth/login`; the bearer token is kept in `localStorage` and attached as an `Authorization` header on protected requests

There is no separate frontend build, no Node.js toolchain, and no bundler.

---

## Target Architecture (Partially implemented — Phases 1–4 of 8 done)

The hackathon requirements include user authentication, asynchronous processing, notifications, and object storage. The target architecture introduces Domain-Driven Design structure across three bounded contexts, delivered incrementally.

> Identity (Phase 2) and Phase 3 (the `cmd/api` split, the `VideoJob` HTTP surface, and `POST /upload`'s ffmpeg execution migrated into the application layer) are both fully implemented as described below. Phase 4 is done: `internal/platform/redis` provides connection plumbing (`add-redis-infrastructure`), and all three of its planned features are wired in — idempotency keys on `POST /upload` (`add-upload-idempotency-keys`), per-user rate limiting on every authenticated route (`add-rate-limiting-middleware`), and a `VideoJob` status cache (`add-videojob-status-cache`). Phase 5 is in progress: `add-minio-infrastructure` added `internal/video/infrastructure/storage`, and `migrate-result-storage-to-minio` wired it into `cmd/api` — processed ZIP results now live in a MinIO bucket, `outputs/` is gone, and MinIO is a fail-closed startup dependency. Uploaded source videos still live in `uploads/`, and downloads are still proxied through the API rather than presigned; those are Phase 5's two remaining changes. Notification, `cmd/worker`, and Video Processing's own async queueing/execution (RabbitMQ, `EnqueueVideoJob`/`StartProcessing`/`CompleteJob`/`FailJob` driven by a real worker instead of in-process by `/upload`) remain planned — each is labeled with the phase that introduces it.

### Bounded Contexts

| Context | Responsibility | Status |
|---|---|---|
| **Identity** | User registration, authentication, JWT issuance and verification | Implemented (Phase 2) |
| **Video Processing** | VideoJob lifecycle — creation, queueing, async execution, result storage | Partially implemented (Phase 3: creation/status/listing wired into HTTP, and synchronous in-process execution driven by `POST /upload`; Phase 5: result storage in MinIO. Real queueing/async execution via RabbitMQ/`cmd/worker` planned, Phase 6) |
| **Notification** | Reacting to domain events and delivering notifications (email, webhook) | Planned (Phase 7) |

### Target Package Topology

```
video-processor/
  cmd/
    api/        # HTTP entrypoint (implemented, Phase 3) — main.go/identity.go moved here
      web/
        index.html  # Extracted from getHTMLForm()
        styles.css
        app.js
    worker/     # Async frame-extraction worker (Phase 6)
  internal/
    platform/
      redis/          # Shared Redis connection adapter (implemented, Phase 4) — Config/Open/Ping/Close, wired into cmd/api by add-upload-idempotency-keys
      ratelimit/      # Redis-backed fixed-window rate Limiter (implemented, Phase 4 — add-rate-limiting-middleware), wired into cmd/api's rateLimitMiddleware on every authenticated route
    identity/                        # Implemented (Phase 2), wired into cmd/api
      domain/         # User aggregate, value objects, repository/password/token ports
      application/    # Use cases: RegisterUser, AuthenticateUser
      infrastructure/ # PostgreSQL adapter, bcrypt adapter, JWT adapter, UUID generator
    video/
      domain/         # VideoJob aggregate + transition methods, value objects, events, repository/FrameExtractor ports (all implemented, Phase 3)
      application/    # Use cases: CreateVideoJob, GetJobStatus, ListUserJobs, EnqueueVideoJob, StartProcessing, CompleteJob, FailJob, ProcessVideoJob (all implemented, Phase 3)
      infrastructure/ # PostgreSQL adapter, ffmpeg-backed FrameExtractor adapter (both implemented, Phase 3 — wired into cmd/api by wire-videojob-http-endpoints / migrate-ffmpeg-execution-to-videojob-application)
        idempotency/  # Redis-backed IdempotencyStore adapter (implemented, Phase 4 — add-upload-idempotency-keys), wired into cmd/api's POST /upload handler
        cache/        # Redis-backed CachedVideoJobRepository decorator (implemented, Phase 4 — add-videojob-status-cache), wired into cmd/api's setupVideo ahead of every use case
        storage/      # MinIO adapter (implemented, Phase 5) — Config/Open/Ping/EnsureBucket connection plumbing (add-minio-infrastructure) plus ResultStorage, the domain port carrying result artifacts into and out of the bucket (migrate-result-storage-to-minio)
        # RabbitMQ publisher planned (Phase 6)
    notification/
      domain/         # NotificationPreference, DeliveryAttempt
      application/    # Use cases: SendJobCompletionNotification, …
      infrastructure/ # Email adapter, webhook adapter (Phase 7)
```

**Migration strategy:** `cmd/api` (formerly the repo-root `main.go`, moved by Phase 3's `extract-cmd-api-entrypoint`) remains functional during the transition. New packages are introduced alongside it. Each feature phase migrates one slice of the handler into the appropriate use case and wires it back to the HTTP layer. No big-bang rewrite.

### Infrastructure Components

| Component | Role | Status |
|---|---|---|
| PostgreSQL | Authoritative state store for users, plus `video_jobs`/`video_job_outbox` (Phase 3) | **Implemented** (Phase 2 for identity; Phase 3 schema/adapter for video, wired into `cmd/api` by `wire-videojob-http-endpoints` and driven by `POST /upload` since `migrate-ffmpeg-execution-to-videojob-application`), required at deployment time — see [docs/operations.md](operations.md) |
| Redis | Idempotency keys, rate limiting, status cache | Connection adapter (`internal/platform/redis`), idempotency keys (`add-upload-idempotency-keys`), rate limiting (`add-rate-limiting-middleware`), and the `VideoJob` status cache (`add-videojob-status-cache`) — all three **implemented** (Phase 4 complete) |
| MinIO | Object storage for uploads and ZIP results (S3-compatible) | ZIP results implemented (`migrate-result-storage-to-minio`): stored, listed, and downloaded through `internal/video/domain.ResultStorage`, with configuration required at startup. Uploaded source videos and presigned download URLs are Phase 5's remaining work |
| RabbitMQ | Durable async task queue for job dispatch | Planned (Phase 6) |

A fourth Redis-backed responsibility — a distributed lock preventing concurrent `cmd/worker` instances from picking up the same job — is planned for Phase 6 (not Phase 4): there is no worker to contend over job pickup until then.

See [docs/roadmap.md](roadmap.md) for the full phase plan.

### Dependency Rules (Target)

1. `domain` packages MUST NOT import `application`, `infrastructure`, or transport packages.
2. `application` packages depend only on repository/port **interfaces** defined in `domain`.
3. `infrastructure` packages implement interfaces from `domain` and may import third-party drivers.
4. `cmd/api` and `cmd/worker` are the only places where `infrastructure` adapters are instantiated and wired (composition root). `cmd/api/identity.go` plays that role for Identity, and `cmd/api/video.go` for all of Video Processing's use cases (`CreateVideoJob`/`GetJobStatus`/`ListUserJobs`/`EnqueueVideoJob`/`StartProcessing`/`CompleteJob`/`FailJob`/`ProcessVideoJob`), today; `cmd/worker` doesn't exist yet (Phase 6).
5. No bounded context may import another context's `domain` or `application` packages directly. Each context defines and owns its own local value object for any identifier that crosses a boundary (e.g. `identity.UserID` and `video.UserID` are distinct types) — cross-context communication uses domain events or translation at the composition root, never a package shared between contexts' `domain` layers. There is no `pkg/` directory; a shared kernel was considered for the crossing `UserID` and rejected as tighter coupling than this architecture's context-independence goal justifies (see `add-videojob-domain-and-application`'s `design.md` in `openspec/changes/archive/`).

Rules 1–3 for `internal/identity/{domain,application}` and `internal/video/{domain,application}` are each enforced by an automated test (`internal/identity/dependency_rules_test.go`, `internal/video/dependency_rules_test.go`), not just convention.
