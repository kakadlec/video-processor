## ADDED Requirements

### Requirement: Idempotency Key Is Derived From Per-User Content Hash

`handleVideoUpload` SHALL derive an idempotency key by hashing the uploaded video's full content with SHA-256 while it is streamed to `uploads/`, and SHALL scope that hash per authenticated user (`idempotency:{userID}:{hash}`), not globally.

#### Scenario: Same user, same content produces the same key

- **GIVEN** two `POST /upload` requests from the same authenticated user, each carrying byte-identical video content
- **WHEN** each request's idempotency key is derived
- **THEN** both requests derive the same Redis key

#### Scenario: Different users, same content produce different keys

- **GIVEN** two `POST /upload` requests carrying byte-identical video content but from two different authenticated users
- **WHEN** each request's idempotency key is derived
- **THEN** the two requests derive different Redis keys, and neither is treated as a duplicate of the other

#### Scenario: Hashing adds no extra read of the upload

- **GIVEN** a `POST /upload` request being saved to `uploads/`
- **WHEN** the handler computes the idempotency hash
- **THEN** it does so via the same read pass already used to write the file to disk, not a second read of the uploaded content

### Requirement: Concurrent Identical Requests Are Serialized Via Atomic Reservation

`handleVideoUpload` SHALL atomically reserve its idempotency key in Redis (a conditional set that fails if the key already exists) before calling `CreateVideoJob`, using a short-lived `"processing"` sentinel value. A second request that fails this reservation because the key already holds `"processing"` SHALL receive `409 Conflict` without creating a `VideoJob` or invoking `ffmpeg`.

#### Scenario: First of two concurrent identical requests proceeds

- **GIVEN** two requests from the same user with identical content arrive concurrently, and neither has an existing idempotency key
- **WHEN** both attempt to reserve the idempotency key at nearly the same time
- **THEN** exactly one reservation succeeds and that request proceeds to `CreateVideoJob`

#### Scenario: Second of two concurrent identical requests is rejected

- **GIVEN** the scenario above, where one request has already reserved the key with the `"processing"` sentinel
- **WHEN** the second request attempts to reserve the same key
- **THEN** the reservation fails and the handler returns `409 Conflict` without creating a `VideoJob` or invoking `ffmpeg`

### Requirement: Reservation Is Finalized To The Real VideoJobID After Creation Succeeds

Once `CreateVideoJob` succeeds for a request that holds the `"processing"` reservation, `handleVideoUpload` SHALL overwrite the idempotency key's value with the created `VideoJobID` and extend its TTL to the full idempotency window (24 hours).

#### Scenario: Key is finalized after job creation

- **GIVEN** a request has reserved its idempotency key with the `"processing"` sentinel and `CreateVideoJob` has just succeeded
- **WHEN** the handler finalizes the key
- **THEN** the key's value becomes the created `VideoJobID` and its TTL becomes the full 24-hour window

### Requirement: A Finalized Duplicate Returns The Existing Job Without Reprocessing

A `POST /upload` request whose idempotency key already holds a `VideoJobID` (not the `"processing"` sentinel) SHALL NOT create a new `VideoJob` or invoke `ffmpeg`. It SHALL delete the redundant file this request itself just saved and return the existing job's current status.

#### Scenario: Duplicate after the original completed

- **GIVEN** a prior request's idempotency key now holds a `VideoJobID` for a job that has since reached `completed`
- **WHEN** a new request with the same user and identical content arrives
- **THEN** the handler does not invoke `ffmpeg` or create a new `VideoJob`, deletes the file it just saved for this request, and returns the existing job's `completed` status

#### Scenario: Duplicate while the original is still processing

- **GIVEN** a prior request's idempotency key now holds a `VideoJobID` for a job still in `processing`
- **WHEN** a new request with the same user and identical content arrives
- **THEN** the handler does not invoke `ffmpeg` or create a new `VideoJob`, deletes the file it just saved for this request, and returns the existing job's current (`processing`) status

#### Scenario: Duplicate's own file never affects the original request's artifacts

- **GIVEN** a duplicate request has saved its own file under its own `uploadID`-prefixed path before discovering the duplicate
- **WHEN** the handler deletes that redundant file
- **THEN** the original request's `uploads/`/`outputs/` artifacts (saved under a different `uploadID`) are left untouched

### Requirement: A Failed Job Clears Its Idempotency Key Immediately

When a `VideoJob` associated with an idempotency key transitions to `failed`, `handleVideoUpload` SHALL delete that idempotency key immediately rather than leaving it to expire naturally, so a subsequent identical-content request is treated as a fresh attempt.

#### Scenario: Retry after failure is not blocked

- **GIVEN** a `VideoJob` created for a given idempotency key has transitioned to `failed`
- **WHEN** a new request with the same user and identical content arrives after that
- **THEN** the handler finds no idempotency key, proceeds as a fresh request, and can create a new `VideoJob`

### Requirement: Idempotency Window Is A Fixed 24 Hours

A finalized idempotency key (holding a real `VideoJobID`) SHALL expire after a fixed 24-hour TTL, not a configurable value.

#### Scenario: Key expires after the window

- **GIVEN** a finalized idempotency key that has existed for longer than 24 hours since it was last written
- **WHEN** a new request with the same user and identical content arrives
- **THEN** the key is no longer present, and the handler treats the request as a fresh submission
