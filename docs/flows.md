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

The diagram below continues the `POST /upload` request from where the previous one left off (after the bearer token check passes) and omits the `Authorization` header for brevity — in practice every request to `/upload`, `/download/:filename`, `/api/status`, and `/uploads/*` carries a valid bearer token; there is no unauthenticated mode.

```
Browser              cmd/api/video.go       internal/video/application   Filesystem
  │                        │                          │                    │
  │  POST /upload           │                          │                   │
  │  (multipart)            │                          │                   │
  │───────────────────────►│                          │                    │
  │                        │  Validate extension       │                   │
  │                        │  Save to uploads/, hashing│                   │
  │                        │  the stream (SHA-256)      │                  │
  │                        │──────────────────────────────────────────────►│
  │                        │  Reserve idempotency key (Redis: UserID+hash) │
  │                        │  reserved=false → poll for the winner's job   │
  │                        │  status (bounded) → return it, or 409 if the  │
  │                        │  bound elapses — no CreateVideoJob below      │
  │                        │  CreateVideoJob            │                  │
  │                        │───────────────────────────►│                  │
  │                        │  ProcessVideoJob:           │                 │
  │                        │    Enqueue → StartProcessing│                 │
  │                        │───────────────────────────►│                  │
  │                        │                            │  ffmpeg exec,    │
  │                        │                            │  zip → temp/     │
  │                        │                            │─────────────────►│
  │                        │                            │  (blocks)        │
  │                        │                            │◄─────────────────│
  │                        │    ResultStorage.Put →      │                 │
  │                        │    bucket/frames_<jobID>.zip│                 │
  │                        │    (local zip removed either way)             │
  │                        │    Fail if extraction OR    │                 │
  │                        │    storage failed           │                 │
  │                        │    (job stays "processing"  │                 │
  │                        │     if both succeeded)      │                 │
  │                        │───────────────────────────►│                  │
  │                        │  Remove uploads/<file>      │                 │
  │                        │    CompleteJob — the result │                 │
  │                        │    is already durable       │                 │
  │                        │───────────────────────────►│                  │
  │◄───────────────────────│                            │                  │
  │  200 { success, zip_path,│                           │                 │
  │        frame_count, images}│                         │                 │
  │                        │                            │                  │
  │  GET /download/<zip_path>│                           │                 │
  │───────────────────────►│                            │                  │
  │◄───────────────────────│  Stream bucket/<zip>       │                  │
  │  ZIP file               │                           │                  │
```

The `ffmpeg` invocation and zip packaging themselves run inside `internal/video/infrastructure/ffmpeg`'s `Extractor`, called through the `FrameExtractor` port `ProcessVideoJob` depends on — see `openspec/specs/videojob-execution/spec.md`.

**Key characteristics (current):**
- Content-hash idempotency: identical bytes uploaded twice by the same user reuse the first request's `VideoJob` rather than running `ffmpeg` again (Phase 4, `add-upload-idempotency-keys`) — see `docs/architecture.md`'s Request pipeline section and `openspec/specs/upload-idempotency/spec.md` for the full reserve/finalize/clear protocol. `REDIS_ADDR` is required at startup for this.
- Single HTTP request blocks for the full duration of `ffmpeg` execution — still fully synchronous, in-process, no queue or worker (that's Phase 6).
- No status polling, no notifications, at this HTTP surface — but the `VideoJob` created for the upload does progress through the real `pending → queued → processing → completed`/`failed` state machine internally (see `openspec/specs/videojob-lifecycle/spec.md`), and is queryable via `GET /api/video-jobs/:id` below.
- On success, the original upload file is deleted; the ZIP object in the MinIO bucket is the only durable artifact (Phase 5, `migrate-result-storage-to-minio`).
- On extraction or storage failure, the original upload is NOT deleted (known gap; see `TestProcessing_Failure_LeavesUploadedFileBehind`). This does not hold for a failure in the later `CompleteJob` step — the upload has already been removed by the time that runs.
- Authentication (Phase 2) is required; artifact ownership is derived only from the authenticated `UserID`, never from caller-supplied fields. For results it comes from the `VideoJob` row (`/download`, `/api/status`); for uploads still on disk it comes from the `.owner` sidecar the `/uploads` static mount checks.

---

## Target: Asynchronous Processing Flow (Phase 6+)

> **This flow does not yet exist.** It is introduced in Phase 6 (`implement-rabbitmq-and-worker`), after Phases 2–5 lay the authentication, persistence, caching, and storage foundations. Phase 3's `wire-videojob-http-endpoints` added a separate, unrelated preview API (`POST /api/video-jobs`, `GET /api/video-jobs/:id`, `GET /api/video-jobs` — see below) that shares the underlying `VideoJob` aggregate but accepts no file content and triggers no processing; it is not this flow and does not become it.

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
  │◄───────────────────────│   Presigned MinIO  │               │                  │
  │  ZIP file               │   URL redirect     │               │                  │
```

**Key characteristics (target):**
- `POST /upload` returns immediately with a job ID and a polling URL.
- Processing runs in a separate `cmd/worker` process, decoupled via RabbitMQ.
- Job state is persisted in PostgreSQL (authoritative); Redis caches status reads.
- Files are stored in MinIO (S3-compatible) instead of the local filesystem.
- On completion, the Notification context is triggered via a domain event over RabbitMQ (Phase 7).

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

See `openspec/specs/videojob-http-api/spec.md` for the full contract and `openspec/specs/videojob-execution/spec.md` for how `POST /upload` now drives those four transition use cases.

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
          └─► show a "Download ZIP" button (fetches the file with the
              Authorization header and saves it via a Blob, since a
              plain link can't carry a custom header)
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
                └─► show download link (presigned MinIO URL)
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
| `GET /download/:filename` | Streams the ZIP from MinIO; owner-only (a non-owner gets the same 404 as a missing key) | Presigned URL instead of a proxied stream (`add-presigned-download-urls`) | No |
| `GET /jobs/{id}/status` | Does not exist | Per-job polling endpoint | N/A — new in Phase 6 |
| `POST /jobs` | Does not exist | Canonical async upload endpoint | N/A — new in Phase 6 |
| `POST /api/video-jobs`, `GET /api/video-jobs/:id`, `GET /api/video-jobs` | Preview job-lifecycle API (Phase 3); JSON metadata only, no processing trigger | Unrelated to this migration — not the same endpoints as `/jobs` above | Not applicable — separate, unreplaced capability |
