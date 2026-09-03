## MODIFIED Requirements

### Requirement: Repeated Single-Job Status Reads Are Served From Cache

`internal/video/infrastructure/cache.CachedVideoJobRepository` SHALL implement `domain.VideoJobRepository`'s `FindByID` as a cache-aside read against Redis, keyed per job (`"videojob:status:" + jobID.String()`). A cache hit SHALL be returned without querying PostgreSQL. This SHALL apply to `GetJobStatus` (backing `GET /api/video-jobs/:id`) and to the internal `FindByID` call `EnqueueVideoJob` makes before writing.

**`StartProcessing`, `CompleteJob`, and `FailJob` SHALL be the exception: they SHALL read through the undecorated repository and write through the cached one.** The rule is that **a decision about who owns a job does not read a cache**. `EnqueueVideoJob` stays cached and that is consistent rather than an omission — a submitter transitioning a job it created moments earlier is not an ownership decision, and no requeue can reach a `pending` row.

Each of the three decides ownership, and a cache entry remains non-authoritative even though the ordered write-through below prevents an older successful transition from replacing a newer one. A Redis error can still leave the previous record standing when both the write and its fallback invalidation fail, and a record written by an older release may predate the fence epoch entirely. For a terminal write, a stale `queued` record makes the aggregate refuse the transition before the fenced statement can run; for a claim, a stale `processing` record can turn a recovered `queued` row into `ErrInvalidStatusTransition` and dead-letter its only new dispatch. Either outcome makes cache availability part of correctness unless the ownership read bypasses it.

The cache exists to absorb repeated polling reads, and all three of these are once-per-job writes, so the exception costs nothing the cache was built to provide. It also puts `StartProcessing`'s lost-claim discrimination on solid ground: the `job.Status()` it inspects after a refused transition is the authoritative row, so `processing` means a lost claim and `pending` means a genuine defect, with no cache caveat on either. This is the same judgement `videojob-result-storage` records for `GET /download/:filename`'s entitlement lookup and `videojob-lease-recovery` records for the sweeper's scan.

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

#### Scenario: A claim is decided against the row, not a stale cache entry

- **GIVEN** a job the sweeper requeued, whose cache entry still says `processing` at the pre-requeue epoch because requeue cache maintenance and fallback invalidation both failed
- **WHEN** the re-dispatched delivery calls `StartProcessing`
- **THEN** the job is loaded from PostgreSQL, found `queued`, and claimed — the recovery is not dead-lettered as an invalid transition

### Requirement: Cache Reflects The Latest State Transition Write

`CachedVideoJobRepository`'s `Update`, `Enqueue`, **`ClaimForProcessing`, and the requeue method** SHALL each write to PostgreSQL first, and only once that write succeeds, attempt to write the job's new serialized state to its cache entry (write-through). This SHALL apply to every state transition: `Complete` and `Fail` reach it through `Update`, `Enqueue` through the dedicated method that commits the transition together with its outbox row, `StartProcessing` through the conditional claim, and an abandoned job's return to the queue through the requeue method that commits its own dispatch event (see `videojob-persistence`). The cache write SHALL atomically reject a record older than the entry already present, and a concurrent cache-miss repopulation (the "PostgreSQL Is Authoritative On Cache Miss" requirement below) SHALL NOT be able to overwrite a write-through entry with an older value it read before the transition committed.

**`ClaimForProcessing` and the requeue method SHALL write through only when the underlying statement affected a row.** A lost claim changed nothing in PostgreSQL, so writing the caller's in-memory `processing` job to the cache would publish a state the authoritative store does not hold — and would do it on behalf of the consumer that *lost*, overwriting the entry the winner just wrote. A requeue that lost its race to another sweeper is the same shape. A decorator that treated "no error" as "write through" would turn a harmless duplicate dispatch into a cache that reports a job as claimed by the wrong process.

**`Update` SHALL NOT write through unless its own write was applied.** A fenced `Update` reports a sentinel rather than success, and a caller with a superseded epoch or a lost same-epoch terminal race has an in-memory job describing an outcome the row does not carry; writing it to the cache would publish the loser's `completed` or `failed` over the holder's state and make `GET /api/video-jobs/:id` contradict PostgreSQL until the entry expired. This is the same rule as the lost claim's, at the other end of the job.

**An inner write error whose commit outcome is ambiguous SHALL invalidate the cache entry**, because a database error does not prove the statement rolled back: the connection may have failed after commit but before the response. Leaving the prior `processing` entry in that case can outlive a terminal row until the TTL. `ErrJobFenced` is the exception and SHALL leave the entry unchanged — it is a decided zero-row outcome, so the cache belongs to the write that won. Invalidation and write-through failures remain best effort and SHALL NOT replace the PostgreSQL result.

Neither `Enqueue`, `ClaimForProcessing`, nor the requeue method SHALL be passed through to the decorated repository uncached. Their staleness is observable against a second system: a job left `pending` in the cache while `queued` in PostgreSQL would make `GET /api/video-jobs/:id` contradict the very row the outbox relay is about to publish; a job left `queued` in the cache while `processing` in PostgreSQL would make a polling client believe no worker had picked it up; and a job left `processing` in the cache after being requeued would hide a recovery that has already happened.

The cached record SHALL mirror the persisted column set exactly, source key, content hash, **and fence epoch** included. Cache-served callers such as `GetJobStatus` and `EnqueueVideoJob` must reconstruct a valid aggregate rather than silently lose fields; in particular, a dropped source key would reject enqueue or produce a dispatch naming no object. The epoch is retained for fidelity and forward compatibility, not for ownership: `StartProcessing`, `CompleteJob`, and `FailJob` bypass the cache, the fence uses the epoch returned by the claim, and the sweeper's bound comes from its authoritative scan. No consumer SHALL derive a fence or requeue decision from the cached value.

**Write-throughs SHALL be ordered atomically by fence epoch and by state progression within an epoch.** The requeue commits its outbox row before its cache write, so the relay can publish and a successor can claim and even complete while that older Redis call is delayed. A late `queued` write from the requeue SHALL NOT replace that successor's `processing` or terminal record. A greater epoch supersedes every lower one; at an equal epoch the order is `pending < queued < processing < terminal`. `completed` and `failed` share the terminal rank, and differing terminal outcomes SHALL NOT replace one another — PostgreSQL's `status = 'processing'` predicate chose one winner. An identical record MAY refresh its TTL. The compare and write SHALL be one Redis operation, not a read followed by `SET`.

If the cache cannot decode or otherwise order the existing entry, the write-through MAY replace a malformed entry; if the atomic operation itself fails, the decorator SHALL attempt to invalidate the key. A missing cache entry remains safe: the next read falls back to PostgreSQL.

**Every write-through SHALL serialize the epoch the database operation actually committed at, not assume the aggregate handed to the decorator already carries it.** A claim can return an epoch newer than the one its pre-claim aggregate loaded, and a requeue advances the stored epoch by one. The authoritative metadata SHALL therefore be used explicitly — the claim's returned epoch, the requeue aggregate's advanced epoch, and `Update`'s caller-supplied epoch. The requeue's aggregate transition advances its in-memory epoch, matching the conditional statement's `lease_epoch = lease_epoch + 1` by construction.

**A won claim's write-through SHALL serialize the epoch the claim reported, not the epoch on the aggregate the caller passed in.** Those differ in exactly the case that matters: a consumer can load a `queued` job at one epoch, lose a race to a sweep that requeues it, and then win the claim on the re-dispatched job at the advanced epoch. Caching the pre-claim aggregate would publish a superseded value, so the next `FindByID` served from that entry would hand its caller an aggregate whose epoch disagrees with the row it claims to describe.

A record written by a previous release carries no epoch at all. Such a record SHALL decode to epoch zero rather than failing the read, and SHALL NOT be treated as authoritative for any fenced write — the fence's own input is the epoch the claim reported, never a value read back from a job, cached or otherwise.

#### Scenario: A concurrent stale read cannot overwrite a newer write-through value

- **GIVEN** a `FindByID` call misses the cache and reads a job's pre-transition state from PostgreSQL
- **WHEN** a concurrent `Update` commits a newer state and write-through-updates the cache before that first call's own cache repopulation runs
- **THEN** the first call's repopulation SHALL NOT overwrite the newer cached entry with its own stale value — a subsequent read still observes the newer, write-through state

#### Scenario: A poll immediately after a transition observes the new state

- **GIVEN** a `VideoJob` whose status was just changed by one of the transition use cases
- **WHEN** a `GetJobStatus` call is made immediately afterward
- **THEN** it observes the new status via a cache hit reflecting the write, not a stale prior value

#### Scenario: A delayed requeue write cannot replace its successor's completion

- **GIVEN** a requeue that committed PostgreSQL but paused before cache maintenance, while its successor claimed and completed the job at the advanced epoch
- **WHEN** the delayed requeue resumes its cache write
- **THEN** the atomic ordering rejects its `queued` record and a subsequent cache hit still returns the successor's terminal state

#### Scenario: A won claim writes through

- **GIVEN** a `VideoJob` cached in `queued` status
- **WHEN** `CachedVideoJobRepository.ClaimForProcessing` succeeds and reports a row affected
- **THEN** a subsequent `FindByID` served from cache returns `processing`

#### Scenario: A successful Update caches the epoch it wrote at

- **GIVEN** a terminal `VideoJob` whose in-memory epoch is zero and a non-zero held epoch passed directly to `CachedVideoJobRepository.Update`
- **WHEN** the underlying update applies and the decorator writes through
- **THEN** the cached record carries the epoch argument the write committed at, not the aggregate's zero

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

#### Scenario: An ambiguous PostgreSQL write error invalidates the cache

- **GIVEN** the underlying PostgreSQL `Update` returns an error that is not `ErrJobFenced`, and the cache still contains the pre-write state
- **WHEN** `CachedVideoJobRepository.Update` handles that ambiguous result
- **THEN** it attempts to delete the cache entry and returns the database error, so the next read falls back to PostgreSQL whether the write committed or rolled back

#### Scenario: A fenced PostgreSQL write leaves the winner's cache entry intact

- **GIVEN** the underlying PostgreSQL `Update` returns `ErrJobFenced`
- **WHEN** `CachedVideoJobRepository.Update` handles that decided zero-row outcome
- **THEN** it returns the sentinel without modifying the cache entry

#### Scenario: A cache write-through failure does not fail the transition

- **GIVEN** the PostgreSQL `Update` call already succeeded
- **WHEN** the subsequent Redis write (or its fallback delete) fails
- **THEN** `Update` still returns success — the error is logged, not surfaced, since PostgreSQL is the authority and its write already committed
