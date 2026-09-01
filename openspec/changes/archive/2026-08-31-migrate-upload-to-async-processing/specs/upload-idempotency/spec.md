## MODIFIED Requirements

### Requirement: Idempotency Key Is Derived From Per-User Content Hash

`handleVideoUpload` SHALL derive an idempotency key by hashing the uploaded video's full content with SHA-256 while it is streamed to the configured MinIO bucket, and SHALL scope that hash per authenticated user (`idempotency:{userID}:{hash}`), not globally.

The hash is available only once the whole part has been read, which is after the object has been written. A duplicate is therefore always detected *after* its own object exists, and the redundant object is deleted rather than never created. Avoiding that would require buffering the upload to hash before storing — which is the local file this migration removes — so the extra object write and delete on the duplicate path are accepted, not engineered around.

**That hash SHALL additionally be persisted on the `VideoJob` the request creates** (see `videojob-persistence`), because the key is no longer only the submitting handler's concern: the process that observes the job's outcome is a different one, and the key has to be reconstructible from the job alone. Both components are then available to it — the owner from the job, the hash from the column — and the derivation SHALL be the same function in both places rather than a re-implementation, so the two cannot drift into computing different keys for the same job.

The reservation **token** SHALL NOT be persisted on the job or anywhere else outside the store. It is a possession capability held by the request that minted it, and it stays that way; `videojob-worker` defines what a later process proves ownership with instead.

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

#### Scenario: The key is reconstructible from the persisted job

- **GIVEN** a `VideoJob` created by a `POST /upload` request
- **WHEN** a process that never saw that request derives the key from the job's owner and persisted content hash
- **THEN** it derives exactly the key the handler reserved

#### Scenario: No ownership token is persisted

- **GIVEN** a `VideoJob` created by a `POST /upload` request that held a reservation
- **WHEN** its persisted representation is inspected
- **THEN** it carries no reservation token, in any column

### Requirement: A Finalized Duplicate Returns The Existing Job, Translated To The Upload Response Shape

A `POST /upload` request whose idempotency key already holds a `VideoJobID` (not an in-flight reservation) SHALL NOT create a new `VideoJob`, SHALL NOT enqueue anything, and SHALL NOT cause any extraction. It SHALL delete the redundant source object this request itself just stored, and SHALL return the existing job's identifier and status URL in **the same response shape a non-duplicate `POST /upload` returns** (see `videojob-execution`), so a client cannot tell the two apart and needs no branch for the duplicate case.

The duplicate SHALL NOT report the existing job's *outcome* in its own response, because it no longer has one to report: an accepted submission is answered with a reference, and the reference is what the duplicate returns. A duplicate of a `completed` job, of a `processing` job, and of a job still `queued` are therefore answered identically, and the client learns the difference on its first poll. The three-way branch the synchronous response required SHALL NOT be preserved.

The duplicate's cleanup SHALL be scoped to its own `uploadID`-derived key. The original request's *source* object is not asserted to survive — source objects are transient by contract (see `videojob-source-storage`), and the process that owns the original's may already have deleted it. What a duplicate SHALL never do is delete a key it did not create.

#### Scenario: Duplicate after the original completed

- **GIVEN** a prior request's idempotency key holds a `VideoJobID` for a job that has since reached `completed`
- **WHEN** a new request with the same user and identical content arrives
- **THEN** no extraction is started and no new `VideoJob` is created, the source object this request stored is deleted, and the response is the ordinary acknowledgement naming the existing job — whose status URL immediately reports `completed`

#### Scenario: Duplicate while the original is still being processed

- **GIVEN** a prior request's idempotency key holds a `VideoJobID` for a job in `queued` or `processing`
- **WHEN** a new request with the same user and identical content arrives
- **THEN** the response is byte-shaped identically to the completed-duplicate case above, naming the same existing job, and no second dispatch is published for it

#### Scenario: Duplicate after the original failed, before its key is cleared

- **GIVEN** a prior request's idempotency key still resolves to a `VideoJobID` for a job that reached `failed`, in the narrow window before that job's key is cleared
- **WHEN** a new request with the same user and identical content arrives in that window
- **THEN** it is answered with a reference to the failed job rather than creating a new one, and a resubmission after the clear is treated as fresh

#### Scenario: Duplicate's own object never affects the original request's artifacts

- **GIVEN** a duplicate request has stored its own source object under its own `uploadID`-prefixed key before discovering the duplicate
- **WHEN** the handler deletes that redundant object
- **THEN** the delete targets only this request's own source key, and neither the original job's source object nor its stored result is affected — no key derived from another request's `uploadID` is ever deleted

### Requirement: A Failed Job Clears Its Idempotency Key Immediately

When a `VideoJob` associated with an idempotency key transitions to `failed`, the process that performed that transition SHALL delete the key immediately rather than leaving it to expire naturally, so a subsequent identical-content request is treated as a fresh attempt.

**That process is `cmd/worker`, not `handleVideoUpload`.** The handler returns as soon as the job is enqueued and never learns the outcome, so naming it here would leave the obligation with a component that cannot discharge it — and failed content would stay deduplicated for the whole fixed window, which is precisely the retry-blocking this requirement exists to prevent. `videojob-worker` carries the worker-side obligation; this requirement is the contract it satisfies.

The deletion SHALL be conditional on the key still referring to that job, so a key already reclaimed by a newer request is never removed. The reservation token performed that check while the handler owned it; the persisted job identifier performs it now, and the two are equivalent in strength because the finalized value names the job and a reclaimed key names neither the old token nor the old job.

A failure to delete SHALL be logged and SHALL NOT fail the job. The key still expires on its own window, so what is lost is promptness, not correctness.

#### Scenario: Retry after failure is not blocked

- **GIVEN** a `VideoJob` created for a given idempotency key has transitioned to `failed`, and the key still holds that job's finalized value
- **WHEN** a new request with the same user and identical content arrives after the clear
- **THEN** the handler finds no idempotency key, proceeds as a fresh request, and can create a new `VideoJob`

#### Scenario: The clear happens without the submitting request

- **GIVEN** the `POST /upload` request that created the job has already returned
- **WHEN** the job later transitions to `failed`
- **THEN** the key is cleared anyway, without that request's participation and without its reservation token

#### Scenario: A key reclaimed by a newer request survives the clear

- **GIVEN** a failed job whose idempotency key has since been reclaimed by a newer submission of identical content
- **WHEN** the clear for the failed job runs
- **THEN** the key still holds the newer submission's value, and that submission is not treated as fresh

## ADDED Requirements

### Requirement: The Idempotency Store Clears a Key By The Job It Names

`IdempotencyStore` SHALL expose an operation that deletes a key only if the key's current value names a given `VideoJobID`, and SHALL report whether it deleted anything. The comparison SHALL be atomic with the delete, so a key reclaimed between the read and the write is never removed.

This exists alongside the token-based clear rather than replacing it. A request that still holds its own reservation token SHALL keep using the token-based clear — it is the stronger check, since a token is unique to one request while a job identifier is merely unique to one job — and the job-based clear SHALL be used only by a process that legitimately never held the token.

The operation SHALL NOT accept a key whose value is an unfinalized reservation. A reservation names no job, so a job-based match against one is meaningless and SHALL report that nothing was deleted rather than removing another request's in-flight reservation.

#### Scenario: A key finalized to the given job is deleted

- **GIVEN** a key whose value is finalized to a specific `VideoJobID`
- **WHEN** the store is asked to clear it for that job
- **THEN** the key is removed and the operation reports that it deleted it

#### Scenario: A key finalized to a different job is left alone

- **GIVEN** a key whose value is finalized to some other `VideoJobID`
- **WHEN** the store is asked to clear it for a job it does not name
- **THEN** the key is unchanged and the operation reports that it deleted nothing

#### Scenario: An unfinalized reservation is never removed by a job-based clear

- **GIVEN** a key holding an in-flight reservation belonging to a newer request
- **WHEN** the store is asked to clear it for an older job
- **THEN** the reservation is intact and the operation reports that it deleted nothing

#### Scenario: An absent key is not an error

- **GIVEN** a key that has already expired or been cleared
- **WHEN** the store is asked to clear it for a job
- **THEN** it reports that it deleted nothing and returns no error
