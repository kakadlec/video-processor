# videojob-lifecycle Specification

## Purpose

Define the `VideoJob` aggregate's full lifecycle behavior in the Video Processing bounded context's `domain` and `application` layers: `CreateVideoJob`, `GetJobStatus`, `ListUserJobs`, plus the state-transition use cases `EnqueueVideoJob`, `StartProcessing`, `CompleteJob`, `FailJob` and the `VideoJob` transition methods and pure `JobStatus` transition-validity function they rest on. No infrastructure or HTTP route is in scope here — see `ddd-architecture` for the aggregate's full canonical shape, `videojob-execution` for the orchestration use case and `ffmpeg` adapter that calls `StartProcessing`/`CompleteJob`/`FailJob` (`EnqueueVideoJob` is called by `POST /upload`'s handler instead, so that it commits with its outbox row — see `videojob-persistence`), and `videojob-http-api` for the HTTP routes that call `CreateVideoJob`/`GetJobStatus`/`ListUserJobs`.

## Requirements

### Requirement: CreateVideoJob Persists a New Job in Pending State

The `CreateVideoJob` use case SHALL create a `VideoJob` with a freshly minted `VideoJobID`, the caller-supplied `UserID`, `OriginalFilename`, and **source `StorageKey`**, a `CreatedAt` timestamp, `JobStatus: pending`, `FrameCount: 0`, and an empty `ErrorReason`, and SHALL persist it via the `VideoJobRepository` port before returning.

The source key is the object key of the uploaded video, distinct from the result `StorageKey` set at completion, and it is accepted here because this is the only point at which it is known: `POST /upload` streams the upload into the bucket before creating the job, and the key embeds a generated `uploadID` that exists nowhere else. A process that later has to fetch the source — a worker, in particular — cannot reconstruct it from any other column.

The source key MAY be empty. `POST /api/video-jobs` creates a job from a JSON filename with no stored object at all, and such a job is legitimately sourceless; what it cannot do is be enqueued, which the requirement below enforces.

#### Scenario: Successful creation

- **GIVEN** a valid `UserID`, `OriginalFilename`, and source `StorageKey`
- **WHEN** `CreateVideoJob.Execute` is called
- **THEN** it returns a result describing a `VideoJob` in `pending` state, and a subsequent `VideoJobRepository.FindByID` for that job's ID returns the same job, carrying the same source key

#### Scenario: Creation without a source key is allowed

- **GIVEN** a valid `UserID` and `OriginalFilename` and no source key
- **WHEN** `CreateVideoJob.Execute` is called
- **THEN** the job is created in `pending` state with an empty source key

#### Scenario: Repository failure is propagated

- **GIVEN** the `VideoJobRepository` returns an error from `Create`
- **WHEN** `CreateVideoJob.Execute` is called
- **THEN** the use case returns that error and does not report success

### Requirement: OriginalFilename Requires a Supported Video Extension

`OriginalFilename` SHALL reject construction for any value that is empty or whose extension is not in the supported set (`.mp4`, `.avi`, `.mov`, `.mkv`, `.wmv`, `.flv`, `.webm` — the same set `cmd/api/main.go`'s `isValidVideoFile` enforces at the legacy HTTP boundary), so that no caller — HTTP or otherwise — can construct a `VideoJob` for an unsupported file type by going around the legacy handler.

#### Scenario: Supported extension is accepted

- **GIVEN** a filename ending in one of the supported extensions
- **WHEN** it is passed to the `OriginalFilename` constructor
- **THEN** it constructs successfully

#### Scenario: Unsupported extension is rejected

- **GIVEN** a non-empty filename whose extension is not in the supported set (e.g. `.txt`)
- **WHEN** it is passed to the `OriginalFilename` constructor
- **THEN** construction fails with an error

### Requirement: GetJobStatus Is Scoped to the Requesting Owner

The `GetJobStatus` use case SHALL return a `VideoJob`'s current status only when the requesting `UserID` matches the job's owning `UserID`. A request for a job that does not exist and a request for a job owned by a different `UserID` SHALL be indistinguishable to the caller. The returned result SHALL include `StorageKey` when the job is `completed` (empty otherwise).

#### Scenario: Owner retrieves their own job status

- **GIVEN** a `VideoJob` owned by `UserID` A exists
- **WHEN** `GetJobStatus.Execute` is called with `UserID` A and that job's ID
- **THEN** it returns the job's current `JobStatus`, `FrameCount`, `ErrorReason`, and `StorageKey`

#### Scenario: Completed job's result includes its StorageKey

- **GIVEN** a `VideoJob` owned by `UserID` A is `completed` with a non-empty `StorageKey`
- **WHEN** `GetJobStatus.Execute` is called with `UserID` A and that job's ID
- **THEN** the returned result's `StorageKey` matches the job's

#### Scenario: Non-owner request is rejected as not found

- **GIVEN** a `VideoJob` owned by `UserID` A exists
- **WHEN** `GetJobStatus.Execute` is called with a different `UserID` B and that job's ID
- **THEN** it returns the same not-found error `GetJobStatus` would return for a nonexistent job ID — never a distinct "forbidden" result

#### Scenario: Nonexistent job ID is rejected as not found

- **GIVEN** no `VideoJob` exists for a given ID
- **WHEN** `GetJobStatus.Execute` is called with that ID
- **THEN** it returns a not-found error

#### Scenario: Malformed job ID is rejected as invalid input, not as not-found

- **GIVEN** a `JobID` string that fails `VideoJobIDParser` validation
- **WHEN** `GetJobStatus.Execute` is called with that string
- **THEN** it returns the parser's error directly, distinct from the not-found error, and does not query the `VideoJobRepository`

### Requirement: ListUserJobs Returns Only the Caller's Own Jobs, Paginated in a Stable Order

The `ListUserJobs` use case SHALL return only `VideoJob`s owned by the requesting `UserID`, ordered by `CreatedAt` descending (newest first) with `VideoJobID` ascending as a tie-breaker for equal `CreatedAt` values, bounded by an offset and limit. `limit` SHALL be between 1 and 100 inclusive; `offset` SHALL be ≥ 0. A request outside those bounds SHALL be rejected with an error rather than silently clamped.

#### Scenario: Listing is scoped to the caller

- **GIVEN** `VideoJob`s exist for both `UserID` A and `UserID` B
- **WHEN** `ListUserJobs.Execute` is called with `UserID` A
- **THEN** the returned list contains only jobs owned by `UserID` A

#### Scenario: Results are ordered newest first

- **GIVEN** `UserID` A owns multiple `VideoJob`s created at different times
- **WHEN** `ListUserJobs.Execute` is called with `UserID` A
- **THEN** the returned list is ordered by `CreatedAt` descending

#### Scenario: Equal CreatedAt values are broken by ascending VideoJobID

- **GIVEN** `UserID` A owns two or more `VideoJob`s with the same `CreatedAt`
- **WHEN** `ListUserJobs.Execute` is called with `UserID` A
- **THEN** those jobs appear in ascending `VideoJobID` order relative to each other

#### Scenario: Offset and limit bound the returned page

- **GIVEN** `UserID` A owns more `VideoJob`s than the requested `limit`
- **WHEN** `ListUserJobs.Execute` is called with a given valid `offset` and `limit`
- **THEN** the returned list contains at most `limit` jobs, starting after `offset` prior jobs in `CreatedAt`-descending order

#### Scenario: Out-of-range limit is rejected

- **GIVEN** a `limit` of `0` or greater than `100`
- **WHEN** `ListUserJobs.Execute` is called with that `limit`
- **THEN** it returns an error and does not query the `VideoJobRepository`

### Requirement: JobStatus Transition Validity Is an Independently Testable Pure Function

`JobStatus` SHALL provide a pure function that reports whether a transition from one status to another is valid, independent of any `VideoJob` instance, implementing the state machine `pending → queued → processing → completed`, `processing → failed`. The `VideoJob` aggregate SHALL expose one method per edge in that state machine — `Enqueue` (`pending → queued`), `StartProcessing` (`queued → processing`), `Complete` (`processing → completed`), `Fail` (`processing → failed`) — each of which SHALL reject the call with an error, and leave the aggregate's state unchanged, when the current status cannot legally make that transition.

`Enqueue` SHALL additionally reject a job whose source key is empty, with a distinct exported sentinel error. That is a precondition on the aggregate rather than an edge in the state machine — the pure transition function still reports `pending → queued` valid, because it is given two statuses and knows nothing about a job — so the aggregate method is the only place the invariant can live. Its rationale, and why it is deliberately absent from `RestoreVideoJob`, belong to the requirement below.

#### Scenario: Valid forward transition is accepted

- **GIVEN** the current status is `pending`
- **WHEN** validity of a transition to `queued` is checked
- **THEN** it reports the transition as valid

#### Scenario: Backwards transition is rejected

- **GIVEN** the current status is `completed` or `failed`
- **WHEN** validity of a transition to any other status is checked
- **THEN** it reports the transition as invalid

#### Scenario: Undefined transition is rejected

- **GIVEN** the current status is `pending`
- **WHEN** validity of a transition directly to `completed` (skipping `queued` and `processing`) is checked
- **THEN** it reports the transition as invalid

#### Scenario: Enqueue moves a pending job to queued

- **GIVEN** a `VideoJob` in `pending` status with a non-empty source key
- **WHEN** `Enqueue` is called
- **THEN** the job's status is `queued`

#### Scenario: Enqueue rejects a pending job with no source key

- **GIVEN** a `VideoJob` in `pending` status whose source key is empty
- **WHEN** `Enqueue` is called
- **THEN** it returns the source-key sentinel error and the job's status remains `pending`

#### Scenario: StartProcessing moves a queued job to processing

- **GIVEN** a `VideoJob` in `queued` status
- **WHEN** `StartProcessing` is called
- **THEN** the job's status is `processing`

#### Scenario: Complete moves a processing job to completed with a result

- **GIVEN** a `VideoJob` in `processing` status
- **WHEN** `Complete` is called with a non-zero `StorageKey` and a non-negative `FrameCount`
- **THEN** the job's status is `completed`, and its `StorageKey`/`FrameCount` match the supplied values

#### Scenario: Fail moves a processing job to failed with a reason

- **GIVEN** a `VideoJob` in `processing` status
- **WHEN** `Fail` is called with a non-empty reason
- **THEN** the job's status is `failed`, and its `ErrorReason` matches the supplied reason

#### Scenario: An out-of-order transition call is rejected without mutating the aggregate

- **GIVEN** a `VideoJob` in `pending` status
- **WHEN** `StartProcessing`, `Complete`, or `Fail` is called on it directly
- **THEN** it returns an error, and the job's status remains `pending`

### Requirement: EnqueueVideoJob, StartProcessing, CompleteJob, and FailJob Persist One State Transition Each

Four application-layer use cases — `EnqueueVideoJob`, `StartProcessing`, `CompleteJob`, `FailJob` — SHALL each load a `VideoJob` by ID via `VideoJobRepository.FindByID`, apply exactly the one aggregate transition method matching their name, and persist the result. `CompleteJob` and `FailJob` persist via `VideoJobRepository.Update`; **`EnqueueVideoJob` persists via `VideoJobRepository.Enqueue`**, which commits the transition together with the event describing it; **`StartProcessing` persists via `VideoJobRepository.ClaimForProcessing`**, which applies the transition only if the stored status is still `queued` (see `videojob-persistence`). `CompleteJob` additionally accepts a `StorageKey` and `FrameCount`; `FailJob` additionally accepts a non-empty failure reason. None of the four SHALL be reachable from any HTTP route defined by `videojob-http-api`.

**`StartProcessing` SHALL be an atomic claim, and losing that claim SHALL be a distinct, non-failing outcome.** It SHALL return a distinct exported sentinel error, SHALL NOT retry, and SHALL NOT call `FailJob` or otherwise mutate the job — another consumer owns it, and writing anything would corrupt that consumer's work.

**That sentinel SHALL be reported for both ways of losing the claim, which are reached at different points in the use case.** The common one is decided before any write: `StartProcessing` loads the job first, and a job already in `processing`, `completed`, or `failed` is rejected by the aggregate's own transition method, which does not know a claim is being attempted and reports an invalid transition. The rarer one is the race after the read, where the conditional persist affects no row. Both mean the same thing — some other consumer holds or held this job — and both SHALL surface as the lost-claim sentinel, so a caller does not have to distinguish an ordinary duplicate delivery from a narrow race to decide what to do about it.

A `pending` job SHALL NOT be reported as a lost claim. It was never dispatched, so a message naming it is an anomaly rather than a duplicate, and collapsing the two would hide the case worth investigating behind the case that is routine.

The claim is what makes duplicate dispatch safe, and it is a *correctness* primitive rather than a *recovery* one. Dispatch is at-least-once by construction (`videojob-outbox-relay` can publish a message whose `published_at` never commits), and `Update` is a read-then-unconditional-write, so without the conditional persist two deliveries would both pass `queued → processing` and both run an extraction over the same source. With it, exactly one wins and the loser mutates nothing.

What it deliberately does **not** do is recover a job whose consumer died mid-extraction. Such a job is stored as `processing`, the predicate `status = 'queued'` refuses any redelivery, and the job remains stranded. Widening the predicate to re-admit a `processing` job SHALL NOT be done without a fencing mechanism that prevents the previous consumer from later writing over the new one's work; that is a later change's, and an implementer SHALL NOT relax the predicate here to make a stranded job recoverable.

`VideoJob.Enqueue` SHALL reject a job whose source key is empty. A job with no stored source cannot be processed, so queueing it would produce a dispatch no worker can act on. This invariant lives on the transition and **SHALL NOT** be added to `RestoreVideoJob`: pairing the field at reconstitution, the way `StorageKey` is paired with `completed` and `ErrorReason` with `failed`, would make every pre-existing `queued` or `processing` row unloadable once the column is added with an empty default — and such rows exist, because a crash or a client disconnect can strand one between the enqueue and the dispatch.

#### Scenario: EnqueueVideoJob transitions and persists

- **GIVEN** a persisted `VideoJob` in `pending` status with a non-empty source key
- **WHEN** `EnqueueVideoJob.Execute` is called with its ID
- **THEN** a subsequent `FindByID` for that job returns it in `queued` status

#### Scenario: A job with no source key cannot be enqueued

- **GIVEN** a persisted `VideoJob` in `pending` status with an empty source key
- **WHEN** `EnqueueVideoJob.Execute` is called with its ID
- **THEN** it returns an error, and a subsequent `FindByID` still returns the job in `pending` status

#### Scenario: A pre-existing queued job with no source key still loads

- **GIVEN** a `video_jobs` row in `queued` status whose `source_key` is empty, as an add-column migration leaves it
- **WHEN** `FindByID` is called for it
- **THEN** it returns the job rather than a domain error, because the source-key invariant is enforced on the transition and not at reconstitution

#### Scenario: StartProcessing transitions and persists

- **GIVEN** a persisted `VideoJob` in `queued` status
- **WHEN** `StartProcessing.Execute` is called with its ID
- **THEN** a subsequent `FindByID` for that job returns it in `processing` status

#### Scenario: Only one of two concurrent claims on the same job succeeds

- **GIVEN** a persisted `VideoJob` in `queued` status
- **WHEN** two `StartProcessing.Execute` calls for that ID run concurrently
- **THEN** exactly one returns successfully and the other returns the lost-claim sentinel, and the job is `processing` with no intermediate state observable to a third reader

#### Scenario: A lost claim mutates nothing and does not fail the job

- **GIVEN** a persisted `VideoJob` already in `processing` status
- **WHEN** `StartProcessing.Execute` is called with its ID
- **THEN** it returns the lost-claim sentinel — not a generic invalid-transition error — the job is still `processing`, its `ErrorReason` is unchanged, and no `FailJob` transition was attempted

#### Scenario: A job that was never enqueued is not reported as a lost claim

- **GIVEN** a persisted `VideoJob` in `pending` status
- **WHEN** `StartProcessing.Execute` is called with its ID
- **THEN** it returns an error that is **not** the lost-claim sentinel, so a dispatch naming an un-enqueued job is distinguishable from a duplicate delivery

#### Scenario: A completed job cannot be re-claimed

- **GIVEN** a persisted `VideoJob` in `completed` status, as a redelivered stale dispatch would name
- **WHEN** `StartProcessing.Execute` is called with its ID
- **THEN** it returns the lost-claim sentinel — decided before any write is attempted — and the job's status, `StorageKey`, and `FrameCount` are unchanged

#### Scenario: CompleteJob transitions and persists a result

- **GIVEN** a persisted `VideoJob` in `processing` status
- **WHEN** `CompleteJob.Execute` is called with its ID, a `StorageKey`, and a `FrameCount`
- **THEN** a subsequent `FindByID` for that job returns it in `completed` status with the given `StorageKey` and `FrameCount`

#### Scenario: FailJob transitions and persists a reason

- **GIVEN** a persisted `VideoJob` in `processing` status
- **WHEN** `FailJob.Execute` is called with its ID and a failure reason
- **THEN** a subsequent `FindByID` for that job returns it in `failed` status with that `ErrorReason`

#### Scenario: An invalid transition is propagated as an error, not silently ignored

- **GIVEN** a persisted `VideoJob` in `pending` status
- **WHEN** `CompleteJob.Execute` is called with its ID
- **THEN** it returns an error, and a subsequent `FindByID` for that job still returns it in `pending` status

#### Scenario: A nonexistent job ID is rejected as not found

- **GIVEN** no `VideoJob` exists for a given ID
- **WHEN** any of the four use cases is called with that ID
- **THEN** it returns `ErrVideoJobNotFound` and does not persist anything

### Requirement: Video Processing Owns a Local UserID, Never Identity's

`internal/video/domain` SHALL define and use its own `UserID` value object, distinct from `internal/identity/domain`'s `UserID` type, satisfying only Video Processing's own invariant (non-empty). The import prohibition this implies is generic architecture behavior already covered by `ddd-architecture`'s "No direct cross-context domain imports" scenario and enforced by this context's own dependency-rules test; this requirement's own scenario below covers only the behavior specific to this capability.

#### Scenario: An already-verified identifier string constructs a valid local UserID

- **GIVEN** a non-empty identifier string produced by the Identity context's bearer-auth verification
- **WHEN** it is passed to `video/domain.NewUserID`
- **THEN** it constructs a valid `video/domain.UserID` usable to create and query `VideoJob`s
