## MODIFIED Requirements

### Requirement: Cache Reflects The Latest State Transition Write

`CachedVideoJobRepository`'s `Update` **and** `Enqueue` SHALL each write to PostgreSQL first, and only once that write succeeds, write the job's new serialized state to its cache entry (write-through), overwriting rather than merely deleting any prior entry. This SHALL apply to every state transition: `StartProcessing`, `Complete`, and `Fail` reach it through `Update`, and `Enqueue` through the dedicated repository method that commits the transition together with its outbox row (see `videojob-persistence`). A concurrent cache-miss repopulation (the "PostgreSQL Is Authoritative On Cache Miss" requirement below) SHALL NOT be able to overwrite a write-through entry with an older value it read before the transition committed.

`Enqueue` SHALL NOT be passed through to the decorated repository uncached. It is the one transition whose staleness is observable against a second system: a job left `pending` in the cache while `queued` in PostgreSQL would make `GET /api/video-jobs/:id` contradict the very row the outbox relay is about to publish.

The cached record SHALL mirror the persisted column set exactly, source key included. The entry serves the `FindByID` that every transition use case makes before it writes, so a field missing from the record is silently dropped on every cache hit — and a dropped source key yields either a rejected `Enqueue` or a `video_job.queued` message naming an object no consumer can fetch.

#### Scenario: A concurrent stale read cannot overwrite a newer write-through value

- **GIVEN** a `FindByID` call misses the cache and reads a job's pre-transition state from PostgreSQL
- **WHEN** a concurrent `Update` commits a newer state and write-through-updates the cache before that first call's own cache repopulation runs
- **THEN** the first call's repopulation SHALL NOT overwrite the newer cached entry with its own stale value — a subsequent read still observes the newer, write-through state

#### Scenario: A poll immediately after a transition observes the new state

- **GIVEN** a `VideoJob` whose status was just changed by one of the four transition use cases
- **WHEN** a `GetJobStatus` call is made immediately afterward
- **THEN** it observes the new status via a cache hit reflecting the write, not a stale prior value

#### Scenario: Enqueue writes through like Update

- **GIVEN** a `VideoJob` cached in `pending` status
- **WHEN** `CachedVideoJobRepository.Enqueue` succeeds
- **THEN** a subsequent `FindByID` served from cache returns `queued`, not `pending`

#### Scenario: A cache hit round-trips the source key

- **GIVEN** a `VideoJob` with a non-empty source key whose cache entry was written by a miss-repopulation or a write-through
- **WHEN** `FindByID` is served from that entry
- **THEN** the reconstructed job carries the same source key, so a transition applied to it can still be enqueued

#### Scenario: PostgreSQL write failure prevents any cache write

- **GIVEN** the underlying PostgreSQL `Update` call fails
- **WHEN** `CachedVideoJobRepository.Update` is called
- **THEN** the cache entry is left unchanged and the error is returned, exactly as if no cache existed

#### Scenario: A cache write-through failure does not fail the transition

- **GIVEN** the PostgreSQL `Update` call already succeeded
- **WHEN** the subsequent Redis write (or its fallback delete) fails
- **THEN** `Update` still returns success — the error is logged, not surfaced, since PostgreSQL is the authority and its write already committed
