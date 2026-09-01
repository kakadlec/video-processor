# videojob-execution Specification

## Purpose

Define the `ProcessVideoJob` application-layer orchestration use case and the `FrameExtractor` domain port/`ffmpeg`-backed adapter that actually runs frame extraction for a `VideoJob`, plus the acknowledgement contract `POST /upload` answers with. `ProcessVideoJob` drives `internal/video/application`'s `StartProcessing`/`FailJob` use cases (defined in `videojob-lifecycle`) end to end for a real video file; its caller is `cmd/worker`, consuming a dispatched message, and the caller is what calls `CompleteJob` on success. `EnqueueVideoJob` is deliberately not among them: `POST /upload` performs that transition itself, so that it commits with the outbox row describing it, and then returns `202` without processing anything. "Synchronously and in-process" describes this use case's own control flow, not the system's — the queue, the consumer, and the worker's own obligations are `videojob-messaging`'s, `videojob-outbox-relay`'s, and `videojob-worker`'s respectively.

## Requirements

### Requirement: ProcessVideoJob Runs a VideoJob's Start/Extract Sequence Synchronously

The `ProcessVideoJob` application-layer use case SHALL, given a `VideoJob` ID and a **source storage key**, call `StartProcessing`, then download the source object to a transient local path through the `SourceStorage` port, then `FrameExtractor.ExtractFrames` against that local path, then store the extracted zip through `ResultStorage`, synchronously and in-process. On a download failure, an extraction failure, or a storage failure it SHALL call `FailJob` and leave the job `failed`. On success it SHALL NOT call `CompleteJob` itself — the job SHALL remain `processing`, and the caller completes it.

"Synchronously and in-process" describes this use case's own control flow, not the system's. Its caller is `cmd/worker`, consuming a dispatched message (see `videojob-worker`); the sequence blocks that consumer for the duration of the extraction and returns a result rather than a promise. An implementer SHALL NOT introduce internal concurrency, a callback, or a queue between these steps.

**`StartProcessing` is a claim, and a lost claim SHALL be propagated unchanged.** When `StartProcessing` reports that the job was no longer `queued`, `ProcessVideoJob` SHALL return that sentinel error and SHALL NOT call `FailJob`, SHALL NOT download the source, and SHALL NOT delete anything. Another consumer owns the job; the correct behavior is to touch nothing at all. An implementer SHALL NOT convert a lost claim into a job failure to make the error handling uniform.

It SHALL NOT call `EnqueueVideoJob`. That transition belongs to the submitting handler, and calling it here would be a rejected `queued → queued` transition: `POST /upload` enqueues the job itself, immediately after creating it, so that the `pending → queued` update commits in the same transaction as the event describing it. `docs/domain-model.md`'s use-case table has always assigned `EnqueueVideoJob` the actor "API (post-upload)". An implementer SHALL NOT restore the call to keep the use case's sequence "complete".

The second parameter is a storage key rather than a local file path, and that is the point of the signature: a path written by the submitting HTTP handler is only meaningful to a process that shares that handler's filesystem, and `cmd/worker` does not. An implementer SHALL NOT reintroduce a local-path parameter, and SHALL NOT move the download into the `ffmpeg` adapter — the adapter takes a local path and knows nothing about object storage, which is the same attribution `videojob-result-storage` established for the result zip.

`ProcessVideoJob` SHALL own the downloaded copy's lifetime and remove it before returning on every path, registering that removal before the extraction attempt so an early return cannot skip it. That copy lives on the consuming process's filesystem, which is not the submitting API's. It SHALL NOT delete the source **object**; that is the caller's, and `videojob-worker` defines the conditions under which the caller may.

The original justification for the `CompleteJob` split no longer holds and SHALL NOT be restated: it existed so the caller could still call `FailJob` if its own further work with the result failed, and the caller has no such further work — storing the result is `ProcessVideoJob`'s own step, so a successful return already means the result is durable. The split is retained because the caller is now a different process with its own acknowledgement obligations, and folding `CompleteJob` inside would put the terminal write out of that caller's reach. An implementer SHALL NOT reintroduce a post-processing failure branch in the caller to justify it; on success the caller completes the job unconditionally.

#### Scenario: Successful extraction and storage leaves the job processing, with the result available to the caller

- **GIVEN** a `VideoJob` in `queued` status and a stored source object `ffmpeg` can decode
- **WHEN** `ProcessVideoJob.Execute` is called with that job's ID and the source key
- **THEN** it returns a non-zero `StorageKey` and a `FrameCount` matching the number of extracted frames, the zip is present in the bucket under that key, the job's persisted status is still `processing`, and the transient local copy no longer exists

#### Scenario: A job that has not been enqueued cannot be processed

- **GIVEN** a `VideoJob` still in `pending` status
- **WHEN** `ProcessVideoJob.Execute` is called with its ID
- **THEN** it returns an error from the `StartProcessing` transition and does not invoke `ffmpeg`, because this use case does not perform the enqueue itself

#### Scenario: A lost claim stops the sequence before any side effect

- **GIVEN** a `VideoJob` already in `processing` status, as a duplicate dispatch would name
- **WHEN** `ProcessVideoJob.Execute` is called with its ID and source key
- **THEN** it returns the lost-claim sentinel, no source object was downloaded, `ffmpeg` was not invoked, `FailJob` was not called, and the job's persisted state is unchanged

#### Scenario: Failed extraction fails the job

- **GIVEN** a `VideoJob` in `queued` status and a stored source object `ffmpeg` cannot decode
- **WHEN** `ProcessVideoJob.Execute` is called with that job's ID and the source key
- **THEN** it calls `FailJob`, the job's persisted status is `failed` with a non-empty `ErrorReason`, and the transient local copy no longer exists

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
