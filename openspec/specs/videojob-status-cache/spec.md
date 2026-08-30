# videojob-status-cache Specification

## Purpose

Define the Redis-backed, cache-aside/write-through caching of `VideoJob` lookups by ID in `internal/video/infrastructure/cache.CachedVideoJobRepository`: when a read is served from cache versus PostgreSQL, how a state-transition write keeps the cache consistent without reopening a race with a concurrent miss-repopulation, and the fallback/self-heal behavior when the cache itself is unavailable or corrupted. This is the third and final Phase 4 feature (of idempotency keys, rate limiting, status cache) to consume `internal/platform/redis` (`redis-infrastructure`), implementing the "Status cache absorbs repeated polling reads" / "Cache invalidation is tied to state transition writes" / "PostgreSQL is authoritative on cache miss" scenarios `ddd-architecture`'s "Redis Responsibilities Are Additive" requirement already documents at the target-state level.

## Requirements

### Requirement: Repeated Single-Job Status Reads Are Served From Cache

`internal/video/infrastructure/cache.CachedVideoJobRepository` SHALL implement `domain.VideoJobRepository`'s `FindByID` as a cache-aside read against Redis, keyed per job (`"videojob:status:" + jobID.String()`). A cache hit SHALL be returned without querying PostgreSQL. This SHALL apply uniformly to every caller of `FindByID` — both `GetJobStatus` (backing `GET /api/video-jobs/:id`) and the internal `FindByID` call each state-transition use case (`EnqueueVideoJob`, `StartProcessing`, `CompleteJob`, `FailJob`) makes before writing.

#### Scenario: Repeated status poll for an unchanged job is served from cache

- **GIVEN** a `VideoJob` whose current state was already cached by a prior lookup
- **WHEN** `GetJobStatus` looks it up again before any state transition occurs
- **THEN** the result is served from the Redis cache entry, with no PostgreSQL query

#### Scenario: A deserialized cache hit reconstructs a fully valid aggregate

- **GIVEN** a cache hit whose stored fields are read back
- **WHEN** the cached value is deserialized into a `*domain.VideoJob`
- **THEN** every field is re-validated through its domain constructor (matching `postgres.Repository`'s own reconstruction discipline), so a hit can never produce an aggregate that bypasses domain invariants

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

### Requirement: PostgreSQL Is Authoritative On Cache Miss Or Cache Failure

If Redis has no entry for a job, or the cache read itself fails (a Redis error, or a value that fails to deserialize), `CachedVideoJobRepository.FindByID` SHALL fall back to the inner repository's PostgreSQL lookup and return its result — this fallback is unconditional and never itself fails the lookup. `FindByID` SHALL also attempt to repopulate the cache with that result via `SET NX`. That repopulation is **guaranteed** to succeed on a true miss (no entry) or a malformed-but-string value (self-healed first via a compare-and-delete, so `SET NX` finds the key genuinely absent) — but is only **best-effort** when the cache read failed with a Redis error, since `SET NX` cannot replace an existing key holding an incompatible type (e.g. a `WRONGTYPE` error from a key that was somehow written to as a non-string). In that narrow case, the PostgreSQL fallback still succeeds every time, but the cache entry for that job may remain unrepopulated until whatever produced the incompatible value is externally resolved.

#### Scenario: Cache miss falls back to PostgreSQL and repopulates the cache

- **GIVEN** no cache entry exists for a job (e.g. never looked up before, or its TTL expired)
- **WHEN** `FindByID` is called
- **THEN** the result comes from PostgreSQL, and the cache is populated with it for subsequent lookups

#### Scenario: A cache read error falls back to PostgreSQL rather than failing the lookup

- **GIVEN** the Redis client returns an error when `FindByID` checks the cache (e.g. a transient connection failure, or a `WRONGTYPE` error from a key holding an incompatible value)
- **WHEN** `FindByID` is called
- **THEN** the lookup still succeeds via PostgreSQL, and the error from the cache read is logged rather than returned to the caller — repopulating the cache with the fresh result is attempted but not guaranteed to succeed in this case (see the requirement text above)

#### Scenario: Cache entries carry a bounded, fixed lifetime

- **GIVEN** any cache entry written by either the cache-aside repopulation path or the write-through path
- **WHEN** the entry is written
- **THEN** it is set with a fixed TTL (not configurable via environment variable), bounding how long any entry survives even if it is never explicitly overwritten or deleted again
