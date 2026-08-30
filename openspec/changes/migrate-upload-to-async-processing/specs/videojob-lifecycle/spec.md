## MODIFIED Requirements

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
