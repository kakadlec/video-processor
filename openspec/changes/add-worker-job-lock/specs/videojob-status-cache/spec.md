## MODIFIED Requirements

### Requirement: Repeated Single-Job Status Reads Are Served From Cache

`internal/video/infrastructure/cache.CachedVideoJobRepository` SHALL implement `domain.VideoJobRepository`'s `FindByID` as a cache-aside read against Redis, keyed per job (`"videojob:status:" + jobID.String()`). A cache hit SHALL be returned without querying PostgreSQL. This SHALL apply to `GetJobStatus` (backing `GET /api/video-jobs/:id`) and to the internal `FindByID` call `EnqueueVideoJob` and `StartProcessing` make before writing.

**`CompleteJob` and `FailJob` SHALL be the exception: they SHALL read through the undecorated repository and write through the cached one.** Their read decides whether a fence applies, and a stale entry decides it wrongly in a way no later write can correct — a claim whose write-through and fallback delete both failed leaves a `queued` record at the holder's own epoch, and the aggregate refuses the terminal transition on it before any statement can run, so the rightful holder can never commit its result. The cache exists to absorb repeated polling reads, and these are once-per-job writes, so the exception costs nothing it was built to provide. This is the same judgement `videojob-result-storage` records for `GET /download/:filename`'s entitlement lookup and `videojob-lease-recovery` records for the sweeper's scan: a decision about who owns a job does not read a cache.

#### Scenario: Repeated status poll for an unchanged job is served from cache

- **GIVEN** a `VideoJob` whose current state was already cached by a prior lookup
- **WHEN** `GetJobStatus` looks it up again before any state transition occurs
- **THEN** the result is served from the Redis cache entry, with no PostgreSQL query

#### Scenario: A deserialized cache hit reconstructs a fully valid aggregate

- **GIVEN** a cache hit whose stored fields are read back
- **WHEN** the cached value is deserialized into a `*domain.VideoJob`
- **THEN** every field is re-validated through its domain constructor (matching `postgres.Repository`'s own reconstruction discipline), so a hit can never produce an aggregate that bypasses domain invariants

#### Scenario: A terminal write is not decided from a cache entry

- **GIVEN** a job whose cache entry says `queued` at the holder's own epoch, because its claim's write-through and fallback delete both failed
- **WHEN** that holder calls `CompleteJob`
- **THEN** the job is loaded from PostgreSQL, found `processing`, and completed — and the cache entry is corrected by the write-through

### Requirement: Cache Reflects The Latest State Transition Write

`CachedVideoJobRepository`'s `Update`, `Enqueue`, **`ClaimForProcessing`, and the requeue method** SHALL each write to PostgreSQL first, and only once that write succeeds, write the job's new serialized state to its cache entry (write-through), overwriting rather than merely deleting any prior entry. This SHALL apply to every state transition: `Complete` and `Fail` reach it through `Update`, `Enqueue` through the dedicated method that commits the transition together with its outbox row, `StartProcessing` through the conditional claim, and an abandoned job's return to the queue through the requeue method that commits its own dispatch event (see `videojob-persistence`). A concurrent cache-miss repopulation (the "PostgreSQL Is Authoritative On Cache Miss" requirement below) SHALL NOT be able to overwrite a write-through entry with an older value it read before the transition committed.

**`ClaimForProcessing` and the requeue method SHALL write through only when the underlying statement affected a row.** A lost claim changed nothing in PostgreSQL, so writing the caller's in-memory `processing` job to the cache would publish a state the authoritative store does not hold — and would do it on behalf of the consumer that *lost*, overwriting the entry the winner just wrote. A requeue that lost its race to another sweeper is the same shape. A decorator that treated "no error" as "write through" would turn a harmless duplicate dispatch into a cache that reports a job as claimed by the wrong process.

**`Update` SHALL NOT write through when the fence refused the write.** A fenced `Update` reports a sentinel rather than success, and the caller holding a superseded epoch has an in-memory job describing an outcome the row does not carry; writing it to the cache would publish the loser's `completed` or `failed` over the holder's state and make `GET /api/video-jobs/:id` contradict PostgreSQL until the entry expired. This is the same rule as the lost claim's, at the other end of the job.

Neither `Enqueue`, `ClaimForProcessing`, nor the requeue method SHALL be passed through to the decorated repository uncached. Their staleness is observable against a second system: a job left `pending` in the cache while `queued` in PostgreSQL would make `GET /api/video-jobs/:id` contradict the very row the outbox relay is about to publish; a job left `queued` in the cache while `processing` in PostgreSQL would make a polling client believe no worker had picked it up; and a job left `processing` in the cache after being requeued would hide a recovery that has already happened.

The cached record SHALL mirror the persisted column set exactly, source key, content hash, **and fence epoch** included. The entry serves the `FindByID` that every transition use case makes before it writes, so a field missing from the record is silently dropped on every cache hit — a dropped source key yields either a rejected `Enqueue` or a queued message naming an object no consumer can fetch, a dropped content hash leaves a failed job's idempotency key unclearable, and a dropped epoch would make a cache-served job report abandonment count zero, which the sweeper's bound reads.

**Every write-through SHALL serialize the epoch the write actually committed at, not whatever epoch the caller's in-memory aggregate happens to carry.** The decorator writes the record from the aggregate it was handed, and that aggregate's epoch can be wrong in three distinct ways: a claim can report an epoch the caller never read, a requeue advances the stored epoch by one, and a `CompleteJob`/`FailJob` aggregate loaded from a previous release's cache record decodes at zero while its write commits at the caller-supplied epoch. In each case the authoritative value SHALL be used — the claim's reported epoch, the requeue's advanced epoch, and `Update`'s epoch argument respectively. The requeue SHALL make its advanced epoch available for this: the aggregate's own requeue transition advances the in-memory epoch, which the conditional statement's `lease_epoch = lease_epoch + 1` matches by construction.

**A won claim's write-through SHALL serialize the epoch the claim reported, not the epoch on the aggregate the caller passed in.** Those differ in exactly the case that matters: a consumer can load a `queued` job at one epoch, lose a race to a sweep that requeues it, and then win the claim on the re-dispatched job at the advanced epoch. Caching the pre-claim aggregate would publish the superseded value, and the next `FindByID` served from that entry would report an abandonment count lower than the row's — which the sweeper's bound reads.

A record written by a previous release carries no epoch at all. Such a record SHALL decode to epoch zero rather than failing the read, and SHALL NOT be treated as authoritative for any fenced write — the fence's own input is the epoch the claim reported, never a value read back from a job, cached or otherwise.

#### Scenario: A concurrent stale read cannot overwrite a newer write-through value

- **GIVEN** a `FindByID` call misses the cache and reads a job's pre-transition state from PostgreSQL
- **WHEN** a concurrent `Update` commits a newer state and write-through-updates the cache before that first call's own cache repopulation runs
- **THEN** the first call's repopulation SHALL NOT overwrite the newer cached entry with its own stale value — a subsequent read still observes the newer, write-through state

#### Scenario: A poll immediately after a transition observes the new state

- **GIVEN** a `VideoJob` whose status was just changed by one of the transition use cases
- **WHEN** a `GetJobStatus` call is made immediately afterward
- **THEN** it observes the new status via a cache hit reflecting the write, not a stale prior value

#### Scenario: A won claim writes through

- **GIVEN** a `VideoJob` cached in `queued` status
- **WHEN** `CachedVideoJobRepository.ClaimForProcessing` succeeds and reports a row affected
- **THEN** a subsequent `FindByID` served from cache returns `processing`

#### Scenario: A successful Update caches the epoch it wrote at

- **GIVEN** a `VideoJob` loaded from a record written by a previous release, so its in-memory epoch is zero, and a `CompleteJob` that commits at a non-zero held epoch
- **WHEN** the decorator writes through
- **THEN** the cached record carries the epoch the write committed at, not zero

#### Scenario: A won requeue caches the advanced epoch

- **GIVEN** a `processing` `VideoJob` at a known epoch
- **WHEN** the requeue succeeds and the decorator writes through
- **THEN** the cached record is `queued` at one epoch greater, matching the persisted row

#### Scenario: A won claim caches the epoch the claim reported

- **GIVEN** a `VideoJob` read as `queued` at one epoch, requeued by a concurrent sweep, and then successfully claimed at the advanced epoch
- **WHEN** the decorator writes through
- **THEN** the cached record carries the epoch the claim reported, matching the persisted row rather than the caller's pre-claim aggregate

#### Scenario: A lost claim writes nothing to the cache

- **GIVEN** a `VideoJob` whose cache entry reflects the state the winning consumer wrote
- **WHEN** `CachedVideoJobRepository.ClaimForProcessing` is called for it and reports no row affected
- **THEN** the cache entry is unchanged, and a subsequent `FindByID` still observes the winner's state rather than the losing caller's in-memory job

#### Scenario: A fenced update writes nothing to the cache

- **GIVEN** a `VideoJob` cached in the state its current holder wrote
- **WHEN** `CachedVideoJobRepository.Update` is called by a caller whose epoch has been superseded and the underlying write is refused by the fence
- **THEN** the sentinel is returned, the cache entry is unchanged, and a subsequent `FindByID` still observes the holder's state

#### Scenario: A won requeue writes through

- **GIVEN** a `VideoJob` cached in `processing` status
- **WHEN** `CachedVideoJobRepository`'s requeue succeeds and reports a row affected
- **THEN** a subsequent `FindByID` served from cache returns `queued` with the advanced epoch

#### Scenario: Enqueue writes through like Update

- **GIVEN** a `VideoJob` cached in `pending` status
- **WHEN** `CachedVideoJobRepository.Enqueue` succeeds
- **THEN** a subsequent `FindByID` served from cache returns `queued`, not `pending`

#### Scenario: A cache hit round-trips the source key, the content hash, and the epoch

- **GIVEN** a `VideoJob` with a non-empty source key and content hash and a non-zero epoch whose cache entry was written by a miss-repopulation or a write-through
- **WHEN** `FindByID` is served from that entry
- **THEN** the reconstructed job carries all three values

#### Scenario: A record written by a previous release still loads

- **GIVEN** a cache entry serialized before the epoch was part of the record
- **WHEN** `FindByID` is served from it
- **THEN** it returns the job with epoch zero rather than an error

#### Scenario: PostgreSQL write failure prevents any cache write

- **GIVEN** the underlying PostgreSQL `Update` call fails
- **WHEN** `CachedVideoJobRepository.Update` is called
- **THEN** the cache entry is left unchanged and the error is returned, exactly as if no cache existed

#### Scenario: A cache write-through failure does not fail the transition

- **GIVEN** the PostgreSQL `Update` call already succeeded
- **WHEN** the subsequent Redis write (or its fallback delete) fails
- **THEN** `Update` still returns success — the error is logged, not surfaced, since PostgreSQL is the authority and its write already committed
