## Context

FIAP X is a POSTECH/FIAP hackathon deliverable. The current implementation is a single `main.go` that accepts a video upload, shells out to `ffmpeg` to extract frames at 1 fps, zips the results, and serves the zip — all synchronously in the HTTP handler. There is no database, no job queue, no authentication, no async worker, and no internal package structure.

The hackathon requirements include: user authentication, asynchronous video processing with status tracking, notifications (email/webhook), object storage for results, and horizontal scalability. None of these fit cleanly into the current flat package. This design defines the conceptual model and package skeleton that all subsequent changes will implement against — without touching a single line of application code.

## Goals / Non-Goals

**Goals:**

- Define the three bounded contexts and their responsibilities.
- Define the `VideoJob` aggregate root: value objects, state machine, invariants.
- Define domain use cases for each context (what, not how).
- Define domain events and integration contracts between contexts.
- Define dependency rules that prevent coupling creep.
- Define the monorepo package topology as the target structure.
- Document the seven key architecture decisions (ADRs) the roadmap will encounter.
- Produce a versioned evolution roadmap that sequences future changes.
- Establish the frontend (inline HTML/CSS/JS in `getHTMLForm()`) as a presentation/delivery layer: document non-regression criteria, contract-compatibility rules, and the incremental extraction path to static files under `web/`.

**Non-Goals:**

- Implementing any of the above in Go code.
- Migrating the existing `main.go` logic into the new package topology.
- Introducing PostgreSQL, Redis, RabbitMQ, or MinIO — even as stubs.
- Adding authentication, async processing, notifications, or object storage.
- Changing the CI workflow, Dockerfile, `go.mod`, or any test file.
- Defining HTTP API schemas or OpenAPI specs (deferred to the feature changes that implement the endpoints).

## Bounded Contexts

### Identity

**Responsibility:** Manage users (registration, login, credential storage) and issue verifiable identity tokens consumed by the other contexts. This context owns the concept of a `User` and its credentials — no other context stores passwords or issues tokens.

**Public interface to other contexts:** A validated `UserID` value object (opaque, comparable, serializable) and a token-verification function/middleware that converts a bearer token into a `UserID` or rejects the request. Other contexts treat `UserID` as a foreign key — they do not call into Identity's domain internals.

**Aggregate root:** `User` (ID, email, hashed credential, created-at).

**Domain events emitted:** `UserRegistered`, `UserAuthenticated`.

### Video Processing

**Responsibility:** Manage the lifecycle of a video processing job: accepting the upload, tracking state transitions, coordinating the async worker, and making the result available. This is the core domain of FIAP X.

**Aggregate root:** `VideoJob` (see below).

**Domain events emitted:** `VideoJobCreated`, `VideoJobQueued`, `VideoJobStarted`, `VideoJobCompleted`, `VideoJobFailed`.

**Public interface to Identity context:** Receives `UserID` at job creation; does not call Identity for anything else.

**Public interface to Notification context:** Emits domain events that the Notification context subscribes to. Does not call Notification directly.

### Notification

**Responsibility:** React to domain events from Video Processing and Identity, and deliver notifications to users (email, webhook). Owns the concept of a `NotificationPreference` and a `DeliveryAttempt`.

**Aggregate root:** `NotificationPreference` (per-user, per-event-type delivery configuration).

**Domain events consumed:** `VideoJobCompleted`, `VideoJobFailed`, `UserRegistered`.

**Public interface to other contexts:** None — Notification is a downstream consumer only; no other context depends on it.

## VideoJob Aggregate Root

### Value Objects

| Value object | Type | Invariant |
|---|---|---|
| `VideoJobID` | UUID v4 | Immutable after creation; globally unique |
| `UserID` | UUID v4 (foreign, from Identity) | Non-nil; not validated beyond format |
| `OriginalFilename` | string | Non-empty; extension must be in the allowed set |
| `StorageKey` | string | Non-empty; opaque path within the configured storage backend |
| `FrameCount` | int | ≥ 0; set only on transition to `completed` |
| `ErrorReason` | string | Non-empty only in `failed` state |
| `JobStatus` | enum (see below) | Transitions only via defined commands |

### Valid Job States

```
pending → queued → processing → completed
                              ↘ failed
```

| State | Meaning | Entered by |
|---|---|---|
| `pending` | Job created, upload stored, not yet on the queue | `CreateVideoJob` use case |
| `queued` | Job published to the async queue; worker has not picked it up yet | `EnqueueVideoJob` use case |
| `processing` | Worker has dequeued and started extracting frames | Worker `StartProcessing` command |
| `completed` | Frames extracted and zip stored; `FrameCount` and `StorageKey` populated | Worker `CompleteJob` command |
| `failed` | Processing failed; `ErrorReason` populated | Worker `FailJob` command |

**Invariants:**
- A job may not transition backwards.
- `FrameCount` MUST be zero until the job reaches `completed`.
- `ErrorReason` MUST be empty unless the job is `failed`.
- `StorageKey` for the result MUST be set atomically with the `completed` transition.
- A `failed` job may be retried by re-entering `queued`, resetting `ErrorReason` (policy TBD in the retry change).

### Use Cases (Video Processing Context)

| Use case | Actor | Pre-condition | Post-condition |
|---|---|---|---|
| `CreateVideoJob` | Authenticated user (via API) | Valid video file uploaded; `UserID` verified | `VideoJob` persisted in `pending`; upload stored at `StorageKey` |
| `EnqueueVideoJob` | API (synchronous, post-upload) | Job in `pending` | Job transitioned to `queued`; message published to async queue |
| `GetJobStatus` | Authenticated user (via API) | Job exists; caller's `UserID` matches job's `UserID` | Returns current `JobStatus`, `FrameCount`, and result URL if `completed` |
| `ListUserJobs` | Authenticated user (via API) | — | Returns paginated list of the caller's jobs with their statuses |
| `StartProcessing` | Worker (internal) | Job in `queued` | Job transitioned to `processing` |
| `CompleteJob` | Worker (internal) | Job in `processing` | Job transitioned to `completed`; result `StorageKey` and `FrameCount` set; `VideoJobCompleted` event emitted |
| `FailJob` | Worker (internal) | Job in `processing` | Job transitioned to `failed`; `ErrorReason` set; `VideoJobFailed` event emitted |

### Use Cases (Identity Context)

| Use case | Actor | Pre-condition | Post-condition |
|---|---|---|---|
| `RegisterUser` | Anonymous | Email not already registered | `User` persisted; `UserRegistered` event emitted |
| `AuthenticateUser` | Anonymous | User exists; credential matches | Token issued; `UserAuthenticated` event emitted |
| `VerifyToken` | API middleware | Token present in request | `UserID` extracted and forwarded; request rejected on invalid token |

### Use Cases (Notification Context)

| Use case | Actor | Pre-condition | Post-condition |
|---|---|---|---|
| `SendJobCompletionNotification` | Event handler | `VideoJobCompleted` event received | Notification delivered per user's preferences |
| `SendJobFailureNotification` | Event handler | `VideoJobFailed` event received | Notification delivered per user's preferences |

## Domain Events and Integration Contracts

All events are serialized as JSON. The `type` field is the canonical discriminator. Events are immutable once emitted.

```
VideoJobCreated   { type, job_id, user_id, original_filename, occurred_at }
VideoJobQueued    { type, job_id, occurred_at }
VideoJobStarted   { type, job_id, occurred_at }
VideoJobCompleted { type, job_id, user_id, frame_count, storage_key, occurred_at }
VideoJobFailed    { type, job_id, user_id, error_reason, occurred_at }
UserRegistered    { type, user_id, email, occurred_at }
UserAuthenticated { type, user_id, occurred_at }
```

Integration events (crossing context boundaries over RabbitMQ) are a strict subset of domain events. `VideoJobCompleted` and `VideoJobFailed` cross from Video Processing to Notification. `UserRegistered` optionally crosses from Identity to Notification. Events that do not cross context boundaries are internal domain events only and do not need to be published to the broker.

## Package Topology (Target)

```
video-processor/
  cmd/
    api/        # HTTP entrypoint (replaces main.go eventually)
    worker/     # Async frame-extraction worker
  web/
    index.html  # Static HTML page (extracted from getHTMLForm() in Phase 3)
    styles.css
    app.js
  internal/
    identity/
      domain/         # User aggregate, value objects, repository interface
      application/    # Use cases (RegisterUser, AuthenticateUser, VerifyToken)
      infrastructure/ # DB adapter, token implementation
    video/
      domain/         # VideoJob aggregate, value objects, repository interface, domain events
      application/    # Use cases (CreateVideoJob, EnqueueVideoJob, GetJobStatus, …)
      infrastructure/ # DB adapter, storage adapter, queue publisher
    notification/
      domain/         # NotificationPreference aggregate, DeliveryAttempt
      application/    # Use cases (SendJobCompletionNotification, …)
      infrastructure/ # Email adapter, webhook adapter
  pkg/
    # Shared primitives with no domain knowledge (e.g. ID generation, clock)
```

**Migration path:** The current `main.go` continues to function as-is during the transition. New packages are introduced alongside it. Each feature change (`opsx:apply`) migrates one slice of the handler into the appropriate application use case, then wires it back to the HTTP layer. No "big bang" rewrite.

## Frontend / Presentation Layer

The current application serves a single HTML page via `getHTMLForm()` in `main.go` — an inline form with vanilla JavaScript that calls `POST /upload` and `GET /api/status`. This page is part of the product and must remain functional throughout the DDD migration.

**The frontend is not a bounded context.** It is a delivery/presentation layer: it has no domain rules, no aggregate roots, and no domain events. It consumes the HTTP API surface exposed by the Video Processing context.

### Extraction Direction

The frontend will be incrementally extracted from the Go string literal to discrete static files under `web/` (see package topology above):

- `web/index.html` — the HTML skeleton extracted from `getHTMLForm()`
- `web/styles.css` — all inline `<style>` content
- `web/app.js` — the vanilla JS fetch logic

The Go server continues to serve these files — via `gin.Static` or equivalent pointing at `web/` (keeping the same `GET /` route, now serving `web/index.html` instead of the inline string). No separate frontend build step, no Node.js toolchain, no bundler. This extraction is scoped to **Phase 3** (`implement-videojob-persistence`), where `main.go` is already being restructured.

### API Contract Compatibility During Migration

When the async processing API is introduced in Phase 6, `POST /upload` becomes non-blocking and returns a job ID immediately instead of waiting for ffmpeg to complete. The frontend must adapt to this change.

Compatibility strategy:

- `POST /upload` SHALL remain available and SHALL continue to accept multipart form data in the same shape during Phase 6 and beyond.
- The **response schema** of `POST /upload` will change in Phase 6: it will return a job ID and a status URL instead of a direct download link.
- `POST /jobs` is the canonical async entrypoint introduced in Phase 6; `POST /upload` MAY eventually be deprecated in favor of it, but SHALL NOT be removed until the frontend is updated and verified working against the new endpoint.
- `GET /jobs/{id}/status` replaces `GET /api/status` as the per-job polling endpoint. `GET /api/status` (which lists all outputs glob-style) remains available for backward compatibility through Phase 6.

### Non-Regression Criteria

Every phase that changes API contracts or routing SHALL verify the following before merging:

| Endpoint / Flow | Criterion |
|---|---|
| `GET /` | Returns HTTP 200 with HTML content; page loads without JS errors |
| Static assets | `GET /web/styles.css` and `GET /web/app.js` return HTTP 200 with correct `Content-Type` after Phase 3 extraction |
| `POST /upload` | Accepts a valid video file; returns a usable response (download link in sync phases; job ID + status URL from Phase 6 onward) |
| `GET /api/status` | Returns HTTP 200 with JSON listing completed outputs |
| `GET /download/:filename` | Returns HTTP 200 with a zip file for a valid filename |
| Full frontend flow | Uploading a video through the web UI results in a downloadable zip — verified via browser or a curl sequence simulating the complete upload → poll → download flow |

### Rule: Backend Contract Changes Must Preserve or Update the Frontend

Any OpenSpec change that adds, renames, or removes an HTTP endpoint consumed by the frontend SHALL include an explicit task to update `web/app.js` (or the inline JS in `getHTMLForm()`, until Phase 3 extraction) to reflect the new contract. A backend contract change is not complete if the frontend silently breaks.

## Dependency Rules

These rules are enforced by Go's own import system (circular imports are compile errors) and by convention in code review:

1. `domain` packages MUST NOT import `application`, `infrastructure`, or any HTTP/transport package.
2. `application` packages MUST NOT import `infrastructure` or any HTTP/transport package directly — they depend only on repository/storage/queue **interfaces** defined in `domain`.
3. `infrastructure` packages implement interfaces defined in `domain` and may import third-party drivers (SQL, Redis, AMQP, S3).
4. `cmd/api` and `cmd/worker` are the only packages allowed to instantiate `infrastructure` adapters and wire them to `application` use cases (composition root / DI boundary).
5. No bounded context's packages may import another bounded context's `domain` or `application` packages directly. Cross-context communication happens through domain events or through the `UserID` value object type shared via `pkg/`.
6. `pkg/` may only contain utilities with no domain knowledge; any package that starts importing domain types must move out of `pkg/` into a context.

## Architecture Decision Records

### ADR-1: Async Transport — RabbitMQ (not Redis Streams)

**Decision:** RabbitMQ is the async message broker for dispatching `VideoJob` processing tasks.

**Rationale:** Video processing jobs are long-running, have real failure modes, and need durable delivery guarantees (survive broker restarts, re-deliver on worker crash). RabbitMQ provides per-message acknowledgement, dead-letter queues, and durable queues out of the box. Redis Streams can also provide at-least-once delivery, but operational complexity (persistence configuration, consumer group management, manual DLQ) increases fast, and Redis's primary role in this system is caching/rate-limiting — not acting as a durable task queue.

**Trade-off:** RabbitMQ is another infrastructure dependency. For a prototype this is overhead. Accepted because the hackathon spec explicitly lists RabbitMQ as a technology requirement.

**Redis does NOT substitute for RabbitMQ** in this architecture. Redis and RabbitMQ serve distinct roles (see ADR-4).

### ADR-2: Object Storage — MinIO (not local volume)

**Decision:** Processed zip results (and, in later phases, uploaded source videos) are stored in MinIO.

**Rationale:** The current `outputs/` directory on the local filesystem is not suitable for a horizontally-scaled worker fleet — workers on different machines cannot share a local directory. MinIO provides an S3-compatible API that both the API and worker containers can address, is self-hostable for the hackathon deployment, and can be swapped for AWS S3 without application code changes (same SDK).

**Trade-off:** Local volume is simpler and zero-dependency for local development. Mitigated by keeping MinIO behind a storage interface — tests can use an in-process fake or a local MinIO container.

### ADR-3: Identity — JWT (not server-side sessions)

**Decision:** Identity tokens are stateless JWTs (HS256 or RS256), verified by a middleware that does not require a database round-trip on every request.

**Rationale:** Stateless tokens work naturally with a horizontally-scaled API (no shared session store required at this layer). The worker does not need to verify tokens (it processes jobs by ID from the queue, where ownership was already validated at enqueue time). Revocation is deferred — acceptable for the hackathon scope.

**Trade-off:** Token revocation (logout, credential reset) requires either short expiry or a denylist. A Redis-backed token denylist is explicitly in scope for Phase 4 (Redis capabilities). Without it, tokens cannot be revoked until expiry.

### ADR-4: Redis Responsibilities (idempotency, rate limiting, status cache, locks)

**Decision:** Redis is mandatory infrastructure with four explicit responsibilities:

1. **Idempotency keys:** Prevent duplicate job creation from client retries. A `POST /upload` with the same content hash within a TTL window returns the existing job ID without re-enqueuing.
2. **Rate limiting:** Limit requests per user per window at the API gateway level (sliding window counter in Redis).
3. **Status cache:** Cache `VideoJob` status responses to reduce PostgreSQL read pressure on polling-heavy clients. Cache TTL is short (seconds); invalidated on every state transition written to the DB.
4. **Distributed locks:** Prevent concurrent worker pickup of the same job (belt-and-suspenders alongside queue acknowledgement).

**PostgreSQL remains the source of truth** for all job state. Redis caches are never authoritative; they are always backed by a PostgreSQL write. A cache miss always falls back to the DB.

**Redis does NOT replace RabbitMQ** as the job queue (see ADR-1).

### ADR-5: Status Delivery — Polling (initially), Webhook (Phase 7)

**Decision:** The initial async processing API will support polling via `GET /jobs/{id}/status`. Webhook delivery is deferred to Phase 7 (Notifications).

**Rationale:** Polling is stateless from the server side: no persistent connection, no webhook registration, no delivery retry complexity. It's the simplest thing that works for the hackathon demo. Webhook delivery requires durable registration, retry logic, and secret verification — all real features better introduced after the core pipeline is proven.

**Trade-off:** Polling causes unnecessary load on the status endpoint under many concurrent jobs. Mitigated by Phase 4's Redis status cache, which absorbs repeated reads.

### ADR-6: Repository Topology — Monorepo

**Decision:** `cmd/api` and `cmd/worker` live in the same repository, sharing `internal/` packages.

**Rationale:** `api` and `worker` share the `VideoJob` aggregate and its persistence logic. Duplicating this across repos creates a synchronization burden that's disproportionate for a team this size. A monorepo with separate `cmd/` entrypoints gives independent deployability (two Docker images) without code duplication. Split if team scaling actually demands it.

**Trade-off:** A single CI pipeline gates both binaries. A change to the worker that breaks the api build blocks both. Managed by keeping the shared `internal/` layer well-tested.

### ADR-7: PostgreSQL as Source of Truth

**Decision:** `VideoJob` state transitions are persisted to PostgreSQL as the primary write path. Redis is a read-through cache only.

**Rationale:** PostgreSQL provides ACID transactions, which are needed to ensure a job's state transition and its associated domain event (or queue publication) happen atomically using the transactional outbox pattern. Redis does not provide the durability or transactional semantics needed for this.

**Outbox pattern:** Rather than publishing to RabbitMQ directly inside a DB transaction (two-phase write), the application writes domain events to an `outbox` table in the same PostgreSQL transaction as the state update. A separate relay process (or polling loop in the worker) reads the outbox and publishes to RabbitMQ, marking events as published. This ensures no events are lost on crash between the DB write and the broker publish.

## Risks / Trade-offs

- **[Overhead before value]** Introducing DDD structure before the first feature is implemented adds ceremony. Mitigated by phasing: this change produces only spec artifacts; code migration happens incrementally in subsequent changes, each of which ships a visible capability.
- **[Over-engineering for a hackathon]** A flat `main.go` might be "good enough" for the demo. Accepted: the spec explicitly evaluates architecture, and the roadmap phases can be partially implemented — the structure does not require completing all eight phases to be valuable.
- **[Aggregate boundary disputes]** The `VideoJob` aggregate currently owns both the upload reference and the processing result. If these grow independently (e.g., an upload can have multiple processing attempts), the aggregate boundary may need to split. Deferred — fine for current scope, noted as a future evolution point.

## Open Questions

- **Outbox relay implementation:** In-process polling loop vs. a dedicated relay process? Deferred to Phase 6 (RabbitMQ and worker) — either approach is consistent with this design.
- **Storage key format:** Will `StorageKey` include the bucket name, or just the object key within a known bucket? Deferred to Phase 5 (MinIO).
- **Token algorithm:** HS256 (single shared secret) vs RS256 (asymmetric, suitable for future service-to-service verification)? Deferred to Phase 2 (Identity). RS256 preferred if the worker ever needs to verify tokens independently.

---

## Roadmap

This roadmap sequences the evolution of FIAP X from the current flat monolith into a fully structured system. Each phase is a distinct OpenSpec change, proposed and implemented separately. Phases are ordered by dependency: each phase can safely assume the prior phase's code is merged.

| Phase | Change name (tentative) | Scope |
|---|---|---|
| **1** | `establish-ddd-architecture-foundation` *(this change)* | OpenSpec artifacts only. Bounded contexts, VideoJob aggregate, package topology, ADRs, dependency rules. Documents frontend as presentation/delivery layer; establishes non-regression criteria and contract-compatibility rules. No code changes. |
| **2** | `implement-identity-and-authentication` | `internal/identity/domain` and `application`; JWT token issuance and verification middleware; `RegisterUser` and `AuthenticateUser` use cases; Identity infrastructure (PostgreSQL adapter); wire into `cmd/api`. No async work yet. |
| **3** | `implement-videojob-persistence` | `internal/video/domain` and `application`; PostgreSQL schema for `video_jobs` and `outbox`; `CreateVideoJob` and `GetJobStatus` use cases; synchronous `ffmpeg` call migrated from `main.go` into `application`; polling status endpoint. `main.go` now delegates to use cases. Frontend extracted to `web/index.html`, `web/styles.css`, `web/app.js`; Go serves them via static file handler. Non-regression for `GET /` and assets verified. |
| **4** | `implement-redis-capabilities` | Redis infrastructure adapter; idempotency keys on `POST /upload`; rate limiting middleware; status cache for `GetJobStatus`; distributed lock for worker job pickup. PostgreSQL remains source of truth; Redis is read-through only. |
| **5** | `implement-minio-object-storage` | MinIO storage adapter behind the `StoragePort` interface; migrate upload and result storage from local filesystem to MinIO; presigned download URLs; update `GetJobStatus` to return a presigned URL for `completed` jobs. |
| **6** | `implement-rabbitmq-and-worker` | RabbitMQ infrastructure; `EnqueueVideoJob` use case publishes to queue; outbox relay reads PostgreSQL outbox and publishes to RabbitMQ; `cmd/worker` picks up messages, runs ffmpeg, calls `CompleteJob` or `FailJob`; `POST /upload` becomes non-blocking (returns job ID immediately). `web/app.js` adapted for the async response: upload returns job ID + status URL; polling loop updated to call `GET /jobs/{id}/status`. |
| **7** | `implement-notifications` | `internal/notification/domain` and `application`; event subscription to `VideoJobCompleted` / `VideoJobFailed` from RabbitMQ; email delivery via SMTP or transactional provider; webhook delivery with retry and HMAC signature; `NotificationPreference` per user. |
| **8** | `implement-observability-and-delivery` | Structured logging (zerolog or slog); Prometheus metrics exposition (`/metrics`); health and readiness endpoints (`/health`, `/ready`); Dockerfile hardening (multi-stage build, non-root user); `docker-compose.yml` for local development stack (PostgreSQL, Redis, RabbitMQ, MinIO); CI image-build step. |
