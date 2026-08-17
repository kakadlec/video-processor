# Architecture

## Current Implementation

The video-processing HTTP surface lives in `cmd/api/main.go` (package `main`) — Phase 3's `extract-cmd-api-entrypoint` moved it there from the repo root. Phase 2 added the first real internal package: `internal/identity`, an explicit DDD slice (domain/application/infrastructure) wired into `cmd/api`'s composition root rather than a package of its own. Phase 3's `wire-videojob-http-endpoints` wired `internal/video` in the same way, behind a preview `/api/video-jobs` HTTP surface, and `migrate-ffmpeg-execution-to-videojob-application` then cut `POST /upload`'s own `ffmpeg` execution over to run through that same `internal/video` application layer — see the Routes table below.

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
      auto-update-pr-branches.yml # Keeps open PR branches current with their base
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
        ├─ Save to uploads/<uploadID>_<filename>
        ├─ CreateVideoJob                          (application, status: pending)
        ├─ ProcessVideoJob:
        │    ├─ EnqueueVideoJob                     (pending → queued)
        │    ├─ StartProcessing                      (queued → processing)
        │    ├─ FrameExtractor.ExtractFrames (infrastructure/ffmpeg):
        │    │    ├─ exec ffmpeg -i <video> -vf fps=1 temp/<jobID>/frame_%04d.png
        │    │    ├─ Glob PNGs from temp/<jobID>/
        │    │    ├─ Write outputs/frames_<jobID>.zip
        │    │    └─ Remove temp/<jobID>/            (defer, always)
        │    └─ CompleteJob or FailJob                (processing → completed/failed)
        ├─ Remove uploads/<file>                    (on success only)
        └─ Return JSON { success, message, zip_path, frame_count, images }
```

The handler returns only after the full sequence completes and the ZIP is written. There is still no queue, no worker, and no async signalling — see `openspec/specs/videojob-execution/spec.md` for the full contract. The `VideoJob` created for the upload is queryable afterward via `GET /api/video-jobs/:id`/`GET /api/video-jobs` (see the preview API note below).

### State (current)

Video-processing artifacts still live in the local filesystem; user accounts live in PostgreSQL:

| Store | Purpose | Durability |
|---|---|---|
| `uploads/` | Transient input; deleted on successful processing | Lost on failure |
| `temp/` | Per-request scratch; always cleaned up (defer) | Ephemeral |
| `outputs/` | Durable ZIP results; served by `/download/:filename` | Persistent across restarts |
| `uploads/*.owner`, `outputs/*.owner` | Sidecar files recording which authenticated `UserID` owns an artifact | Same lifecycle as the artifact they accompany |
| PostgreSQL `users` table | User accounts (normalized email, password hash) | Persistent, external to the container |
| PostgreSQL `video_jobs` table | `VideoJob` rows — created both by `POST /upload` (this pipeline) and by `POST /api/video-jobs` (the preview API below); the two share the same repository and owner-scoping | Persistent, external to the container |

No cache, no message broker.

### Routes (current)

| Route | Handler | Description |
|---|---|---|
| `GET /` | inline | Serves embedded `cmd/api/web/index.html` (via `go:embed`); always public |
| `POST /api/auth/register` | `handleRegister` | Create a user account |
| `POST /api/auth/login` | `handleLogin` | Authenticate and issue a bearer JWT |
| `POST /upload` | `handleVideoUpload` | Accept multipart video, process synchronously; requires a bearer token |
| `GET /download/:filename` | `handleDownload` | Serve a ZIP from `outputs/`; owner-only |
| `GET /api/status` | `handleStatus` | JSON list of ZIPs in `outputs/`; scoped to the caller's own uploads |
| `GET /uploads/*` | `gin.Static` | Static file serving of `uploads/`; owner-only; `.owner` sidecar files are never served regardless |
| `GET /outputs/*` | `gin.Static` | Static file serving of `outputs/`; owner-only; `.owner` sidecar files are never served regardless |
| `POST /api/video-jobs` | `handleCreateVideoJob` | Create a `VideoJob` record (JSON `original_filename`, no file content); requires a bearer token; preview API, see below |
| `GET /api/video-jobs/:id` | `handleGetVideoJobStatus` | Get a `VideoJob`'s status; owner-only (non-owner and nonexistent both 404) |
| `GET /api/video-jobs` | `handleListVideoJobs` | Paginated list of the caller's own `VideoJob`s |

`IDENTITY_POSTGRES_DSN`, `IDENTITY_JWT_SIGNING_KEY`, and `VIDEO_POSTGRES_DSN` are all required at startup — the API composition root fails to start otherwise. There is no unauthenticated fallback mode. See [docs/operations.md](operations.md) for configuration.

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

## Target Architecture (Partially implemented — Phase 3 of 8 done)

The hackathon requirements include user authentication, asynchronous processing, notifications, and object storage. The target architecture introduces Domain-Driven Design structure across three bounded contexts, delivered incrementally.

> Identity (Phase 2) and Phase 3 (the `cmd/api` split, the `VideoJob` HTTP surface, and `POST /upload`'s ffmpeg execution migrated into the application layer) are both fully implemented as described below. Notification, `cmd/worker`, and Video Processing's own async queueing/execution (Redis/MinIO/RabbitMQ, `EnqueueVideoJob`/`StartProcessing`/`CompleteJob`/`FailJob` driven by a real worker instead of in-process by `/upload`) remain planned — each is labeled with the phase that introduces it.

### Bounded Contexts

| Context | Responsibility | Status |
|---|---|---|
| **Identity** | User registration, authentication, JWT issuance and verification | Implemented (Phase 2) |
| **Video Processing** | VideoJob lifecycle — creation, queueing, async execution, result storage | Partially implemented (Phase 3: creation/status/listing wired into HTTP, and synchronous in-process execution driven by `POST /upload`, both done; real queueing/async execution/result storage via RabbitMQ/`cmd/worker`/MinIO planned, Phases 5–6) |
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
    identity/                        # Implemented (Phase 2), wired into cmd/api
      domain/         # User aggregate, value objects, repository/password/token ports
      application/    # Use cases: RegisterUser, AuthenticateUser
      infrastructure/ # PostgreSQL adapter, bcrypt adapter, JWT adapter, UUID generator
    video/
      domain/         # VideoJob aggregate + transition methods, value objects, events, repository/FrameExtractor ports (all implemented, Phase 3)
      application/    # Use cases: CreateVideoJob, GetJobStatus, ListUserJobs, EnqueueVideoJob, StartProcessing, CompleteJob, FailJob, ProcessVideoJob (all implemented, Phase 3)
      infrastructure/ # PostgreSQL adapter, ffmpeg-backed FrameExtractor adapter (both implemented, Phase 3 — wired into cmd/api by wire-videojob-http-endpoints / migrate-ffmpeg-execution-to-videojob-application), MinIO adapter, RabbitMQ publisher (Phases 5–6)
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
| Redis | Idempotency keys, rate limiting, status cache, distributed locks | Planned (Phase 4) |
| MinIO | Object storage for uploads and ZIP results (S3-compatible) | Planned (Phase 5) |
| RabbitMQ | Durable async task queue for job dispatch | Planned (Phase 6) |

See [docs/roadmap.md](roadmap.md) for the full phase plan.

### Dependency Rules (Target)

1. `domain` packages MUST NOT import `application`, `infrastructure`, or transport packages.
2. `application` packages depend only on repository/port **interfaces** defined in `domain`.
3. `infrastructure` packages implement interfaces from `domain` and may import third-party drivers.
4. `cmd/api` and `cmd/worker` are the only places where `infrastructure` adapters are instantiated and wired (composition root). `cmd/api/identity.go` plays that role for Identity, and `cmd/api/video.go` for all of Video Processing's use cases (`CreateVideoJob`/`GetJobStatus`/`ListUserJobs`/`EnqueueVideoJob`/`StartProcessing`/`CompleteJob`/`FailJob`/`ProcessVideoJob`), today; `cmd/worker` doesn't exist yet (Phase 6).
5. No bounded context may import another context's `domain` or `application` packages directly. Each context defines and owns its own local value object for any identifier that crosses a boundary (e.g. `identity.UserID` and `video.UserID` are distinct types) — cross-context communication uses domain events or translation at the composition root, never a package shared between contexts' `domain` layers. There is no `pkg/` directory; a shared kernel was considered for the crossing `UserID` and rejected as tighter coupling than this architecture's context-independence goal justifies (see `add-videojob-domain-and-application`'s `design.md` in `openspec/changes/archive/`).

Rules 1–3 for `internal/identity/{domain,application}` and `internal/video/{domain,application}` are each enforced by an automated test (`internal/identity/dependency_rules_test.go`, `internal/video/dependency_rules_test.go`), not just convention.
