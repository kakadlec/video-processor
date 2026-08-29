## MODIFIED Requirements

### Requirement: ProcessVideoJob Runs a VideoJob's Start/Extract Sequence Synchronously

The `ProcessVideoJob` application-layer use case SHALL, given a `VideoJob` ID and a **source storage key**, call `StartProcessing`, then download the source object to a transient local path through the `SourceStorage` port, then `FrameExtractor.ExtractFrames` against that local path, then store the extracted zip through `ResultStorage`, synchronously, in-process, with no queue or worker involved. On a download failure, an extraction failure, or a storage failure it SHALL call `FailJob` and leave the job `failed`. On success it SHALL NOT call `CompleteJob` itself — the job SHALL remain `processing`, and the caller completes it.

It SHALL NOT call `EnqueueVideoJob`. That transition now belongs to the caller, and calling it here would be a rejected `queued → queued` transition: `POST /upload` enqueues the job itself, immediately after creating it, so that the `pending → queued` update commits in the same transaction as the event describing it. `docs/domain-model.md`'s use-case table has always assigned `EnqueueVideoJob` the actor "API (post-upload)"; this aligns the code with it. An implementer SHALL NOT restore the call to keep the use case's sequence "complete".

The second parameter is a storage key rather than a local file path, and that is the point of the signature: a path written by the calling HTTP handler is only meaningful to a process that shares that handler's filesystem. A later change runs this sequence in `cmd/worker`, which does not. An implementer SHALL NOT reintroduce a local-path parameter, and SHALL NOT move the download into the `ffmpeg` adapter — the adapter takes a local path and knows nothing about object storage, which is the same attribution `videojob-result-storage` established for the result zip.

`ProcessVideoJob` SHALL own the downloaded copy's lifetime and remove it before returning on every path, registering that removal before the extraction attempt so an early return cannot skip it. It SHALL NOT delete the source **object**; that is the caller's, since the caller stored it and reaches exit paths this use case never runs on (see `videojob-source-storage`).

The original justification for the `CompleteJob` split no longer holds and SHALL NOT be restated: it existed so the caller could still call `FailJob` if its own further work with the result failed, and the caller has no such further work — storing the result is `ProcessVideoJob`'s own step, so a successful return already means the result is durable. The split is retained only because moving `CompleteJob` inside `ProcessVideoJob` is a separate refactor with its own blast radius across this capability's callers and tests, which this change does not take on. An implementer SHALL NOT preserve or reintroduce a post-processing failure branch in the handler to justify it; on success the handler completes the job unconditionally.

#### Scenario: Successful extraction and storage leaves the job processing, with the result available to the caller

- **GIVEN** a `VideoJob` in `queued` status and a stored source object `ffmpeg` can decode
- **WHEN** `ProcessVideoJob.Execute` is called with that job's ID and the source key
- **THEN** it returns a non-zero `StorageKey` and a `FrameCount` matching the number of extracted frames, the zip is present in the bucket under that key, the job's persisted status is still `processing`, and the transient local copy no longer exists

#### Scenario: A job that has not been enqueued cannot be processed

- **GIVEN** a `VideoJob` still in `pending` status
- **WHEN** `ProcessVideoJob.Execute` is called with its ID
- **THEN** it returns an error from the `StartProcessing` transition and does not invoke `ffmpeg`, because this use case no longer performs the enqueue itself

#### Scenario: Failed extraction fails the job

- **GIVEN** a `VideoJob` in `queued` status and a stored source object `ffmpeg` cannot decode
- **WHEN** `ProcessVideoJob.Execute` is called with that job's ID and the source key
- **THEN** the job ends in `failed` status with a non-empty `ErrorReason`, and the transient local copy no longer exists

#### Scenario: Failed download fails the job before ffmpeg runs

- **GIVEN** a source key whose object cannot be retrieved from the bucket
- **WHEN** `ProcessVideoJob.Execute` is called with it
- **THEN** the job ends in `failed` status with a non-empty `ErrorReason`, `ffmpeg` is never invoked, and the persisted reason names neither the storage endpoint nor the bucket

#### Scenario: Failed storage fails the job

- **GIVEN** a `VideoJob` whose frames were extracted successfully, and a `ResultStorage` that cannot store the zip
- **WHEN** `ProcessVideoJob.Execute` returns
- **THEN** the job ends in `failed` status with a non-empty `ErrorReason`, and the result reports failure rather than a `StorageKey` pointing at an object that was never stored

#### Scenario: An extraction error with no message still yields a non-empty failure reason

- **GIVEN** `FrameExtractor.ExtractFrames` returns an error whose message is empty
- **WHEN** `ProcessVideoJob.Execute` processes that failure
- **THEN** `FailJob` is called with a non-empty fallback reason, never an empty string
