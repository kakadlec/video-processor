## MODIFIED Requirements

### Requirement: Valid State Machine Transitions Only

The `VideoJob` status SHALL only advance through the defined state machine. Backwards transitions and undefined transitions SHALL be rejected as domain errors.

`Enqueue` SHALL additionally require a non-empty source key. That is a precondition on the aggregate rather than an edge in the state machine — a job with no stored source object cannot be processed, so queueing it would record a dispatch no worker could ever act on. `videojob-lifecycle` owns the invariant's full statement, including why it is deliberately absent from `RestoreVideoJob`.

#### Scenario: Job advances from pending to queued

- **GIVEN** a `VideoJob` in `pending` state with a non-empty source key
- **WHEN** `EnqueueVideoJob` is called
- **THEN** the job transitions to `queued`

#### Scenario: A pending job with no source key is not queued

- **GIVEN** a `VideoJob` in `pending` state whose source key is empty, as `POST /api/video-jobs` creates one
- **WHEN** `EnqueueVideoJob` is called
- **THEN** the domain layer rejects the command with an error and the job remains `pending`

#### Scenario: Job advances from queued to processing

- **GIVEN** a `VideoJob` in `queued` state
- **WHEN** the worker dequeues the job and calls `StartProcessing`
- **THEN** the job transitions to `processing`

#### Scenario: Job advances from processing to completed

- **GIVEN** a `VideoJob` in `processing` state
- **WHEN** the worker successfully extracts frames and calls `CompleteJob`
- **THEN** the job transitions to `completed` with `FrameCount` and result `StorageKey` populated

#### Scenario: Job advances from processing to failed

- **GIVEN** a `VideoJob` in `processing` state
- **WHEN** the worker encounters an unrecoverable error and calls `FailJob`
- **THEN** the job transitions to `failed` with a non-empty `ErrorReason`

#### Scenario: Backwards transition is rejected

- **GIVEN** a `VideoJob` in `completed` or `failed` state
- **WHEN** any transition command is applied
- **THEN** the domain layer rejects the command with an error; the job state is not mutated
