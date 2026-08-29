## ADDED Requirements

### Requirement: Enqueue Persists the Queued Transition and Its Event Transactionally

`domain.VideoJobRepository` SHALL expose an `Enqueue` method, and `internal/video/infrastructure/postgres.Repository` SHALL implement it by updating the job's `video_jobs` row to `queued` and inserting a `video_job_outbox` row describing that transition, in a single database transaction — so that a queued job and the event announcing it are never observably inconsistent, exactly as `Create` already guarantees for job creation.

The outbox row's `event_type` SHALL be `video_job.queued`, following the `video_job.created` constant already in this package, and that string SHALL be a single shared constant rather than a literal repeated at the insert and at the relay's claim. A drifted literal produces a relay that matches nothing and reports no error.

This is a dedicated method rather than a status-dependent behavior added to `Update`. `Update` is also `CompleteJob`'s and `FailJob`'s path, so making it outbox-aware would turn event emission into a side effect of a general-purpose method and would decide, as a by-product, the shape Phase 7 inherits for `VideoJobCompleted`/`VideoJobFailed`.

`internal/video/infrastructure/cache.CachedVideoJobRepository` SHALL implement `Enqueue` write-through, like `Update`: PostgreSQL first, then an unconditional cache write. It SHALL NOT pass the call through uncached — a job left `pending` in the cache while `queued` in PostgreSQL would make `GET /api/video-jobs/:id` contradict the row the relay is about to publish.

#### Scenario: Enqueue records a matching outbox row

- **GIVEN** a persisted `VideoJob` in `pending` status with a non-empty source key
- **WHEN** `Repository.Enqueue` is called with it after its `Enqueue` transition has been applied
- **THEN** its row's status is `queued`, and a `video_job_outbox` row exists whose `event_type` is `video_job.queued`, whose payload carries that job's `job_id`, `user_id`, and source key, and whose `published_at` is `NULL`

#### Scenario: A failed outbox insert leaves the job unqueued

- **GIVEN** `Repository.Enqueue` is called and the `video_jobs` update succeeds but the `video_job_outbox` insert fails
- **WHEN** the call returns an error
- **THEN** the job's persisted status is still `pending` — the transaction rolls back both writes, so no job is left `queued` with nothing to dispatch it

#### Scenario: The cached decorator writes through

- **GIVEN** a cached `VideoJob` in `pending` status
- **WHEN** `CachedVideoJobRepository.Enqueue` succeeds
- **THEN** a subsequent `FindByID` served from cache returns `queued`, not `pending`

## MODIFIED Requirements

### Requirement: PostgreSQL Repository Implements VideoJobRepository

`internal/video/infrastructure/postgres.Repository` SHALL implement `domain.VideoJobRepository`'s `Create`, `FindByID`, `FindByUserID`, and `FindCompletedByUserID(ctx, userID)` against a `video_jobs` table, using parameterized queries and reconstructing `*domain.VideoJob` via `domain.RestoreVideoJob` from stored rows.

The `video_jobs` table SHALL carry a `source_key` column holding the job's **source** object key, distinct from `storage_key`, which holds the result key set at completion. It SHALL be added as `TEXT NOT NULL DEFAULT ''` — an additive migration with no backfill, because the key embeds a generated `uploadID` that exists in no other column and cannot be reconstructed for a pre-existing row. `Create`, `FindByID`, `FindByUserID`, and `FindCompletedByUserID` SHALL all round-trip it.

`FindCompletedByUserID` SHALL restrict to `completed` jobs **in the query** and SHALL return all of them, taking no offset or limit. It orders identically to `FindByUserID`: `CreatedAt` descending with `VideoJobID` ascending as a tie-breaker.

The absence of pagination is deliberate and is the reason this method exists separately from `FindByUserID`. Its only caller is `GET /api/status`, which accepts no pagination parameters and whose filesystem-backed predecessor returned every zip the caller owned. Reusing `FindByUserID` and filtering afterwards would be wrong twice over: a page of recent `pending`/`failed` jobs would displace a user's completed results out of the listing, and any limit at all would make older results permanently unreachable through the only listing endpoint the frontend consumes. Filtering on status in SQL is what makes returning the full set reasonable — the rows returned are exactly the rows rendered.

`internal/video/infrastructure/cache.CachedVideoJobRepository` SHALL pass `FindCompletedByUserID` straight through to the decorated repository without caching it, exactly as it already does for `FindByUserID` — the status cache is keyed by individual job ID and has nothing to offer a multi-row listing.

#### Scenario: A created job round-trips through FindByID

- **GIVEN** a `VideoJob` persisted via `Repository.Create`
- **WHEN** `Repository.FindByID` is called with that job's ID
- **THEN** it returns a `*domain.VideoJob` with the same `ID`, `UserID`, `OriginalFilename`, source key, `StorageKey`, `FrameCount`, `ErrorReason`, `Status`, and `CreatedAt`

#### Scenario: A pre-migration row loads with an empty source key

- **GIVEN** a `video_jobs` row written before the `source_key` column existed, in any status
- **WHEN** `Repository.FindByID` is called for it
- **THEN** it returns the job with an empty source key rather than an error

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

### Requirement: Update Persists a VideoJob's Transitioned State

`Repository.Update` SHALL persist an already-loaded `VideoJob`'s current `status`, `frame_count`, `error_reason`, and `storage_key` to its existing `video_jobs` row, identified by its unchanging `id`. It SHALL NOT write a `video_job_outbox` row.

That exclusion is now load-bearing rather than incidental. Two repository methods write outbox rows — `Create` for `video_job.created` and `Enqueue` for `video_job.queued` — and `Update` is deliberately not a third. It is the path `StartProcessing`, `CompleteJob`, and `FailJob` all take, so emitting an event from it would make event production a status-dependent side effect of a general-purpose write, and would settle the shape of `VideoJobCompleted`/`VideoJobFailed` as a by-product rather than as Phase 7's own decision. `Update` SHALL NOT be given an outbox write to avoid adding a dedicated method.

#### Scenario: Update persists a transitioned job

- **GIVEN** a `VideoJob` was previously persisted via `Create` and has since had a transition method applied to it in memory
- **WHEN** `Repository.Update` is called with that job
- **THEN** a subsequent `Repository.FindByID` for its ID returns a job matching the updated `status`, `frame_count`, `error_reason`, and `storage_key`

#### Scenario: Update does not write an outbox row

- **GIVEN** a previously persisted `VideoJob`
- **WHEN** `Repository.Update` is called with it
- **THEN** no new `video_job_outbox` row is committed as a result of that call
