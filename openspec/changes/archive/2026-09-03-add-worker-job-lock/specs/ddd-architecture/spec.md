## MODIFIED Requirements

### Requirement: Valid State Machine Transitions Only

The `VideoJob` status SHALL only advance through the defined state machine. Undefined transitions SHALL be rejected as domain errors, and backwards transitions SHALL be rejected with **one named exception**: `processing → queued`, the edge by which a job abandoned by a dead worker is returned to the queue for another one (`videojob-lease-recovery`). No other backwards edge SHALL exist; `completed` and `failed` remain terminal, with no outgoing transitions at all.

The exception is deliberately narrow, and its narrowness is the requirement. It exists because a job whose worker died mid-extraction is otherwise unrecoverable — the claim predicate refuses a `processing` row and the broker's redelivery arrives too early to help — and it SHALL NOT be generalised into a retry facility, an operator-triggered re-run, or a way to undo a claim a live worker still holds. Its use is confined to a job whose lease has been observed as lapsed.

`Enqueue` SHALL additionally require a non-empty source key. That is a precondition on the aggregate rather than an edge in the state machine — a job with no stored source object cannot be processed, so queueing it would record a dispatch no worker could ever act on. `videojob-lifecycle` owns the invariant's full statement, including why it is deliberately absent from `RestoreVideoJob`. The requeue edge SHALL carry the same precondition, for the same reason.

#### Scenario: Job advances from pending to queued

- **GIVEN** a `VideoJob` in `pending` state with a non-empty source key
- **WHEN** `EnqueueVideoJob` is called
- **THEN** the job transitions to `queued`

#### Scenario: A pending job with no source key is not queued

- **GIVEN** a `VideoJob` in `pending` state whose source key is empty, as `POST /api/video-jobs` creates one
- **WHEN** `EnqueueVideoJob` is called
- **THEN** the domain layer rejects the command with an error and the job remains `pending`

#### Scenario: Job advances from queued to processing

- **GIVEN** a `VideoJob` in `queued` state
- **WHEN** the worker dequeues the job and calls `StartProcessing`
- **THEN** the job transitions to `processing`

#### Scenario: Job advances from processing to completed

- **GIVEN** a `VideoJob` in `processing` state
- **WHEN** the worker successfully extracts frames and calls `CompleteJob`
- **THEN** the job transitions to `completed` with `FrameCount` and result `StorageKey` populated

#### Scenario: Job advances from processing to failed

- **GIVEN** a `VideoJob` in `processing` state
- **WHEN** the worker encounters an unrecoverable error and calls `FailJob`
- **THEN** the job transitions to `failed` with a non-empty `ErrorReason`

#### Scenario: An abandoned job returns from processing to queued

- **GIVEN** a `VideoJob` in `processing` state whose worker's lease has lapsed
- **WHEN** the sweeper requeues it
- **THEN** the job transitions to `queued` and is dispatched again

#### Scenario: Backwards transition is rejected

- **GIVEN** a `VideoJob` in `completed` or `failed` state
- **WHEN** any transition command is applied
- **THEN** the domain layer rejects the command with an error; the job state is not mutated

#### Scenario: No backwards transition other than the requeue is permitted

- **GIVEN** a `VideoJob` in `queued` state
- **WHEN** a transition back to `pending` is applied
- **THEN** the domain layer rejects the command with an error; the job state is not mutated

### Requirement: Redis Responsibilities Are Additive, Not a Replacement for PostgreSQL or RabbitMQ

Redis SHALL be used as a mandatory performance and reliability layer with **four** defined responsibilities: idempotency keys, rate limiting, a status cache, and a worker job lease. It SHALL NOT replace PostgreSQL as the authoritative state store, and it SHALL NOT replace RabbitMQ as the durable job queue.

The fourth responsibility — deferred from Phase 4 because no worker existed to contend over job pickup — is now delivered, and what ships is narrower than "a distributed lock" and SHALL be described as it is. The lease is a **liveness** signal: it distinguishes a job someone is working on from one abandoned by a dead worker, and it is read only by the recovery sweeper. It SHALL NOT gate job pickup, which remains a conditional-`UPDATE` claim in PostgreSQL, and it SHALL NOT be the fence: the guard preventing a superseded worker from writing over its successor is a column in the same row as the state it guards (`videojob-lease-recovery`).

That split preserves the system's fail-open execution path without making takeover fail open. Lease acquisition and renewal errors do not stop a claimed job, but a lease-query error is "cannot tell" and authorizes no recovery. A lease consulted at pickup would instead make an outage mean either "never process" or "assume every lease lapsed", and the second permits two workers on one live job — so the lease is kept out of pickup and the fence is kept in PostgreSQL.

#### Scenario: Idempotency key prevents duplicate job creation

- **GIVEN** a client retries a `POST /upload` with the same content within the idempotency window
- **WHEN** the API processes the retry
- **THEN** Redis returns the existing `VideoJobID` and the handler returns the existing job without creating a duplicate or re-enqueuing

#### Scenario: Rate limiting rejects excess requests

- **GIVEN** a user exceeds the configured request rate
- **WHEN** their next request arrives
- **THEN** the rate-limiting middleware (backed by Redis) rejects it with HTTP 429 before it reaches the handler

#### Scenario: Status cache absorbs repeated polling reads

- **GIVEN** a client polls `GET /api/video-jobs/:id` repeatedly
- **WHEN** the job state has not changed since the last DB write
- **THEN** the response is served from the Redis status cache without a PostgreSQL query

#### Scenario: Cache invalidation is tied to state transition writes

- **GIVEN** a job transitions to a new state and that transition is written to PostgreSQL
- **WHEN** the write succeeds
- **THEN** the Redis status cache entry for that job is invalidated or updated atomically with the DB write (or immediately after, within the same request/transaction scope)

#### Scenario: PostgreSQL is authoritative on cache miss

- **GIVEN** the Redis status cache has no entry for a job
- **WHEN** a status request arrives
- **THEN** the application falls back to PostgreSQL, returns the correct current state, and may repopulate the cache

#### Scenario: The worker lease distinguishes a live job from an abandoned one

- **GIVEN** two `VideoJob`s in `processing` state, one held by a running worker that renews its lease and one whose worker died
- **WHEN** the recovery sweeper runs
- **THEN** only the abandoned job is returned to `queued`, and the live one is left alone

#### Scenario: Job pickup does not depend on Redis

- **GIVEN** Redis is unreachable
- **WHEN** two workers are dispatched the same `queued` job
- **THEN** exactly one claims it, because the claim is a conditional PostgreSQL update that consults no lease
