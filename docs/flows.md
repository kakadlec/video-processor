# Flows

## Current: Asynchronous Upload Flow

Processing is asynchronous. `POST /upload` stores the bytes, records the job, and answers `202` with a status URL; a separate process — `cmd/worker` — consumes the dispatch and does the work. The client polls, then downloads.

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
The diagram below continues the `POST /upload` request from where the previous one left off (after the bearer token check passes) and omits the `Authorization` header for brevity — in practice every request to `/upload`, `/api/video-jobs/:id`, `/download/:filename`, and `/api/status` carries a valid bearer token; there is no unauthenticated mode.

### Submission — `POST /upload` (`cmd/api`, in-request)

Nothing is extracted here. The request stores the bytes, records the job, and acknowledges.

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
  │                        │  (bounded) → 202 naming it, or 409 if the      │
  │                        │  bound elapses — no CreateVideoJob below      │
  │                        │  CreateVideoJob            │                  │
  │                        │  (carrying the source key  │                  │
  │                        │   AND the content hash)    │                  │
  │                        │───────────────────────────►│                  │
  │                        │  EnqueueVideoJob            │                 │
  │                        │  (pending → queued AND a    │                 │
  │                        │   video_job.queued.v2 outbox│                 │
  │                        │   row, one transaction —    │                 │
  │                        │   the relay publishes it    │                 │
  │                        │   later, out of band)       │                 │
  │                        │───────────────────────────►│                  │
  │                        │  Finalize the reservation   │                 │
  │                        │  (after the enqueue, never  │                 │
  │                        │   before)                   │                 │
  │◄───────────────────────│                            │                  │
  │  202 { job_id,          │                            │                 │
  │        status: "queued",│                            │                 │
  │        status_url }     │                            │                 │
  │                        │                            │                  │
  │  The source object is STILL IN THE BUCKET here — it is the worker's     │
  │  input. The request deletes it only on the paths where the job never    │
  │  reached `queued` (duplicate, CreateVideoJob error, EnqueueVideoJob     │
  │  error), through one defer guarded on that fact.                        │
```

### Processing — `cmd/worker` (out of band)

```
RabbitMQ            cmd/worker/main.go     internal/video/application   MinIO bucket
  │                        │                          │                    │
  │  deliver (prefetch 1)  │                          │                    │
  │───────────────────────►│                          │                    │
  │                        │  ParseJobQueuedMessage    │                    │
  │                        │  (undecodable → Reject →  │                    │
  │                        │   dead-letter exchange)   │                    │
  │                        │  ProcessVideoJob:          │                   │
  │                        │    StartProcessing →       │                   │
  │                        │    ClaimForProcessing      │                   │
  │                        │    UPDATE … WHERE id=$1     │                  │
  │                        │      AND status='queued'   │                   │
  │                        │      RETURNING lease_epoch │                   │
  │                        │───────────────────────────►│                  │
  │                        │    no row → ErrJobClaimLost │                  │
  │                        │    → Reject → DLQ, nothing  │                  │
  │                        │      touched at all         │                  │
  │                        │    acquire/renew Redis lease│                  │
  │                        │      at returned epoch      │                  │
  │                        │    SourceStorage.Get →      │                  │
  │                        │    temp/<jobID>_source      │                  │
  │                        │◄──────────────────────────────────────────────│
  │                        │                            │  ffmpeg exec,    │
  │                        │                            │  zip → temp/     │
  │                        │                            │  (blocks; this   │
  │                        │                            │   is the WORKER's│
  │                        │                            │   filesystem)    │
  │                        │    ResultStorage.Put →      │                  │
  │                        │    bucket/frames_<jobID>.zip│                  │
  │                        │──────────────────────────────────────────────►│
  │                        │    FailJob if fetch,        │                  │
  │                        │    extraction OR storage    │                  │
  │                        │    failed, fenced by epoch  │                  │
  │                        │───────────────────────────►│                  │
  │                        │  ErrJobFenced: Reject → DLQ,│                  │
  │                        │    keep source and release  │                  │
  │                        │    no lease                 │                  │
  │                        │  on applied failure:        │                  │
  │                        │    delete the source object │                  │
  │                        │    clear the idempotency key│                  │
  │                        │    (rebuilt from UserID +   │                  │
  │                        │     persisted content_hash) │                  │
  │                        │    release held lease → Ack │                  │
  │                        │  on success:                │                  │
  │                        │    CompleteJob at the held  │                  │
  │                        │    epoch, retried 4×        │                  │
  │                        │    with backoff on a context│                  │
  │                        │    detached from shutdown   │                  │
  │                        │───────────────────────────►│                  │
  │                        │    still failing → Reject → │                  │
  │                        │    DLQ, source KEPT, result │                  │
  │                        │    StorageKey logged        │                  │
  │                        │    delete the source object │                  │
  │                        │──────────────────────────────────────────────►│
  │◄───────────────────────│    release lease → Ack      │                  │
  │  ack / reject           │                            │                  │
```

An acknowledgement asserts that a **terminal outcome exists**, not that processing succeeded or that this call necessarily applied it. An applied failure is acked with cleanup; an identical failure already present is also acked, but this later run performs no second cleanup because it did not apply the outcome. A fenced run is rejected and performs no source, idempotency, or lease cleanup because this actor did not apply the terminal outcome.

A sibling sweeper scans bounded batches of `processing` rows every 60 seconds. After two successful observations that no lease is held at the same epoch, it conditionally returns the row to `queued`, increments the epoch, and writes a fresh outbox dispatch in one transaction. After three requeues, or for a legacy row with no source key, it applies terminal abandonment instead. A Redis query error resets prior confirmation and takes over nothing.

### Polling and download

```
Browser              cmd/api/video.go                             MinIO bucket
  │                        │                                          │
  │  GET <status_url>       │   = GET /api/video-jobs/<job_id>         │
  │  (2s, backing off to 10s)                                          │
  │───────────────────────►│                                          │
  │◄───────────────────────│                                          │
  │  200 { status: "queued" | "processing" }  → keep polling           │
  │  429 + Retry-After                        → back off, KEEP polling │
  │  200 { status: "failed", error_reason }   → stop, show the reason  │
  │  200 { status: "completed", frame_count, storage_key } → stop      │
  │                        │                                          │
  │  GET /download/<storage_key>                                       │
  │───────────────────────►│                                          │
  │                        │  entitlement from the VideoJob row       │
  │                        │  Stat bucket/<zip>                        │
  │                        │─────────────────────────────────────────►│
  │                        │  Presign (local; no network call)         │
  │◄───────────────────────│                                          │
  │  200 { url, expires_at }│  Cache-Control: no-store                 │
  │                        │                                          │
  │  GET <url>  (no Authorization header)                              │
  │──────────────────────────────────────────────────────────────────►│
  │◄──────────────────────────────────────────────────────────────────│
  │  ZIP file — straight from MinIO; the API is not in this path        │
```

The `ffmpeg` invocation and zip packaging themselves run inside `internal/video/infrastructure/ffmpeg`'s `Extractor`, called through the `FrameExtractor` port `ProcessVideoJob` depends on — see `openspec/specs/videojob-execution/spec.md` and `openspec/specs/videojob-worker/spec.md`.

**Key characteristics (current):**
- `POST /upload` returns `202` as soon as the job is `queued`. The status code acknowledges the **submission**, not the work — a client must not read success from it. There is no frame count, result key, or download URL in that body, because none exists yet.
- **Uploads are not processed unless at least one worker is running.** With `cmd/api` alone, jobs accumulate in `queued` and the API reports exactly that.
- Content-hash idempotency: identical bytes uploaded twice by the same user reuse the first request's `VideoJob` rather than running `ffmpeg` again (Phase 4, `add-upload-idempotency-keys`) — see `docs/architecture.md`'s Request pipeline section and `openspec/specs/upload-idempotency/spec.md`. `REDIS_ADDR` is required at startup for this. A duplicate is answered with the **same** `202` shape naming the existing job, whatever state that job is in, so a client needs no duplicate branch and learns the difference on its first poll.
- **Clearing a failed job's key belongs to whichever worker applied the failure**, not the handler — either the consumer or the abandonment sweeper. The key is rebuilt from the job's `UserID` and persisted `content_hash`, and deleted only if it still names that job. Fenced and already-present outcomes clear nothing; the reservation token is never persisted.
- Dispatch is **at-least-once**, and `queued → processing` is an atomic conditional PostgreSQL claim (`WHERE id = $1 AND status = 'queued' RETURNING lease_epoch`). Of two consumers handed the same message exactly one wins. Worker death after the claim is recovered separately: an epoch-scoped Redis lease supplies liveness, the sweeper advances the PostgreSQL epoch and re-dispatches, and terminal writes from the previous holder are fenced.
- The client learns everything through `GET /api/video-jobs/:id`, polling from 2 s and backing off to a 10 s ceiling. Those polls share one per-user rate-limit budget with the submission and the download issuance, which is why the interval is chosen against the default 60/60s rather than for responsiveness. A `429` is a back-off signal, not a job failure.
- Nothing the client uploads touches local disk on the way in, and `cmd/api` has no `temp/` directory at all any more. The only local copy is the one `ProcessVideoJob` downloads for `ffmpeg`, on the **worker's** filesystem, removed on every path (Phase 5, `migrate-upload-storage-to-minio`).
- The source object belongs to the request until its job commits as `queued`; afterwards only a consumer that applied a terminal result or the sweeper that applied abandonment deletes it. Fenced runs delete nothing. Jobs never dispatched and best-effort cleanup failures can still leak, so the bucket's `uploads/`-prefix expiration rule remains the only exhaustive reclamation guarantee. See `docs/operations.md`.
- Authentication (Phase 2) is required on every step of the path; artifact ownership is derived only from the authenticated `UserID`, never from caller-supplied fields, and always from the `VideoJob` row. `cmd/worker` makes **no** access-control decision — it acts on an internal dispatch, never on behalf of a caller — and holds no Identity configuration. A job identifier alone grants nothing.
- The download is a two-step exchange, not a proxied stream (Phase 5, `add-presigned-download-urls`). `GET /download/:filename` authorizes and issues; the client redeems the issued URL against MinIO with no `Authorization` header. Entitlement is evaluated **only** at issuance, since the URL carries no identity — nothing re-checks ownership when it is redeemed, and nothing can withdraw it before its five minutes are up. The `Stat` before signing is not optional: signing is offline and succeeds for a key holding no object, so without it a missing object would surface as MinIO's own `404` instead of this endpoint's byte-identical one.

---

## What is still missing (Phase 7)

- **Notifications.** On completion the Notification context is meant to be triggered by a `VideoJobCompleted` / `VideoJobFailed` domain event over RabbitMQ. Neither event is written anywhere yet: `CompleteJob` and `FailJob` persist through `Repository.Update`, which is deliberately not an outbox writer, so Phase 7 decides their shape on its own terms.

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

**This is not the upload flow above, even though it shares the same `VideoJob` aggregate:**
- `POST /api/video-jobs` takes a JSON filename string, not a multipart video file — no file content is ever accepted or stored.
- No code path reachable from these three routes triggers processing: `handleCreateVideoJob`/`handleGetVideoJobStatus`/`handleListVideoJobs` never call `EnqueueVideoJob`/`StartProcessing`/`CompleteJob`/`FailJob`, so every job created via `POST /api/video-jobs` stays `status: "pending"` forever.
- The frontend (`cmd/api/web/app.js`) does not call `POST /api/video-jobs` or `GET /api/video-jobs`. It does call `GET /api/video-jobs/:id` — see below.
- Deliberately not named `/jobs`. **No `/jobs` endpoint was ever introduced**, and the asynchronous cutover did not add one: `POST /upload` is the async submission endpoint, because it is the endpoint that receives the bytes. `POST /api/video-jobs` takes a filename with no source key, and `VideoJob.Enqueue` rejects a job without one, so it still has nothing to enqueue.
- A `pending` status here does **not** mean "waiting for a worker". A job awaiting a worker is `queued`; `pending` means it was never dispatched at all.

**`GET /api/video-jobs/:id` has a second role now**, though: it is the endpoint a `POST /upload` response's `status_url` names, so the frontend does call it — as the async flow's status channel. Its contract is unchanged, and a caller does not have to know which entry point created a job in order to poll it. `POST /api/video-jobs` and `GET /api/video-jobs` are unaffected.

**`EnqueueVideoJob`/`StartProcessing`/`CompleteJob`/`FailJob` do exist now**, added by `migrate-ffmpeg-execution-to-videojob-application`, but they're driven from `POST /upload` and `cmd/worker`, not from these routes. Because `POST /upload` also calls `CreateVideoJob` (see the Synchronous Upload Flow above), `GET /api/video-jobs`/`GET /api/video-jobs/:id` now legitimately show `completed`/`failed` jobs for a user who has used `/upload`, alongside any still-`pending` jobs created directly via `POST /api/video-jobs` itself — both are the same `VideoJob` aggregate in the same repository, scoped by owner the same way. See `openspec/specs/videojob-http-api/spec.md`'s "Listing includes jobs created outside this API" scenario.

See `openspec/specs/videojob-http-api/spec.md` for the full contract, `openspec/specs/videojob-lifecycle/spec.md` for the transition use cases, `openspec/specs/videojob-execution/spec.md` for `POST /upload`'s acknowledgement contract, and `openspec/specs/videojob-worker/spec.md` for what the worker does with a dispatch — `EnqueueVideoJob` runs in the handler, `StartProcessing`/`FailJob` inside `ProcessVideoJob`, and `CompleteJob` in the worker itself.

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
        → 202 { job_id, status, status_url } — returns immediately
  └─► poll GET <status_url>, starting at 2s and backing off ×1.5
      to a 10s ceiling (an aria-live region announces progress)
        on 429:
          └─► honour Retry-After UNCAPPED (the limiter's window is
              configurable and routinely exceeds the 10s ceiling; capping
              it would just earn another 429), lengthen the ordinary
              interval underneath, and keep polling — a throttled poll is
              not a failed job
        on "queued" / "processing":
          └─► keep polling
        on "completed":
          └─► show a "Download ZIP" button
                └─► GET /download/<storage_key> with the Authorization
                    header → { url, expires_at }
                └─► navigate an anchor at that url; MinIO serves the ZIP
                    (the download attribute is ignored cross-origin — the
                    attachment comes from the signed disposition)
          └─► GET /api/status    → refresh file list
          └─► stop polling
        on "failed":
          └─► show the job's error_reason
          └─► stop polling
        on 401 (token expired/invalid):
          └─► clear the stored token, prompt to log in again
```

`cmd/api/web/index.html`, `cmd/api/web/styles.css`, and `cmd/api/web/app.js` are embedded into the binary via `go:embed` and served at `GET /`, `GET /styles.css`, and `GET /app.js` respectively. There is no separate build step. The login/register panel is always present and must be used to obtain a bearer token before uploads or status/download requests succeed.

---

## API Contract Changes at the Async Cutover

`migrate-upload-to-async-processing` changed exactly one endpoint's contract, and that change is **breaking** for any client that read a result out of `POST /upload`.

| Endpoint | Before the cutover | After | Removed? |
|---|---|---|---|
| `POST /api/auth/register` | Creates a user (Phase 2) | Unchanged | No |
| `POST /api/auth/login` | Issues a bearer JWT (Phase 2) | Unchanged | No |
| `POST /upload` | Blocked for the whole `ffmpeg` run; `200 { success, message, zip_path, frame_count, images }` | **`202 { job_id, status, status_url }`.** Same path, same method, same bearer gate, same multipart form, same extension validation — only the response changed. Invalid submissions are still rejected synchronously with the status they used before | No |
| `GET /api/video-jobs/:id` | Preview read (Phase 3) | Unchanged contract, new role: the status channel `POST /upload`'s `status_url` names | No |
| `GET /api/status` | Lists the caller's `completed` jobs' results, with size and timestamp read from MinIO | Unchanged | No |
| `GET /download/:filename` | Issues a 5-minute presigned MinIO URL (`{ url, expires_at }`); owner-only (a non-owner gets the same 404 as a missing key), and the API never carries the bytes | Unchanged | No |
| `POST /api/video-jobs`, `GET /api/video-jobs` | Preview job-lifecycle API (Phase 3); JSON metadata only, no processing trigger | Unchanged — still no processing trigger | No |

**No `/jobs` or `GET /jobs/{id}/status` endpoint was introduced.** Earlier drafts of this document reserved those paths for the async flow; the cutover put the async contract on `POST /upload` and `GET /api/video-jobs/:id` instead, because those are the endpoints that already receive the bytes and report the status.
