# videojob-execution Specification

## Purpose

Define the `ProcessVideoJob` application-layer orchestration use case and the `FrameExtractor` domain port/`ffmpeg`-backed adapter that actually runs frame extraction for a `VideoJob`, synchronously and in-process. This is the piece that drives `internal/video/application`'s `EnqueueVideoJob`/`StartProcessing`/`CompleteJob`/`FailJob` use cases (defined in `videojob-lifecycle`) end to end for a real video file, wired into `cmd/api`'s `POST /upload` handler. No queue or worker is in scope here — that's Phase 6.

## Requirements

### Requirement: ProcessVideoJob Runs a VideoJob's Full Processing Sequence Synchronously

The `ProcessVideoJob` application-layer use case SHALL, given a `VideoJob` ID and a video file path, call `EnqueueVideoJob`, then `StartProcessing`, then `FrameExtractor.ExtractFrames`, then `CompleteJob` (on extraction success) or `FailJob` (on extraction failure) — synchronously, in-process, with no queue or worker involved.

#### Scenario: Successful processing completes the job

- **GIVEN** a `VideoJob` in `pending` status and a video file `ffmpeg` can decode
- **WHEN** `ProcessVideoJob.Execute` is called with that job's ID and the file's path
- **THEN** the job ends in `completed` status with a non-zero `StorageKey` and a `FrameCount` matching the number of extracted frames

#### Scenario: Failed extraction fails the job

- **GIVEN** a `VideoJob` in `pending` status and a video file `ffmpeg` cannot decode
- **WHEN** `ProcessVideoJob.Execute` is called with that job's ID and the file's path
- **THEN** the job ends in `failed` status with a non-empty `ErrorReason`

#### Scenario: An extraction error with no message still yields a non-empty failure reason

- **GIVEN** `FrameExtractor.ExtractFrames` returns an error whose message is empty
- **WHEN** `ProcessVideoJob.Execute` processes that failure
- **THEN** `FailJob` is called with a non-empty fallback reason, never an empty string

### Requirement: FrameExtractor Extracts Frames And Reports Their Names

The `FrameExtractor` domain port SHALL, given a `VideoJobID` and a video file path, invoke `ffmpeg` to extract one frame per second as PNG images, package them into a zip file, and return a `StorageKey` identifying the zip, the extracted frame count, and the list of extracted image filenames. Its `ffmpeg`-backed implementation (`internal/video/infrastructure/ffmpeg`) SHALL always remove its per-job temporary extraction directory, whether extraction succeeds or fails.

#### Scenario: Extraction reports the storage key, frame count, and image names

- **WHEN** `FrameExtractor.ExtractFrames` is called with a decodable video file
- **THEN** it returns a non-zero `StorageKey`, a `frameCount` equal to the number of extracted frames, and an `imageNames` slice of that same length

#### Scenario: Temporary extraction directory is always removed

- **WHEN** `FrameExtractor.ExtractFrames` is called, whether extraction succeeds or fails
- **THEN** no per-job temporary directory remains under `temp/` after it returns
