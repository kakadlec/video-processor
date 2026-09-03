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

`JobStatus` SHALL provide a pure function that reports whether a transition from one status to another is valid, independent of any `VideoJob` instance, implementing the state machine `pending → queued → processing → completed`, `processing → failed`, and `processing → queued`. The `VideoJob` aggregate SHALL expose one method per edge in that state machine — `Enqueue` (`pending → queued`), `StartProcessing` (`queued → processing`), `Complete` (`processing → completed`), `Fail` (`processing → failed`), `Requeue` (`processing → queued`) — each of which SHALL reject the call with an error, and leave the aggregate's state unchanged, when the current status cannot legally make that transition.

**`processing → queued` is the state machine's only backwards edge, and it exists for exactly one purpose: returning a job whose worker died to the queue** (see `videojob-lease-recovery`). It SHALL NOT be used to retry a job that reached a terminal state, to re-dispatch a job on request, or to undo a claim a worker still holds. No further backwards edge SHALL be added on the strength of this one; `completed` and `failed` remain terminal, with no outgoing edges at all.

`Requeue` SHALL be a distinct aggregate method rather than a second caller of `Enqueue`. The two transitions differ in origin status, in what may be assumed about the job's fields, and in who is allowed to perform them, and collapsing them would let a `pending` job be requeued or an abandoned one be treated as a first dispatch.

`Enqueue` SHALL additionally reject a job whose source key is empty, with a distinct exported sentinel error. That is a precondition on the aggregate rather than an edge in the state machine — the pure transition function still reports `pending → queued` valid, because it is given two statuses and knows nothing about a job — so the aggregate method is the only place the invariant can live. Its rationale, and why it is deliberately absent from `RestoreVideoJob`, belong to the requirement below. `Requeue` SHALL reject an empty source key for the same reason and with the same sentinel: a job re-dispatched without a source is a message no worker can act on.

#### Scenario: Valid forward transition is accepted

- **GIVEN** the current status is `pending`
- **WHEN** validity of a transition to `queued` is checked
- **THEN** it reports the transition as valid

#### Scenario: Backwards transition is rejected

- **GIVEN** the current status is `completed` or `failed`
- **WHEN** validity of a transition to any other status is checked
- **THEN** it reports the transition as invalid

#### Scenario: The requeue edge is the only permitted backwards transition

- **GIVEN** the current status is `processing`
- **WHEN** validity of a transition to `queued` is checked, and separately validity of `queued → pending` and `completed → queued`
- **THEN** `processing → queued` is reported valid and both of the others are reported invalid

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

#### Scenario: Requeue moves a processing job back to queued

- **GIVEN** a `VideoJob` in `processing` status with a non-empty source key
- **WHEN** `Requeue` is called
- **THEN** the job's status is `queued`

#### Scenario: Requeue is rejected for a job that is not processing

- **GIVEN** a `VideoJob` in `pending`, `queued`, `completed`, or `failed` status
- **WHEN** `Requeue` is called
- **THEN** it returns an error and the job's status is unchanged

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
- **WHEN** `StartProcessing`, `Complete`, `Fail`, or `Requeue` is called on it directly
- **THEN** it returns an error, and the job's status remains `pending`

### Requirement: EnqueueVideoJob, StartProcessing, CompleteJob, and FailJob Persist One State Transition Each

Four application-layer use cases — `EnqueueVideoJob`, `StartProcessing`, `CompleteJob`, `FailJob` — SHALL each load a `VideoJob` by ID via `VideoJobRepository.FindByID`, apply exactly the one aggregate transition method matching their name, and persist the result. `CompleteJob` and `FailJob` persist via `VideoJobRepository.Update`; **`EnqueueVideoJob` persists via `VideoJobRepository.Enqueue`**, which commits the transition together with the event describing it; **`StartProcessing` persists via `VideoJobRepository.ClaimForProcessing`**, which applies the transition only if the stored status is still `queued` (see `videojob-persistence`). `CompleteJob` additionally accepts a `StorageKey` and `FrameCount`; `FailJob` additionally accepts a non-empty failure reason. None of the four SHALL be reachable from any HTTP route defined by `videojob-http-api`.

**`StartProcessing` SHALL be an atomic claim, and losing that claim SHALL be a distinct, non-failing outcome.** It SHALL return a distinct exported sentinel error, SHALL NOT retry, and SHALL NOT call `FailJob` or otherwise mutate the job — another consumer owns it, and writing anything would corrupt that consumer's work.

**That sentinel SHALL be reported for both ways of losing the claim, which are reached at different points in the use case.** The common one is decided before any write: `StartProcessing` loads the job first, and a job already in `processing`, `completed`, or `failed` is rejected by the aggregate's own transition method, which does not know a claim is being attempted and reports an invalid transition. The rarer one is the race after the read, where the conditional persist affects no row. Both mean the same thing — some other consumer holds or held this job — and both SHALL surface as the lost-claim sentinel, so a caller does not have to distinguish an ordinary duplicate delivery from a narrow race to decide what to do about it.

A `pending` job SHALL NOT be reported as a lost claim. It was never dispatched, so a message naming it is an anomaly rather than a duplicate, and collapsing the two would hide the case worth investigating behind the case that is routine.

**`StartProcessing` SHALL additionally report the fence epoch its claim won**, read by the claiming statement itself, and **`CompleteJob` and `FailJob` SHALL each require that epoch as an input and persist conditionally on it**, returning a distinct exported fence sentinel when the stored row no longer carries it **or when another actor has already committed a different terminal outcome at the same epoch**. They SHALL NOT re-read the epoch from the job they loaded: by then the row may carry a successor's, and a fence checked against that value would pass in exactly the case it exists to reject. The lost-claim sentinel and the fence sentinel SHALL remain distinct — the first says another consumer took the job before this one started, while the second says this actor may no longer establish the terminal outcome after work began.

**Loss of the terminal write SHALL surface as the fence sentinel even when it is detected before the write.** Both use cases load the job and apply the aggregate transition first, so a job that has already been requeued reads as `queued` and one a successor has already finished reads as terminal; in both cases the aggregate refuses the transition and reports an invalid transition, and the caller never reaches the fenced statement. Reporting that raw error would defeat the fence at the only layer that acts on it — `videojob-worker`'s disposition table branches on the fence sentinel — so `CompleteJob` and `FailJob` SHALL map a refused transition to the fence sentinel — but only where the loaded value proves this actor lost the terminal outcome, which is narrower than "no longer `processing`":

- **A strictly greater epoch SHALL be reported as the fence**, whatever the status. Only a requeue advances the epoch, so a greater value is proof that this caller's job was taken from it.
- **An equal epoch on a `queued` row SHALL NOT be reported as the fence.** A genuine requeue always advances the epoch, so this combination cannot describe a takeover; what it does describe is a stale cache entry, reachable whenever a claim's write-through and its fallback delete both failed. It cannot be resolved by attempting the write, because the aggregate refuses the transition before any statement runs, so there SHALL be a concrete path to an authoritative read: **`CompleteJob` and `FailJob` SHALL load the job through the undecorated repository and persist through the cached one**, taking the two as separate collaborators wired that way in every composition root that constructs them. Reading past the cache for a terminal write is the same judgement `GET /download/:filename` already makes for its entitlement lookup and the recovery sweeper makes for its scan: a decision about who owns a job is a correctness decision and does not read a cache. The write still goes through the decorator, so the cache keeps reflecting the transition.

  Making the re-read *conditional* on observing this case SHALL NOT be substituted. It leaves the use case's correctness depending on a cache entry in every other case for no benefit — these are once-per-job writes, not the polling reads the cache exists for — and it is a branch that only executes in the rare state nobody exercises.
- **An equal epoch on a terminal row SHALL be reported as the fence only when the recorded outcome differs from the one this caller is writing.** When the recorded status, storage key, frame count, and failure reason are the ones this call would write, the call SHALL report success rather than the fence — fencing it would dead-letter a message whose work completed and skip the source-object cleanup that a committed outcome licenses.

  **That success SHALL be distinguishable from one this call applied.** Matching fields do not prove this caller wrote them: two sweepers at the requeue bound write the same epoch, the same `failed`, and the same fixed reason, so the loser would see a row identical to its own intent. The use cases SHALL therefore return that distinction rather than a bare error — reporting whether the terminal state was *applied by this call* or was *already present*, which requires the repository write beneath them to report it too — and a caller SHALL perform the cleanup a terminal outcome licenses only when the write was applied by this call, or when this call is retrying an attempt it made itself and whose response it lost. A caller with no previous attempt of its own SHALL treat "already present" as another actor's outcome and clean up nothing — which is what keeps the sweeper's single-cleanup guarantee intact while still letting a worker's retry finish its own work.

A `pending` job SHALL NOT be mapped to the fence at any epoch, for the same reason `StartProcessing` does not map one to a lost claim: it was never dispatched, so a terminal write naming it is an anomaly worth surfacing as itself rather than hiding behind the routine case. This mirrors the pending-versus-lost-claim discrimination `StartProcessing` already makes, and SHALL be implemented as deliberately as that one.

#### Scenario: A terminal write on a requeued job reports the fence, not an invalid transition

- **GIVEN** a worker holding a job's epoch whose job was swept, requeued to `queued`, and re-dispatched while it was extracting
- **WHEN** it calls `CompleteJob` with its held epoch
- **THEN** the call reports the fence sentinel rather than an invalid-transition error, and the job is not modified

#### Scenario: A retried terminal write that already committed reports success

- **GIVEN** a worker whose `CompleteJob` committed but whose database response was lost, so it retries with the same epoch, storage key, and frame count
- **WHEN** the retry loads the job and finds it already `completed` at that epoch with exactly those values
- **THEN** the call reports success rather than the fence, so the caller acknowledges the message and performs the cleanup its committed outcome licenses

#### Scenario: A stale cached queued read does not fence the rightful holder

- **GIVEN** a worker that won a claim whose cache write-through and fallback delete both failed, leaving a `queued` record at the same epoch it holds
- **WHEN** it later calls `CompleteJob` while that stale record remains in Redis
- **THEN** the outcome is decided against the authoritative row rather than the cached one, and the write succeeds

#### Scenario: A terminal write on a job a successor already finished reports the fence

- **GIVEN** a worker holding a job's epoch whose job has since been requeued, re-claimed, and completed by another worker
- **WHEN** it calls `CompleteJob` or `FailJob` with its held epoch
- **THEN** the call reports the fence sentinel and the persisted outcome is still the successor's

#### Scenario: A terminal write naming a pending job is not disguised as a fence

- **GIVEN** a `pending` job that was never enqueued or claimed
- **WHEN** `CompleteJob` or `FailJob` is called for it
- **THEN** the call reports the invalid transition rather than the fence sentinel

The claim is what makes duplicate dispatch safe, and it is a *correctness* primitive rather than a *recovery* one. Dispatch is at-least-once by construction (`videojob-outbox-relay` can publish a message whose `published_at` never commits), and `Update` was historically a read-then-unconditional-write, so without the conditional persist two deliveries would both pass `queued → processing` and both run an extraction over the same source. With it, exactly one wins and the loser mutates nothing.

Recovery of a job whose consumer died mid-extraction is a **separate** mechanism and SHALL remain so. `ClaimForProcessing`'s predicate SHALL keep naming `queued` alone: widening it to re-admit a `processing` row would make the claim depend on the lease that decides abandonment, and that lease fails open, so a lease-store outage would license two workers to claim one live job. `videojob-lease-recovery` instead returns an abandoned job to `queued` through the aggregate's `Requeue` edge, after which this claim applies unchanged. An implementer SHALL NOT relax the predicate here to make a stranded job recoverable.

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

#### Scenario: StartProcessing reports the epoch its claim won

- **GIVEN** a persisted `VideoJob` in `queued` status
- **WHEN** `StartProcessing.Execute` succeeds
- **THEN** it returns the fence epoch stored on the claimed row, and `CompleteJob` called with that epoch succeeds

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
- **WHEN** `CompleteJob.Execute` is called with its ID, the epoch it was claimed at, a `StorageKey`, and a `FrameCount`
- **THEN** a subsequent `FindByID` for that job returns it in `completed` status with the given `StorageKey` and `FrameCount`

#### Scenario: FailJob transitions and persists a reason

- **GIVEN** a persisted `VideoJob` in `processing` status
- **WHEN** `FailJob.Execute` is called with its ID, the epoch it was claimed at, and a failure reason
- **THEN** a subsequent `FindByID` for that job returns it in `failed` status with that `ErrorReason`

#### Scenario: A terminal write carrying a superseded epoch is refused

- **GIVEN** a persisted `VideoJob` whose fence epoch has advanced since a caller claimed it
- **WHEN** that caller calls `CompleteJob.Execute` or `FailJob.Execute` with the epoch it claimed at
- **THEN** it returns the fence sentinel — distinct from the lost-claim sentinel and from `ErrVideoJobNotFound` — and the job's persisted state is unchanged

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
