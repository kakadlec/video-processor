# Architecture

## Current Implementation

The video-processing HTTP surface lives in `cmd/api` (package `main`, split across `main.go`, `identity.go`, and `video.go`) — Phase 3's `extract-cmd-api-entrypoint` moved it there from the repo root. Phase 2 added the first real internal package: `internal/identity`, an explicit DDD slice (domain/application/infrastructure) wired into `cmd/api`'s composition root rather than a package of its own. Phase 3's `wire-videojob-http-endpoints` wired `internal/video` in the same way, behind a preview `/api/video-jobs` HTTP surface, and `migrate-ffmpeg-execution-to-videojob-application` then cut `POST /upload`'s own `ffmpeg` execution over to run through that same `internal/video` application layer — see the Routes table below. Phase 6's `migrate-upload-to-async-processing` then moved that execution out of the request entirely: `cmd/worker` is a second composition root that consumes the job queue and runs the pipeline, and `POST /upload` answers `202` as soon as the job is queued. `add-worker-job-lock` completed the phase with epoch-scoped Redis leases, fenced terminal writes, and a sweeper that re-dispatches jobs abandoned in `processing`.

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
    worker/
      main.go          # Worker composition root: consumer lifecycle, disposition table, terminal cleanup
      sweeper.go       # Lease-aware recovery of jobs abandoned in processing
      main_test.go     # createDirs / configuration-surface tests
      worker_test.go   # End-to-end dispatch tests against real PostgreSQL, Redis, MinIO, and a broker
      sweeper_test.go  # Recovery, fencing, scan rotation, and shutdown tests
  internal/
    identity/
      domain/         # User, UserID, ports (repository, password hasher, token issuer/verifier)
      application/    # RegisterUser, AuthenticateUser use cases
      infrastructure/ # PostgreSQL adapter, bcrypt adapter, JWT adapter, UUID generator
    video/
      domain/         # VideoJob aggregate + transition methods, value objects, repository/FrameExtractor ports
      application/    # CreateVideoJob, GetJobStatus, ListUserJobs, EnqueueVideoJob, StartProcessing, CompleteJob, FailJob, ProcessVideoJob use cases
      infrastructure/ # PostgreSQL adapter, UUID generator, ffmpeg-backed FrameExtractor adapter,
                      #   MinIO storage, Redis idempotency/cache/lease, AMQP messaging (topology, Publisher, Relay, Consumer)
    platform/
      redis/ ratelimit/ rabbitmq/   # Cross-cutting connection/lifecycle plumbing owned by no context
  go.mod / go.sum  # Module definition: gin, pgx, golang-jwt, bcrypt, google/uuid, go-redis, minio-go, amqp091-go
  Dockerfile       # Multi-stage build: builder -> test -> runtime (non-root); builds and carries both binaries
  docker-compose.yml # Local dev stack: postgres, redis, minio, rabbitmq, app, worker, and the app-test service used to run the suite
  .github/
    workflows/
      ci.yml                      # Build & Test, SAST (gosec), Vulnerability Scan
      release-please.yml          # Automated release management
  docs/            # Project documentation (this directory)
  openspec/        # Spec-driven change governance artifacts
```

### Request pipeline (current)

Processing is asynchronous. `POST /upload` stores the bytes, records the job, and answers `202`; a separate process does the work. The two halves are shown separately because they run in different processes, on different filesystems, and can fail independently.

**API half — `cmd/api`, inside the request:**

```
Browser / client
  │
  └─► POST /upload (multipart)
        │
        ├─ Validate extension (.mp4, .avi, .mov, .mkv, .wmv, .flv, .webm)
        ├─ SourceStorage.Put → bucket/uploads/<uploadID>_<filename>, hashing
        │    the stream (SHA-256) in the same pass; nothing touches local disk
        ├─ Delete that source object                (one defer, registered the
        │    moment Put succeeds, and guarded on whether the enqueue
        │    committed: before that the object is this request's, after it
        │    the object is the worker's input and MUST survive. Best effort
        │    — a failure is logged with the key, never fatal)
        ├─ Reserve(userID + content hash)          (Redis, idempotency)
        │    ├─ error (Redis down/erroring) → log, proceed without a
        │    │    reservation (fail-open); Finalize/Clear below are
        │    │    skipped for this request — see fail-open-upload-idempotency
        │    ├─ reserved=false → poll Lookup up to a bounded 2s window; a
        │    │    resolved duplicate returns 202 naming the existing job —
        │    │    byte-shaped like a fresh acknowledgement, so a client needs
        │    │    no duplicate branch — an unresolved one returns 409, and
        │    │    neither calls CreateVideoJob
        │    └─ reserved=true  → proceed below
        ├─ CreateVideoJob                          (application, status: pending,
        │                                            carrying the source key)
        │    └─ on failure: Clear the idempotency reservation if one was
        │         obtained, return 500
        ├─ EnqueueVideoJob                         (pending → queued; the status
        │                                            update AND a
        │                                            video_job.queued.v2
        │                                            outbox row commit in one
        │                                            transaction — the relay
        │                                            publishes it later, out of
        │                                            band)
        │    └─ on failure: Clear the reservation, return 500 — the job row
        │         exists but is unqueued, and the source object's defer
        │         still deletes it
        ├─ Finalize the reservation if one was obtained
        │    → real VideoJobID, 24h TTL (non-fatal if this write
        │      itself fails — see below). After the enqueue, not before:
        │      see the idempotency note below
        └─ 202 { job_id, status: "queued", status_url: "/api/video-jobs/<id>" }
             no frame count, no result key, no download URL — none of them
             exists yet; the client polls status_url for all of it
```

The source object is deliberately **still in the bucket** when this response is returned: it is the worker's input.

**Worker half — `cmd/worker`, out of band:**

```
video.jobs.queued.v2  (prefetch 1, one delivery at a time)
  │
  └─► handle(body)
        ├─ ParseJobQueuedMessage                   (undecodable → Reject → DLQ;
        │    redelivering it would fail identically forever)
        ├─ NewStorageKey(msg.source_key)           (empty/invalid → Reject → DLQ)
        ├─ ProcessVideoJob (job_id, source_key):
        │    ├─ StartProcessing → ClaimForProcessing
        │    │    UPDATE video_jobs SET status='processing'
        │    │      WHERE id=$1 AND status='queued' RETURNING lease_epoch
        │    │    └─ no row affected → ErrJobClaimLost → Reject → DLQ,
        │    │         touching nothing at all (another consumer owns it)
        │    ├─ Acquire Redis lease at the returned epoch; renew every 30s
        │    │    for the run (errors fail open; an absent lease is
        │    │    reacquired, a newer epoch stops the heartbeat)
        │    ├─ SourceStorage.Get → temp/<jobID>_source (no extension; ffmpeg
        │    │    probes content, so no path component comes from a filename)
        │    ├─ Remove temp/<jobID>_source           (ProcessVideoJob's own
        │    │    defer — registered before extraction, so a failed run
        │    │    can't leave the copy behind)
        │    ├─ FrameExtractor.ExtractFrames (infrastructure/ffmpeg):
        │    │    ├─ exec.CommandContext ffmpeg -i <video> -vf fps=1 temp/<jobID>/frame_%04d.png
        │    │    ├─ Glob PNGs from temp/<jobID>/
        │    │    ├─ Write temp/<jobID>.zip          (beside the frame dir, so
        │    │    │    the defer below can't delete it)
        │    │    └─ Remove temp/<jobID>/            (defer, always)
        │    ├─ Remove temp/<jobID>.zip           (ProcessVideoJob's own
        │    │    defer — registered before the store attempt, so a failed
        │    │    upload can't leave the zip behind)
        │    ├─ ResultStorage.Put (infrastructure/storage):
        │    │    └─ FPutObject → bucket/frames_<jobID>.zip
        │    └─ FailJob if fetch, extraction, OR storage failed
        │         (processing → failed), conditional on the claimed epoch
        ├─ ErrJobFenced from failure or completion:
        │    └─ Reject → DLQ; keep source and idempotency key; release no
        │         lease; log held epoch and any result key
        ├─ result.Success == false and `failed` was already present:
        │    └─ Ack without cleanup (this actor applied nothing)
        ├─ result.Success == false and this run applied `failed`:
        │    ├─ Delete the source object
        │    ├─ ClearJobIdempotencyKey (conditional on this job still owning it)
        │    ├─ Release the lease at this run's epoch
        │    └─ Ack
        └─ result.Success == true:
             ├─ CompleteJob (processing → completed at the claimed epoch),
             │    retried 4× with detached context and backoff
             │    └─ still failing → Reject → DLQ, source object and lease KEPT,
             │         result StorageKey logged
             ├─ Delete the source object (gated on this terminal commit)
             ├─ Release the lease at this run's epoch
             └─ Ack
```

`ProcessVideoJob` still leaves a successful job in `processing` for its caller to complete, but the caller is now `cmd/worker` and the split has acquired a real reason. Storing the result is `ProcessVideoJob`'s own step, so a successful return already means the artifact is durable — but the worker has to own the terminal write, because acknowledging the message is what that write licenses. Folding `CompleteJob` inside the use case would put the commit out of reach of the retry-and-dead-letter policy the worker applies to it.

**Claim and recovery are separate correctness decisions.** `ClaimForProcessing` still admits only `queued`, so duplicate dispatch cannot run two extractions and Redis is absent from pickup. Recovery is a periodic sibling goroutine in `cmd/worker`: each 60-second cycle scans at most 50 `processing` rows using a rotating keyset cursor, queries the Redis lease at the row's epoch, and acts only after two successful not-held observations at the same epoch. A query error for that row clears its confirmation and takes over nothing. Recovery conditionally writes `processing → queued`, increments `lease_epoch`, and inserts a fresh `video_job.queued.v2` outbox row in one transaction. The old holder's terminal `Update` requires both its epoch and `status = 'processing'`, so a requeue or another terminal winner fences it. After three requeues, or when a legacy row has no source key, the sweeper commits `failed` and cleans up only if its own write was applied.

**Outbox relay (Phase 6, `add-videojob-source-key-and-outbox-relay`; generation bumped by `migrate-upload-to-async-processing`):** `POST /upload` records a job dispatch; it never talks to the broker. `Repository.Enqueue` commits the `pending → queued` update and a `video_job.queued.v2` outbox row together, and a relay goroutine started by `cmd/api/main.go` carries those rows to RabbitMQ afterwards — `internal/video/infrastructure/messaging.Relay`, composing an `OutboxRepository` in `internal/video/infrastructure/postgres` with an AMQP `Publisher`, so neither infrastructure package has to depend on both a database driver and a broker client. Each cycle opens a transaction, claims a bounded batch with `SELECT … FOR UPDATE SKIP LOCKED` filtered to `event_type = 'video_job.queued.v2'` (the table still holds an unpublished `video_job.created` row for every job ever created, and those are internal events that must never reach the job queue), publishes each message mandatory on a confirm-mode channel, stamps `published_at` only for messages the broker both acknowledged **and** did not return, then commits. Holding row locks across a broker round trip is the deliberate cost: committing the claim first would, on a crash before the publish, lose a dispatch silently, whereas this ordering can only ever republish. A nack is not an error — the job queue's `reject-publish` overflow policy nacks when it is full, which is designed back-pressure — so the row simply stays unstamped for the next poll. The relay dials through `internal/platform/rabbitmq.Open` in its own goroutine, declares `JobDispatchTopology()` after **every** successful dial (the worker's consumer declares the same topology on every dial too, so neither process depends on the other having started first, and a reconnect to a recreated broker would otherwise publish into a missing exchange), and retries with backoff. `RABBITMQ_URL` is therefore required at startup while broker *reachability* is not — see [docs/operations.md](operations.md). **`cmd/worker` consumes that queue**, so a published message is now a live trigger. The generation suffix is carried by the exchange, the queue, **and the `event_type` string** — not by the exchange alone. Versioning the exchange isolates nothing on its own: every replica's relay reads the same `video_job_outbox` table, so with one shared event type a redeployed replica's relay would claim a not-yet-redeployed replica's row and publish it into the new generation. What that protects against is the rolling-deploy window, not stale messages: during a deploy where an in-request replica and a worker are both live, both can act on one legitimately-`queued` job — the claim decides who processes it, but the loser's cleanup would then delete the source out from under the winner's running extraction. A migration stamped `published_at` on the previous generation's still-unpublished rows, recording an exclusion the event-type filter already enforces. See `openspec/specs/videojob-outbox-relay/spec.md` and `openspec/specs/videojob-messaging/spec.md` for the full contract.

**Idempotency (Phase 4, `add-upload-idempotency-keys`; fail-open correction, `fail-open-upload-idempotency`):** `POST /upload` deduplicates by content, not just by request. The upload's SHA-256 hash plus the authenticated `UserID` form a Redis-backed `IdempotencyKey` (`internal/video/domain`, implemented by `internal/video/infrastructure/idempotency.RedisStore`). A `Reserve` call wins the race for a given key with a short-lived sentinel; the loser polls `Lookup` for up to a bounded window and returns the winner's job status instead of starting a second `ffmpeg` run. The reservation is `Finalize`d (24h TTL) once `CreateVideoJob` **and** `EnqueueVideoJob` have both succeeded, or `Clear`ed immediately if either of them fails — freeing an immediate retry. Finalizing before the enqueue would advertise a `VideoJobID` for 24 hours to duplicates of content whose job never reached `queued`; the ordering is specified rather than incidental (`upload-idempotency`'s "Reservation Is Finalized To The Real VideoJobID Only By Its Owning Token"), because the other ordering trades one narrow 24h block for another — a worker that fails the job clears the key *by job ID*, and until `Finalize` runs the key is a bare reservation that `ClearByJob` leaves alone by design. **Clearing a failed job's key is now the worker's**, not the handler's: the handler returns before the outcome exists. It rebuilds the key from the job's `UserID` and a new persisted `content_hash` column — the reservation *token* is never persisted, since it is a possession capability of the request that minted it — and reads that hash from the **undecorated** PostgreSQL repository, because a cache record written by a previous release carries no `content_hash` at all and would silently yield an unbuildable key. Two different users uploading identical content each get their own key and their own job (the key includes `UserID`). If `Reserve` itself errors (Redis unreachable/erroring, as opposed to a normal reservation conflict), `handleVideoUpload` fails open: it logs the error and proceeds to `CreateVideoJob` without a reservation, rather than blocking the upload — the same posture already used by rate limiting and the status cache, so a Redis outage degrades deduplication instead of the critical upload path itself. See `openspec/specs/upload-idempotency/spec.md` for the full contract.

**Rate limiting (Phase 4, `add-rate-limiting-middleware`):** every route gated by `identity.requireBearerAuth()` is also gated by a per-user, Redis-backed fixed-window rate limiter (`internal/platform/ratelimit.Limiter`, mounted as `cmd/api/ratelimit.go`'s `rateLimitMiddleware` on the `videoRoutes` group, immediately after the auth middleware). A request that crosses the configured limit within the current window is rejected with `429` and a `Retry-After` header before any handler runs, keyed independently per authenticated `UserID`. Thresholds are configurable via optional `RATE_LIMIT_MAX_REQUESTS`/`RATE_LIMIT_WINDOW_SECONDS` (defaults 60 requests / 60 seconds) — unlike `REDIS_ADDR`, neither is required at startup. If the Redis check itself fails or exceeds a short internal timeout, the middleware fails open (allows the request, logs the error) rather than blocking otherwise-healthy traffic on an infrastructure hiccup. That timeout only actually bounds the real Redis call because `internal/platform/redis.Open` sets `ContextTimeoutEnabled: true` on the shared client — go-redis v9 otherwise silently discards a passed context's deadline on every command, a subtlety this change had to fix and cover with a real-client test (`internal/platform/redis/client_test.go`) after an initial fix attempt turned out not to work. Unauthenticated routes (`/api/auth/register`, `/api/auth/login`, static assets) are out of scope. The limiter governs *requests to this API*, which since `add-presigned-download-urls` is narrower than it reads: `GET /download/:filename` issues a URL and is limited, but the transfer that URL authorizes goes to MinIO, which this middleware does not sit in front of. Bounding artifact egress is an object-storage concern. See `openspec/specs/rate-limiting/spec.md` for the full contract.

**Status cache (Phase 4, extended by Phase 6 recovery):** `internal/video/infrastructure/cache.CachedVideoJobRepository` provides cache-aside `FindByID` reads for polling. `GetJobStatus` and `EnqueueVideoJob` use those reads; ownership decisions (`StartProcessing`, `CompleteJob`, `FailJob`, download entitlement, and the sweeper scan) bypass them and read PostgreSQL. Every applied transition still writes through the decorator. Miss-repopulation uses `SET NX`, while transition write-through uses an atomic Redis CAS ordered first by `lease_epoch` and then by state progression within the epoch; a delayed requeue write therefore cannot replace a successor's `processing` or terminal record. An ambiguous database error invalidates the entry because it may have occurred after commit; `ErrJobFenced` is a decided zero-row result and leaves the winner's entry intact. Cache failures remain best effort, and entries carry a fixed five-minute TTL. See `openspec/specs/videojob-status-cache/spec.md`.

**Result download (Phase 5, `add-presigned-download-urls`):** `GET /download/:filename` no longer carries result bytes. It authorizes exactly as before — the key must parse to a `VideoJobID`, and that job must exist, belong to the caller, be `completed`, and record that exact key, read from the *undecorated* PostgreSQL repository — then confirms the object exists with one `Stat` and returns `200 {"url", "expires_at"}`. The client redeems that URL against MinIO directly:

```
Browser / client
  │
  ├─► GET /download/frames_<jobID>.zip   (bearer token)
  │     ├─ five entitlement conditions, from the VideoJob row
  │     ├─ ResultStorage.Stat            (signing is offline and would
  │     │    succeed for a key holding no object, so a missing object is
  │     │    refused here rather than surfacing as MinIO's own 404)
  │     ├─ ResultStorage.PresignGet      (5-minute TTL, no network call)
  │     └─ 200 {"url", "expires_at"}     (Cache-Control: no-store)
  │
  └─► GET <url>                          (no Authorization header)
        └─ MinIO streams the ZIP straight to the client; cmd/api is not involved
```

Three properties follow from that and are accepted rather than mitigated. An issued URL **cannot be revoked** — deleting the job or changing its owner leaves an outstanding URL working until it expires, which is why the TTL is a five-minute constant rather than configuration. The URL **is a credential**, so it is never logged, echoed into an error, or persisted; failures on this path log the `StorageKey` instead, and every response carries `Cache-Control: no-store`. And result **bytes are no longer rate limited** — see the rate-limiting note above.

The reported `expires_at` is read off the issued URL's own `X-Amz-Date`/`X-Amz-Expires`, not computed as `now + TTL`: the signing library stamps the signature at whole-second precision and truncates the lifetime, so the naive value overstates the window MinIO actually admits — and overstating is the direction that makes a client retry into a `403`. The bound is on **request admission**: a request arriving after the instant is refused, while a transfer already in flight runs to completion.

Because the URL's host is covered by the SigV4 signature, it cannot be rewritten after issuance — hence `VIDEO_MINIO_PUBLIC_ENDPOINT`, the browser-facing address the presigning client is built against. See `openspec/specs/videojob-result-storage/spec.md` and [docs/operations.md](operations.md).

`POST /upload` returns as soon as the job is `queued`; the ZIP is written later, by the worker. The client follows the `status_url` in the `202` body — `GET /api/video-jobs/:id` — until the job is `completed`, then calls `GET /download/:filename`. See `openspec/specs/videojob-execution/spec.md` and `openspec/specs/videojob-worker/spec.md` for the full contract.

### State (current)

Both uploaded source videos and results live in MinIO; user accounts and job rows in PostgreSQL. The only local directory left is scratch:

| Store | Purpose | Durability |
|---|---|---|
| `temp/` (**`cmd/worker`'s filesystem**) | Per-job scratch — the downloaded source copy, the extracted frames, and the zip built from them; always cleaned up (defer). `cmd/api` no longer creates or touches it | Ephemeral |
| MinIO bucket, `uploads/` prefix | Uploaded source videos, keyed `uploads/<uploadID>_<filename>`. Transient, with one owner at a time: the request until queueing, then the worker or recovery sweep that applies the terminal outcome. A mid-extraction crash is recovered automatically; an upload never dispatched, dead-lettered before claim, or interrupted after terminal commit but before cleanup can still leak, so the `uploads/`-prefix expiration rule remains the **only** exhaustive reclamation guarantee | Not meant to outlive its job |
| MinIO bucket, flat keys | Durable ZIP results, keyed `frames_<jobID>.zip`; served directly to the client under a presigned URL `/download/:filename` issues. Flat because `app.js` still uses the key verbatim as that route's single path segment — a `/` would percent-encode and break the match, which is exactly why sources may carry a prefix and results may not. The constraint survived the move to presigned URLs unchanged, since the route did | Persistent, external to the container |
| PostgreSQL `users` table | User accounts (normalized email, password hash) | Persistent, external to the container |
| PostgreSQL `video_jobs` table | `VideoJob` rows — created both by `POST /upload` (this pipeline) and by `POST /api/video-jobs` (the preview API below); the two share the same repository and owner-scoping | Persistent, external to the container |

RabbitMQ carries job dispatch — `video.jobs.v2` / `video.jobs.queued.v2`, with an unversioned `video.jobs.dlx` fanout sink. Redis backs idempotency keys, rate limiting, the non-authoritative `VideoJob` status cache, and epoch-scoped worker leases. PostgreSQL remains authoritative for job state, claims, fence epochs, and recovery transitions.

### Routes (current)

| Route | Handler | Description |
|---|---|---|
| `GET /` | inline | Serves embedded `cmd/api/web/index.html` (via `go:embed`); always public |
| `POST /api/auth/register` | `handleRegister` | Create a user account |
| `POST /api/auth/login` | `handleLogin` | Authenticate and issue a bearer JWT |
| `POST /upload` | `handleVideoUpload` | Accept multipart video, store it, queue the job, return `202 {job_id, status, status_url}`; requires a bearer token. **No processing happens in the request** — `cmd/worker` does the work |
| `GET /download/:filename` | `(*videoModule).handleDownload` | Authorize, then issue a 5-minute presigned URL for the stored ZIP: `200` with `{"url", "expires_at"}`, never the bytes. Owner-only, entitlement decided from the `VideoJob` row and evaluated *only* here, since an issued URL carries no identity. Every rejection returns a byte-identical 404; every response carries `Cache-Control: no-store` |
| `GET /api/status` | `(*videoModule).handleStatus` | JSON list of the caller's `completed` jobs' results; size and timestamp come from the stored object |
| `POST /api/video-jobs` | `handleCreateVideoJob` | Create a `VideoJob` record (JSON `original_filename`, no file content); requires a bearer token; preview API, see below |
| `GET /api/video-jobs/:id` | `handleGetVideoJobStatus` | Get a `VideoJob`'s status; owner-only (non-owner and nonexistent both 404). This is the endpoint `POST /upload`'s `status_url` names — the async flow's status channel, not just a preview read |
| `GET /api/video-jobs` | `handleListVideoJobs` | Paginated list of the caller's own `VideoJob`s |

`IDENTITY_POSTGRES_DSN`, `IDENTITY_JWT_SIGNING_KEY`, `VIDEO_POSTGRES_DSN`, (as of `add-upload-idempotency-keys`) `REDIS_ADDR`, and (as of `migrate-result-storage-to-minio`, now also backing source storage) `VIDEO_MINIO_ENDPOINT`/`VIDEO_MINIO_ACCESS_KEY`/`VIDEO_MINIO_SECRET_KEY`/`VIDEO_MINIO_BUCKET` are all required at startup — the API composition root fails to start otherwise. There is no unauthenticated fallback mode. `RABBITMQ_URL` (as of `add-videojob-source-key-and-outbox-relay`) is required too, though a *reachable* broker is not. `RATE_LIMIT_MAX_REQUESTS`/`RATE_LIMIT_WINDOW_SECONDS` (as of `add-rate-limiting-middleware`) are optional, with defaults.

`cmd/worker` is its own composition root with its own, deliberately smaller configuration surface: `RABBITMQ_URL`, `VIDEO_POSTGRES_DSN`, `REDIS_ADDR`, and the four required `VIDEO_MINIO_*` variables — and **no `IDENTITY_*` at all**, because it makes no access-control decision and requiring identity configuration would misrepresent what the process does. It serves no HTTP and exposes no port. See [docs/operations.md](operations.md) for configuration.

**`/api/video-jobs` is a preview API, not the async processing flow.** It was wired by Phase 3's `wire-videojob-http-endpoints`, alongside (not replacing) the `/upload` flow above. A `VideoJob` created *through this API* has no processing trigger — `handleCreateVideoJob` never calls `EnqueueVideoJob`/`StartProcessing`/`CompleteJob`/`FailJob` — and none is currently planned for it: `migrate-ffmpeg-execution-to-videojob-application` gave those four use cases a real caller, but it's `POST /upload`, not `POST /api/video-jobs`, so a job created via this API still stays `pending` indefinitely. Giving this preview API its own trigger would need a separate, not-yet-proposed future change. **The asynchronous cutover did not change this**, and the distinction is now easy to get wrong: `POST /upload` is the async submission endpoint, because it is the one that receives the bytes; `POST /api/video-jobs` takes a filename in JSON, has no source key, and `VideoJob.Enqueue` rejects a job without one. No separate `/jobs` endpoint was introduced. A `pending` status here therefore does **not** mean "waiting for a worker" — a job awaiting a worker is `queued`. `GET /api/video-jobs/:id`, on the other hand, is now consumed by the frontend as the upload flow's status channel. See `openspec/specs/videojob-http-api/spec.md` for the full contract.

**`GET /api/video-jobs`/`GET /api/video-jobs/:id` do show non-`pending` jobs, though** — `POST /upload` also calls `CreateVideoJob`, so a user who has uploaded via `/upload` will see those jobs (real `completed`/`failed` status, real `frame_count`/`storage_key`) alongside any still-`pending` jobs they created directly via `POST /api/video-jobs`. Same aggregate, same repository, same owner-scoping — not a bug, see `openspec/specs/videojob-execution/spec.md` for how `/upload` drives that state machine.

CORS headers (`Access-Control-Allow-Origin: *`) are applied globally.

### Frontend (current)

The web UI lives in `cmd/api/web/index.html`, `cmd/api/web/styles.css`, and `cmd/api/web/app.js`, embedded into the binary via `go:embed` and served at `GET /`, `GET /styles.css`, and `GET /app.js` respectively. It contains:

- Plain HTML form for file selection, plus a login/register panel (Phase 2)
- CSS in `cmd/api/web/styles.css`
- Vanilla JavaScript in `cmd/api/web/app.js` using `fetch` to call `POST /upload`, `GET /api/video-jobs/:id`, `GET /api/status`, `POST /api/auth/register`, and `POST /api/auth/login`; the bearer token is kept in `localStorage` and attached as an `Authorization` header on protected requests
- Since the cutover the page **polls**: it submits, reads `status_url` off the `202`, then polls with an interval that starts at 2s and backs off to a 10s ceiling. Those polls share one per-user rate-limit budget with the submission and the download issuance, so the interval is chosen against the default 60/60s rather than picked for responsiveness. A `429` is a back-off signal, not a job failure — `Retry-After` is honoured **uncapped** (the limiter's window is configurable and routinely exceeds the 10s ceiling; capping it would just earn another `429`), while the ordinary interval keeps advancing underneath so one long wait does not become the cadence

There is no separate frontend build, no Node.js toolchain, and no bundler.

---

## Target Architecture (Partially implemented — Phases 1–6 done)

The hackathon requirements include user authentication, asynchronous processing, notifications, and object storage. The target architecture introduces Domain-Driven Design structure across three bounded contexts, delivered incrementally.

> Identity (Phase 2) and Phase 3 (the `cmd/api` split, the `VideoJob` HTTP surface, and `POST /upload`'s ffmpeg execution migrated into the application layer) are both fully implemented as described below. Phase 4 is done: `internal/platform/redis` provides connection plumbing (`add-redis-infrastructure`), and all three of its planned features are wired in — idempotency keys on `POST /upload` (`add-upload-idempotency-keys`), per-user rate limiting on every authenticated route (`add-rate-limiting-middleware`), and a `VideoJob` status cache (`add-videojob-status-cache`). Phase 5 is done: `add-minio-infrastructure` added `internal/video/infrastructure/storage`, `migrate-result-storage-to-minio` wired it into `cmd/api`, `migrate-upload-storage-to-minio` moved uploaded source videos there too, and `add-presigned-download-urls` took the API out of the result-byte path — `GET /download/:filename` now issues a bounded URL the client redeems against MinIO directly. Both `outputs/` and `uploads/` are gone along with the whole ownership-sidecar mechanism, MinIO is a fail-closed startup dependency, and `ProcessVideoJob` now takes a storage key rather than a local path — the seam Phase 6's worker needs. Phase 6 is done: `add-rabbitmq-infrastructure` shipped the shared AMQP adapter and job topology; `add-videojob-source-key-and-outbox-relay` made source keys durable and added transactional dispatch; `migrate-upload-to-async-processing` added `cmd/worker` and the `202` acknowledgement; and `add-worker-job-lock` added Redis leases, PostgreSQL fence epochs, and the recovery sweeper. **Processing is asynchronous, and jobs abandoned after a successful claim are recovered by the lease sweeper.** Queued jobs whose dispatch is never published or is dead-lettered before claim remain outside that recovery scope. Notification (Phase 7) remains planned.

### Bounded Contexts

| Context | Responsibility | Status |
|---|---|---|
| **Identity** | User registration, authentication, JWT issuance and verification | Implemented (Phase 2) |
| **Video Processing** | VideoJob lifecycle — creation, queueing, async execution, recovery, result storage | Implemented (Phase 3: lifecycle and HTTP; Phase 5: source/results in MinIO; Phase 6: asynchronous dispatch and `cmd/worker`, plus lease/fence-based recovery for jobs abandoned in `processing`) |
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
    worker/     # Async frame-extraction worker (implemented, Phase 6): own composition root, no HTTP, no IDENTITY_* configuration
      main.go     # Consumer, fenced terminal disposition, lifecycle
      sweeper.go  # Redis-lease-aware recovery and bounded abandonment
  internal/
    platform/
      redis/          # Shared Redis connection adapter (implemented, Phase 4) — Config/Open/Ping/Close, wired into cmd/api by add-upload-idempotency-keys
      ratelimit/      # Redis-backed fixed-window rate Limiter (implemented, Phase 4 — add-rate-limiting-middleware), wired into cmd/api's rateLimitMiddleware on every authenticated route
      rabbitmq/       # Shared AMQP connection adapter (implemented, Phase 6 — add-rabbitmq-infrastructure) — Config/Open/Ping/Close plus a generic Topology descriptor and DeclareTopology; names no exchange or queue of its own. Its one caller is the outbox relay (add-videojob-source-key-and-outbox-relay), which owns the connection cmd/api opens
    identity/                        # Implemented (Phase 2), wired into cmd/api
      domain/         # User aggregate, value objects, repository/password/token ports
      application/    # Use cases: RegisterUser, AuthenticateUser
      infrastructure/ # PostgreSQL adapter, bcrypt adapter, JWT adapter, UUID generator
    video/
      domain/         # VideoJob aggregate + transition methods, value objects, events, repository/FrameExtractor ports (all implemented, Phase 3)
      application/    # Use cases: CreateVideoJob, GetJobStatus, ListUserJobs, EnqueueVideoJob, StartProcessing, CompleteJob, FailJob, ProcessVideoJob (all implemented, Phase 3)
      infrastructure/ # PostgreSQL adapter, ffmpeg-backed FrameExtractor adapter (both implemented, Phase 3 — wired into cmd/api by wire-videojob-http-endpoints / migrate-ffmpeg-execution-to-videojob-application)
        idempotency/  # Redis-backed IdempotencyStore adapter (implemented, Phase 4 — add-upload-idempotency-keys), wired into cmd/api's POST /upload handler
        cache/        # Redis-backed CachedVideoJobRepository decorator (implemented, Phase 4; epoch/status-aware write ordering added in Phase 6)
        lease/        # Epoch-scoped Redis worker lease (implemented, Phase 6 — add-worker-job-lock)
        storage/      # MinIO adapter (implemented, Phase 5) — Config/Open/Ping/EnsureBucket connection plumbing (add-minio-infrastructure) plus ResultStorage, the domain port carrying result artifacts into and out of the bucket (migrate-result-storage-to-minio)
        messaging/    # This context's job-dispatch topology, the outbox relay that publishes into it, and the consumer that reads from it (implemented, Phase 6 — add-rabbitmq-infrastructure for JobDispatchTopology(), add-videojob-source-key-and-outbox-relay for Publisher/Relay, migrate-upload-to-async-processing for Consumer): Publisher wraps a confirm-mode channel and publishes mandatory; Relay claims outbox rows through postgres.OutboxRepository, publishes them, and stamps published_at, started as a goroutine by cmd/api/main.go and stopped on shutdown; Consumer dials, redeclares the topology on every dial, sets prefetch 1, and hands each delivery to a Handler that returns Ack or Reject — it knows nothing about jobs, claims, or storage, and cmd/worker holds the whole decision table
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
| Redis | Idempotency keys, rate limiting, status cache, worker leases | Connection adapter and the first three responsibilities are implemented from Phase 4; `add-worker-job-lock` completed the fourth in Phase 6 with `internal/video/infrastructure/lease`. Redis remains non-authoritative and is not consulted by the PostgreSQL claim or fence |
| MinIO | Object storage for uploads and ZIP results (S3-compatible) | Fully implemented: ZIP results through `internal/video/domain.ResultStorage` (`migrate-result-storage-to-minio`) and uploaded source videos through `SourceStorage` (`migrate-upload-storage-to-minio`), sharing one bucket, separated by key prefix, with configuration required at startup. Results are handed to clients as presigned URLs rather than proxied (`add-presigned-download-urls`), so the API is absent from the transfer path |
| RabbitMQ | Durable async task queue for job dispatch | **Implemented, publishing, and consumed** (Phase 6, `add-rabbitmq-infrastructure` + `add-videojob-source-key-and-outbox-relay` + `migrate-upload-to-async-processing`): `internal/platform/rabbitmq` opens, health-checks, and declares a topology; `internal/video/infrastructure/messaging` defines this context's exchange, queue, and dead-letter sink, the outbox relay that publishes `video_job.queued.v2` events into it, and the consumer `cmd/worker` reads them with. `RABBITMQ_URL` is **required at both `cmd/api` and `cmd/worker` startup**, but a reachable broker is not — relay and consumer each own their connection, dial in their own goroutine, redeclare the topology after every dial, and retry with backoff |

The fourth Redis responsibility is implemented as a **lease for liveness**, not a lock around pickup. Concurrent pickup remains prevented by PostgreSQL's literal `status = 'queued'` claim. Redis gives the sweeper advisory evidence about whether a `processing` row still has a live worker, while PostgreSQL's `lease_epoch` and conditional terminal update provide the fence. A lease-query error itself authorizes no takeover, but fail-open acquisition or renewal can leave a live extraction without a matching key; two later successful absence observations may requeue it and let a successor run concurrently. The PostgreSQL fence prevents the superseded run from overwriting the authoritative outcome, but deliberately does not prevent duplicated extraction work.

See [docs/roadmap.md](roadmap.md) for the full phase plan.

### Dependency Rules (Target)

1. `domain` packages MUST NOT import `application`, `infrastructure`, or transport packages.
2. `application` packages depend only on repository/port **interfaces** defined in `domain`.
3. `infrastructure` packages implement interfaces from `domain` and may import third-party drivers.
4. `cmd/api` and `cmd/worker` are the only places where `infrastructure` adapters are instantiated and wired (composition root), and both now exist. `cmd/api/identity.go` plays that role for Identity and `cmd/api/video.go` for the use cases the HTTP surface needs (`CreateVideoJob`/`GetJobStatus`/`ListUserJobs`/`EnqueueVideoJob`); `cmd/worker/main.go` wires the ones the pipeline needs (`ProcessVideoJob`, which drives `StartProcessing`/`FailJob`, plus `CompleteJob` and `ClearJobIdempotencyKey`). Neither binary switches behaviour on a mode flag, and each requires only the configuration it uses.
5. No bounded context may import another context's `domain` or `application` packages directly. Each context defines and owns its own local value object for any identifier that crosses a boundary (e.g. `identity.UserID` and `video.UserID` are distinct types) — cross-context communication uses domain events or translation at the composition root, never a package shared between contexts' `domain` layers. There is no `pkg/` directory; a shared kernel was considered for the crossing `UserID` and rejected as tighter coupling than this architecture's context-independence goal justifies (see `add-videojob-domain-and-application`'s `design.md` in `openspec/changes/archive/`).

Rules 1–3 for `internal/identity/{domain,application}` and `internal/video/{domain,application}` are each enforced by an automated test (`internal/identity/dependency_rules_test.go`, `internal/video/dependency_rules_test.go`), not just convention.
