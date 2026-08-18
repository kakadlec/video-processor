# videojob-execution Specification

## Purpose

Define the `ProcessVideoJob` application-layer orchestration use case and the `FrameExtractor` domain port/`ffmpeg`-backed adapter that actually runs frame extraction for a `VideoJob`, synchronously and in-process. This is the piece that drives `internal/video/application`'s `EnqueueVideoJob`/`StartProcessing`/`CompleteJob`/`FailJob` use cases (defined in `videojob-lifecycle`) end to end for a real video file, wired into `cmd/api`'s `POST /upload` handler. No queue or worker is in scope here — that's Phase 6.

## Requirements

### Requirement: ProcessVideoJob Runs a VideoJob's Enqueue/Start/Extract Sequence Synchronously

The `ProcessVideoJob` application-layer use case SHALL, given a `VideoJob` ID and a video file path, call `EnqueueVideoJob`, then `StartProcessing`, then `FrameExtractor.ExtractFrames`, synchronously, in-process, with no queue or worker involved. On extraction failure it SHALL call `FailJob` and leave the job `failed`. On extraction success it SHALL NOT call `CompleteJob` itself — the job SHALL remain `processing`, and the caller is responsible for completing it once any further steps it performs with the result (e.g. `cmd/api/video.go`'s `handleVideoUpload` recording output artifact ownership) have themselves succeeded. This split exists specifically so the caller can still call `FailJob` — a valid `processing → failed` transition — if its own further steps fail, rather than being stuck with an already-`completed` job pointing at a result that never became usable (`completed → failed` is not a valid transition).

#### Scenario: Successful extraction leaves the job processing, with the result available to the caller

- **GIVEN** a `VideoJob` in `pending` status and a video file `ffmpeg` can decode
- **WHEN** `ProcessVideoJob.Execute` is called with that job's ID and the file's path
- **THEN** it returns a non-zero `StorageKey` and a `FrameCount` matching the number of extracted frames, and the job's persisted status is still `processing`

#### Scenario: Failed extraction fails the job

- **GIVEN** a `VideoJob` in `pending` status and a video file `ffmpeg` cannot decode
- **WHEN** `ProcessVideoJob.Execute` is called with that job's ID and the file's path
- **THEN** the job ends in `failed` status with a non-empty `ErrorReason`

#### Scenario: An extraction error with no message still yields a non-empty failure reason

- **GIVEN** `FrameExtractor.ExtractFrames` returns an error whose message is empty
- **WHEN** `ProcessVideoJob.Execute` processes that failure
- **THEN** `FailJob` is called with a non-empty fallback reason, never an empty string

### Requirement: POST /upload Completes The Job Only After Its Result Is Durably Usable

`POST /upload`'s handler (`cmd/api/video.go`'s `handleVideoUpload`) SHALL call `CompleteJob` only after every step it performs with `ProcessVideoJob`'s successful result (recording the output artifact's ownership) has itself succeeded. If any such step fails, the handler SHALL call `FailJob` instead, so the job's persisted status never claims `completed` for a result the caller could not make durably usable.

#### Scenario: Successful processing and successful ownership recording completes the job

- **GIVEN** `ProcessVideoJob` extracted frames successfully and recording the output artifact's ownership also succeeds
- **WHEN** the request finishes
- **THEN** the job's persisted status is `completed`, with the `StorageKey` and `FrameCount` `ProcessVideoJob` returned

#### Scenario: A failure recording output ownership fails the job instead of leaving it completed

- **GIVEN** `ProcessVideoJob` extracted frames successfully but recording the output artifact's ownership fails
- **WHEN** the request finishes
- **THEN** the job's persisted status is `failed`, not `completed`, and `GetJobStatus` never reports a `StorageKey` for the now-deleted output artifact

### Requirement: FrameExtractor Extracts Frames And Reports Their Names

The `FrameExtractor` domain port SHALL, given a `VideoJobID` and a video file path, invoke `ffmpeg` to extract one frame per second as PNG images, package them into a zip file, and return a `StorageKey` identifying the zip, the extracted frame count, and the list of extracted image filenames. Its `ffmpeg`-backed implementation (`internal/video/infrastructure/ffmpeg`) SHALL always remove its per-job temporary extraction directory, whether extraction succeeds or fails.

#### Scenario: Extraction reports the storage key, frame count, and image names

- **WHEN** `FrameExtractor.ExtractFrames` is called with a decodable video file
- **THEN** it returns a non-zero `StorageKey`, a `frameCount` equal to the number of extracted frames, and an `imageNames` slice of that same length

#### Scenario: Temporary extraction directory is always removed

- **WHEN** `FrameExtractor.ExtractFrames` is called, whether extraction succeeds or fails
- **THEN** no per-job temporary directory remains under `temp/` after it returns
