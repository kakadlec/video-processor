## MODIFIED Requirements

### Requirement: A Finalized Duplicate Returns The Existing Job, Translated To The Upload Response Shape

A `POST /upload` request whose idempotency key already holds a `VideoJobID` (not an in-flight reservation) SHALL NOT create a new `VideoJob` or invoke `ffmpeg`. It SHALL delete the redundant file this request itself just saved and return the existing job's current status translated into `ProcessingResult` — the same response shape a non-duplicate `POST /upload` returns — never `GetJobStatusResult`'s own field names.

#### Scenario: Duplicate after the original completed

- **GIVEN** a prior request's idempotency key now holds a `VideoJobID` for a job that has since reached `completed`
- **WHEN** a new request with the same user and identical content arrives
- **THEN** the handler does not invoke `ffmpeg` or create a new `VideoJob`, deletes the file it just saved for this request, and returns `ProcessingResult{Success: true, Message: <English>, ZipPath: <the job's StorageKey>, FrameCount: <the job's FrameCount>}`

#### Scenario: Duplicate after the original failed

- **GIVEN** a prior request's idempotency key still resolves to a `VideoJobID` for a job that reached `failed` (a narrow window before that job's own `FailJob` handling clears the key)
- **WHEN** a new request with the same user and identical content arrives in that window
- **THEN** the handler returns `ProcessingResult{Success: false, Message: <English, incorporating the job's failure reason>}` without creating a new `VideoJob`

#### Scenario: Duplicate while the original is still processing

- **GIVEN** a prior request's idempotency key now holds a `VideoJobID` for a job still in `processing` (reached via the bounded-retry-then-lookup path)
- **WHEN** a new request with the same user and identical content arrives
- **THEN** the handler does not invoke `ffmpeg` or create a new `VideoJob`, deletes the file it just saved for this request, and returns `ProcessingResult{Success: false, Message: <English: still being processed, try again shortly>}`

#### Scenario: Duplicate's own file never affects the original request's artifacts

- **GIVEN** a duplicate request has saved its own file under its own `uploadID`-prefixed path before discovering the duplicate
- **WHEN** the handler deletes that redundant file
- **THEN** the original request's uploaded file under `uploads/` (saved under a different `uploadID`) and its stored result object are both left untouched
