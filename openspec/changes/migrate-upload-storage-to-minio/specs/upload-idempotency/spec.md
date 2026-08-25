## MODIFIED Requirements

### Requirement: Idempotency Key Is Derived From Per-User Content Hash

`handleVideoUpload` SHALL derive an idempotency key by hashing the uploaded video's full content with SHA-256 while it is streamed to the configured MinIO bucket, and SHALL scope that hash per authenticated user (`idempotency:{userID}:{hash}`), not globally.

The hash is available only once the whole part has been read, which is after the object has been written. A duplicate is therefore always detected *after* its own object exists, and the redundant object is deleted rather than never created. Avoiding that would require buffering the upload to hash before storing — which is the local file this migration removes — so the extra object write and delete on the duplicate path are accepted, not engineered around.

#### Scenario: Same user, same content produces the same key

- **GIVEN** two `POST /upload` requests from the same authenticated user, each carrying byte-identical video content
- **WHEN** each request's idempotency key is derived
- **THEN** both requests derive the same Redis key

#### Scenario: Different users, same content produce different keys

- **GIVEN** two `POST /upload` requests carrying byte-identical video content but from two different authenticated users
- **WHEN** each request's idempotency key is derived
- **THEN** the two requests derive different Redis keys, and neither is treated as a duplicate of the other

#### Scenario: Hashing adds no extra read of the upload

- **GIVEN** a `POST /upload` request being streamed to the bucket
- **WHEN** the handler computes the idempotency hash
- **THEN** it does so via the same read pass already used to upload the object, not a second read of the uploaded content

### Requirement: A Finalized Duplicate Returns The Existing Job, Translated To The Upload Response Shape

A `POST /upload` request whose idempotency key already holds a `VideoJobID` (not an in-flight reservation) SHALL NOT create a new `VideoJob` or invoke `ffmpeg`. It SHALL delete the redundant source object this request itself just stored and return the existing job's current status translated into `ProcessingResult` — the same response shape a non-duplicate `POST /upload` returns — never `GetJobStatusResult`'s own field names.

The duplicate's cleanup SHALL be scoped to its own `uploadID`-derived key. The original request's *source* object is not asserted to survive — source objects are transient by contract (see `videojob-source-storage`), so an original that already completed has deleted its own. What a duplicate SHALL never do is delete a key it did not create.

#### Scenario: Duplicate after the original completed

- **GIVEN** a prior request's idempotency key now holds a `VideoJobID` for a job that has since reached `completed`
- **WHEN** a new request with the same user and identical content arrives
- **THEN** the handler does not invoke `ffmpeg` or create a new `VideoJob`, deletes the source object it just stored for this request, and returns `ProcessingResult{Success: true, Message: <English>, ZipPath: <the job's StorageKey>, FrameCount: <the job's FrameCount>}`

#### Scenario: Duplicate after the original failed

- **GIVEN** a prior request's idempotency key still resolves to a `VideoJobID` for a job that reached `failed` (a narrow window before that job's own `FailJob` handling clears the key)
- **WHEN** a new request with the same user and identical content arrives in that window
- **THEN** the handler returns `ProcessingResult{Success: false, Message: <English, incorporating the job's failure reason>}` without creating a new `VideoJob`

#### Scenario: Duplicate while the original is still processing

- **GIVEN** a prior request's idempotency key now holds a `VideoJobID` for a job still in `processing` (reached via the bounded-retry-then-lookup path)
- **WHEN** a new request with the same user and identical content arrives
- **THEN** the handler does not invoke `ffmpeg` or create a new `VideoJob`, deletes the source object it just stored for this request, and returns `ProcessingResult{Success: false, Message: <English: still being processed, try again shortly>}`

#### Scenario: Duplicate's own object never affects the original request's artifacts

- **GIVEN** a duplicate request has stored its own source object under its own `uploadID`-prefixed key before discovering the duplicate
- **WHEN** the handler deletes that redundant object
- **THEN** the delete targets only this request's own source key, and the original request's stored result object remains readable — no key derived from another request's `uploadID` is ever deleted
