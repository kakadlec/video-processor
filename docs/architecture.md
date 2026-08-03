# Architecture

## Current Implementation

The entire application lives in a single file, `main.go` (package `main`), with no internal packages.

```
video-processor/
  main.go          # HTTP server, router, all handlers, business logic
  main_test.go     # Integration tests (drive the real handlers via httptest)
  go.mod / go.sum  # Module definition; single direct dependency: gin v1.12
  Dockerfile       # Single-stage build (intentional anti-pattern for study)
  .github/
    workflows/
      ci.yml              # Build & Test, SAST (gosec), Vulnerability Scan
      release-please.yml  # Automated release management
  docs/            # Project documentation (this directory)
  openspec/        # Spec-driven change governance artifacts
```

### Request pipeline (current)

All processing happens synchronously inside the HTTP handler:

```
Browser / client
  │
  └─► POST /upload (multipart)
        │
        ├─ Validate extension (.mp4, .avi, .mov, .mkv, .wmv, .flv, .webm)
        ├─ Save to uploads/<timestamp>_<filename>
        ├─ exec ffmpeg -i <video> -vf fps=1 temp/<timestamp>/frame_%04d.png
        ├─ Glob PNGs from temp/<timestamp>/
        ├─ Write outputs/frames_<timestamp>.zip
        ├─ Remove temp/<timestamp>/   (defer)
        ├─ Remove uploads/<file>      (on success only)
        └─ Return JSON { success, message, zip_path, frame_count, images }
```

The handler returns only after `ffmpeg` completes and the ZIP is written. There is no queue, no worker, and no async signalling.

### State (current)

All state lives in the local filesystem:

| Directory | Purpose | Durability |
|---|---|---|
| `uploads/` | Transient input; deleted on successful processing | Lost on failure |
| `temp/` | Per-request scratch; always cleaned up (defer) | Ephemeral |
| `outputs/` | Durable ZIP results; served by `/download/:filename` | Persistent across restarts |

No database, no cache, no message broker.

### Routes (current)

| Route | Handler | Description |
|---|---|---|
| `GET /` | inline | Returns inline HTML page from `getHTMLForm()` |
| `POST /upload` | `handleVideoUpload` | Accept multipart video, process synchronously |
| `GET /download/:filename` | `handleDownload` | Serve a ZIP from `outputs/` |
| `GET /api/status` | `handleStatus` | JSON list of all ZIPs in `outputs/` |
| `GET /uploads/*` | `gin.Static` | Static file serving of `uploads/` |
| `GET /outputs/*` | `gin.Static` | Static file serving of `outputs/` |

CORS headers (`Access-Control-Allow-Origin: *`) are applied globally.

### Frontend (current)

The web UI is a Go string literal returned by `getHTMLForm()` in `main.go`. It contains:

- Plain HTML form for file selection
- Inline CSS (`<style>` block)
- Vanilla JavaScript using `fetch` to call `POST /upload` and `GET /api/status`

There is no separate frontend build, no Node.js toolchain, and no bundler.

---

## Target Architecture (Planned)

The hackathon requirements include user authentication, asynchronous processing, notifications, and object storage. The planned architecture introduces Domain-Driven Design structure across three bounded contexts.

> **All components in this section are planned and not yet implemented.** Each is labeled with the phase that introduces it.

### Bounded Contexts

| Context | Responsibility |
|---|---|
| **Identity** | User registration, authentication, JWT issuance and verification |
| **Video Processing** | VideoJob lifecycle — creation, queueing, async execution, result storage |
| **Notification** | Reacting to domain events and delivering notifications (email, webhook) |

### Target Package Topology

```
video-processor/
  cmd/
    api/        # HTTP entrypoint — replaces main.go (Phase 3+)
    worker/     # Async frame-extraction worker (Phase 6)
  web/
    index.html  # Extracted from getHTMLForm() (Phase 3)
    styles.css
    app.js
  internal/
    identity/
      domain/         # User aggregate, value objects, repository interface
      application/    # Use cases: RegisterUser, AuthenticateUser, VerifyToken
      infrastructure/ # PostgreSQL adapter, JWT implementation (Phase 2)
    video/
      domain/         # VideoJob aggregate, value objects, events, repository interface
      application/    # Use cases: CreateVideoJob, EnqueueVideoJob, GetJobStatus, …
      infrastructure/ # PostgreSQL adapter, MinIO adapter, RabbitMQ publisher (Phases 3–6)
    notification/
      domain/         # NotificationPreference, DeliveryAttempt
      application/    # Use cases: SendJobCompletionNotification, …
      infrastructure/ # Email adapter, webhook adapter (Phase 7)
  pkg/
    # Shared primitives with no domain knowledge (ID generation, clock)
```

**Migration strategy:** `main.go` remains functional during the transition. New packages are introduced alongside it. Each feature phase migrates one slice of the handler into the appropriate use case and wires it back to the HTTP layer. No big-bang rewrite.

### Infrastructure Components (Planned)

| Component | Role | Introduced |
|---|---|---|
| PostgreSQL | Authoritative state store for users, jobs, outbox | Phase 2–3 |
| Redis | Idempotency keys, rate limiting, status cache, distributed locks | Phase 4 |
| MinIO | Object storage for uploads and ZIP results (S3-compatible) | Phase 5 |
| RabbitMQ | Durable async task queue for job dispatch | Phase 6 |

See [docs/roadmap.md](roadmap.md) for the full phase plan.

### Dependency Rules (Target)

1. `domain` packages MUST NOT import `application`, `infrastructure`, or transport packages.
2. `application` packages depend only on repository/port **interfaces** defined in `domain`.
3. `infrastructure` packages implement interfaces from `domain` and may import third-party drivers.
4. `cmd/api` and `cmd/worker` are the only places where `infrastructure` adapters are instantiated and wired (composition root).
5. No bounded context may import another context's `domain` or `application` packages. Cross-context communication uses domain events or the shared `UserID` value object from `pkg/`.
