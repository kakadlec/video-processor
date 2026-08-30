# upload-idempotency Specification

## Purpose

Define the Redis-backed idempotency-key mechanism for `POST /upload`: key derivation from per-user content hash, atomic reservation via ownership token, finalization/clearing semantics, TTL/expiry behavior, and the duplicate-request response contract. This is the first Phase 4 feature (of idempotency keys, rate limiting, status cache) to consume `internal/platform/redis` (`redis-infrastructure`), implementing the "Idempotency key prevents duplicate job creation" behavior `ddd-architecture`'s "Redis Responsibilities Are Additive" requirement already documents at the target-state level.

## Requirements
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

### Requirement: Concurrent Identical Requests Are Serialized Via Token-Owned Atomic Reservation

`handleVideoUpload` SHALL atomically reserve its idempotency key in Redis (a conditional set that fails if the key already exists) before calling `CreateVideoJob`, storing a short-lived sentinel value that embeds a unique ownership token generated for that reservation. A second request that fails this reservation SHALL NOT immediately return an error; it SHALL instead look up the key's current value in a short bounded retry loop, and only return `409 Conflict` (without creating a `VideoJob` or invoking `ffmpeg`) if that bound is exceeded without the key resolving to a real `VideoJobID`.

If the reservation attempt itself fails with a store-level error (e.g. Redis is unreachable or times out) rather than resolving to either a successful reservation or a genuine conflict, `handleVideoUpload` SHALL NOT treat this as a conflict or return an error to the caller. It SHALL log the error and proceed directly to `CreateVideoJob` as if no idempotency protection were available for this request. A request that proceeds this way holds no valid reservation token, and SHALL NOT attempt to finalize or clear an idempotency key at any later point in its own handling.

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

#### Scenario: A Reserve error does not block the upload

- **GIVEN** the idempotency store's `Reserve` call returns a store-level error (e.g. Redis is unreachable) rather than a successful reservation or a genuine conflict
- **WHEN** `handleVideoUpload` handles that error
- **THEN** it logs the error and proceeds to `CreateVideoJob`/`ProcessVideoJob` for this upload, returning the same success/failure response an upload with no idempotency layer at all would return, rather than an error referencing idempotency

#### Scenario: A request that proceeded without a reservation does not attempt to finalize or clear one

- **GIVEN** a request proceeded through `CreateVideoJob` after a `Reserve` error, holding no valid reservation token
- **WHEN** that request later reaches a point in `handleVideoUpload` that would otherwise call `Finalize` or `Clear`
- **THEN** it skips that call entirely, without attempting it against an empty or invalid token

### Requirement: Reservation Is Finalized To The Real VideoJobID Only By Its Owning Token

Once `CreateVideoJob` **and** `EnqueueVideoJob` have both succeeded for a request that holds a reservation, `handleVideoUpload` SHALL overwrite the idempotency key's value with a finalized value that embeds both the created `VideoJobID` and the request's own ownership token, and extend its TTL to the full idempotency window (24 hours), but only if the key still holds that same request's reservation under that token — a stale request whose token no longer matches (because its reservation already expired and was reclaimed) SHALL NOT overwrite a newer reservation or finalized value. Embedding the token in the finalized value (not just the reservation) is what lets a later `Clear` call (see below) verify ownership after finalization too, not only while a reservation is still in flight.

The enqueue now sits between creation and finalization, and finalizing before it would be wrong in a way that stays invisible until it fires: a finalized key advertises its `VideoJobID` to every duplicate for 24 hours, so finalizing a job that then failed to reach `queued` would deduplicate later uploads of the same bytes onto a job stuck in `pending` that nothing will ever process. The reservation is therefore finalized only once the job is in a state something can act on.

#### Scenario: Key is finalized after the job is created and queued

- **GIVEN** a request holds a reservation under its own ownership token and both `CreateVideoJob` and `EnqueueVideoJob` have just succeeded
- **WHEN** the handler finalizes the key
- **THEN** the key's value becomes a finalized value embedding both the request's ownership token and the created `VideoJobID`, its TTL becomes the full 24-hour window, and `Lookup` against this key returns that `VideoJobID`

#### Scenario: A stale request cannot finalize over a newer reservation

- **GIVEN** a request's reservation has expired and a second, unrelated request has since reserved the same key under a different ownership token
- **WHEN** the first (stale) request belatedly attempts to finalize using its own, now-superseded token
- **THEN** the finalize attempt is rejected as a no-op, and the second request's reservation is left untouched

### Requirement: A Failure Before Finalize Clears The Reservation; Finalize Failure Is Non-Fatal

If `CreateVideoJob` **or** `EnqueueVideoJob` fails for a request holding a reservation, `handleVideoUpload` SHALL clear that reservation (using its own ownership token) so a subsequent request is not forced to wait out the sentinel's TTL. If both succeed but the subsequent finalize call itself fails, `handleVideoUpload` SHALL log the failure and proceed with processing the created job normally, rather than failing the request — the created `VideoJob` in PostgreSQL remains authoritative and valid even if this attempt is never indexed for future idempotency lookups.

The enqueue branch is not a special case bolted on; it is the rule the creation branch already followed, applied to the second step that can now fail before the job is dispatchable. Both leave the request unable to deliver a result, and a reservation held after either one would deduplicate a retry of the same bytes onto nothing for the full sentinel TTL. The job row `CreateVideoJob` already committed is left behind in `pending` in the enqueue case, deliberately: it is inert, it is the same state `POST /api/video-jobs` produces, and deleting it would need a compensating write on a path that has just demonstrated the database is not cooperating.

#### Scenario: CreateVideoJob failure clears the reservation

- **GIVEN** a request holds a reservation and `CreateVideoJob` returns an error
- **WHEN** the handler handles that error
- **THEN** it clears the reservation using its own ownership token before returning the error response

#### Scenario: EnqueueVideoJob failure clears the reservation and skips processing

- **GIVEN** a request holds a reservation, `CreateVideoJob` succeeded, and `EnqueueVideoJob` returns an error
- **WHEN** the handler handles that error
- **THEN** it clears the reservation using its own ownership token, never invokes `ProcessVideoJob`, and returns an error response — and the source object is still deleted by the handler's existing `defer`, so an immediate retry of the same content is neither deduplicated nor blocked

#### Scenario: Finalize failure does not fail the request

- **GIVEN** `CreateVideoJob` and `EnqueueVideoJob` have both succeeded but the subsequent call to finalize the idempotency key fails
- **WHEN** the handler handles that failure
- **THEN** it logs the failure and proceeds to `ProcessVideoJob` for the newly created job as if finalization had succeeded

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
