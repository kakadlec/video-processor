# videojob-persistence Specification

## Purpose

Define the PostgreSQL-backed implementation of `domain.VideoJobRepository` in the Video Processing bounded context's `infrastructure` layer, and the transactional-outbox behavior on job creation, the initial `pending → queued` transition, and recovery `processing → queued` transitions, plus the bounded, index-served aggregates of in-flight work that the concrete repository — and deliberately not the domain port — exposes for `service-metrics`' pipeline gauges. This is infrastructure only — no HTTP route or composition-root wiring is in scope here; see `videojob-lifecycle` for the domain/application use cases this repository implements the port for, and the Change Backlog in `docs/roadmap.md` for what wires it in.
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
- **THEN** it returns a `*domain.VideoJob` with the same `ID`, `UserID`, `OriginalFilename`, source key, content hash, `StorageKey`, `FrameCount`, `ErrorReason`, and `Status`
- **AND** a non-zero `CreatedAt` PostgreSQL minted at persist time — not one the caller supplied, since `Create` no longer accepts one (see `mint-videojob-timestamps-in-database` in `docs/roadmap.md`)

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

This is a dedicated method rather than a status-dependent behavior added to `Update`. `Update` is also `CompleteJob`'s and `FailJob`'s path, so folding the dispatch event into it would have turned event emission into a side effect of a general-purpose method and would have decided, as a by-product, the shape Phase 7 inherits for `VideoJobCompleted`/`VideoJobFailed`. Phase 7 has since decided that shape on its own terms (`videojob-terminal-events`), and `Update` does now write an outbox row — its own terminal events, gated on its conditional statement having applied. The separation stands: each write path emits the event describing the transition it performs, and neither is a general-purpose emitter.

`internal/video/infrastructure/cache.CachedVideoJobRepository` SHALL implement `Enqueue` write-through, like `Update`: PostgreSQL first, then the cache's atomic epoch/status-ordered write. It SHALL NOT pass the call through uncached — a job left `pending` in the cache while `queued` in PostgreSQL would make `GET /api/video-jobs/:id` contradict the row the relay is about to publish. The ordered write also prevents a delayed enqueue cache update from replacing a newer state.

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

`Repository.Update` SHALL persist an already-loaded `VideoJob`'s current `status`, `frame_count`, `error_reason`, and `storage_key` to its existing `video_jobs` row, identified by its unchanging `id`, **by the fence epoch its caller claimed with, and by the row still being `processing`**. **It SHALL write a `video_job_outbox` row describing the terminal outcome, in the same transaction, and only when the conditional statement affected a row.**

That inclusion is Phase 7's deliberate decision, replacing this requirement's previous exclusion. The exclusion existed so that the shape of `VideoJobCompleted`/`VideoJobFailed` would not be settled as a by-product of a general-purpose write before the Notification context had a say; it is now settled on its own terms by `videojob-terminal-events`, which owns the payloads, the event types, and the emission rule. The other half of the original objection — that `Update` is a general-purpose write — SHALL be read as no longer holding in fact: `Update`'s only callers are `CompleteJob` and `FailJob`, and its statement hardcodes `status = 'processing'` as the precondition, so it can persist nothing but a terminal transition. A separate outbox-writing sibling next to `Update` SHALL NOT be introduced, because it would leave `Update` with no callers; the contrast with `Enqueue` is that `Enqueue` has a distinct precondition and a distinct caller, and such a sibling would have neither.

**The event write SHALL be gated by the conditional statement's own row count, not by a separately evaluated predicate.** Affecting no row SHALL write no event, on both of the paths where that happens — the fenced refusal and the already-recorded identical outcome — so that the actor who wins an outcome and the actor who announces it can never diverge.

`Update` SHALL be **fenced**: its predicate SHALL include the caller-supplied epoch, and affecting no row SHALL be classified rather than collapsed — see the three readings below. It SHALL report to its caller whether the write was **applied**, not only whether it errored: an error-only signature cannot express the case where the row already carries exactly this caller's outcome, and `videojob-lease-recovery`'s single-cleanup guarantee depends on that distinction. **The same distinction now also gates event emission.** Any existing row other than the exact same terminal outcome at the same epoch SHALL be reported with a distinct exported sentinel — the row exists, but the caller no longer owns a matching `processing` job. This is a deliberate reversal of this requirement's previous "`Update` SHALL remain unconditional" clause, and the reason it reversed is that recovery now exists: a worker presumed dead can return mid-run, and the only thing that can stop it committing over its successor is a predicate in the same statement as the write.

**The predicate SHALL also require the stored status to be `processing`**, and that conjunct SHALL NOT be dropped as redundant with the epoch. It is what makes a terminal write *exclusive* rather than merely *ordered*: the epoch advances only on a requeue, so two actors can legitimately hold the same epoch for one job — a live but leaseless worker and the sweeper that decided to abandon it both act at the epoch they observed. Both would pass an epoch-only predicate, both would commit, and the second would overwrite the first. With the status conjunct the first write leaves the row terminal, the second matches no row, and exactly one actor may then perform the cleanup that follows a terminal state **and record the one event announcing it**. An argument that the aggregate's own transition check prevents this SHALL NOT be substituted: both actors evaluate that check against copies loaded before either write.

This is safe precisely because `Update` performs only `processing → completed` and `processing → failed`. `Enqueue` owns `pending → queued`, `ClaimForProcessing` owns `queued → processing`, and the requeue method above owns `processing → queued`; no caller of `Update` writes from any other status. **A job whose in-memory status is neither `completed` nor `failed` SHALL be refused with an error before any statement runs, rather than written with no corresponding event type.**

The fence SHALL NOT be confused with the claim. `ClaimForProcessing` decides who *starts* a job; `Update` decides who may *finish* one. `StartProcessing` SHALL NOT be routed through `Update`.

The ways `Update` can affect no row SHALL be distinguished through an authoritative follow-up lookup, like the classification after a lost claim. The complete classification is:

- **No row with the ID** returns `ErrVideoJobNotFound`.
- **A matching epoch on a terminal row carrying exactly the outcome being written** is idempotent success with `Applied=false`, not a fence. The caller then follows its outcome-specific contract: a completion retry may finish its own source/lease cleanup after a possibly lost response, while an already-present failure is acknowledged without cleanup. **No second event is written.**
- **Every other existing row** returns `ErrJobFenced`. This includes a strictly greater epoch (takeover), a matching epoch with a different terminal outcome (another actor at that epoch finished first), a lower epoch, and any non-`processing` status that is not the identical outcome. The caller rejects, keeps the source object, clears no idempotency key, and performs no cleanup. **No event is written.** The application and current worker log need not distinguish those predicates because none grants cleanup rights.

#### Scenario: Update persists a transitioned job

- **GIVEN** a `VideoJob` was previously persisted via `Create` and has since had a transition method applied to it in memory
- **WHEN** `Repository.Update` is called with that job and the epoch its row carries
- **THEN** a subsequent `Repository.FindByID` for its ID returns a job matching the updated `status`, `frame_count`, `error_reason`, and `storage_key`

#### Scenario: Update refuses a write carrying a superseded epoch

- **GIVEN** a persisted `VideoJob` whose `lease_epoch` has advanced since a caller read it
- **WHEN** `Repository.Update` is called with that caller's epoch
- **THEN** it returns the fence sentinel and every column of the row is unchanged

#### Scenario: Two actors at the same epoch cannot both commit a terminal state

- **GIVEN** a persisted `processing` `VideoJob` and two actors that both observed it at the same epoch — a leaseless worker still running and a sweeper that has reached the abandonment bound
- **WHEN** both call `Repository.Update` with that epoch, one writing `completed` and the other `failed`
- **THEN** exactly one affects a row, the other is refused, and the persisted job carries only the winner's outcome

#### Scenario: Update writes the terminal outbox row in the same transaction

- **GIVEN** a persisted `processing` `VideoJob` and a caller holding its current epoch
- **WHEN** `Repository.Update` is called with a terminal outcome
- **THEN** the updated row and exactly one unpublished terminal outbox row naming that job are both visible after the call, and neither is visible before it

#### Scenario: A refused Update writes no outbox row

- **GIVEN** a persisted `VideoJob` whose stored row does not satisfy the caller's epoch-and-`processing` predicate
- **WHEN** `Repository.Update` is called with it
- **THEN** it reports a refusal or an unapplied write, and no new `video_job_outbox` row is committed as a result of that call

### Requirement: ClaimForProcessing Persists the Processing Transition Only If the Job Is Still Queued

`Repository.ClaimForProcessing` SHALL persist a `VideoJob`'s `queued → processing` transition through a single statement whose predicate includes the stored status (`… WHERE id = $1 AND status = 'queued'`), and SHALL report to its caller whether a row was affected **and, when one was, that row's `lease_epoch`**. Affecting no row SHALL be reported as a distinct outcome, not as success and not as a not-found error — the row exists; another consumer simply reached it first.

The epoch SHALL be read by the claiming statement itself, not by a preceding or following query. A value read before the claim can be stale by the time the claim lands — a sweep can requeue the job in between — and the winner would then hold an epoch that fences its own terminal write; a value read afterwards is a second statement another writer can interleave with.

The predicate SHALL name `queued` and nothing else. It SHALL NOT be widened to admit a `processing` row whose lease has lapsed, however tempting that is as a route to recovery: the lease is Redis-backed and fails open, so a lease-store outage would license two workers to claim one live job. `videojob-lease-recovery` returns an abandoned job to `queued` instead, after which this predicate applies unchanged.

It SHALL be a single statement. A read-then-write, a transaction that selects and then updates, or a check performed in Go SHALL NOT be substituted: the guarantee is that the database evaluates the predicate and applies the update atomically, and any decomposition reintroduces the race the method exists to close.

It SHALL NOT write a `video_job_outbox` row, SHALL NOT advance `lease_epoch`, and SHALL NOT lock the row beyond the statement's own duration. The caller goes on to run an extraction lasting minutes; a claim that held a transaction open across it would be unusable.

`CachedVideoJobRepository` SHALL implement this method and SHALL write through **only when a row was affected**. A lost claim changed nothing in PostgreSQL, so writing the in-memory job to the cache would publish a state the authoritative store does not hold.

#### Scenario: A queued job is claimed

- **GIVEN** a persisted `VideoJob` in `queued` status
- **WHEN** `ClaimForProcessing` is called for it
- **THEN** it reports the row as affected together with that row's epoch, and a subsequent `FindByID` returns the job in `processing` status

#### Scenario: The claim does not advance the epoch

- **GIVEN** a persisted `VideoJob` in `queued` status at a known epoch
- **WHEN** `ClaimForProcessing` succeeds
- **THEN** the epoch it reports and the epoch stored on the row are both that same value

#### Scenario: A job that is not queued is not claimed and is not modified

- **GIVEN** a persisted `VideoJob` in `processing`, `completed`, `failed`, or `pending` status
- **WHEN** `ClaimForProcessing` is called for it
- **THEN** it reports no row affected, and a subsequent `FindByID` returns the job with its status and every other column unchanged

#### Scenario: A processing job with a lapsed lease is still not claimable

- **GIVEN** a persisted `VideoJob` in `processing` status whose lease has expired
- **WHEN** `ClaimForProcessing` is called for it
- **THEN** it reports no row affected — recovery is the requeue path's job, not this predicate's

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

### Requirement: The video_jobs Table Carries a Fence Epoch

The `video_jobs` table SHALL carry a `lease_epoch BIGINT NOT NULL DEFAULT 0` column. Application transitions SHALL generate non-negative values by initializing at zero and incrementing on requeue; the database schema carries no additional `CHECK`, and restoration SHALL accept the stored integer. The column is added additively with no backfill, declared inline for a database created from scratch and applied through `ADD COLUMN IF NOT EXISTS` for one that already exists, exactly as `source_key` and `content_hash` are.

The default SHALL be the correct value for every pre-existing row rather than a placeholder: the epoch counts how many times a job has been returned to the queue after abandonment, and a row written before this column existed has been returned zero times. A `processing` row carrying the default is therefore an ordinary abandonment candidate, which is precisely the backlog this change is meant to recover.

`Create`, `FindByID`, `FindByUserID`, and `FindCompletedByUserID` SHALL round-trip the value, and `domain.RestoreVideoJob` SHALL accept it. Reconstitution SHALL NOT reject a stored row solely because of a status/epoch pairing. Normal transitions create `pending` only at epoch zero and may reach `queued`, `processing`, or a terminal status at epoch zero or later, but the restoration boundary validates the persisted fields independently rather than inventing a cross-field invariant.

Only the requeue path SHALL advance it. `Create`, `Enqueue`, `Update`, and `ClaimForProcessing` SHALL leave it as they found it, so the stored value reads unambiguously as the job's abandonment count and can be used as the bound `videojob-lease-recovery` requires.

#### Scenario: A pre-migration row loads at epoch zero

- **GIVEN** a `video_jobs` row written before the `lease_epoch` column existed, in any status
- **WHEN** `Repository.FindByID` is called for it
- **THEN** it returns the job with epoch zero rather than an error

#### Scenario: The epoch round-trips through every read path

- **GIVEN** a job whose epoch has been advanced by a requeue
- **WHEN** it is read back via `FindByID`, `FindByUserID`, and `FindCompletedByUserID`
- **THEN** every one of them reports the same epoch

#### Scenario: An ordinary transition does not advance the epoch

- **GIVEN** a job at a known epoch
- **WHEN** it is claimed, then completed
- **THEN** its stored epoch is the same value it started with

### Requirement: Requeue Persists the Abandonment Transition and Its Event Transactionally

`domain.VideoJobRepository` SHALL expose a requeue method, and `internal/video/infrastructure/postgres.Repository` SHALL implement it by updating the job's row to `queued`, advancing `lease_epoch` by one, and inserting a `video_job_outbox` row describing that dispatch, **in a single database transaction** — so an abandoned job and the event that re-dispatches it are never observably inconsistent, exactly as `Enqueue` already guarantees for the first dispatch.

The update SHALL be conditional on the row still being in `processing` status **and** still carrying the epoch its caller observed, and the method SHALL report whether a row was affected. Affecting no row SHALL be reported as a distinct outcome rather than as success or as an error: another sweeper won, or the job has since finished. The whole transaction, including the outbox insert, SHALL be rolled back in that case.

The outbox row SHALL be indistinguishable from the one `Enqueue` writes — same `event_type` constant, same payload shape, same fields — so recovery reuses the dispatch path end to end rather than introducing a second message the worker would have to recognise.

It SHALL be a distinct method rather than a mode of `Enqueue` or of `Update`. `Enqueue` asserts a `pending → queued` transition on a job that has never run; `Update` is the formerly unconditional-by-id terminal path that this requirement's sibling now fences. Folding the requeue into either would give a general-purpose method a second concurrency contract.

`CachedVideoJobRepository` SHALL implement it write-through, and SHALL write through **only when a row was affected** — a requeue that lost its race changed nothing in PostgreSQL, and publishing the caller's in-memory `queued` job would contradict the winner.

#### Scenario: A requeue moves the job and writes its dispatch together

- **GIVEN** a persisted `VideoJob` in `processing` status at a known epoch
- **WHEN** the requeue is called with that epoch
- **THEN** it reports a row affected, the job's status is `queued`, its epoch is one greater, and exactly one unpublished `video_job_outbox` row exists carrying the current generation's `event_type` and that job's `job_id`, `user_id`, `source_key`, and `content_hash`

#### Scenario: A requeue whose epoch is stale changes nothing

- **GIVEN** a persisted `VideoJob` in `processing` status whose epoch has advanced since the caller observed it
- **WHEN** the requeue is called with the observed epoch
- **THEN** it reports no row affected, the job's status and epoch are unchanged, and no outbox row was written

#### Scenario: A requeue of a job that has since finished changes nothing

- **GIVEN** a persisted `VideoJob` that reached `completed` after the caller observed it as `processing`
- **WHEN** the requeue is called
- **THEN** it reports no row affected and the job is still `completed`

#### Scenario: A failed outbox insert leaves the job processing

- **GIVEN** a requeue whose `video_jobs` update succeeds but whose outbox insert fails
- **WHEN** the call returns an error
- **THEN** the job's persisted status is still `processing` at its original epoch — the transaction rolls back both writes

#### Scenario: The cached decorator writes through only on a won requeue

- **GIVEN** a cached `VideoJob` in `processing` status
- **WHEN** the requeue succeeds and reports a row affected
- **THEN** a subsequent `FindByID` served from cache returns `queued`; and when it reports no row affected, the cache entry is left unchanged

### Requirement: The Repository Enumerates Processing Jobs for the Sweeper

`domain.VideoJobRepository` SHALL expose a method returning a bounded batch of jobs currently in `processing` status, and `postgres.Repository` SHALL implement it with the status filter **in the query**, ordered deterministically, taking an explicit limit.

Successive calls SHALL NOT be able to return the same bounded prefix forever. A fixed `ORDER BY id LIMIT n` starves recovery: if the first `n` `processing` rows belong to healthy long-running extractions whose leases keep being renewed, every cycle examines those same rows and an abandoned job outside that prefix is never reached, for as long as those extractions last. The scan SHALL therefore advance across cycles — a keyset cursor carried between sweeps, or an ordering that puts the least recently examined rows first — so that recovery latency is bounded by the number of `processing` rows and the sweep interval, not by when unrelated jobs happen to finish. Where a cursor is used, its zero value SHALL mean "start of scan" and SHALL omit the keyset predicate rather than being bound as a value: the first cycle and every wrap pass it, and the column it would compare against is a `uuid`.

The filter SHALL NOT be applied in Go over a broader read. The result set this feeds is scanned on a timer for the life of the deployment, and a scan whose cost grows with total job history is the failure mode `videojob-outbox-relay`'s claim index exists to avoid. An index supporting the predicate SHALL exist for the same reason.

It SHALL NOT be exposed through any HTTP route: it is not owner-scoped and returns other users' jobs by construction.

`CachedVideoJobRepository` SHALL pass it straight through without caching, as it already does for the other multi-row reads.

#### Scenario: Only processing jobs are returned

- **GIVEN** jobs in `pending`, `queued`, `processing`, `completed`, and `failed` status
- **WHEN** the method is called
- **THEN** only the `processing` ones are returned

#### Scenario: The batch is bounded

- **GIVEN** more `processing` jobs than the requested limit
- **WHEN** the method is called with that limit
- **THEN** at most that many jobs are returned

#### Scenario: The first scan and a wrapped scan return rows

- **GIVEN** `processing` jobs and a scan starting from the zero cursor, as the first cycle and every wrap do
- **WHEN** the method is called
- **THEN** it returns rows rather than failing, because the zero cursor selects the start of the scan instead of being compared against an identifier

#### Scenario: An abandoned job outside the first batch is still reached

- **GIVEN** more `processing` jobs than the batch limit, where every job within one batch's worth is healthy and leased, and an abandoned one sorts after them
- **WHEN** the sweeper runs repeatedly while those healthy jobs keep running
- **THEN** the abandoned job is eventually returned by a scan and recovered, rather than waiting for the healthy jobs to finish

#### Scenario: Jobs of every owner are returned

- **GIVEN** `processing` jobs belonging to two different users
- **WHEN** the method is called
- **THEN** both users' jobs are returned, because recovery is not owner-scoped

### Requirement: A Repository Error Carries the Unavailability Sentinel Only When It Establishes That the Server Could Not Answer

`internal/video/infrastructure/postgres.Repository` SHALL mark with a distinct domain sentinel exactly those failures whose own evidence establishes that **the server did not, or could not, answer the statement**, so a caller can tell *the database could not answer* from *the database answered and the answer could not be used*. Marked errors SHALL be wrapped so the sentinel is reachable by `errors.Is`, with the original error preserved as the wrapped cause.

**The classification SHALL be made from the error's own evidence, and SHALL NOT be derived from which driver method returned it.** Marking whatever a `Scan`, `Query` or `Exec` call returned is a *call-site* classification and not an availability one: each of those calls reports permanent failures through the same return value as transient ones. A `Scan` yields a lost connection and a value-conversion or destination-count failure alike; a `Query` or `Exec` yields a lost connection and a server-answered refusal alike. Marking one of the permanent kinds would tell `videojob-worker` to retry a message that can never succeed, which at a prefetch of one blocks every replica against healthy work — the exact hazard the narrow sentinel exists to prevent, reintroduced by the classification meant to enable it.

**It SHALL be a permission list, not a deny-list.** An error carrying none of the enumerated evidence SHALL NOT be marked. A failure mode nobody has classified therefore behaves as it does today rather than entering a retry loop nobody reasoned about, and a driver upgrade introducing a new error cannot silently widen the retryable set.

The evidence that establishes unavailability is: the connection could not be established; the connection was lost while the statement was in flight; the connection was already closed or is otherwise unusable; or the server itself answered that it cannot serve the request now — resource exhaustion, administrative shutdown, or a refusal to accept connections. The concrete error values and enumerated SQLSTATE values carrying that evidence are a property of the pinned driver rather than of this requirement, and the implementation SHALL verify them against that driver **and against `database/sql`'s own pooling**, which sits between the driver and the caller, rather than assuming they survive it. Evidence that cannot be confirmed to reach a caller SHALL stay out of the list.

**The sentinel SHALL NOT be specified, or read, as a promise that the statement had no effect.** A connection lost in flight leaves the outcome unknown: the statement may have committed. What the sentinel asserts is that *this caller could not learn the outcome*. `videojob-execution` and `videojob-worker` are written against that weaker and true guarantee, and an implementer SHALL NOT strengthen it — in particular SHALL NOT restrict the marking to failures provably raised before the statement was sent, which would exclude most real outages and is a stronger property than any caller needs.

The following SHALL NOT carry the sentinel:

- **An error in which the server answered and refused** — an undefined table or column, an insufficient privilege, a constraint violation, a syntax error. These are permanent on a running database, and the likeliest of them here is a schema a migration left half-applied, which under a call-site rule would mark every call unavailable at once.
- **A failure produced while interpreting a row that was received** — a value conversion, or a scan whose destination count does not match the selected column list. Both are permanent, and the second is reachable through exactly the `SELECT`/`Scan` ordering mistake this adapter already warns about in a comment of its own.
- **A failure to reconstruct the aggregate from stored values** — parsing a stored identifier, user id, filename or storage key, and `RestoreVideoJob` refusing a stored status outside the closed set. These are properties of the stored data and are permanent on a perfectly healthy database. Marking them as unavailability would tell a caller a row will load later when it never will.

**`sql.ErrNoRows` SHALL continue to be mapped to `domain.ErrVideoJobNotFound` before any marking is applied**, and the not-found sentinel SHALL NOT also carry the unavailability sentinel. This ordering is the one detail of this requirement that is easy to get wrong and invisible in a test that only stops the database: a not-found row that carried the unavailability marker would be retried forever by `videojob-worker` instead of dead-lettered.

The contract SHALL hold for the repository's methods as a whole, not only for the methods whose callers currently consult it, so that a reader can tell from the requirement which errors carry the sentinel without auditing call sites. Exactly one caller branches on it today — the claim step `videojob-execution` describes — and that SHALL NOT be read as narrowing the contract.

`internal/video/infrastructure/cache.CachedVideoJobRepository` SHALL pass the sentinel through unchanged on every method it decorates, wrapping or replacing nothing, so that a decorated call and an undecorated one are indistinguishable to a caller testing for it. A decorator that swallowed it would silently restore the defect for whichever call sites read through the cache. Its own cache-store failures SHALL remain best effort and SHALL NOT be reported as repository unavailability: the authoritative store answered, and `videojob-status-cache` requires those failures to be invisible to the caller.

#### Scenario: An unreachable database is reported as unavailability

- **GIVEN** a repository whose database is unreachable
- **WHEN** `FindByID` or `ClaimForProcessing` is called
- **THEN** the returned error carries the unavailability sentinel

#### Scenario: A connection lost in flight is reported as unavailability without asserting the statement had no effect

- **GIVEN** a `ClaimForProcessing` whose connection is lost after the statement is sent
- **WHEN** the caller receives the error
- **THEN** it carries the unavailability sentinel, and the caller SHALL NOT infer from it that the `queued → processing` transition did not commit

#### Scenario: An error the server answered is not reported as unavailability

- **GIVEN** a reachable database whose `video_jobs` table no longer carries a column the adapter selects
- **WHEN** `FindByID` is called
- **THEN** it returns the server's own refusal, and that error does **not** carry the unavailability sentinel, because a retry against a running server cannot change it

#### Scenario: A row that was received but could not be scanned is not reported as unavailability

- **GIVEN** a statement that returns a row whose values `database/sql` cannot convert into the adapter's scan destinations
- **WHEN** the adapter scans it
- **THEN** the returned error does **not** carry the unavailability sentinel, because the server answered and the failure is in interpreting what it sent

#### Scenario: A row that cannot be reconstructed is not reported as unavailability

- **GIVEN** a reachable database holding a `video_jobs` row the aggregate refuses to reconstruct
- **WHEN** `FindByID` is called for it
- **THEN** it returns an error that does **not** carry the unavailability sentinel

#### Scenario: A missing row is not reported as unavailability

- **GIVEN** a reachable database and an identifier no row matches
- **WHEN** `FindByID` is called
- **THEN** it returns `domain.ErrVideoJobNotFound`, and that error does not carry the unavailability sentinel

#### Scenario: The cache decorator does not mask the sentinel

- **GIVEN** the caching repository decorating a repository whose database is unreachable
- **WHEN** a decorated method is called
- **THEN** the error it returns carries the unavailability sentinel, identically to the undecorated repository's

#### Scenario: A cache-store failure is not reported as unavailability

- **GIVEN** a reachable database and an unreachable cache store
- **WHEN** a decorated read or write is performed
- **THEN** it succeeds against the authoritative store and returns no error carrying the unavailability sentinel

### Requirement: The Repository Exposes Bounded Aggregates of In-Flight Work

`postgres.Repository` SHALL expose read-only aggregates describing work currently in flight, so that a process which is permitted to serve can report on the progress of processes which are not.

**These aggregates SHALL NOT be added to `domain.VideoJobRepository`.** They are methods on the concrete PostgreSQL repository and on nothing else. Widening the domain port would oblige every existing implementation of it — the cache decorator and the application layer's test doubles — to carry methods that exist for one collector in one process, which is the cost the port exists to avoid. The collector that reads them SHALL therefore be built on the **undecorated** repository, the way the download entitlement lookup already is, and that is a compile-time property rather than a convention: the decorator does not implement these methods, so it cannot be passed where they are required. Should a seam ever be wanted, the idiom this repository already uses is a consumer-declared unexported interface in the reading package (`messaging.outboxClaimer` over `postgres.OutboxRepository`), not a widened domain port.

Two aggregates SHALL exist:

- **Jobs per in-flight state**, together with the age of the oldest job in each state, for the states `queued` and `processing` and for no other. `pending` SHALL be excluded, and on a stronger footing than cost: a job created through the job-lifecycle API has no processing trigger and remains `pending` permanently by design, so a count of them climbs monotonically and describes nothing. The terminal states SHALL be excluded because the interesting quantity for a state a job enters once is a rate rather than a level, and a rate is owed by the process that writes the transition.
- **Unpublished outbox events per event type**, together with the age of the oldest unpublished event in each, restricted to the **explicit set of event types the relays claim on**. It SHALL NOT be computed over every unpublished row: creation events are written to the same table and are claimed by no relay, so they keep `published_at` NULL permanently and by design, and an unrestricted aggregate would report a backlog that grows for the life of the deployment while describing nothing that is pending.

  This aggregate is the reason the requirement exists in this shape. Both relays claim from one table filtered on `event_type`, and the relay carrying terminal outcomes runs inside the worker process. Reading this aggregate from the HTTP service's own pool is therefore how the state of a relay in a process that serves nothing becomes observable at all.

**Each aggregate SHALL return one entry for every member of its label set, whether or not any row matches.** A state or event type with no rows SHALL be reported with a count of **zero** and **no age**, not omitted. This is a correctness requirement rather than a convenience, because the collector reading these aggregates distinguishes *nothing is waiting* from *the value could not be computed* by emitting a sample in the first case and none in the second: an aggregate that simply returned no row for an empty state — which is what a bare `GROUP BY` does — would make an idle system indistinguishable from a failed collection at the only place that distinction is made. The age is the one value legitimately absent when the set is empty, since there is no oldest row for it to describe.

Each aggregate SHALL be computed **in the query**, not by filtering a broader read in Go; completing the closed label set with a zero-valued entry is not such a filter and may be done either in the statement or after it. **An index supporting each predicate SHALL exist** — the same requirement, for the same reason, that the sweeper's scan and the relay's claim already carry: these statements run on a timer for the life of the deployment, and a scan whose cost grows with total job history is the failure mode those indexes exist to avoid.

The unpublished-event predicate is already served by an existing partial index. **The `video_jobs` predicates are not, and both halves need one — this change adds two partial indexes rather than one.** The `queued` predicate is served by nothing at all. The `processing` predicate is served for a *filter* by the sweeper's `(id) WHERE status = 'processing'` index, but that index is keyed by `id` to match the sweep's keyset cursor and therefore cannot answer *the age of the oldest* without reading every matching row; a `(created_at) WHERE status = 'processing'` index is what makes that a single ordered row. So `video_jobs` gains a `(created_at)` partial index per in-flight state, each shaped like the sweeper's index beside them. A job enters and leaves each of them on the same edges by which it enters and leaves the sweeper's, so the write cost is a cost of a shape the schema already accepts; the `processing` rows carry two partial index entries rather than one, which is the price of two different questions — a keyset cursor and an oldest-row lookup — that no single key order answers.

Each in-flight state's predicate SHALL be written as a **literal** in the statement rather than supplied as a parameter, because a partial index whose predicate is a literal is matched only when the planner can prove the query's predicate implies it, which it cannot do for a value it does not yet have. The set of states is closed and small, so this costs one statement or one `UNION ALL` branch per state and buys the index match the requirement above demands.

**The count SHALL saturate at 10,000 per entry and the age SHALL NOT saturate.** The asymmetry follows from the costs rather than from taste: a count over a partial index costs work proportional to the number of matching rows, in exactly the case that repeats on every collection interval for as long as the backlog lasts, whereas the oldest-age lookup is a single ordered row from the same index. The bound is stated as a number rather than left to the implementation because the statement's shape, the gauge's meaning and the test that asserts saturation all depend on the same value, and three artifacts choosing it independently is how they stop agreeing. **10,000** sits roughly three orders of magnitude above any healthy value — the local stack runs three workers at prefetch 1, so a healthy `processing` count is at most three and a healthy `queued` count is single digits — while a bounded index-only read of at most 10,000 entries per entry per scrape stays comfortably under a millisecond. A saturated count still reports *at least this many*, and past the bound it is the age, which does not saturate, that carries how bad it is.

Both aggregates SHALL take a context and SHALL be bounded by it. Neither SHALL be exposed through any HTTP route that returns data to a caller: they are not owner-scoped and describe every user's work by construction.

No caching layer SHALL be introduced for either aggregate, in the repository decorator or anywhere else — a value computed at a different moment from the one the caller asked about is the failure the object-storage reachability check already refuses, and it is least visible in a level.

#### Scenario: Only the in-flight states are aggregated

- **GIVEN** jobs in `pending`, `queued`, `processing`, `completed`, and `failed` status
- **WHEN** the per-state aggregate is read
- **THEN** it reports `queued` and `processing` and reports no other state

#### Scenario: An in-flight state with no rows is reported as zero

- **GIVEN** no job in `queued` status and at least one in `processing`
- **WHEN** the per-state aggregate is read
- **THEN** it still returns an entry for `queued`, with a count of zero and no age, rather than omitting it

#### Scenario: The oldest queued job ages while nothing consumes

- **GIVEN** jobs enqueued and no consumer running
- **WHEN** the per-state aggregate is read repeatedly
- **THEN** the `queued` count holds and the oldest-`queued` age increases with each read

#### Scenario: Creation events are excluded from the outbox aggregate

- **GIVEN** an outbox containing unpublished creation events, which no relay claims, alongside unpublished dispatch and terminal events
- **WHEN** the per-event-type aggregate is read
- **THEN** it reports only the event types the relays claim on, each of them present with a zero count when it has no unpublished row, and the creation events appear in no entry

#### Scenario: The count saturates and the age does not

- **GIVEN** more rows in an in-flight state than the count's stated bound of 10,000
- **WHEN** the aggregate is read
- **THEN** the count reports 10,000 and the age reports the true age of the oldest row

#### Scenario: Each predicate is served by an index

- **WHEN** the query plan for each aggregate is inspected against a populated database
- **THEN** each is served by an index over its predicate, each oldest-age lookup reads a single ordered row from it, and none reads the whole table

#### Scenario: The aggregates are not on the domain port

- **WHEN** the domain repository port and its implementations are inspected
- **THEN** neither aggregate appears on it, no existing implementation or test double carries them, and the cache decorator does not implement them
