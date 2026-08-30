# Flows

## Current: Synchronous Upload Flow

Processing is fully synchronous. The HTTP connection stays open until `ffmpeg` finishes and the ZIP is ready.

### Authentication (Phase 2)

`IDENTITY_POSTGRES_DSN`/`IDENTITY_JWT_SIGNING_KEY` are required at startup, and every step below runs behind bearer-token middleware:

```
Browser                        Go server (cmd/api/main.go / identity.go)     PostgreSQL
  │                                     │                                 │
  │  POST /api/auth/register            │                                 │
  │  { email, password }                │                                 │
  │────────────────────────────────────►│  Hash password (bcrypt)         │
  │                                     │  Persist user                   │
  │                                     │─────────────────────────────────►│
  │◄────────────────────────────────────│                                 │
  │  201 { id, email, created_at }       │                                 │
  │                                     │                                 │
  │  POST /api/auth/login               │                                 │
  │  { email, password }                │                                 │
  │────────────────────────────────────►│  Verify credentials             │
  │                                     │  Issue signed JWT               │
  │◄────────────────────────────────────│                                 │
  │  200 { access_token, expires_at }    │                                 │
  │                                     │                                 │
  │  POST /upload                       │                                 │
  │  Authorization: Bearer <token>      │                                 │
  │────────────────────────────────────►│  Verify token → UserID          │
  │                                     │  (401 and stop here if invalid) │
  │                                     │  ... continues below ...        │
```

The diagram below continues the `POST /upload` request from where the previous one left off (after the bearer token check passes) and omits the `Authorization` header for brevity — in practice every request to `/upload`, `/download/:filename`, and `/api/status` carries a valid bearer token; there is no unauthenticated mode.

```
Browser              cmd/api/video.go       internal/video/application   MinIO bucket
  │                        │                          │                    │
  │  POST /upload           │                          │                   │
  │  (multipart)            │                          │                   │
  │───────────────────────►│                          │                    │
  │                        │  Validate extension       │                   │
  │                        │  SourceStorage.Put →       │                   │
  │                        │  uploads/<uploadID>_<name>,│                   │
  │                        │  hashing the stream        │                   │
  │                        │  (SHA-256) on the same pass│                   │
  │                        │──────────────────────────────────────────────►│
  │                        │  Reserve idempotency key (Redis: UserID+hash) │
  │                        │  reserved=false → poll for the winner's job   │
  │                        │  status (bounded) → return it, or 409 if the  │
  │                        │  bound elapses — no CreateVideoJob below      │
  │                        │  CreateVideoJob            │                  │
  │                        │  (carrying the source key) │                  │
  │                        │───────────────────────────►│                  │
  │                        │  EnqueueVideoJob            │                 │
  │                        │  (pending → queued AND a    │                 │
  │                        │   video_job.queued outbox   │                 │
  │                        │   row, one transaction —    │                 │
  │                        │   the relay publishes it    │                 │
  │                        │   later, out of band)       │                 │
  │                        │───────────────────────────►│                  │
  │                        │  ProcessVideoJob:           │                 │
  │                        │    StartProcessing          │                 │
  │                        │───────────────────────────►│                  │
  │                        │                            │  ffmpeg exec,    │
  │                        │                            │  zip → temp/     │
  │                        │                            │─────────────────►│
  │                        │                            │  (blocks)        │
  │                        │                            │◄─────────────────│
  │                        │    ResultStorage.Put →      │                 │
  │                        │    bucket/frames_<jobID>.zip│                 │
  │                        │    (local zip and downloaded source removed    │
  │                        │     either way, by ProcessVideoJob's defers)   │
  │                        │    Fail if fetch, extraction│                  │
  │                        │    OR storage failed        │                 │
  │                        │    (job stays "processing"  │                 │
  │                        │     if all three succeeded) │                 │
  │                        │───────────────────────────►│                  │
  │                        │    CompleteJob — the result │                 │
  │                        │    is already durable       │                 │
  │                        │───────────────────────────►│                  │
  │                        │  Delete uploads/<uploadID>_<name>              │
  │                        │  (one defer, every exit path, best effort)     │
  │                        │──────────────────────────────────────────────►│
  │◄───────────────────────│                            │                  │
  │  200 { success, zip_path,│                           │                 │
  │        frame_count, images}│                         │                 │
  │                        │                            │                  │
  │  GET /download/<zip_path>│                           │                 │
  │───────────────────────►│                            │                  │
  │                        │  Stat bucket/<zip>          │                 │
  │                        │──────────────────────────────────────────────►│
  │                        │  Presign (local; no network call)             │
  │◄───────────────────────│                            │                  │
  │  200 { url, expires_at }│  Cache-Control: no-store   │                 │
  │                        │                            │                  │
  │  GET <url>  (no Authorization header)                                  │
  │──────────────────────────────────────────────────────────────────────►│
  │◄──────────────────────────────────────────────────────────────────────│
  │  ZIP file — straight from MinIO; the API is not in this path            │
```

The `ffmpeg` invocation and zip packaging themselves run inside `internal/video/infrastructure/ffmpeg`'s `Extractor`, called through the `FrameExtractor` port `ProcessVideoJob` depends on — see `openspec/specs/videojob-execution/spec.md`.

**Key characteristics (current):**
- Content-hash idempotency: identical bytes uploaded twice by the same user reuse the first request's `VideoJob` rather than running `ffmpeg` again (Phase 4, `add-upload-idempotency-keys`) — see `docs/architecture.md`'s Request pipeline section and `openspec/specs/upload-idempotency/spec.md` for the full reserve/finalize/clear protocol. `REDIS_ADDR` is required at startup for this.
- Single HTTP request blocks for the full duration of `ffmpeg` execution — still fully synchronous and in-process. There **is** a queue now, and the outbox relay publishes a `video_job.queued` message for every upload (Phase 6, `add-videojob-source-key-and-outbox-relay`), but **nothing consumes it**: the message names a job this same request has already driven to `completed`/`failed` with its source object deleted. It is a side effect, not a trigger. The worker that turns it into one is `migrate-upload-to-async-processing`.
- No status polling, no notifications, at this HTTP surface — but the `VideoJob` created for the upload does progress through the real `pending → queued → processing → completed`/`failed` state machine internally (see `openspec/specs/videojob-lifecycle/spec.md`), and is queryable via `GET /api/video-jobs/:id` below.
- Nothing the client uploads touches local disk on the way in. The source is streamed straight into the bucket, and the only local copy is the one `ProcessVideoJob` downloads for `ffmpeg`, which it removes on every path (Phase 5, `migrate-upload-storage-to-minio`).
- The source object is deleted on success **and** on failure, through a single `defer` registered the moment the object exists — so the ZIP object is the only artifact meant to survive the request. That deletion is best effort: one call, no retry, logged with the key if it fails. See `docs/operations.md` for the recommended `uploads/`-prefix lifecycle rule that backstops it.
- Authentication (Phase 2) is required; artifact ownership is derived only from the authenticated `UserID`, never from caller-supplied fields, and always from the `VideoJob` row. The `.owner` sidecar mechanism is gone entirely — source objects are never served, so there is nothing to authorize for them.
- The download is a two-step exchange, not a proxied stream (Phase 5, `add-presigned-download-urls`). `GET /download/:filename` authorizes and issues; the client redeems the issued URL against MinIO with no `Authorization` header. Entitlement is evaluated **only** at issuance, since the URL carries no identity — nothing re-checks ownership when it is redeemed, and nothing can withdraw it before its five minutes are up. The `Stat` before signing is not optional: signing is offline and succeeds for a key holding no object, so without it a missing object would surface as MinIO's own `404` instead of this endpoint's byte-identical one.

---

## Target: Asynchronous Processing Flow (Phase 6+)

> **The publishing half of this flow exists; the consuming half does not.** `add-rabbitmq-infrastructure` and `add-videojob-source-key-and-outbox-relay` shipped the broker, the topology, and the outbox relay, so `POST /upload` really does enqueue a job and a message really does reach RabbitMQ — but `cmd/worker` does not exist, nothing dequeues, and `POST /upload` still blocks and still returns a finished result rather than `202`. `migrate-upload-to-async-processing` is the cutover that makes the rest of this diagram true, after Phases 2–5 laid the authentication, persistence, caching, and storage foundations. Phase 3's `wire-videojob-http-endpoints` added a separate, unrelated preview API (`POST /api/video-jobs`, `GET /api/video-jobs/:id`, `GET /api/video-jobs` — see below) that shares the underlying `VideoJob` aggregate but accepts no file content and triggers no processing; it is not this flow and does not become it.

```
Browser              API server (cmd/api)    RabbitMQ    Worker (cmd/worker)    MinIO
  │                        │                    │               │                  │
  │  POST /upload           │                    │               │                  │
  │  (multipart)            │                    │               │                  │
  │───────────────────────►│                    │               │                  │
  │                        │  Validate & store  │               │                  │
  │                        │  upload → MinIO    │               │                  │
  │                        │──────────────────────────────────────────────────────►│
  │                        │  CreateVideoJob    │               │                  │
  │                        │  EnqueueVideoJob   │               │                  │
  │                        │───────────────────►│               │                  │
  │                        │                    │               │                  │
  │◄───────────────────────│                    │               │                  │
  │  202 { job_id,          │                    │               │                  │
  │        status_url }     │                    │               │                  │
  │                        │                    │               │                  │
  │  GET /jobs/{id}/status  │                    │               │                  │
  │  (polling)              │                    │               │                  │
  │───────────────────────►│                    │               │                  │
  │◄───────────────────────│                    │               │                  │
  │  { status: "queued" }   │                    │               │                  │
  │                        │                    │               │                  │
  │                        │                    │  Dequeue job   │                  │
  │                        │                    │───────────────►│                  │
  │                        │                    │               │  Fetch video      │
  │                        │                    │               │  from MinIO       │
  │                        │                    │               │─────────────────►│
  │                        │                    │               │  exec ffmpeg      │
  │                        │                    │               │  Store ZIP        │
  │                        │                    │               │  in MinIO         │
  │                        │                    │               │─────────────────►│
  │                        │                    │               │  CompleteJob      │
  │                        │                    │               │  (DB + event)     │
  │                        │                    │               │                  │
  │  GET /jobs/{id}/status  │                    │               │                  │
  │───────────────────────►│                    │               │                  │
  │◄───────────────────────│                    │               │                  │
  │  { status: "completed", │                    │               │                  │
  │    download_url }       │                    │               │                  │
  │                        │                    │               │                  │
  │  GET <download_url>     │                    │               │                  │
  │───────────────────────►│                    │               │                  │
  │◄───────────────────────│                    │               │                  │
  │  200 { url, expires_at }│                    │               │                  │
  │                        │                    │               │                  │
  │  GET <url>             │                    │               │                  │
  │──────────────────────────────────────────────────────────────────────────────►│
  │◄──────────────────────────────────────────────────────────────────────────────│
  │  ZIP file — as it already works today (Phase 5)                                │
```

**Key characteristics (target):**
- `POST /upload` returns immediately with a job ID and a polling URL.
- Processing runs in a separate `cmd/worker` process, decoupled via RabbitMQ.
- Job state is persisted in PostgreSQL (authoritative); Redis caches status reads.
- Files are stored in MinIO (S3-compatible) instead of the local filesystem.
- On completion, the Notification context is triggered via a domain event over RabbitMQ (Phase 7).
- The download itself is unchanged from today: `download_url` points at `GET /download/:filename`, which authorizes and returns a signed URL the client redeems against MinIO. It is not a redirect — an earlier sketch of this diagram showed one, and `add-presigned-download-urls` rejected that shape because a `fetch` following a cross-origin redirect makes the feature depend on MinIO's CORS configuration.

---

## Preview: VideoJob HTTP API (Phase 3)

Phase 3's `wire-videojob-http-endpoints` wired `internal/video/application`'s `CreateVideoJob`, `GetJobStatus`, and `ListUserJobs` use cases into three new, bearer-authenticated routes, entirely separate from both flows above:

```
POST /api/video-jobs        { "original_filename": "movie.mp4" }
  → 201 { job_id, original_filename, status: "pending", created_at }

GET /api/video-jobs/:id
  → 200 { job_id, status, frame_count, error_reason, storage_key }
  (non-owner or nonexistent id: 404, identical either way)

GET /api/video-jobs?offset=0&limit=20
  → 200 { jobs: [ { job_id, original_filename, status }, … ] }
```

**This is not the Target Asynchronous Processing Flow above, even though it shares the same `VideoJob` aggregate:**
- `POST /api/video-jobs` takes a JSON filename string, not a multipart video file — no file content is ever accepted or stored.
- No code path reachable from these three routes triggers processing: `handleCreateVideoJob`/`handleGetVideoJobStatus`/`handleListVideoJobs` never call `EnqueueVideoJob`/`StartProcessing`/`CompleteJob`/`FailJob`, so every job created via `POST /api/video-jobs` stays `status: "pending"` forever.
- The frontend (`cmd/api/web/app.js`) does not call these routes; it keeps using `POST /upload` exclusively.
- Deliberately not named `/jobs`/`GET /jobs/{id}/status` — those paths are reserved for the real Phase 6 endpoint described above, which accepts a real upload and enqueues real processing. Reusing the name here would misrepresent this preview API as that endpoint.

**`EnqueueVideoJob`/`StartProcessing`/`CompleteJob`/`FailJob` do exist now**, added by `migrate-ffmpeg-execution-to-videojob-application`, but they're driven from `POST /upload`, not from these routes. Because `POST /upload` also calls `CreateVideoJob` (see the Synchronous Upload Flow above), `GET /api/video-jobs`/`GET /api/video-jobs/:id` now legitimately show `completed`/`failed` jobs for a user who has used `/upload`, alongside any still-`pending` jobs created directly via `POST /api/video-jobs` itself — both are the same `VideoJob` aggregate in the same repository, scoped by owner the same way. See `openspec/specs/videojob-http-api/spec.md`'s "Listing includes jobs created outside this API" scenario.

See `openspec/specs/videojob-http-api/spec.md` for the full contract, `openspec/specs/videojob-lifecycle/spec.md` for the four transition use cases, and `openspec/specs/videojob-execution/spec.md` for how `POST /upload` drives them — `EnqueueVideoJob` from the handler itself, the other three through `ProcessVideoJob`.

---

## Frontend Interaction Sequences

### Current (`cmd/api/web/index.html`, `cmd/api/web/styles.css`, `cmd/api/web/app.js`, served via `go:embed`)

```
Page load
  └─► Read access token from localStorage, if any
  └─► GET /api/status  (with Authorization header if a token is present)
        → populate "Arquivos Processados" list

User clicks Entrar/Cadastrar
  └─► POST /api/auth/login or /api/auth/register
        on success: store access_token in localStorage, refresh file list
        on error:   show error message

User submits upload form
  └─► POST /upload  (with Authorization header if a token is present)
        → blocks until processing completes
        on success:
          └─► show a "Download ZIP" button
                └─► GET /download/<zip_path> with the Authorization
                    header → { url, expires_at }
                └─► navigate an anchor at that url; MinIO serves the ZIP
                    (the download attribute is ignored cross-origin — the
                    attachment comes from the signed disposition)
          └─► GET /api/status    → refresh file list
        on error:
          └─► show error message
        on 401 (token expired/invalid):
          └─► clear the stored token, prompt to log in again
```

`cmd/api/web/index.html`, `cmd/api/web/styles.css`, and `cmd/api/web/app.js` are embedded into the binary via `go:embed` and served at `GET /`, `GET /styles.css`, and `GET /app.js` respectively. There is no separate build step. The login/register panel is always present and must be used to obtain a bearer token before uploads or status/download requests succeed.

### After Phase 6 (async API)

```
Page load
  └─► GET /api/status    → populate existing results (backward-compat endpoint)

User submits form
  └─► POST /upload       → returns immediately with { job_id, status_url }
        └─► poll GET /jobs/{job_id}/status every N seconds
              on "completed":
                └─► show download link (issued by GET /download/:filename,
                    as it already is today)
                └─► stop polling
              on "failed":
                └─► show error message
                └─► stop polling
```

`cmd/api/web/app.js` is updated in Phase 6 to implement the polling loop. `POST /upload` and `GET /api/status` remain available for backward compatibility.

---

## API Contract Compatibility During Migration

| Endpoint | Current behavior | Phase 6 behavior | Removed? |
|---|---|---|---|
| `POST /api/auth/register` | Creates a user (Phase 2) | Unchanged | No |
| `POST /api/auth/login` | Issues a bearer JWT (Phase 2) | Unchanged | No |
| `POST /upload` | Blocks; returns ZIP download link; requires a bearer token | Returns immediately; returns job ID + status URL | No — kept for compatibility |
| `GET /api/status` | Lists the caller's `completed` jobs' results, with size and timestamp read from MinIO | Unchanged (compat) | No — kept for compatibility |
| `GET /download/:filename` | Issues a 5-minute presigned MinIO URL (`{ url, expires_at }`); owner-only (a non-owner gets the same 404 as a missing key), and the API never carries the bytes | Unchanged | No |
| `GET /jobs/{id}/status` | Does not exist | Per-job polling endpoint | N/A — new in Phase 6 |
| `POST /jobs` | Does not exist | Canonical async upload endpoint | N/A — new in Phase 6 |
| `POST /api/video-jobs`, `GET /api/video-jobs/:id`, `GET /api/video-jobs` | Preview job-lifecycle API (Phase 3); JSON metadata only, no processing trigger | Unrelated to this migration — not the same endpoints as `/jobs` above | Not applicable — separate, unreplaced capability |
