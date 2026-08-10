# Architecture

## Current Implementation

The video-processing HTTP surface still lives in `main.go` (package `main`) — Phase 3's `cmd/api` extraction hasn't happened yet. Phase 2 added the first real internal package: `internal/identity`, an explicit DDD slice (domain/application/infrastructure) wired into `main.go`'s composition root rather than a package of its own.

```
video-processor/
  main.go          # HTTP server, router, video-processing handlers, business logic
  identity.go       # Identity composition root: wires internal/identity into main.go's router
  main_test.go     # Integration tests (drive the real handlers via httptest)
  identity_test.go # Identity HTTP/middleware/ownership tests
  internal/
    identity/
      domain/         # User, UserID, ports (repository, password hasher, token issuer/verifier)
      application/    # RegisterUser, AuthenticateUser use cases
      infrastructure/ # PostgreSQL adapter, bcrypt adapter, JWT adapter, UUID generator
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

Video-processing artifacts still live in the local filesystem; user accounts live in PostgreSQL:

| Store | Purpose | Durability |
|---|---|---|
| `uploads/` | Transient input; deleted on successful processing | Lost on failure |
| `temp/` | Per-request scratch; always cleaned up (defer) | Ephemeral |
| `outputs/` | Durable ZIP results; served by `/download/:filename` | Persistent across restarts |
| `uploads/*.owner`, `outputs/*.owner` | Sidecar files recording which authenticated `UserID` owns an artifact | Same lifecycle as the artifact they accompany |
| PostgreSQL `users` table | User accounts (normalized email, password hash) | Persistent, external to the container |

No cache, no message broker.

### Routes (current)

| Route | Handler | Description |
|---|---|---|
| `GET /` | inline | Returns inline HTML page from `getHTMLForm()`; always public |
| `POST /api/auth/register` | `handleRegister` | Create a user account |
| `POST /api/auth/login` | `handleLogin` | Authenticate and issue a bearer JWT |
| `POST /upload` | `handleVideoUpload` | Accept multipart video, process synchronously; requires a bearer token |
| `GET /download/:filename` | `handleDownload` | Serve a ZIP from `outputs/`; owner-only |
| `GET /api/status` | `handleStatus` | JSON list of ZIPs in `outputs/`; scoped to the caller's own uploads |
| `GET /uploads/*` | `gin.Static` | Static file serving of `uploads/`; owner-only; `.owner` sidecar files are never served regardless |
| `GET /outputs/*` | `gin.Static` | Static file serving of `outputs/`; owner-only; `.owner` sidecar files are never served regardless |

`IDENTITY_POSTGRES_DSN` and `IDENTITY_JWT_SIGNING_KEY` are both required at startup — the API composition root fails to start otherwise. There is no unauthenticated fallback mode. See [docs/operations.md](operations.md) for configuration.

CORS headers (`Access-Control-Allow-Origin: *`) are applied globally.

### Frontend (current)

The web UI is a Go string literal returned by `getHTMLForm()` in `main.go`. It contains:

- Plain HTML form for file selection, plus a login/register panel (Phase 2)
- Inline CSS (`<style>` block)
- Vanilla JavaScript using `fetch` to call `POST /upload`, `GET /api/status`, `POST /api/auth/register`, and `POST /api/auth/login`; the bearer token is kept in `localStorage` and attached as an `Authorization` header on protected requests

There is no separate frontend build, no Node.js toolchain, and no bundler.

---

## Target Architecture (Partially implemented — Phase 2 of 8 done)

The hackathon requirements include user authentication, asynchronous processing, notifications, and object storage. The target architecture introduces Domain-Driven Design structure across three bounded contexts, delivered incrementally.

> Identity (Phase 2) is implemented as described below. Video Processing and Notification, and the `cmd/api`/`cmd/worker` split, remain planned — each is labeled with the phase that introduces it.

### Bounded Contexts

| Context | Responsibility | Status |
|---|---|---|
| **Identity** | User registration, authentication, JWT issuance and verification | Implemented (Phase 2) |
| **Video Processing** | VideoJob lifecycle — creation, queueing, async execution, result storage | Planned (Phase 3+) |
| **Notification** | Reacting to domain events and delivering notifications (email, webhook) | Planned (Phase 7) |

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
    identity/                        # Implemented (Phase 2), wired into main.go rather than cmd/api
      domain/         # User aggregate, value objects, repository/password/token ports
      application/    # Use cases: RegisterUser, AuthenticateUser
      infrastructure/ # PostgreSQL adapter, bcrypt adapter, JWT adapter, UUID generator
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

### Infrastructure Components

| Component | Role | Status |
|---|---|---|
| PostgreSQL | Authoritative state store for users (jobs/outbox tables land in Phase 3) | **Implemented** (Phase 2), required at deployment time — see [docs/operations.md](operations.md) |
| Redis | Idempotency keys, rate limiting, status cache, distributed locks | Planned (Phase 4) |
| MinIO | Object storage for uploads and ZIP results (S3-compatible) | Planned (Phase 5) |
| RabbitMQ | Durable async task queue for job dispatch | Planned (Phase 6) |

See [docs/roadmap.md](roadmap.md) for the full phase plan.

### Dependency Rules (Target)

1. `domain` packages MUST NOT import `application`, `infrastructure`, or transport packages.
2. `application` packages depend only on repository/port **interfaces** defined in `domain`.
3. `infrastructure` packages implement interfaces from `domain` and may import third-party drivers.
4. `cmd/api` and `cmd/worker` are the only places where `infrastructure` adapters are instantiated and wired (composition root). Today, before the Phase 3 `cmd/api` extraction, `main.go`/`identity.go` play that role for Identity.
5. No bounded context may import another context's `domain` or `application` packages. Cross-context communication uses domain events or the shared `UserID` value object from `pkg/`.

Rules 1–3 for `internal/identity/{domain,application}` are enforced by an automated test (`internal/identity/dependency_rules_test.go`), not just convention.
