# videojob-persistence Specification

## Purpose

Define the PostgreSQL-backed implementation of `domain.VideoJobRepository` in the Video Processing bounded context's `infrastructure` layer, and the transactional-outbox behavior on job creation and on the `pending → queued` transition. This is infrastructure only — no HTTP route or composition-root wiring is in scope here; see `videojob-lifecycle` for the domain/application use cases this repository implements the port for, and the Change Backlog in `docs/roadmap.md` for what wires it in.

## Requirements

### Requirement: PostgreSQL Repository Implements VideoJobRepository

`internal/video/infrastructure/postgres.Repository` SHALL implement `domain.VideoJobRepository`'s `Create`, `FindByID`, `FindByUserID`, and `FindCompletedByUserID(ctx, userID)` against a `video_jobs` table, using parameterized queries and reconstructing `*domain.VideoJob` via `domain.RestoreVideoJob` from stored rows.

The `video_jobs` table SHALL carry a `source_key` column holding the job's **source** object key, distinct from `storage_key`, which holds the result key set at completion. It SHALL be added as `TEXT NOT NULL DEFAULT ''` — an additive migration with no backfill, because the key embeds a generated `uploadID` that exists in no other column and cannot be reconstructed for a pre-existing row. `Create`, `FindByID`, `FindByUserID`, and `FindCompletedByUserID` SHALL all round-trip it.

The table SHALL additionally carry a `content_hash` column, added the same way (`TEXT NOT NULL DEFAULT ''`, additive, no backfill), holding the SHA-256 digest the submitting handler computed over the uploaded bytes. It exists so a process other than that handler can reconstruct the job's idempotency key, which `upload-idempotency` derives from the owner and the content hash. Storing the digest and not the reservation **token** is deliberate: the digest is already recoverable from the stored object, whereas the token is a possession capability that SHALL NOT be persisted (see `videojob-worker`). `Create`, `FindByID`, `FindByUserID`, and `FindCompletedByUserID` SHALL round-trip it, and a pre-migration row SHALL load with an empty value rather than an error.

`FindCompletedByUserID` SHALL restrict to `completed` jobs **in the query** and SHALL return all of them, taking no offset or limit. It orders identically to `FindByUserID`: `CreatedAt` descending with `VideoJobID` ascending as a tie-breaker.

The absence of pagination is deliberate and is the reason this method exists separately from `FindByUserID`. Its only caller is `GET /api/status`, which accepts no pagination parameters and whose filesystem-backed predecessor returned every zip the caller owned. Reusing `FindByUserID` and filtering afterwards would be wrong twice over: a page of recent `pending`/`failed` jobs would displace a user's completed results out of the listing, and any limit at all would make older results permanently unreachable through the only listing endpoint the frontend consumes. Filtering on status in SQL is what makes returning the full set reasonable — the rows returned are exactly the rows rendered.

`internal/video/infrastructure/cache.CachedVideoJobRepository` SHALL pass `FindCompletedByUserID` straight through to the decorated repository without caching it, exactly as it already does for `FindByUserID` — the status cache is keyed by individual job ID and has nothing to offer a multi-row listing.

#### Scenario: A created job round-trips through FindByID

- **GIVEN** a `VideoJob` persisted via `Repository.Create`
- **WHEN** `Repository.FindByID` is called with that job's ID
- **THEN** it returns a `*domain.VideoJob` with the same `ID`, `UserID`, `OriginalFilename`, source key, content hash, `StorageKey`, `FrameCount`, `ErrorReason`, `Status`, and `CreatedAt`

#### Scenario: A pre-migration row loads with an empty source key and content hash

- **GIVEN** a `video_jobs` row written before the `source_key` and `content_hash` columns existed, in any status
- **WHEN** `Repository.FindByID` is called for it
- **THEN** it returns the job with both values empty rather than an error

#### Scenario: FindByID reports not-found for an unknown ID

- **GIVEN** no `VideoJob` exists for a given ID
- **WHEN** `Repository.FindByID` is called with that ID
- **THEN** it returns `domain.ErrVideoJobNotFound`

#### Scenario: FindByUserID orders by CreatedAt descending

- **GIVEN** `VideoJob`s persisted for the same `UserID` with distinct `CreatedAt` values
- **WHEN** `Repository.FindByUserID` is called
- **THEN** the returned jobs are ordered newest-`CreatedAt`-first

#### Scenario: FindByUserID orders and paginates via the stored index

- **GIVEN** multiple `VideoJob`s persisted for the same `UserID`, some with equal `CreatedAt` values
- **WHEN** `Repository.FindByUserID` is called with an offset and limit
- **THEN** the returned jobs are ordered by `CreatedAt` descending with `VideoJobID` ascending as a tie-breaker, bounded by the given offset and limit — matching the ordering `videojob-lifecycle`'s `ListUserJobs` requirement documents

#### Scenario: FindCompletedByUserID returns only completed jobs

- **GIVEN** a `UserID` with jobs in `pending`, `processing`, `failed`, and `completed` statuses
- **WHEN** `Repository.FindCompletedByUserID` is called
- **THEN** only the `completed` jobs are returned

#### Scenario: Non-completed jobs do not hide completed ones

- **GIVEN** a `UserID` whose most recently created jobs are all non-`completed`, with `completed` jobs older than them
- **WHEN** `Repository.FindCompletedByUserID` is called
- **THEN** the completed jobs are returned, rather than an empty result

#### Scenario: All completed jobs are returned, with no implicit limit

- **GIVEN** a `UserID` with more `completed` jobs than `ListUserJobs`' maximum page size
- **WHEN** `Repository.FindCompletedByUserID` is called
- **THEN** every one of them is returned

#### Scenario: FindCompletedByUserID is scoped to its user

- **GIVEN** `completed` jobs belonging to two different users
- **WHEN** `Repository.FindCompletedByUserID` is called for one of them
- **THEN** only that user's jobs are returned

### Requirement: VideoJobCreated Is Recorded to an Outbox Transactionally With Job Creation

`Repository.Create` SHALL insert the `video_jobs` row and a `video_job_outbox` row describing the job's creation in a single database transaction, so that the two are never observably inconsistent: either both are committed or neither is.

#### Scenario: Successful creation records a matching outbox row

- **GIVEN** a valid `*domain.VideoJob` passed to `Repository.Create`
- **WHEN** the call succeeds
- **THEN** a `video_job_outbox` row exists whose `event_type` is `video_job.created`, whose `payload` contains `type: "video_job.created"` plus that job's `job_id`, `user_id`, `original_filename`, and `occurred_at`, and whose `published_at` is `NULL`

#### Scenario: A failed job-row insert leaves no outbox row

- **GIVEN** `Repository.Create` is called with a `VideoJob` whose insert into `video_jobs` violates a database constraint (e.g. a duplicate ID)
- **WHEN** the call returns an error
- **THEN** no corresponding `video_job_outbox` row was committed

#### Scenario: A failed outbox insert leaves no job row

- **GIVEN** `Repository.Create` is called and the `video_jobs` insert succeeds but the subsequent `video_job_outbox` insert fails
- **WHEN** the call returns an error
- **THEN** no corresponding `video_jobs` row was committed either — the transaction rolls back both writes, not just the one that failed

### Requirement: Enqueue Persists the Queued Transition and Its Event Transactionally

`domain.VideoJobRepository` SHALL expose an `Enqueue` method, and `internal/video/infrastructure/postgres.Repository` SHALL implement it by updating the job's `video_jobs` row to `queued` and inserting a `video_job_outbox` row describing that transition, in a single database transaction — so that a queued job and the event announcing it are never observably inconsistent, exactly as `Create` already guarantees for job creation.

The outbox row's `event_type` SHALL be the current job-dispatch generation's queued-event string (see `videojob-messaging`), following the `video_job.created` constant already in this package, and that string SHALL be a single shared constant rather than a literal repeated at the insert, at the relay's claim, and at the routing key. A drifted literal produces a relay that matches nothing and reports no error.

The payload SHALL carry `type` (that same event-type string), `job_id`, `user_id`, `source_key`, `content_hash`, and `occurred_at`, matching the `VideoJobQueued` event `docs/domain-model.md` already defines and mirroring the shape `video_job.created` already persists. The discriminator and the timestamp are not optional extras: the relay forwards the stored payload verbatim, so whatever is written here *is* the wire contract a consumer parses, and an event without a `type` cannot be dispatched on by a subscriber that will eventually see more than one kind.

This is a dedicated method rather than a status-dependent behavior added to `Update`. `Update` is also `CompleteJob`'s and `FailJob`'s path, so making it outbox-aware would turn event emission into a side effect of a general-purpose method and would decide, as a by-product, the shape Phase 7 inherits for `VideoJobCompleted`/`VideoJobFailed`.

`internal/video/infrastructure/cache.CachedVideoJobRepository` SHALL implement `Enqueue` write-through, like `Update`: PostgreSQL first, then an unconditional cache write. It SHALL NOT pass the call through uncached — a job left `pending` in the cache while `queued` in PostgreSQL would make `GET /api/video-jobs/:id` contradict the row the relay is about to publish.

#### Scenario: Enqueue records a matching outbox row

- **GIVEN** a persisted `VideoJob` in `pending` status with a non-empty source key
- **WHEN** `Repository.Enqueue` is called with it after its `Enqueue` transition has been applied
- **THEN** its row's status is `queued`, and a `video_job_outbox` row exists whose `event_type` is the current generation's queued-event string, whose payload carries `type` set to that same string plus that job's `job_id`, `user_id`, `source_key`, `content_hash`, and `occurred_at`, and whose `published_at` is `NULL`

#### Scenario: A failed outbox insert leaves the job unqueued

- **GIVEN** `Repository.Enqueue` is called and the `video_jobs` update succeeds but the `video_job_outbox` insert fails
- **WHEN** the call returns an error
- **THEN** the job's persisted status is still `pending` — the transaction rolls back both writes, so no job is left `queued` with nothing to dispatch it

#### Scenario: The cached decorator writes through

- **GIVEN** a cached `VideoJob` in `pending` status
- **WHEN** `CachedVideoJobRepository.Enqueue` succeeds
- **THEN** a subsequent `FindByID` served from cache returns `queued`, not `pending`

### Requirement: Update Persists a VideoJob's Transitioned State

`Repository.Update` SHALL persist an already-loaded `VideoJob`'s current `status`, `frame_count`, `error_reason`, and `storage_key` to its existing `video_jobs` row, identified by its unchanging `id`. It SHALL NOT write a `video_job_outbox` row.

That exclusion is now load-bearing rather than incidental. Two repository methods write outbox rows — `Create` for `video_job.created` and `Enqueue` for the job-dispatch generation's queued event — and `Update` is deliberately not a third. It is the path `CompleteJob` and `FailJob` both take, so emitting an event from it would make event production a status-dependent side effect of a general-purpose write, and would settle the shape of `VideoJobCompleted`/`VideoJobFailed` as a by-product rather than as Phase 7's own decision. `Update` SHALL NOT be given an outbox write to avoid adding a dedicated method.

`Update` SHALL remain **unconditional** — a write identified by `id` alone. `StartProcessing` no longer takes this path (see `ClaimForProcessing` below), and the predicate that makes that transition a claim SHALL NOT be pushed down into `Update` instead. `Update` is `CompleteJob`'s and `FailJob`'s path, and those are writes by a process that already holds the job; giving them a claim predicate would decide their concurrency semantics as a side effect of solving a different problem — the same argument that gave `Enqueue` its own method.

#### Scenario: Update persists a transitioned job

- **GIVEN** a `VideoJob` was previously persisted via `Create` and has since had a transition method applied to it in memory
- **WHEN** `Repository.Update` is called with that job
- **THEN** a subsequent `Repository.FindByID` for its ID returns a job matching the updated `status`, `frame_count`, `error_reason`, and `storage_key`

#### Scenario: Update does not write an outbox row

- **GIVEN** a previously persisted `VideoJob`
- **WHEN** `Repository.Update` is called with it
- **THEN** no new `video_job_outbox` row is committed as a result of that call

### Requirement: ClaimForProcessing Persists the Processing Transition Only If the Job Is Still Queued

`Repository.ClaimForProcessing` SHALL persist a `VideoJob`'s `queued → processing` transition through a single statement whose predicate includes the stored status (`… WHERE id = $1 AND status = 'queued'`), and SHALL report to its caller whether a row was affected. Affecting no row SHALL be reported as a distinct outcome, not as success and not as a not-found error — the row exists; another consumer simply reached it first.

It SHALL be a single statement. A read-then-write, a transaction that selects and then updates, or a check performed in Go SHALL NOT be substituted: the guarantee is that the database evaluates the predicate and applies the update atomically, and any decomposition reintroduces the race the method exists to close.

It SHALL NOT write a `video_job_outbox` row, and SHALL NOT lock the row beyond the statement's own duration. The caller goes on to run an extraction lasting minutes; a claim that held a transaction open across it would be unusable.

`CachedVideoJobRepository` SHALL implement this method and SHALL write through **only when a row was affected**. A lost claim changed nothing in PostgreSQL, so writing the in-memory job to the cache would publish a state the authoritative store does not hold.

#### Scenario: A queued job is claimed

- **GIVEN** a persisted `VideoJob` in `queued` status
- **WHEN** `ClaimForProcessing` is called for it
- **THEN** it reports the row as affected, and a subsequent `FindByID` returns the job in `processing` status

#### Scenario: A job that is not queued is not claimed and is not modified

- **GIVEN** a persisted `VideoJob` in `processing`, `completed`, `failed`, or `pending` status
- **WHEN** `ClaimForProcessing` is called for it
- **THEN** it reports no row affected, and a subsequent `FindByID` returns the job with its status and every other column unchanged

#### Scenario: Two concurrent claims on one job produce exactly one winner

- **GIVEN** a persisted `VideoJob` in `queued` status
- **WHEN** two `ClaimForProcessing` calls for that ID execute concurrently
- **THEN** exactly one reports a row affected and the other reports none

#### Scenario: An unknown ID is reported as not found, not as a lost claim

- **GIVEN** no `video_jobs` row matches a given ID
- **WHEN** `ClaimForProcessing` is called with it
- **THEN** the caller can distinguish this from a lost claim, so a dispatch naming a nonexistent job is not mistaken for a duplicate

### Requirement: Outbox Rows Predating the Dispatch Generation Are Stamped Published by Migration

`Repository.Enqueue` SHALL write its outbox row under the **current generation's** `event_type`, which is the same constant the relay's claim and the broker's routing key use (see `videojob-messaging`). A generation bump therefore changes the persisted event-type value, and the constant SHALL remain single so the three cannot drift.

A schema migration SHALL stamp `published_at` on every `video_job_outbox` row that carries a **previous** generation's dispatch `event_type` and whose `published_at` is null at the time it runs. Those rows are already unreachable to the current relay, whose claim matches only its own generation's string; the migration records that fact rather than establishing it, and keeps them from being re-read forever by a relay of the old generation that is still running.

The boundary SHALL be the event type, not the moment of execution. The migration runs on every startup, so it SHALL be expected to stamp previous-generation rows written *after* an earlier execution — by a replica of the previous build still serving during a rolling deploy — and that is correct rather than a leak: such a row is undeliverable for the same reason as the rest. What it SHALL NOT do is install a standing rule, trigger, or predicate keyed on anything other than the previous generation's event type, since a mechanism that suppressed the current generation's rows would silently disable dispatch.

It SHALL be idempotent under re-execution, like every other migration this repository runs at startup, and SHALL NOT touch `video_job.created` rows, which have always been internal and unpublished by design. It SHALL NOT touch the current generation's rows either — a migration written against the *unversioned* dispatch event type is what keeps these two sets apart, and one written against "any unpublished dispatch row" would stamp live work as published and drop it silently.

`videojob-outbox-relay` states why these rows must not be dispatched; this requirement is the mechanism.

#### Scenario: Pre-existing unpublished rows of the previous generation are stamped

- **GIVEN** unpublished dispatch rows written under the previous generation's `event_type`
- **WHEN** the migration runs
- **THEN** every one of them carries a `published_at` value and none is returned by a subsequent relay claim of either generation

#### Scenario: Rows written after the migration are unaffected

- **GIVEN** the migration has already run
- **WHEN** a new job is enqueued, writing a fresh row under the current generation's `event_type`
- **THEN** that row is unpublished and the relay claims it normally

#### Scenario: The created-event backlog is left alone

- **GIVEN** unpublished `video_job.created` rows accumulated since Phase 3
- **WHEN** the migration runs
- **THEN** their `published_at` values are still null
