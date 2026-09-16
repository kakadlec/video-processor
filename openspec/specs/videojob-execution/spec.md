# videojob-execution Specification

## Purpose

Define the `ProcessVideoJob` application-layer orchestration use case and the `FrameExtractor` domain port/`ffmpeg`-backed adapter that actually runs frame extraction for a `VideoJob`, plus the acknowledgement contract `POST /upload` answers with. `ProcessVideoJob` drives `internal/video/application`'s `StartProcessing`/`FailJob` use cases (defined in `videojob-lifecycle`) end to end for a real video file, and `RetryVideoJob` on a transient object-storage failure; its caller is `cmd/worker`, consuming a dispatched message, and the caller is what calls `CompleteJob` on success. `EnqueueVideoJob` is deliberately not among them: `POST /upload` performs that transition itself, so that it commits with the outbox row describing it, and then returns `202` without processing anything. "Synchronously and in-process" describes this use case's own control flow, not the system's — the queue, the consumer, and the worker's own obligations are `videojob-messaging`'s, `videojob-outbox-relay`'s, and `videojob-worker`'s respectively.
## Requirements
### Requirement: ProcessVideoJob Runs a VideoJob's Start/Extract Sequence Synchronously

The `ProcessVideoJob` application-layer use case SHALL, given a `VideoJob` ID and a **source storage key**, call `StartProcessing`, then download the source object to a transient local path through the `SourceStorage` port, then `FrameExtractor.ExtractFrames` against that local path, then store the extracted zip through `ResultStorage`, synchronously and in-process. On an extraction failure, or a download failure because the source object does not exist, it SHALL call `FailJob` and leave the job `failed`. On any other download failure or result-storage failure it SHALL follow the transient object-storage retry below. On success it SHALL NOT call `CompleteJob` itself — the job SHALL remain `processing`, and the caller completes it.

**A transient object-storage failure SHALL return the job to `queued` rather than fail it, within the same bound recovery uses.** A `SourceStorage.Get` failure other than the domain's not-found sentinel, or any `ResultStorage.Put` failure, is a property of the object store at that moment rather than of the job, and committing `failed` for it would turn a momentary outage into a permanent outcome no message disposition can undo, because the terminal row is written before the caller ever sees the failure. `ProcessVideoJob` SHALL therefore:

- when the epoch its claim won is already at `domain.MaxJobRequeues`, call `FailJob` exactly as for any other failure — the bound is spent;
- otherwise pause for a fixed `storageRetryPause` of 5 seconds, so the object store gets a moment before a fresh dispatch is attempted, and then call `RetryVideoJob` with the epoch it holds, which performs the same fenced `processing → queued` write, epoch advance and transactional dispatch row that `videojob-lease-recovery`'s sweeper performs, through the same repository operation;
- on an applied retry, return the domain's requeued-for-retry sentinel together with the held epoch and whether the write was applied, calling neither `FailJob` nor `CompleteJob`;
- when the retry write is refused because the row is no longer `processing` at the held epoch, return the fence sentinel, exactly as a refused `FailJob` does;
- when the retry write fails for any other reason, return that error unchanged. The row's state is then not known to this run: the write may not have committed, leaving the row `processing` for the sweeper, or it may have committed and lost its result, leaving the row `queued` with its dispatch already recorded. Either outcome is owned by a mechanism that already resolves it, and this use case SHALL NOT probe the row to find out.

The bound SHALL be the single constant `domain.MaxJobRequeues`, shared with the sweeper, because both callers advance the same fence epoch and a job cannot have more attempts left according to one than to the other. The pause SHALL be a constant rather than configuration, and a test SHALL pin both its value and that it is taken only on the retry path. `ProcessVideoJob` SHALL NOT delete the source object or clear the idempotency key on this path: the next attempt needs both.

This covers a momentary outage, not a sustained one. With the relay polling every few seconds and workers idle, the bound can be spent in well under a minute, after which the job is failed as before. Widening the pause or the bound SHALL be argued as its own change rather than tuned here.

"Synchronously and in-process" describes this use case's own control flow, not the system's. Its caller is `cmd/worker`, consuming a dispatched message (see `videojob-worker`); the sequence blocks that consumer for the duration of the extraction and returns a result rather than a promise. An implementer SHALL NOT introduce internal concurrency, a callback, or a queue between these steps.

**`StartProcessing` is a claim, and a lost claim SHALL be propagated unchanged.** When `StartProcessing` reports that the job was no longer `queued`, `ProcessVideoJob` SHALL return that sentinel error and SHALL NOT call `FailJob`, SHALL NOT download the source, and SHALL NOT delete anything. Another consumer owns the job; the correct behavior is to touch nothing at all. An implementer SHALL NOT convert a lost claim into a job failure to make the error handling uniform.

**A claim whose *outcome* the persistence layer could not report SHALL be propagated as its own sentinel, distinct from a lost claim.** When `StartProcessing` fails because the repository could not answer — the condition `videojob-persistence` requires the adapter to mark — `ProcessVideoJob` SHALL surface a sentinel meaning *this call could not learn the claim's outcome*, and SHALL otherwise behave exactly as it does for a lost claim: it SHALL NOT call `FailJob`, SHALL NOT download the source, SHALL NOT acquire a lease, and SHALL NOT touch anything. The two sentinels answer different questions and SHALL NOT be collapsed into one: a lost claim means **another actor owns this job**, and an unknown outcome means **this call cannot say who owns it, including whether this worker does**. `videojob-worker` gives them opposite dispositions on that difference alone.

**The sentinel SHALL NOT be defined as meaning the claim did not happen.** The claim step can fail to learn the outcome in three places. The authoritative load that precedes the claim can fail before any claim is attempted, leaving the row in whatever state it already had. The conditional claim statement's result is read over the connection that carried it, so a failure reading that result leaves two possible states: the transition did not commit, or it committed and this call did not learn the epoch. A statement issued after the claim has provably affected no row — to tell a lost claim from a missing job — leaves only the first. The sentinel is the same from all three, and what it asserts is what holds on every branch it admits — that this use case ran no extraction, acquired no lease, read no source object, wrote no event and called no terminal write — and that intersection is what `videojob-worker`'s disposition is licensed by. An implementer SHALL NOT strengthen it into a claim about the stored row, and SHALL NOT add a follow-up read to resolve the ambiguity: a read issued against a repository that has just failed to answer is no likelier to answer, and one that succeeds reports only what was true at that instant. The committed branch is accounted for by `videojob-lease-recovery`, which reaches a `processing` row holding no lease — and no lease is held here, because this use case acquires one only once a claim is reported **won**. Every other branch is decided by the redelivery's own load and claim, per `videojob-worker`.

The conversion SHALL happen at the single point where `Execute` propagates the claim step's error, and SHALL preserve the original error as a wrapped cause. It SHALL NOT be applied to the same repository-unavailability condition arising **after** the claim has been won — a failure write that cannot commit, in particular — which SHALL keep propagating unchanged. By then the row is `processing` and the claim predicate admits `queued` alone, so nothing is gained by telling the caller the failure was transient, and `videojob-worker` would requeue a message that could only lose the claim. An implementer SHALL NOT simplify this by marking the condition once at the repository and letting the caller branch on it wherever it appears; the distinction the caller needs is *where in the sequence* the failure happened, which only this use case can state.

**The fence epoch `StartProcessing` reports SHALL be carried through the sequence**: `ProcessVideoJob` SHALL pass it to its own `FailJob` calls and SHALL report it in its result, so the caller's `CompleteJob` is fenced by the same value. It SHALL NOT re-read the epoch from the job at the point of the write — by then the row may carry a successor's, and a fence checked against that value would pass in exactly the case it exists to reject.

**`ProcessVideoJob` SHALL attempt to maintain the job's lease for the duration of its extraction.** It SHALL acquire the lease at the moment `StartProcessing` reports a won claim — it is the only component that observes that moment, since its caller does not regain control until the sequence returns — and SHALL renew it until the sequence ends. When those lease operations succeed, the lease SHALL remain held throughout the extraction. It SHALL NOT release it: the lease must survive until the caller's terminal write commits, so release belongs to that caller, and the conditional release makes performing it elsewhere safe. Acquire and renew failures SHALL be logged and SHALL NOT stop the extraction; a renewal that finds a superseded epoch SHALL stop renewing rather than overwrite it.

**`ProcessVideoJob`'s result SHALL report whether its own `FailJob` write was applied by this call or found the outcome already present.** The caller performs `CompleteJob` itself and reads that distinction from its return value, but the failure write happens inside this use case, and the caller's obligations turn on it: `videojob-worker` gates deleting the source object and clearing the idempotency key on **having applied this job's terminal write**, and a run that merely found the row already `failed` — as a sweeper reaching the requeue bound at the same epoch leaves it — applied nothing and owns neither cleanup. Reporting the failure without that flag would make the caller's rule unimplementable and would let two actors clean up after one job.

**A fenced write SHALL be reported, never retried and never worked around.** When `FailJob` is refused because the stored row no longer matches `processing` at the held epoch — either a newer epoch superseded the run or another actor committed a different terminal outcome at the same epoch — `ProcessVideoJob` SHALL surface that sentinel to its caller rather than falling back to an unfenced write, re-loading the job, or reporting the job as failed. Its error result SHALL preserve the job ID and the epoch this run held, including when that epoch is non-zero, so the worker's fence log identifies the refused attempt. The persisted outcome belongs to the newer holder or same-epoch terminal winner. The transient local copy SHALL still be removed, as on every other path.

It SHALL NOT call `EnqueueVideoJob`. That transition belongs to the submitting handler, and calling it here would be a rejected `queued → queued` transition: `POST /upload` enqueues the job itself, immediately after creating it, so that the `pending → queued` update commits in the same transaction as the event describing it. `docs/domain-model.md`'s use-case table has always assigned `EnqueueVideoJob` the actor "API (post-upload)". An implementer SHALL NOT restore the call to keep the use case's sequence "complete". It SHALL NOT call the requeue transition for any reason other than the transient object-storage retry above: returning an *abandoned* job to the queue is `videojob-lease-recovery`'s sweeper's, and the retry is the one case where this use case requeues the job it is running, deliberately and through `RetryVideoJob`, because the failure is the object store's rather than the job's and the fresh dispatch replaces the delivery this run holds.

The second parameter is a storage key rather than a local file path, and that is the point of the signature: a path written by the submitting HTTP handler is only meaningful to a process that shares that handler's filesystem, and `cmd/worker` does not. An implementer SHALL NOT reintroduce a local-path parameter, and SHALL NOT move the download into the `ffmpeg` adapter — the adapter takes a local path and knows nothing about object storage, which is the same attribution `videojob-result-storage` established for the result zip.

`ProcessVideoJob` SHALL own the downloaded copy's lifetime and remove it before returning on every path, registering that removal before the extraction attempt so an early return cannot skip it. That copy lives on the consuming process's filesystem, which is not the submitting API's. It SHALL NOT delete the source **object**; that is the caller's, and `videojob-worker` defines the conditions under which the caller may.

The original justification for the `CompleteJob` split no longer holds and SHALL NOT be restated: it existed so the caller could still call `FailJob` if its own further work with the result failed, and the caller has no such further work — storing the result is `ProcessVideoJob`'s own step, so a successful return already means the result is durable. The split is retained because the caller is now a different process with its own acknowledgement obligations, and folding `CompleteJob` inside would put the terminal write out of that caller's reach. An implementer SHALL NOT reintroduce a post-processing failure branch in the caller to justify it; on success the caller completes the job unconditionally, with the epoch this use case reports.

#### Scenario: The lease is held for the duration of the extraction

- **GIVEN** a `VideoJob` in `queued` status, an extraction that outlives the lease's own expiry, and a lease store whose acquire and renew operations succeed
- **WHEN** `ProcessVideoJob.Execute` runs it
- **THEN** the lease store reports the job as held throughout, and still reports it as held when `Execute` returns

#### Scenario: An extraction proceeds when the lease store is unavailable

- **GIVEN** a `VideoJob` in `queued` status and a lease store that errors on every call
- **WHEN** `ProcessVideoJob.Execute` runs it
- **THEN** the sequence completes normally and the failures are logged

#### Scenario: Successful extraction and storage leaves the job processing, with the result available to the caller

- **GIVEN** a `VideoJob` in `queued` status and a stored source object `ffmpeg` can decode
- **WHEN** `ProcessVideoJob.Execute` is called with that job's ID and the source key
- **THEN** it returns a non-zero `StorageKey`, a `FrameCount` matching the number of extracted frames, and the fence epoch its claim won; the zip is present in the bucket under that key; the job's persisted status is still `processing`; and the transient local copy no longer exists

#### Scenario: A job that has not been enqueued cannot be processed

- **GIVEN** a `VideoJob` still in `pending` status
- **WHEN** `ProcessVideoJob.Execute` is called with its ID
- **THEN** it returns an error from the `StartProcessing` transition and does not invoke `ffmpeg`, because this use case does not perform the enqueue itself

#### Scenario: A lost claim stops the sequence before any side effect

- **GIVEN** a `VideoJob` already in `processing` status, as a duplicate dispatch would name
- **WHEN** `ProcessVideoJob.Execute` is called with its ID and source key
- **THEN** it returns the lost-claim sentinel, no source object was downloaded, `ffmpeg` was not invoked, `FailJob` was not called, and the job's persisted state is unchanged

#### Scenario: A claim the repository could not answer stops the sequence with its own sentinel

- **GIVEN** a `VideoJob` in `queued` status and a repository that answers the authoritative load but cannot reach the database for the claim statement, so that statement never reaches the server
- **WHEN** `ProcessVideoJob.Execute` is called with its ID and source key
- **THEN** it returns the unknown-outcome sentinel rather than the lost-claim one, no source object was downloaded, no lease was acquired, `ffmpeg` was not invoked, `FailJob` was not called, and the job's persisted state is unchanged

#### Scenario: A claim that committed and lost its result yields the same sentinel and the same absence of side effects

- **GIVEN** a `VideoJob` in `queued` status and a claim statement that commits its transition and then fails while its result is read
- **WHEN** `ProcessVideoJob.Execute` is called with its ID and source key
- **THEN** it returns the same unknown-outcome sentinel, no lease was acquired, no source object was downloaded, `ffmpeg` was not invoked and `FailJob` was not called — and the stored row is `processing`, which this use case neither observes nor asserts anything about

#### Scenario: A load the repository could not answer yields the same sentinel before any claim is attempted

- **GIVEN** a `VideoJob` in any status and a repository that cannot answer the authoritative load `StartProcessing` performs before its claim
- **WHEN** `ProcessVideoJob.Execute` is called with its ID and source key
- **THEN** it returns the unknown-outcome sentinel, no claim was attempted, no lease was acquired, no source object was downloaded, `ffmpeg` was not invoked and `FailJob` was not called — and nothing is asserted about the stored row

#### Scenario: A malformed job identifier is not reported as an unknown claim outcome

- **GIVEN** a job identifier that does not parse as a `VideoJobID`
- **WHEN** `ProcessVideoJob.Execute` is called with it
- **THEN** it returns the invalid-identifier error, which carries neither the unknown-outcome sentinel nor the repository-unavailability one, and the claim step was never reached — the identifier is rejected before any repository call, so the failure is a permanent property of the message and `videojob-worker` dead-letters it

#### Scenario: The same unavailability after the claim is not reported as an unknown claim outcome

- **GIVEN** a run that won its claim and whose `FailJob` write then cannot reach the database
- **WHEN** `ProcessVideoJob.Execute` returns
- **THEN** the error is not the unknown-outcome sentinel, so the caller dead-letters the message and leaves the `processing` row to the sweeper

#### Scenario: A failure the caller did not write is reported as already present

- **GIVEN** an extraction whose failure write finds the row already `failed` at the same epoch, with the outcome another actor committed
- **WHEN** `ProcessVideoJob.Execute` returns
- **THEN** the result reports the failure as already present rather than applied by this call, so the caller deletes no source object and clears no idempotency key

#### Scenario: Failed extraction fails the job

- **GIVEN** a `VideoJob` in `queued` status and a stored source object `ffmpeg` cannot decode
- **WHEN** `ProcessVideoJob.Execute` is called with that job's ID and the source key
- **THEN** it calls `FailJob` with the epoch its claim won, the job's persisted status is `failed` with a non-empty `ErrorReason`, the result reports that write as applied by this call, and the transient local copy no longer exists

#### Scenario: A failure write refused by the fence is reported, not retried

- **GIVEN** a `VideoJob` whose extraction failed and whose fence epoch advanced while that extraction ran
- **WHEN** `ProcessVideoJob.Execute` reaches its `FailJob` call
- **THEN** it returns the fence sentinel together with the job ID and held epoch, the job is not moved to `failed`, no unfenced write is attempted, and the transient local copy no longer exists

#### Scenario: A source object that cannot be fetched fails the job

- **GIVEN** a `VideoJob` in `queued` status and a source key naming no stored object
- **WHEN** `ProcessVideoJob.Execute` is called with that job's ID and the key
- **THEN** it calls `FailJob`, the job's persisted status is `failed`, and the recorded reason names neither the storage endpoint nor the bucket

#### Scenario: A result that cannot be stored returns the job to queued

- **GIVEN** a `VideoJob` in `queued` status at an epoch below `domain.MaxJobRequeues`, whose frames extract successfully but whose zip cannot be stored
- **WHEN** `ProcessVideoJob.Execute` is called
- **THEN** after the retry pause it returns the requeued-for-retry sentinel, `FailJob` is not called, the job's persisted status is `queued` at the next epoch with a new dispatch row written in the same transaction, and no `StorageKey` is reported

#### Scenario: A source object that cannot be fetched for a transient reason returns the job to queued

- **GIVEN** a `VideoJob` in `queued` status at an epoch below `domain.MaxJobRequeues`, whose source object exists but cannot be read because the object store is failing
- **WHEN** `ProcessVideoJob.Execute` is called
- **THEN** it returns the requeued-for-retry sentinel, `ffmpeg` is not invoked, `FailJob` is not called, and the job's persisted status is `queued` at the next epoch

#### Scenario: A transient storage failure with the retry bound spent fails the job

- **GIVEN** a claimed `VideoJob` whose held epoch equals `domain.MaxJobRequeues` and whose zip cannot be stored
- **WHEN** `ProcessVideoJob.Execute` is called
- **THEN** it calls `FailJob` without pausing, the job's persisted status is `failed`, and no further dispatch row is written

#### Scenario: A retry write refused by the fence is reported as fenced

- **GIVEN** a claimed `VideoJob` whose zip cannot be stored and whose epoch advanced while this run held it
- **WHEN** `ProcessVideoJob.Execute` reaches its retry write
- **THEN** it returns the fence sentinel, the job is not moved to `queued` by this run, and no unfenced write is attempted

#### Scenario: An extraction error with no message still yields a non-empty failure reason

- **GIVEN** `FrameExtractor.ExtractFrames` returns an error whose message is empty
- **WHEN** `ProcessVideoJob.Execute` processes that failure
- **THEN** `FailJob` is called with a non-empty fallback reason, never an empty string

### Requirement: FrameExtractor Extracts Frames And Reports Their Names

The `FrameExtractor` domain port SHALL, given a `VideoJobID` and a video file path, invoke `ffmpeg` to extract one frame per second as PNG images, package them into a zip file on the local filesystem, and return **the local path of that zip**, the extracted frame count, and the list of extracted image filenames. It SHALL NOT return a `StorageKey` and SHALL NOT know where the result is ultimately stored — placing the zip in durable storage is `ProcessVideoJob`'s responsibility, through the `ResultStorage` port.

Its `ffmpeg`-backed implementation (`internal/video/infrastructure/ffmpeg`) SHALL write both the frames and the zip under `temp/`, never under `outputs/`, and SHALL always remove its per-job temporary extraction directory, whether extraction succeeds or fails.

#### Scenario: Extraction reports the zip path, frame count, and image names

- **WHEN** `FrameExtractor.ExtractFrames` is called with a decodable video file
- **THEN** it returns a path to an existing zip file under `temp/`, a `frameCount` equal to the number of extracted frames, and an `imageNames` slice of that same length

#### Scenario: Temporary extraction directory is always removed

- **WHEN** `FrameExtractor.ExtractFrames` is called, whether extraction succeeds or fails
- **THEN** no per-job temporary frame directory remains under `temp/` after it returns

#### Scenario: The extractor writes nothing outside temp

- **WHEN** `FrameExtractor.ExtractFrames` is called, whether extraction succeeds or fails
- **THEN** it creates no file or directory named `outputs`, and every file it produces is under `temp/`

### Requirement: POST /upload Acknowledges the Submission Without Processing It

`POST /upload` SHALL store the uploaded source, create the `VideoJob`, enqueue it, and return **`202 Accepted`** carrying at least the job identifier and the URL at which the job's status can be read. It SHALL NOT call `ProcessVideoJob`, `StartProcessing`, `CompleteJob`, or `FailJob`, and SHALL NOT wait for extraction to begin or finish.

The response SHALL NOT report a frame count, a result storage key, or a download URL, because none of them exists yet. A client SHALL learn the outcome by reading the status URL the response names (see `videojob-http-api`), and SHALL NOT infer success from the `202` — the status code acknowledges the submission, not the work.

**This is a breaking change to the endpoint's contract and SHALL be documented as one.** The endpoint keeps its path, its method, its bearer gate, its multipart form, and its file-extension validation; only the response changes, and the change is not backward compatible for any client that read a result from it.

The handler SHALL still reject an invalid submission synchronously with the status it used before. Validation, authentication, and storage failures happen before the job exists and SHALL NOT be reported as `202`.

#### Scenario: A valid submission is acknowledged, not processed

- **GIVEN** an authenticated user posting a video with a supported extension
- **WHEN** the request completes
- **THEN** the response is `202` carrying the job identifier and a status URL, the job's persisted status is `queued`, no extraction has been attempted by the responding process, and the response body contains no frame count, result key, or download URL

#### Scenario: The submission returns before extraction finishes

- **GIVEN** a submitted video whose extraction takes appreciably longer than the request
- **WHEN** the response is received
- **THEN** reading the status URL immediately afterwards reports `queued` or `processing`, demonstrating that the response did not wait for the work

#### Scenario: An invalid submission is still rejected synchronously

- **GIVEN** an authenticated user posting a file with an unsupported extension
- **WHEN** the request completes
- **THEN** the response is the same rejection it was before the cutover, no `VideoJob` was created, and nothing was enqueued

#### Scenario: A failure before the job is queued is not reported as accepted

- **GIVEN** an authenticated user posting a valid video while the job cannot be created or enqueued
- **WHEN** the request completes
- **THEN** the response reports the failure rather than `202`, and no message naming a job is published
