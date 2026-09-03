## MODIFIED Requirements

### Requirement: ProcessVideoJob Runs a VideoJob's Start/Extract Sequence Synchronously

The `ProcessVideoJob` application-layer use case SHALL, given a `VideoJob` ID and a **source storage key**, call `StartProcessing`, then download the source object to a transient local path through the `SourceStorage` port, then `FrameExtractor.ExtractFrames` against that local path, then store the extracted zip through `ResultStorage`, synchronously and in-process. On a download failure, an extraction failure, or a storage failure it SHALL call `FailJob` and leave the job `failed`. On success it SHALL NOT call `CompleteJob` itself — the job SHALL remain `processing`, and the caller completes it.

"Synchronously and in-process" describes this use case's own control flow, not the system's. Its caller is `cmd/worker`, consuming a dispatched message (see `videojob-worker`); the sequence blocks that consumer for the duration of the extraction and returns a result rather than a promise. An implementer SHALL NOT introduce internal concurrency, a callback, or a queue between these steps.

**`StartProcessing` is a claim, and a lost claim SHALL be propagated unchanged.** When `StartProcessing` reports that the job was no longer `queued`, `ProcessVideoJob` SHALL return that sentinel error and SHALL NOT call `FailJob`, SHALL NOT download the source, and SHALL NOT delete anything. Another consumer owns the job; the correct behavior is to touch nothing at all. An implementer SHALL NOT convert a lost claim into a job failure to make the error handling uniform.

**The fence epoch `StartProcessing` reports SHALL be carried through the sequence**: `ProcessVideoJob` SHALL pass it to its own `FailJob` calls and SHALL report it in its result, so the caller's `CompleteJob` is fenced by the same value. It SHALL NOT re-read the epoch from the job at the point of the write — by then the row may carry a successor's, and a fence checked against that value would pass in exactly the case it exists to reject.

**`ProcessVideoJob` SHALL hold the job's lease for the duration of its extraction.** It SHALL acquire the lease at the moment `StartProcessing` reports a won claim — it is the only component that observes that moment, since its caller does not regain control until the sequence returns — and SHALL renew it until the sequence ends. It SHALL NOT release it: the lease must survive until the caller's terminal write commits, so release belongs to that caller, and the conditional release makes performing it elsewhere safe. Acquire and renew failures SHALL be logged and SHALL NOT stop the extraction; a renewal that finds a superseded epoch SHALL stop renewing rather than overwrite it.

**`ProcessVideoJob`'s result SHALL report whether its own `FailJob` write was applied by this call or found the outcome already present.** The caller performs `CompleteJob` itself and reads that distinction from its return value, but the failure write happens inside this use case, and the caller's obligations turn on it: `videojob-worker` gates deleting the source object and clearing the idempotency key on **having applied this job's terminal write**, and a run that merely found the row already `failed` — as a sweeper reaching the requeue bound at the same epoch leaves it — applied nothing and owns neither cleanup. Reporting the failure without that flag would make the caller's rule unimplementable and would let two actors clean up after one job.

**A fenced write SHALL be reported, never retried and never worked around.** When `FailJob` is refused because the epoch has been superseded, `ProcessVideoJob` SHALL surface that sentinel to its caller rather than falling back to an unfenced write, re-loading the job, or reporting the job as failed. Its error result SHALL preserve the job ID and the epoch this run held, including when that epoch is non-zero, so the worker's takeover log identifies the attempt that was fenced. The job was taken over while this sequence was running, and its outcome belongs to whoever holds it now. The transient local copy SHALL still be removed, as on every other path.

It SHALL NOT call `EnqueueVideoJob`. That transition belongs to the submitting handler, and calling it here would be a rejected `queued → queued` transition: `POST /upload` enqueues the job itself, immediately after creating it, so that the `pending → queued` update commits in the same transaction as the event describing it. `docs/domain-model.md`'s use-case table has always assigned `EnqueueVideoJob` the actor "API (post-upload)". An implementer SHALL NOT restore the call to keep the use case's sequence "complete". It SHALL NOT call the requeue transition either: returning an abandoned job to the queue is `videojob-lease-recovery`'s sweeper's, and a use case that requeued the job it was running would re-dispatch its own work.

The second parameter is a storage key rather than a local file path, and that is the point of the signature: a path written by the submitting HTTP handler is only meaningful to a process that shares that handler's filesystem, and `cmd/worker` does not. An implementer SHALL NOT reintroduce a local-path parameter, and SHALL NOT move the download into the `ffmpeg` adapter — the adapter takes a local path and knows nothing about object storage, which is the same attribution `videojob-result-storage` established for the result zip.

`ProcessVideoJob` SHALL own the downloaded copy's lifetime and remove it before returning on every path, registering that removal before the extraction attempt so an early return cannot skip it. That copy lives on the consuming process's filesystem, which is not the submitting API's. It SHALL NOT delete the source **object**; that is the caller's, and `videojob-worker` defines the conditions under which the caller may.

The original justification for the `CompleteJob` split no longer holds and SHALL NOT be restated: it existed so the caller could still call `FailJob` if its own further work with the result failed, and the caller has no such further work — storing the result is `ProcessVideoJob`'s own step, so a successful return already means the result is durable. The split is retained because the caller is now a different process with its own acknowledgement obligations, and folding `CompleteJob` inside would put the terminal write out of that caller's reach. An implementer SHALL NOT reintroduce a post-processing failure branch in the caller to justify it; on success the caller completes the job unconditionally, with the epoch this use case reports.

#### Scenario: The lease is held for the duration of the extraction

- **GIVEN** a `VideoJob` in `queued` status and an extraction that outlives the lease's own expiry
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

#### Scenario: A result that cannot be stored fails the job

- **GIVEN** a `VideoJob` in `queued` status whose frames extract successfully but whose zip cannot be stored
- **WHEN** `ProcessVideoJob.Execute` is called
- **THEN** it calls `FailJob`, the job's persisted status is `failed`, and no `StorageKey` is reported

#### Scenario: An extraction error with no message still yields a non-empty failure reason

- **GIVEN** `FrameExtractor.ExtractFrames` returns an error whose message is empty
- **WHEN** `ProcessVideoJob.Execute` processes that failure
- **THEN** `FailJob` is called with a non-empty fallback reason, never an empty string
