# Domain Model

> The domain model described here is the **target design** established by the DDD architecture foundation. Identity (Phase 2) is implemented as described below, as is Video Processing (Phases 3–6). Notification is partially implemented: `add-notification-domain-and-preferences` (Phase 7) shipped its `NotificationPreference` aggregate, its persistence, and its HTTP surface, while every delivery use case below is still planned. See [docs/roadmap.md](roadmap.md) for the phase plan.

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

**Responsibility:** Manage the full lifecycle of a video processing job — accepting an upload, tracking state transitions, running the async worker, and making the result available. This is the core domain of FIAP X. It spans two processes: `cmd/api` accepts and reports, `cmd/worker` executes.

**Aggregate root:** `VideoJob` (see below).

**Domain events emitted:** `VideoJobCreated`, `VideoJobQueued`, `VideoJobStarted`, `VideoJobCompleted`, `VideoJobFailed`.

**Public interface to Identity:** Receives a `UserID` at job creation; does not call Identity for anything else.

**Public interface to Notification:** Emits domain events consumed by Notification. Does not call Notification directly.

**Introduced in:** Phase 3, decomposed across several changes — `add-videojob-domain-and-application`, `add-videojob-infrastructure`, `wire-videojob-http-endpoints`, and `migrate-ffmpeg-execution-to-videojob-application`. See [docs/roadmap.md](roadmap.md)'s Change Backlog for their order and dependencies.

---

### Notification

**Responsibility:** Own the user's delivery preferences — where and how they want to be told a job ended — and, once the delivery changes ship, react to domain events from Video Processing and Identity to deliver notifications (email, webhook).

**Aggregate root:** `NotificationPreference`, identified by the triple `(UserID, EventType, Channel)` — there is no surrogate id, because nothing references one by an id, and the triple gives the upsert its conflict target for free. Both `EventType` and `Channel` are **closed sets**, parsed through the domain and never re-checked in a handler: `video_job.completed.v1` and `video_job.failed.v1`, and `webhook` alone. `email` is deliberately rejected until `add-notification-email-delivery` ships an adapter that honours it — a channel a user can configure and that silently delivers nothing is worse than a rejected value.

The aggregate has **three shapes**, and the split is load-bearing rather than stylistic. A *write intent* carries an **optional** secret, because omitting one is a legitimate update. A *stored preference* always carries one, because the create path refuses anything else. A *read view* carries `HasSecret bool` and no secret field at all. Collapsing them would force the use case to know create-from-update before writing — the pre-read the persistence design exists to avoid.

**Absence of a row means not subscribed.** There is no implicit default and no backfill: a user who has registered nothing receives nothing, which is the only safe reading when the destination is a URL the system was never given. `enabled` is a stored flag rather than a deletion, so disabling keeps the destination and the secret and re-enabling is not a re-registration; there is deliberately no `DELETE` route.

**The webhook secret is a credential, and non-disclosure is its whole protection.** HMAC signing needs the original bytes, so it cannot be hashed the way a password is. It is accepted on write, never returned by any read, never logged, and not even *loaded* on the read path — every read projects `secret <> ''` instead of the column. `domain.Secret` renders a redacted placeholder from `String`/`GoString`/`Format`, refuses to `MarshalJSON`, holds its value behind an unexported pointer so `%p` reaches an address, and exposes the bytes through one deliberately-named `Reveal()`. Required on create, optional on update, and an explicitly empty value is rejected rather than read as a removal.

**Domain events consumed:** `VideoJobCompleted`, `VideoJobFailed`, `UserRegistered` — all still planned. Nothing consumes anything yet.

**Public interface to other contexts:** None — Notification is a downstream consumer only. Having an authenticated HTTP surface of its own does not make it a context another context calls to trigger a delivery, and the surface is for a *user* managing their own preferences.

**Cross-context independence:** it imports neither `internal/video` nor `internal/identity` — enforced by `internal/notification/dependency_rules_test.go` — so it declares its own `UserID` value object and its own copies of the two event-type strings. `TestNotificationEventTypesMatchTheEmittedTerminalEventTypes` in `cmd/api`, the one place that legitimately imports both sides, pins those copies against `internal/video/infrastructure/postgres`'s.

**Introduced in:** Phase 7 — `add-notification-domain-and-preferences` for the aggregate, its PostgreSQL adapter, and the two routes; `add-notification-webhook-delivery` and `add-notification-email-delivery` for delivery (see [docs/roadmap.md](roadmap.md)).

---

## VideoJob Aggregate Root

`VideoJob` is the aggregate root of the Video Processing bounded context. It owns the complete state of a single video processing request, from upload to result delivery.

### Value Objects

| Value object | Type | Invariant |
|---|---|---|
| `VideoJobID` | UUID v4 | Immutable after creation; globally unique |
| `UserID` | UUID v4 (foreign, from Identity) | Non-nil; not validated beyond format |
| `OriginalFilename` | string | Non-empty; extension must be in the allowed set |
| `StorageKey` | string | Non-empty; opaque path within the configured storage backend. Held by the aggregate in **two distinct roles** — see `SourceKey` below — which must never be conflated |
| `SourceKey` | `StorageKey` | The uploaded video's object key (`uploads/<uploadID>_<filename>`). Set at creation and never after; MAY be zero, because `POST /api/video-jobs` creates a job from a filename with no stored object. A job whose `SourceKey` is zero cannot be enqueued — the invariant lives on the `Enqueue` transition, deliberately not on reconstitution, so rows predating the column still load |
| `ContentHash` | string | SHA-256 digest of the uploaded bytes, computed on the same pass that stored them. Set at creation and never after; MAY be empty (a job from `POST /api/video-jobs`, or a row predating the column). It exists so a process that never saw the submitting request can rebuild the job's idempotency key from `UserID` + hash — the reservation **token** is deliberately *not* persisted, being a possession capability of the request that minted it. Like `SourceKey`, it is not validated at reconstitution |
| `FrameCount` | int | ≥ 0; set only on transition to `completed` |
| `ErrorReason` | string | Non-empty only in `failed` state |
| `JobStatus` | enum | Transitions only via defined commands (see state machine below) |
| `LeaseEpoch` | integer (application-generated values are non-negative) | Counts how many times recovery returned the job to `queued`; application paths start at zero and advance only on requeue, while the persistence boundary does not add a database `CHECK` or reject a stored integer during restoration |

### State Machine

```
pending → queued → processing → completed
                             ↘ failed
          queued ← recovery ← processing
```

`processing → queued` is the single backwards edge. It is available only to the recovery sweeper after the job is confirmed unleased; it is not a general retry or operator reset.

| State | Meaning | Entered by |
|---|---|---|
| `pending` | Job created, upload stored; not yet on the queue | `CreateVideoJob` use case |
| `queued` | Dispatch recorded in the outbox and committed with the transition; the broker message follows, published out of band by the relay | `EnqueueVideoJob` for first dispatch, or the recovery sweeper's fenced requeue |
| `processing` | `cmd/worker` has dequeued the job and won the claim; frame extraction is under way and the worker attempts to maintain an epoch-scoped lease | `cmd/worker`'s `StartProcessing` command — an **atomic conditional claim**, see below |
| `completed` | Frames extracted and ZIP stored; `FrameCount` and `StorageKey` populated, and the terminal event recorded in the outbox with the same commit | `cmd/worker`'s epoch-fenced `CompleteJob` command |
| `failed` | Processing failed or exhausted recovery; `ErrorReason` populated, and the terminal event recorded in the outbox with the same commit | `ProcessVideoJob`'s fenced `FailJob`, or the recovery sweeper's abandonment write |

**Invariants:**
- A job may not transition backwards except for `processing → queued`, used only to recover a confirmed abandonment; `completed` and `failed` remain terminal.
- `FrameCount` MUST be zero until the job reaches `completed`.
- `ErrorReason` MUST be empty unless the job is `failed`.
- `StorageKey` for the result MUST be set atomically with the `completed` transition.
- `SourceKey` MUST be non-zero to enter `queued`, and is fixed at creation — it is never rewritten by a transition.
- `ContentHash` is fixed at creation and never rewritten by a transition either.
- **`queued → processing` is a claim, not merely a transition.** It is persisted through one conditional statement (`… WHERE id = $1 AND status = 'queued' RETURNING lease_epoch`), so of two consumers handed the same dispatch exactly one wins and receives the epoch it must carry. Losing returns a distinct sentinel and touches nothing.
- **A terminal write is fenced.** `CompleteJob` and `FailJob` apply only while the row is still `processing` at the epoch the claim returned. A requeue advances the epoch; a previous holder then receives `ErrJobFenced` and cannot overwrite the successor.
- **The Redis lease is liveness, not ownership.** Pickup never consults it. The sweeper requires two successful not-held observations at the same epoch before conditionally requeueing; a Redis query error for that job clears its confirmation and takes over nothing. Because acquisition and renewal fail open, an absent lease is evidence that requires confirmation, not proof that no worker is running.

### Use Cases

#### Video Processing Context

| Use case | Actor | Pre-condition | Post-condition |
|---|---|---|---|
| `CreateVideoJob` | Authenticated user (via API) | Valid video file uploaded; `UserID` verified | `VideoJob` persisted in `pending`, carrying the `SourceKey` the upload was stored under (empty when the caller supplied no object) |
| `EnqueueVideoJob` | API (post-upload) | Job in `pending` with a non-zero `SourceKey` | Job transitions to `queued` and a `VideoJobQueued` outbox row is committed in the **same transaction** — the dispatch is *recorded for relay*, not published. The broker message is the relay's separate step and may not arrive at all while the broker is down |
| `GetJobStatus` | Authenticated user (via API) | Job exists; caller's `UserID` matches job's `UserID` | Returns current `JobStatus`, `FrameCount`, and result URL if `completed` |
| `ListUserJobs` | Authenticated user (via API) | — | Returns paginated list of the caller's jobs with their statuses |
| `StartProcessing` | `cmd/worker` consumer | Job in `queued` | Job transitions to `processing` through an atomic PostgreSQL claim and returns the row's current `LeaseEpoch`; a lost claim writes nothing |
| `CompleteJob` | `cmd/worker` consumer | Job in `processing`, result stored, caller holds the claimed epoch | Job transitions to `completed` only if status and epoch still match; `StorageKey` and `FrameCount` are set, and a `VideoJobCompleted` outbox row is committed in the **same transaction** as that write. A superseded caller, or one that lost a same-epoch race to a **different** terminal outcome, receives `ErrJobFenced`; an identical outcome already present is successful but not applied. Neither of those two records an event — the row the conditional statement actually affected is what carries it |
| `FailJob` | `ProcessVideoJob` or recovery sweeper | Job in `processing`, caller holds the observed epoch | Job transitions to `failed` only if status and epoch still match; `ErrorReason` is set and a `VideoJobFailed` outbox row is committed in the **same transaction**, on the same applied-write condition as `CompleteJob`. The return distinguishes a write this call applied from an identical terminal outcome already present |
| Recovery sweep (`Requeue`/abandon) | `cmd/worker` sweeper | Job in `processing`, observed unleased twice at the same epoch | Below the bound, atomically returns it to `queued`, advances `LeaseEpoch`, and records a new dispatch. At the bound (or with no source key), applies fenced `failed` instead |
| `ClearJobIdempotencyKey` | `cmd/worker` consumer or sweeper | This actor applied the job's `failed` write | Deletes the key only if it still names that job; fenced and already-present outcomes clear nothing |

#### Identity Context (implemented, Phase 2)

| Use case | Actor | Pre-condition | Post-condition |
|---|---|---|---|
| `RegisterUser` | Anonymous | Email not already registered | `User` persisted; no domain event emitted (see Domain Events below) |
| `AuthenticateUser` | Anonymous | User exists; credential matches | Bearer JWT issued; no domain event emitted |

Token verification is bearer middleware (`requireBearerAuth`), not a use case object: it extracts `Authorization: Bearer <token>`, verifies it via the `domain.TokenVerifier` port, and stores the resulting `UserID` in the request context or rejects with `401` before any handler runs.

#### Notification Context

| Use case | Actor | Pre-condition | Post-condition |
|---|---|---|---|
| `SetPreference` | Authenticated user | Event type, channel, destination, and (on create) secret all parse | Exactly one preference upserted for the caller's own triple, in one atomic statement that reads no row; the result carries `HasSecret`, never the secret. **Implemented** (`add-notification-domain-and-preferences`) |
| `ListPreferences` | Authenticated user | — | The caller's own preferences, ordered by `(event_type, channel)`; an empty set is a legitimate answer. The `UserID` is a parameter, never read from anywhere ambient. **Implemented** (`add-notification-domain-and-preferences`) |
| `SendJobCompletionNotification` | Event handler | `VideoJobCompleted` event received | Notification delivered per user's preferences. Planned — `add-notification-webhook-delivery` |
| `SendJobFailureNotification` | Event handler | `VideoJobFailed` event received | Notification delivered per user's preferences. Planned — `add-notification-webhook-delivery` |

`SetPreference` deliberately does **not** pre-read to decide create-from-update. The branch that matters is a property of the *request* — did the caller send a secret? — and each answer maps to one atomic statement in the adapter: an `INSERT … ON CONFLICT DO UPDATE` that overwrites the secret, or an `UPDATE … RETURNING` that never names the `secret` column. Affecting **zero rows** on the second is the create-with-no-secret case, and is the only signal needed to refuse it (`ErrSecretRequired`, surfaced as a `400`). No transaction, no retry loop, and no read-then-write window on either path.

---

## Domain Events

All events are serialized as JSON. The `type` field is the canonical discriminator. Events are immutable once emitted.

| Event | JSON fields | Crosses context boundary? | Status |
|---|---|---|---|
| `VideoJobCreated` | `type`, `job_id`, `user_id`, `original_filename`, `occurred_at` | No (internal) | **Recorded** (Phase 3): `internal/video/infrastructure/postgres.Repository.Create` writes this exact payload to the `video_job_outbox` table transactionally with the `video_jobs` row — not yet published anywhere, since nothing consumes it (marked "No" cross-context above) |
| `VideoJobQueued` | `type`, `job_id`, `user_id`, `source_key`, `content_hash`, `occurred_at` | No (internal) | **Recorded, published, and consumed** (Phase 6): `postgres.Repository.Enqueue` writes this payload to `video_job_outbox` transactionally with the `pending → queued` update, `messaging.Relay` publishes it verbatim to `video.jobs.v2`, and `cmd/worker` consumes it. `source_key` is what the worker needs to fetch the video. `content_hash` travels with it for traceability, but the worker does **not** clear the idempotency key from the message field: `ClearJobIdempotencyKey` takes only a job ID and loads the persisted hash through the undecorated repository, so the key it deletes is derived from committed state rather than from a payload that may describe an earlier attempt. `type` and the routing key are both `video_job.queued.v2` — the generation suffix is carried by the event-type string as well as the exchange, because every replica's relay claims from one shared outbox table filtered on it |
| `VideoJobStarted` | `type`, `job_id`, `occurred_at` | No (internal) | Planned. Not emitted: `StartProcessing` persists through the conditional claim, which writes no outbox row |
| `VideoJobCompleted` | `type`, `job_id`, `user_id`, `frame_count`, `storage_key`, `occurred_at` | Yes — Video Processing → Notification | **Recorded and published, not yet consumed** (Phase 7, `emit-videojob-terminal-events`): `postgres.Repository.Update` writes this payload to `video_job_outbox` in the same transaction as the fenced terminal `UPDATE`, and only when that statement affected a row; `cmd/worker`'s terminal relay publishes it to `video.jobs.terminal.v1`. `type` and the routing key are both `video_job.completed.v1`. Nothing consumes the queue until `add-notification-webhook-delivery` ships |
| `VideoJobFailed` | `type`, `job_id`, `user_id`, `error_reason`, `occurred_at` | Yes — Video Processing → Notification | **Recorded and published, not yet consumed** (Phase 7, `emit-videojob-terminal-events`): written and published exactly as `VideoJobCompleted` is, by the same transactional write and the same relay, under `video_job.failed.v1` onto the same queue — one queue carries both outcomes, since a consumer interested in a job's end is interested in either |
| `UserRegistered` | `type`, `user_id`, `email`, `occurred_at` | Yes (optionally) — Identity → Notification | Planned — deferred until Notification (Phase 7) needs it; Identity itself (Phase 2) is implemented |
| `UserAuthenticated` | `type`, `user_id`, `occurred_at` | No (internal) | Planned; not emitted by the current `AuthenticateUser` use case |

Integration events that cross context boundaries are published to RabbitMQ (Phase 6) via a transactional-outbox relay. That relay now exists (`openspec/specs/videojob-outbox-relay/spec.md`) and its source table is `video_job_outbox` (Phase 3), and there are now two of them, each claiming its own disjoint set of event types from that one table: the dispatch relay in `cmd/api` carries `VideoJobQueued` to the job queue that `cmd/worker` consumes, and the terminal relay in `cmd/worker` carries `VideoJobCompleted` and `VideoJobFailed` to the terminal-event queue. `VideoJobCreated` rows are written and deliberately never published — an internal event, and each relay's claim filters on `event_type` precisely so that permanent backlog cannot starve the rows that are meant to go out. The two events that cross to Notification are recorded as of `emit-videojob-terminal-events`: `Repository.Update` — the write path behind `CompleteJob` and `FailJob` — commits the event with the outcome, so a terminal job and its announcement cannot exist without one another, and an actor whose write was refused by the fence or found its own outcome already stored records nothing. Nothing consumes them yet; the queue holds them until Phase 7's delivery change ships, and deduplicating repeated deliveries is that consumer's obligation. See `openspec/specs/videojob-persistence/spec.md` for what `video_job_outbox` currently records. Internal domain events do not need to be published to the broker. Identity (Phase 2) is implemented but does not emit either event above yet — see the Identity Context use-case table.

---

## Cross-Context Contracts

- **`UserID`** is the identifier that crosses bounded context boundaries, but not as a shared type: each context defines and owns its own local `UserID` value object (`internal/identity/domain/user_id.go` and, as of Phase 3's `add-videojob-domain-and-application`, `internal/video/domain/user_id.go`) — two distinct Go types, never a package shared between the two contexts' `domain` layers. Translation between a source context's identifier and a consuming context's local type happens only at the composition root or via consumed integration events. `cmd/api` is where that translation happens: `cmd/api/video.go`'s HTTP handlers (wired by Phase 3's `wire-videojob-http-endpoints`) convert the bearer-auth middleware's already-verified `identity.UserID` string into `video.UserID` on every request. `cmd/worker` (Phase 6) is a second composition root but performs **no** such translation — it never sees a token and reads the `user_id` off the dispatch message, which the API already translated. It has no Identity configuration at all. No `pkg/` directory exists or is planned — a shared kernel was considered and rejected (see `add-videojob-domain-and-application`'s `design.md` in the archive) as tighter coupling than this architecture's context-independence goal justifies.
- The Video Processing context's own `UserID` constructor enforces only non-emptiness, not any identifier format or existence check — the identifier itself is minted once, at user creation, by Identity's `UserIDGenerator`; the bearer-auth middleware only verifies the token and supplies that already-minted, already-verified value to the handler, which Video Processing's `UserID` then wraps.
- The Notification context receives integration events over RabbitMQ and resolves delivery preferences by `UserID` alone, never by calling into Identity or Video Processing internals. As of `add-notification-domain-and-preferences` the preferences it will resolve exist and are stored in its own database under its own DSN; the event consumption is still planned. Its independence is enforced rather than asserted — `internal/notification/dependency_rules_test.go` fails the build on an import of either other context.
