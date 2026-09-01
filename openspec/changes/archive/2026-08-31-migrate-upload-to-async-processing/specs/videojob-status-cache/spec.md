## MODIFIED Requirements

### Requirement: Cache Reflects The Latest State Transition Write

`CachedVideoJobRepository`'s `Update`, `Enqueue`, **and `ClaimForProcessing`** SHALL each write to PostgreSQL first, and only once that write succeeds, write the job's new serialized state to its cache entry (write-through), overwriting rather than merely deleting any prior entry. This SHALL apply to every state transition: `Complete` and `Fail` reach it through `Update`, `Enqueue` through the dedicated method that commits the transition together with its outbox row, and `StartProcessing` through the conditional claim (see `videojob-persistence`). A concurrent cache-miss repopulation (the "PostgreSQL Is Authoritative On Cache Miss" requirement below) SHALL NOT be able to overwrite a write-through entry with an older value it read before the transition committed.

**`ClaimForProcessing` SHALL write through only when the underlying statement affected a row.** A lost claim changed nothing in PostgreSQL, so writing the caller's in-memory `processing` job to the cache would publish a state the authoritative store does not hold — and would do it on behalf of the consumer that *lost*, overwriting the entry the winner just wrote. A decorator that treated "no error" as "write through" would turn a harmless duplicate dispatch into a cache that reports a job as claimed by the wrong process.

Neither `Enqueue` nor `ClaimForProcessing` SHALL be passed through to the decorated repository uncached. Their staleness is observable against a second system: a job left `pending` in the cache while `queued` in PostgreSQL would make `GET /api/video-jobs/:id` contradict the very row the outbox relay is about to publish, and a job left `queued` in the cache while `processing` in PostgreSQL would make a polling client believe no worker had picked it up.

The cached record SHALL mirror the persisted column set exactly, source key and content hash included. The entry serves the `FindByID` that every transition use case makes before it writes, so a field missing from the record is silently dropped on every cache hit — a dropped source key yields either a rejected `Enqueue` or a `video_job.queued` message naming an object no consumer can fetch, and a dropped content hash leaves a failed job's idempotency key unclearable, deduplicating that content for the rest of its window.

#### Scenario: A concurrent stale read cannot overwrite a newer write-through value

- **GIVEN** a `FindByID` call misses the cache and reads a job's pre-transition state from PostgreSQL
- **WHEN** a concurrent `Update` commits a newer state and write-through-updates the cache before that first call's own cache repopulation runs
- **THEN** the first call's repopulation SHALL NOT overwrite the newer cached entry with its own stale value — a subsequent read still observes the newer, write-through state

#### Scenario: A poll immediately after a transition observes the new state

- **GIVEN** a `VideoJob` whose status was just changed by one of the four transition use cases
- **WHEN** a `GetJobStatus` call is made immediately afterward
- **THEN** it observes the new status via a cache hit reflecting the write, not a stale prior value

#### Scenario: A won claim writes through

- **GIVEN** a `VideoJob` cached in `queued` status
- **WHEN** `CachedVideoJobRepository.ClaimForProcessing` succeeds and reports a row affected
- **THEN** a subsequent `FindByID` served from cache returns `processing`

#### Scenario: A lost claim writes nothing to the cache

- **GIVEN** a `VideoJob` whose cache entry reflects the state the winning consumer wrote
- **WHEN** `CachedVideoJobRepository.ClaimForProcessing` is called for it and reports no row affected
- **THEN** the cache entry is unchanged, and a subsequent `FindByID` still observes the winner's state rather than the losing caller's in-memory job

#### Scenario: Enqueue writes through like Update

- **GIVEN** a `VideoJob` cached in `pending` status
- **WHEN** `CachedVideoJobRepository.Enqueue` succeeds
- **THEN** a subsequent `FindByID` served from cache returns `queued`, not `pending`

#### Scenario: A cache hit round-trips the source key and the content hash

- **GIVEN** a `VideoJob` with a non-empty source key and content hash whose cache entry was written by a miss-repopulation or a write-through
- **WHEN** `FindByID` is served from that entry
- **THEN** the reconstructed job carries both values, so a transition applied to it can still be enqueued and a failure can still clear its idempotency key

#### Scenario: PostgreSQL write failure prevents any cache write

- **GIVEN** the underlying PostgreSQL `Update` call fails
- **WHEN** `CachedVideoJobRepository.Update` is called
- **THEN** the cache entry is left unchanged and the error is returned, exactly as if no cache existed

#### Scenario: A cache write-through failure does not fail the transition

- **GIVEN** the PostgreSQL `Update` call already succeeded
- **WHEN** the subsequent Redis write (or its fallback delete) fails
- **THEN** `Update` still returns success — the error is logged, not surfaced, since PostgreSQL is the authority and its write already committed
