# Domain Model

> The domain model described here is the **target design** established by the DDD architecture foundation. Identity (Phase 2) is implemented as described below. Video Processing and Notification, and their aggregate roots and domain events, are introduced incrementally across Phases 3–7. See [docs/roadmap.md](roadmap.md) for the phase plan.

## Bounded Contexts

### Identity

**Responsibility:** Own the concept of a `User` — registration, credential storage, login, and JWT issuance. No other context stores passwords or issues tokens.

**Public interface to other contexts:**
- A `UserID` value object (opaque UUID, comparable, serializable) passed to other contexts.
- A token-verification middleware that converts a bearer token into a `UserID` or rejects the request.

**Aggregate root:** `User` (ID, email, hashed credential, created-at).

**Domain events emitted:** none in this slice — `RegisterUser`/`AuthenticateUser` return synchronously and don't publish events. Domain events for Identity (e.g. `UserRegistered`) are deferred until a consumer needs them (see Notification, Phase 7); introducing them speculatively was an explicit non-goal of Phase 2's design.

**Introduced in:** Phase 2 (`implement-identity-authentication-from-scratch`) — implemented; see [openspec/specs/identity-authentication/spec.md](../openspec/specs/identity-authentication/spec.md).

---

### Video Processing

**Responsibility:** Manage the full lifecycle of a video processing job — accepting an upload, tracking state transitions, coordinating the async worker, and making the result available. This is the core domain of FIAP X.

**Aggregate root:** `VideoJob` (see below).

**Domain events emitted:** `VideoJobCreated`, `VideoJobQueued`, `VideoJobStarted`, `VideoJobCompleted`, `VideoJobFailed`.

**Public interface to Identity:** Receives a `UserID` at job creation; does not call Identity for anything else.

**Public interface to Notification:** Emits domain events consumed by Notification. Does not call Notification directly.

**Introduced in:** Phase 3 (`implement-videojob-persistence`).

---

### Notification

**Responsibility:** React to domain events from Video Processing and Identity, and deliver notifications to users (email, webhook).

**Aggregate root:** `NotificationPreference` (per-user, per-event-type delivery configuration).

**Domain events consumed:** `VideoJobCompleted`, `VideoJobFailed`, `UserRegistered`.

**Public interface to other contexts:** None — Notification is a downstream consumer only.

**Introduced in:** Phase 7 (`implement-notifications`).

---

## VideoJob Aggregate Root

`VideoJob` is the aggregate root of the Video Processing bounded context. It owns the complete state of a single video processing request, from upload to result delivery.

### Value Objects

| Value object | Type | Invariant |
|---|---|---|
| `VideoJobID` | UUID v4 | Immutable after creation; globally unique |
| `UserID` | UUID v4 (foreign, from Identity) | Non-nil; not validated beyond format |
| `OriginalFilename` | string | Non-empty; extension must be in the allowed set |
| `StorageKey` | string | Non-empty; opaque path within the configured storage backend |
| `FrameCount` | int | ≥ 0; set only on transition to `completed` |
| `ErrorReason` | string | Non-empty only in `failed` state |
| `JobStatus` | enum | Transitions only via defined commands (see state machine below) |

### State Machine

```
pending → queued → processing → completed
                             ↘ failed
```

| State | Meaning | Entered by |
|---|---|---|
| `pending` | Job created, upload stored; not yet on the queue | `CreateVideoJob` use case |
| `queued` | Job published to the async queue; worker has not picked it up | `EnqueueVideoJob` use case |
| `processing` | Worker has dequeued the job and started extracting frames | Worker `StartProcessing` command |
| `completed` | Frames extracted and ZIP stored; `FrameCount` and `StorageKey` populated | Worker `CompleteJob` command |
| `failed` | Processing failed; `ErrorReason` populated | Worker `FailJob` command |

**Invariants:**
- A job may not transition backwards.
- `FrameCount` MUST be zero until the job reaches `completed`.
- `ErrorReason` MUST be empty unless the job is `failed`.
- `StorageKey` for the result MUST be set atomically with the `completed` transition.

### Use Cases

#### Video Processing Context

| Use case | Actor | Pre-condition | Post-condition |
|---|---|---|---|
| `CreateVideoJob` | Authenticated user (via API) | Valid video file uploaded; `UserID` verified | `VideoJob` persisted in `pending`; upload stored at `StorageKey` |
| `EnqueueVideoJob` | API (post-upload) | Job in `pending` | Job transitions to `queued`; message published to async queue |
| `GetJobStatus` | Authenticated user (via API) | Job exists; caller's `UserID` matches job's `UserID` | Returns current `JobStatus`, `FrameCount`, and result URL if `completed` |
| `ListUserJobs` | Authenticated user (via API) | — | Returns paginated list of the caller's jobs with their statuses |
| `StartProcessing` | Worker (internal) | Job in `queued` | Job transitions to `processing` |
| `CompleteJob` | Worker (internal) | Job in `processing` | Job transitions to `completed`; `StorageKey` and `FrameCount` set; `VideoJobCompleted` event emitted |
| `FailJob` | Worker (internal) | Job in `processing` | Job transitions to `failed`; `ErrorReason` set; `VideoJobFailed` event emitted |

#### Identity Context (implemented, Phase 2)

| Use case | Actor | Pre-condition | Post-condition |
|---|---|---|---|
| `RegisterUser` | Anonymous | Email not already registered | `User` persisted; no domain event emitted (see Domain Events below) |
| `AuthenticateUser` | Anonymous | User exists; credential matches | Bearer JWT issued; no domain event emitted |

Token verification is bearer middleware (`requireBearerAuth`), not a use case object: it extracts `Authorization: Bearer <token>`, verifies it via the `domain.TokenVerifier` port, and stores the resulting `UserID` in the request context or rejects with `401` before any handler runs.

#### Notification Context

| Use case | Actor | Pre-condition | Post-condition |
|---|---|---|---|
| `SendJobCompletionNotification` | Event handler | `VideoJobCompleted` event received | Notification delivered per user's preferences |
| `SendJobFailureNotification` | Event handler | `VideoJobFailed` event received | Notification delivered per user's preferences |

---

## Domain Events

All events are serialized as JSON. The `type` field is the canonical discriminator. Events are immutable once emitted.

| Event | JSON fields | Crosses context boundary? | Status |
|---|---|---|---|
| `VideoJobCreated` | `type`, `job_id`, `user_id`, `original_filename`, `occurred_at` | No (internal) | Planned (Phase 3+) |
| `VideoJobQueued` | `type`, `job_id`, `occurred_at` | No (internal) | Planned (Phase 6) |
| `VideoJobStarted` | `type`, `job_id`, `occurred_at` | No (internal) | Planned (Phase 6) |
| `VideoJobCompleted` | `type`, `job_id`, `user_id`, `frame_count`, `storage_key`, `occurred_at` | Yes — Video Processing → Notification | Planned (Phase 6–7) |
| `VideoJobFailed` | `type`, `job_id`, `user_id`, `error_reason`, `occurred_at` | Yes — Video Processing → Notification | Planned (Phase 6–7) |
| `UserRegistered` | `type`, `user_id`, `email`, `occurred_at` | Yes (optionally) — Identity → Notification | Planned — deferred until Notification (Phase 7) needs it; Identity itself (Phase 2) is implemented |
| `UserAuthenticated` | `type`, `user_id`, `occurred_at` | No (internal) | Planned; not emitted by the current `AuthenticateUser` use case |

Integration events that cross context boundaries are published to RabbitMQ (Phase 6). Internal domain events do not need to be published to the broker. Identity (Phase 2) is implemented but does not emit either event above yet — see the Identity Context use-case table.

---

## Cross-Context Contracts

- **`UserID`** is the only value object that crosses bounded context boundaries as a type. It is defined in `pkg/` so both the Identity and Video Processing contexts can reference it without importing each other.
- The Video Processing context treats `UserID` as a foreign key — it validates format only, not existence (existence is guaranteed by the API middleware that runs before the handler).
- The Notification context receives integration events over RabbitMQ and resolves delivery preferences by `UserID` alone, never by calling into Identity or Video Processing internals.
