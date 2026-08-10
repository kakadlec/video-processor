# Flows

## Current: Synchronous Upload Flow

Processing is fully synchronous. The HTTP connection stays open until `ffmpeg` finishes and the ZIP is ready.

### Authentication (optional, Phase 2)

When `IDENTITY_POSTGRES_DSN`/`IDENTITY_JWT_SIGNING_KEY` are configured, every step below runs behind bearer-token middleware:

```
Browser                        Go server (main.go / identity.go)     PostgreSQL
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

Without those two env vars set, identity is disabled: `/api/auth/*` don't exist, and `/upload`, `/download/:filename`, `/api/status`, `/uploads/*`, `/outputs/*` all run exactly as shown below with no auth check.

```
Browser                   Go server (main.go)              Filesystem
  │                             │                               │
  │  POST /upload (multipart)   │                               │
  │────────────────────────────►│                               │
  │                             │  Validate extension           │
  │                             │  Save to uploads/             │
  │                             │──────────────────────────────►│
  │                             │                               │
  │                             │  exec ffmpeg -i <video>       │
  │                             │    -vf fps=1                  │
  │                             │    temp/<ts>/frame_%04d.png   │
  │                             │──────────────────────────────►│
  │                             │  (blocks until complete)      │
  │                             │                               │
  │                             │  Glob PNGs from temp/<ts>/    │
  │                             │◄──────────────────────────────│
  │                             │                               │
  │                             │  Write outputs/frames_<ts>.zip│
  │                             │──────────────────────────────►│
  │                             │                               │
  │                             │  Remove temp/<ts>/            │
  │                             │  Remove uploads/<file>        │
  │                             │──────────────────────────────►│
  │                             │                               │
  │◄────────────────────────────│                               │
  │  200 { success, zip_path,   │                               │
  │        frame_count, images }│                               │
  │                             │                               │
  │  GET /download/<zip_path>   │                               │
  │────────────────────────────►│                               │
  │◄────────────────────────────│  Serve outputs/<zip>          │
  │  ZIP file                   │                               │
```

**Key characteristics (current):**
- Single HTTP request blocks for the full duration of `ffmpeg` execution.
- No job ID, no status polling, no notifications.
- On success, the original upload file is deleted; the ZIP in `outputs/` is the only durable artifact.
- On failure, the original upload is NOT deleted (known gap; see `TestProcessing_Failure_LeavesUploadedFileBehind`).
- Authentication (Phase 2) is optional and off by default; when enabled, artifact ownership is derived only from the authenticated `UserID`, never from caller-supplied fields, and enforced identically on `/download`, `/api/status`, and the `/uploads`/`/outputs` static mounts.

---

## Target: Asynchronous Processing Flow (Phase 6+)

> **This flow does not yet exist.** It is introduced in Phase 6 (`implement-rabbitmq-and-worker`), after Phases 2–5 lay the authentication, persistence, caching, and storage foundations.

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

## Frontend Interaction Sequences

### Current (inline HTML/CSS/JS in `getHTMLForm()`)

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

The JS is embedded in the Go string returned by `getHTMLForm()`. There is no separate build step. The login/register panel is optional at the UI level too — it works the same whether or not the server has identity configured; without it, requests simply carry no `Authorization` header and the server accepts them unauthenticated.

### After Phase 3 (static files extracted to `web/`)

`GET /` continues to return the same HTML page, but the server now serves it from `web/index.html` via a static file handler. `web/styles.css` and `web/app.js` are served from `GET /web/styles.css` and `GET /web/app.js`. Functionality is identical; the code just lives in dedicated files instead of a Go string literal.

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

`web/app.js` is updated in Phase 6 to implement the polling loop. `POST /upload` and `GET /api/status` remain available for backward compatibility.

---

## API Contract Compatibility During Migration

| Endpoint | Current behavior | Phase 6 behavior | Removed? |
|---|---|---|---|
| `POST /api/auth/register` | Creates a user (Phase 2, only when identity is configured) | Unchanged | No |
| `POST /api/auth/login` | Issues a bearer JWT (Phase 2, only when identity is configured) | Unchanged | No |
| `POST /upload` | Blocks; returns ZIP download link; requires a bearer token when identity is configured | Returns immediately; returns job ID + status URL | No — kept for compatibility |
| `GET /api/status` | Lists ZIPs in `outputs/`; scoped to the caller's own uploads when identity is configured | Lists outputs (compat) | No — kept for compatibility |
| `GET /download/:filename` | Serves ZIP from `outputs/`; owner-only when identity is configured (a non-owner gets the same 404 as a missing file) | Serves from MinIO (via redirect or proxy) | No |
| `GET /jobs/{id}/status` | Does not exist | Per-job polling endpoint | N/A — new in Phase 6 |
| `POST /jobs` | Does not exist | Canonical async upload endpoint | N/A — new in Phase 6 |
