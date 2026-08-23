## MODIFIED Requirements

### Requirement: ProcessVideoJob Runs a VideoJob's Enqueue/Start/Extract Sequence Synchronously

The `ProcessVideoJob` application-layer use case SHALL, given a `VideoJob` ID and a video file path, call `EnqueueVideoJob`, then `StartProcessing`, then `FrameExtractor.ExtractFrames`, then store the extracted zip through `ResultStorage`, synchronously, in-process, with no queue or worker involved. On either an extraction failure or a storage failure it SHALL call `FailJob` and leave the job `failed`. On success it SHALL NOT call `CompleteJob` itself — the job SHALL remain `processing`, and the caller is responsible for completing it once any further steps it performs with the result have themselves succeeded. This split exists specifically so the caller can still call `FailJob` — a valid `processing → failed` transition — if its own further steps fail, rather than being stuck with an already-`completed` job pointing at a result that never became usable (`completed → failed` is not a valid transition).

#### Scenario: Successful extraction and storage leaves the job processing, with the result available to the caller

- **GIVEN** a `VideoJob` in `pending` status and a video file `ffmpeg` can decode
- **WHEN** `ProcessVideoJob.Execute` is called with that job's ID and the file's path
- **THEN** it returns a non-zero `StorageKey` and a `FrameCount` matching the number of extracted frames, the zip is present in the bucket under that key, and the job's persisted status is still `processing`

#### Scenario: Failed extraction fails the job

- **GIVEN** a `VideoJob` in `pending` status and a video file `ffmpeg` cannot decode
- **WHEN** `ProcessVideoJob.Execute` is called with that job's ID and the file's path
- **THEN** the job ends in `failed` status with a non-empty `ErrorReason`

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
