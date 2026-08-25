# videojob-execution Specification

## Purpose

Define the `ProcessVideoJob` application-layer orchestration use case and the `FrameExtractor` domain port/`ffmpeg`-backed adapter that actually runs frame extraction for a `VideoJob`, synchronously and in-process. This is the piece that drives `internal/video/application`'s `EnqueueVideoJob`/`StartProcessing`/`CompleteJob`/`FailJob` use cases (defined in `videojob-lifecycle`) end to end for a real video file, wired into `cmd/api`'s `POST /upload` handler. No queue or worker is in scope here — that's Phase 6.

## Requirements

### Requirement: ProcessVideoJob Runs a VideoJob's Enqueue/Start/Extract Sequence Synchronously

The `ProcessVideoJob` application-layer use case SHALL, given a `VideoJob` ID and a **source storage key**, call `EnqueueVideoJob`, then `StartProcessing`, then download the source object to a transient local path through the `SourceStorage` port, then `FrameExtractor.ExtractFrames` against that local path, then store the extracted zip through `ResultStorage`, synchronously, in-process, with no queue or worker involved. On a download failure, an extraction failure, or a storage failure it SHALL call `FailJob` and leave the job `failed`. On success it SHALL NOT call `CompleteJob` itself — the job SHALL remain `processing`, and the caller completes it.

The second parameter is a storage key rather than a local file path, and that is the point of the signature: a path written by the calling HTTP handler is only meaningful to a process that shares that handler's filesystem. Phase 6 runs this sequence in `cmd/worker`, which does not. An implementer SHALL NOT reintroduce a local-path parameter, and SHALL NOT move the download into the `ffmpeg` adapter — the adapter takes a local path and knows nothing about object storage, which is the same attribution `videojob-result-storage` established for the result zip.

`ProcessVideoJob` SHALL own the downloaded copy's lifetime and remove it before returning on every path, registering that removal before the extraction attempt so an early return cannot skip it. It SHALL NOT delete the source **object**; that is the caller's, since the caller stored it and reaches exit paths this use case never runs on (see `videojob-source-storage`).

The original justification for the `CompleteJob` split no longer holds and SHALL NOT be restated: it existed so the caller could still call `FailJob` if its own further work with the result failed, and the caller has no such further work — storing the result is `ProcessVideoJob`'s own step, so a successful return already means the result is durable. The split is retained only because moving `CompleteJob` inside `ProcessVideoJob` is a separate refactor with its own blast radius across this capability's callers and tests, which this change does not take on. An implementer SHALL NOT preserve or reintroduce a post-processing failure branch in the handler to justify it; on success the handler completes the job unconditionally.

#### Scenario: Successful extraction and storage leaves the job processing, with the result available to the caller

- **GIVEN** a `VideoJob` in `pending` status and a stored source object `ffmpeg` can decode
- **WHEN** `ProcessVideoJob.Execute` is called with that job's ID and the source key
- **THEN** it returns a non-zero `StorageKey` and a `FrameCount` matching the number of extracted frames, the zip is present in the bucket under that key, the job's persisted status is still `processing`, and the transient local copy no longer exists

#### Scenario: Failed extraction fails the job

- **GIVEN** a `VideoJob` in `pending` status and a stored source object `ffmpeg` cannot decode
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

### Requirement: POST /upload Completes The Job Only After Its Result Is Durably Usable

`POST /upload`'s handler (`cmd/api/video.go`'s `handleVideoUpload`) SHALL call `CompleteJob` only after `ProcessVideoJob` has reported that the result is stored in the bucket. Because storing the result is now part of `ProcessVideoJob`'s own sequence, a `ProcessVideoJob` result reporting success is itself the durability guarantee the handler waits for; the handler SHALL NOT record any additional ownership artifact before completing the job.

If `ProcessVideoJob` reports failure, the handler SHALL NOT call `CompleteJob`, so the job's persisted status never claims `completed` for a result that was not stored.

#### Scenario: A stored result completes the job

- **GIVEN** `ProcessVideoJob` extracted frames and stored the zip successfully
- **WHEN** the request finishes
- **THEN** the job's persisted status is `completed`, with the `StorageKey` and `FrameCount` `ProcessVideoJob` returned

#### Scenario: A result that was not stored leaves the job failed, not completed

- **GIVEN** `ProcessVideoJob` extracted frames successfully but could not store the zip
- **WHEN** the request finishes
- **THEN** the job's persisted status is `failed`, not `completed`, and `GetJobStatus` never reports a `StorageKey` for an object that is not in the bucket

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
