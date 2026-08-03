# Flows

## Current: Synchronous Upload Flow

Processing is fully synchronous. The HTTP connection stays open until `ffmpeg` finishes and the ZIP is ready.

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
  └─► GET /api/status    → populate "Arquivos Processados" list

User submits form
  └─► POST /upload       → blocks until processing completes
        on success:
          └─► show download link for zip_path
          └─► GET /api/status    → refresh file list
        on error:
          └─► show error message
```

The JS is embedded in the Go string returned by `getHTMLForm()`. There is no separate build step.

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
| `POST /upload` | Blocks; returns ZIP download link | Returns immediately; returns job ID + status URL | No — kept for compatibility |
| `GET /api/status` | Lists all ZIPs in `outputs/` | Lists outputs (compat) | No — kept for compatibility |
| `GET /download/:filename` | Serves ZIP from `outputs/` | Serves from MinIO (via redirect or proxy) | No |
| `GET /jobs/{id}/status` | Does not exist | Per-job polling endpoint | N/A — new in Phase 6 |
| `POST /jobs` | Does not exist | Canonical async upload endpoint | N/A — new in Phase 6 |
