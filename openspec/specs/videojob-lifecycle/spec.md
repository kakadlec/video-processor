# videojob-lifecycle Specification

## Purpose

Define the `VideoJob` aggregate's full lifecycle behavior in the Video Processing bounded context's `domain` and `application` layers: `CreateVideoJob`, `GetJobStatus`, `ListUserJobs`, plus the state-transition use cases `EnqueueVideoJob`, `StartProcessing`, `CompleteJob`, `FailJob` and the `VideoJob` transition methods and pure `JobStatus` transition-validity function they rest on. No infrastructure or HTTP route is in scope here — see `ddd-architecture` for the aggregate's full canonical shape, `videojob-execution` for the orchestration use case and `ffmpeg` adapter that actually calls these four transition use cases, and `videojob-http-api` for the HTTP routes that call `CreateVideoJob`/`GetJobStatus`/`ListUserJobs`.

## Requirements

### Requirement: CreateVideoJob Persists a New Job in Pending State

The `CreateVideoJob` use case SHALL create a `VideoJob` with a freshly minted `VideoJobID`, the caller-supplied `UserID` and `OriginalFilename`, a `CreatedAt` timestamp, `JobStatus: pending`, `FrameCount: 0`, and an empty `ErrorReason`, and SHALL persist it via the `VideoJobRepository` port before returning.

#### Scenario: Successful creation

- **GIVEN** a valid `UserID` and `OriginalFilename`
- **WHEN** `CreateVideoJob.Execute` is called
- **THEN** it returns a result describing a `VideoJob` in `pending` state, and a subsequent `VideoJobRepository.FindByID` for that job's ID returns the same job

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

- **GIVEN** a `VideoJob` in `pending` status
- **WHEN** `Enqueue` is called
- **THEN** the job's status is `queued`

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

Four application-layer use cases — `EnqueueVideoJob`, `StartProcessing`, `CompleteJob`, `FailJob` — SHALL each load a `VideoJob` by ID via `VideoJobRepository.FindByID`, apply exactly the one aggregate transition method matching their name, and persist the result via `VideoJobRepository.Update`. `CompleteJob` additionally accepts a `StorageKey` and `FrameCount`; `FailJob` additionally accepts a non-empty failure reason. None of the four SHALL be reachable from any HTTP route defined by `videojob-http-api`.

#### Scenario: EnqueueVideoJob transitions and persists

- **GIVEN** a persisted `VideoJob` in `pending` status
- **WHEN** `EnqueueVideoJob.Execute` is called with its ID
- **THEN** a subsequent `FindByID` for that job returns it in `queued` status

#### Scenario: StartProcessing transitions and persists

- **GIVEN** a persisted `VideoJob` in `queued` status
- **WHEN** `StartProcessing.Execute` is called with its ID
- **THEN** a subsequent `FindByID` for that job returns it in `processing` status

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
- **THEN** it returns `ErrVideoJobNotFound` and does not call `Update`

### Requirement: Video Processing Owns a Local UserID, Never Identity's

`internal/video/domain` SHALL define and use its own `UserID` value object, distinct from `internal/identity/domain`'s `UserID` type, satisfying only Video Processing's own invariant (non-empty). The import prohibition this implies is generic architecture behavior already covered by `ddd-architecture`'s "No direct cross-context domain imports" scenario and enforced by this context's own dependency-rules test; this requirement's own scenario below covers only the behavior specific to this capability.

#### Scenario: An already-verified identifier string constructs a valid local UserID

- **GIVEN** a non-empty identifier string produced by the Identity context's bearer-auth verification
- **WHEN** it is passed to `video/domain.NewUserID`
- **THEN** it constructs a valid `video/domain.UserID` usable to create and query `VideoJob`s
