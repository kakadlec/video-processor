# upload-idempotency Specification

## Purpose

Define the Redis-backed idempotency-key mechanism for `POST /upload`: key derivation from per-user content hash, atomic reservation via ownership token, finalization/clearing semantics, TTL/expiry behavior, and the duplicate-request response contract. This is the first Phase 4 feature (of idempotency keys, rate limiting, status cache) to consume `internal/platform/redis` (`redis-infrastructure`), implementing the "Idempotency key prevents duplicate job creation" behavior `ddd-architecture`'s "Redis Responsibilities Are Additive" requirement already documents at the target-state level.

## Requirements

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

### Requirement: Concurrent Identical Requests Are Serialized Via Token-Owned Atomic Reservation

`handleVideoUpload` SHALL atomically reserve its idempotency key in Redis (a conditional set that fails if the key already exists) before calling `CreateVideoJob`, storing a short-lived sentinel value that embeds a unique ownership token generated for that reservation. A second request that fails this reservation SHALL NOT immediately return an error; it SHALL instead look up the key's current value in a short bounded retry loop, and only return `409 Conflict` (without creating a `VideoJob` or invoking `ffmpeg`) if that bound is exceeded without the key resolving to a real `VideoJobID`.

#### Scenario: First of two concurrent identical requests proceeds

- **GIVEN** two requests from the same user with identical content arrive concurrently, and neither has an existing idempotency key
- **WHEN** both attempt to reserve the idempotency key at nearly the same time
- **THEN** exactly one reservation succeeds, with its own unique ownership token, and that request proceeds to `CreateVideoJob`

#### Scenario: Second of two concurrent identical requests resolves once the first finalizes

- **GIVEN** the scenario above, where one request has already reserved the key with its own ownership token
- **WHEN** the second request's reservation attempt fails and the first request's reservation finalizes to a real `VideoJobID` before the second request's bounded retry window elapses
- **THEN** the second request returns that job's status (translated per the requirement below) without creating a `VideoJob` or invoking `ffmpeg`, and never receives `409`

#### Scenario: A reservation that never resolves times out as 409

- **GIVEN** a request has reserved the key but never finalizes or clears it (e.g. it crashed) before a second, near-simultaneous request's bounded retry window elapses
- **WHEN** the second request's bounded retry window elapses without the key resolving to a real `VideoJobID`
- **THEN** the second request returns `409 Conflict` without creating a `VideoJob` or invoking `ffmpeg`

### Requirement: Reservation Is Finalized To The Real VideoJobID Only By Its Owning Token

Once `CreateVideoJob` succeeds for a request that holds a reservation, `handleVideoUpload` SHALL overwrite the idempotency key's value with a finalized value that embeds both the created `VideoJobID` and the request's own ownership token, and extend its TTL to the full idempotency window (24 hours), but only if the key still holds that same request's reservation under that token — a stale request whose token no longer matches (because its reservation already expired and was reclaimed) SHALL NOT overwrite a newer reservation or finalized value. Embedding the token in the finalized value (not just the reservation) is what lets a later `Clear` call (see below) verify ownership after finalization too, not only while a reservation is still in flight.

#### Scenario: Key is finalized after job creation

- **GIVEN** a request holds a reservation under its own ownership token and `CreateVideoJob` has just succeeded
- **WHEN** the handler finalizes the key
- **THEN** the key's value becomes a finalized value embedding both the request's ownership token and the created `VideoJobID`, its TTL becomes the full 24-hour window, and `Lookup` against this key returns that `VideoJobID`

#### Scenario: A stale request cannot finalize over a newer reservation

- **GIVEN** a request's reservation has expired and a second, unrelated request has since reserved the same key under a different ownership token
- **WHEN** the first (stale) request belatedly attempts to finalize using its own, now-superseded token
- **THEN** the finalize attempt is rejected as a no-op, and the second request's reservation is left untouched

### Requirement: Job-Creation Failure Clears The Reservation; Finalize Failure Is Non-Fatal

If `CreateVideoJob` fails for a request holding a reservation, `handleVideoUpload` SHALL clear that reservation (using its own ownership token) so a subsequent request is not forced to wait out the sentinel's TTL. If `CreateVideoJob` succeeds but the subsequent finalize call itself fails, `handleVideoUpload` SHALL log the failure and proceed with processing the created job normally, rather than failing the request — the created `VideoJob` in PostgreSQL remains authoritative and valid even if this attempt is never indexed for future idempotency lookups.

#### Scenario: CreateVideoJob failure clears the reservation

- **GIVEN** a request holds a reservation and `CreateVideoJob` returns an error
- **WHEN** the handler handles that error
- **THEN** it clears the reservation using its own ownership token before returning the error response

#### Scenario: Finalize failure does not fail the request

- **GIVEN** `CreateVideoJob` has succeeded but the subsequent call to finalize the idempotency key fails
- **WHEN** the handler handles that failure
- **THEN** it logs the failure and proceeds to `ProcessVideoJob` for the newly created job as if finalization had succeeded

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
- **THEN** the original request's `uploads/`/`outputs/` artifacts (saved under a different `uploadID`) are left untouched

### Requirement: A Failed Job Clears Its Idempotency Key Immediately

When a `VideoJob` associated with an idempotency key transitions to `failed`, `handleVideoUpload` SHALL delete that idempotency key immediately (using its own ownership token, so it cannot clear a key already reclaimed by a newer request) rather than leaving it to expire naturally, so a subsequent identical-content request is treated as a fresh attempt.

#### Scenario: Retry after failure is not blocked

- **GIVEN** a `VideoJob` created for a given idempotency key has transitioned to `failed`, and the key still holds that same request's own reservation/finalized value
- **WHEN** a new request with the same user and identical content arrives after that
- **THEN** the handler finds no idempotency key, proceeds as a fresh request, and can create a new `VideoJob`

### Requirement: Idempotency Window Is A Fixed 24 Hours

A finalized idempotency key (holding a real `VideoJobID`) SHALL expire after a fixed 24-hour TTL, not a configurable value.

#### Scenario: Key expires after the window

- **GIVEN** a finalized idempotency key that has existed for longer than 24 hours since it was last written
- **WHEN** a new request with the same user and identical content arrives
- **THEN** the key is no longer present, and the handler treats the request as a fresh submission
