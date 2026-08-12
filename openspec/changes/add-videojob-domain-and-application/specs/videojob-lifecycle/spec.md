## ADDED Requirements

### Requirement: CreateVideoJob Persists a New Job in Pending State

The `CreateVideoJob` use case SHALL create a `VideoJob` with a freshly minted `VideoJobID`, the caller-supplied `UserID` and `OriginalFilename`, `JobStatus: pending`, `FrameCount: 0`, and an empty `ErrorReason`, and SHALL persist it via the `VideoJobRepository` port before returning.

#### Scenario: Successful creation

- **GIVEN** a valid `UserID` and `OriginalFilename`
- **WHEN** `CreateVideoJob.Execute` is called
- **THEN** it returns a result describing a `VideoJob` in `pending` state, and a subsequent `VideoJobRepository.FindByID` for that job's ID returns the same job

#### Scenario: Repository failure is propagated

- **GIVEN** the `VideoJobRepository` returns an error from `Create`
- **WHEN** `CreateVideoJob.Execute` is called
- **THEN** the use case returns that error and does not report success

### Requirement: GetJobStatus Is Scoped to the Requesting Owner

The `GetJobStatus` use case SHALL return a `VideoJob`'s current status only when the requesting `UserID` matches the job's owning `UserID`. A request for a job that does not exist and a request for a job owned by a different `UserID` SHALL be indistinguishable to the caller.

#### Scenario: Owner retrieves their own job status

- **GIVEN** a `VideoJob` owned by `UserID` A exists
- **WHEN** `GetJobStatus.Execute` is called with `UserID` A and that job's ID
- **THEN** it returns the job's current `JobStatus`, `FrameCount`, and `ErrorReason`

#### Scenario: Non-owner request is rejected as not found

- **GIVEN** a `VideoJob` owned by `UserID` A exists
- **WHEN** `GetJobStatus.Execute` is called with a different `UserID` B and that job's ID
- **THEN** it returns the same not-found error `GetJobStatus` would return for a nonexistent job ID — never a distinct "forbidden" result

#### Scenario: Nonexistent job ID is rejected as not found

- **GIVEN** no `VideoJob` exists for a given ID
- **WHEN** `GetJobStatus.Execute` is called with that ID
- **THEN** it returns a not-found error

### Requirement: ListUserJobs Returns Only the Caller's Own Jobs, Paginated

The `ListUserJobs` use case SHALL return only `VideoJob`s owned by the requesting `UserID`, bounded by an offset and limit.

#### Scenario: Listing is scoped to the caller

- **GIVEN** `VideoJob`s exist for both `UserID` A and `UserID` B
- **WHEN** `ListUserJobs.Execute` is called with `UserID` A
- **THEN** the returned list contains only jobs owned by `UserID` A

#### Scenario: Offset and limit bound the returned page

- **GIVEN** `UserID` A owns more `VideoJob`s than the requested `limit`
- **WHEN** `ListUserJobs.Execute` is called with a given `offset` and `limit`
- **THEN** the returned list contains at most `limit` jobs, starting after `offset` prior jobs in the repository's defined ordering

### Requirement: JobStatus Transition Validity Is an Independently Testable Pure Function

`JobStatus` SHALL provide a pure function that reports whether a transition from one status to another is valid, independent of any `VideoJob` instance, implementing the state machine `pending → queued → processing → completed`, `processing → failed`.

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

### Requirement: Video Processing Owns a Local UserID, Never Identity's

`internal/video/domain` SHALL define and use its own `UserID` value object, distinct from `internal/identity/domain`'s `UserID` type, satisfying only Video Processing's own invariant (non-empty). The import prohibition this implies is generic architecture behavior already covered by `ddd-architecture`'s "No direct cross-context domain imports" scenario and enforced by this context's own dependency-rules test; this requirement's own scenario below covers only the behavior specific to this capability.

#### Scenario: An already-verified identifier string constructs a valid local UserID

- **GIVEN** a non-empty identifier string produced by the Identity context's bearer-auth verification
- **WHEN** it is passed to `video/domain.NewUserID`
- **THEN** it constructs a valid `video/domain.UserID` usable to create and query `VideoJob`s
