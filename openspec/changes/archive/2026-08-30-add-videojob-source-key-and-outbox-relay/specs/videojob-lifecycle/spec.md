## MODIFIED Requirements

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

Four application-layer use cases — `EnqueueVideoJob`, `StartProcessing`, `CompleteJob`, `FailJob` — SHALL each load a `VideoJob` by ID via `VideoJobRepository.FindByID`, apply exactly the one aggregate transition method matching their name, and persist the result. `StartProcessing`, `CompleteJob`, and `FailJob` persist via `VideoJobRepository.Update`; **`EnqueueVideoJob` persists via `VideoJobRepository.Enqueue`**, which commits the transition together with the event describing it (see `videojob-persistence`). `CompleteJob` additionally accepts a `StorageKey` and `FrameCount`; `FailJob` additionally accepts a non-empty failure reason. None of the four SHALL be reachable from any HTTP route defined by `videojob-http-api`.

`VideoJob.Enqueue` SHALL reject a job whose source key is empty. A job with no stored source cannot be processed, so queueing it would produce a dispatch no worker can act on. This invariant lives on the transition and **SHALL NOT** be added to `RestoreVideoJob`: pairing the field at reconstitution, the way `StorageKey` is paired with `completed` and `ErrorReason` with `failed`, would make every pre-existing `queued` or `processing` row unloadable once the column is added with an empty default — and such rows exist, because `POST /upload` drives the whole sequence inside one request and a crash or a client disconnect strands one.

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
