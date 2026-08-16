# videojob-persistence Specification

## Purpose

Define the PostgreSQL-backed implementation of `domain.VideoJobRepository` in the Video Processing bounded context's `infrastructure` layer, and the transactional-outbox behavior on job creation. This is infrastructure only — no HTTP route or composition-root wiring is in scope here; see `videojob-lifecycle` for the domain/application use cases this repository implements the port for, and the Change Backlog in `docs/roadmap.md` for what wires it in.

## Requirements

### Requirement: PostgreSQL Repository Implements VideoJobRepository

`internal/video/infrastructure/postgres.Repository` SHALL implement `domain.VideoJobRepository`'s `Create`, `FindByID`, and `FindByUserID` against a `video_jobs` table, using parameterized queries and reconstructing `*domain.VideoJob` via `domain.RestoreVideoJob` from stored rows.

#### Scenario: A created job round-trips through FindByID

- **GIVEN** a `VideoJob` persisted via `Repository.Create`
- **WHEN** `Repository.FindByID` is called with that job's ID
- **THEN** it returns a `*domain.VideoJob` with the same `ID`, `UserID`, `OriginalFilename`, `StorageKey`, `FrameCount`, `ErrorReason`, `Status`, and `CreatedAt`

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

### Requirement: VideoJobCreated Is Recorded to an Outbox Transactionally With Job Creation

`Repository.Create` SHALL insert the `video_jobs` row and a `video_job_outbox` row describing the job's creation in a single database transaction, so that the two are never observably inconsistent: either both are committed or neither is.

#### Scenario: Successful creation records a matching outbox row

- **GIVEN** a valid `*domain.VideoJob` passed to `Repository.Create`
- **WHEN** the call succeeds
- **THEN** a `video_job_outbox` row exists whose `event_type` is `video_job.created`, whose `payload` contains that job's `job_id`, `user_id`, `original_filename`, and `occurred_at`, and whose `published_at` is `NULL`

#### Scenario: A failed job-row insert leaves no outbox row

- **GIVEN** `Repository.Create` is called with a `VideoJob` whose insert into `video_jobs` violates a database constraint (e.g. a duplicate ID)
- **WHEN** the call returns an error
- **THEN** no corresponding `video_job_outbox` row was committed

#### Scenario: A failed outbox insert leaves no job row

- **GIVEN** `Repository.Create` is called and the `video_jobs` insert succeeds but the subsequent `video_job_outbox` insert fails
- **WHEN** the call returns an error
- **THEN** no corresponding `video_jobs` row was committed either — the transaction rolls back both writes, not just the one that failed
