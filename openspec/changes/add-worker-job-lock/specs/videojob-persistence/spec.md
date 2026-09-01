## ADDED Requirements

### Requirement: The video_jobs Table Carries a Fence Epoch

The `video_jobs` table SHALL carry a `lease_epoch` column holding a non-negative integer, added as `NOT NULL DEFAULT 0` — an additive migration with no backfill, declared inline for a database created from scratch and applied through `ADD COLUMN IF NOT EXISTS` for one that already exists, exactly as `source_key` and `content_hash` are.

The default SHALL be the correct value for every pre-existing row rather than a placeholder: the epoch counts how many times a job has been returned to the queue after abandonment, and a row written before this column existed has been returned zero times. A `processing` row carrying the default is therefore an ordinary abandonment candidate, which is precisely the backlog this change is meant to recover.

`Create`, `FindByID`, `FindByUserID`, and `FindCompletedByUserID` SHALL round-trip the value, and `domain.RestoreVideoJob` SHALL accept it. It SHALL NOT be paired with any status at reconstitution: every status is reachable at every epoch.

Only the requeue path SHALL advance it. `Create`, `Enqueue`, `Update`, and `ClaimForProcessing` SHALL leave it as they found it, so the stored value reads unambiguously as the job's abandonment count and can be used as the bound `videojob-lease-recovery` requires.

#### Scenario: A pre-migration row loads at epoch zero

- **GIVEN** a `video_jobs` row written before the `lease_epoch` column existed, in any status
- **WHEN** `Repository.FindByID` is called for it
- **THEN** it returns the job with epoch zero rather than an error

#### Scenario: The epoch round-trips through every read path

- **GIVEN** a job whose epoch has been advanced by a requeue
- **WHEN** it is read back via `FindByID`, `FindByUserID`, and `FindCompletedByUserID`
- **THEN** every one of them reports the same epoch

#### Scenario: An ordinary transition does not advance the epoch

- **GIVEN** a job at a known epoch
- **WHEN** it is claimed, then completed
- **THEN** its stored epoch is the same value it started with

### Requirement: Requeue Persists the Abandonment Transition and Its Event Transactionally

`domain.VideoJobRepository` SHALL expose a requeue method, and `internal/video/infrastructure/postgres.Repository` SHALL implement it by updating the job's row to `queued`, advancing `lease_epoch` by one, and inserting a `video_job_outbox` row describing that dispatch, **in a single database transaction** — so an abandoned job and the event that re-dispatches it are never observably inconsistent, exactly as `Enqueue` already guarantees for the first dispatch.

The update SHALL be conditional on the row still being in `processing` status **and** still carrying the epoch its caller observed, and the method SHALL report whether a row was affected. Affecting no row SHALL be reported as a distinct outcome rather than as success or as an error: another sweeper won, or the job has since finished. The whole transaction, including the outbox insert, SHALL be rolled back in that case.

The outbox row SHALL be indistinguishable from the one `Enqueue` writes — same `event_type` constant, same payload shape, same fields — so recovery reuses the dispatch path end to end rather than introducing a second message the worker would have to recognise.

It SHALL be a distinct method rather than a mode of `Enqueue` or of `Update`. `Enqueue` asserts a `pending → queued` transition on a job that has never run; `Update` is the unconditional-by-id path this requirement's sibling now fences. Folding the requeue into either would give a general-purpose method a second concurrency contract.

`CachedVideoJobRepository` SHALL implement it write-through, and SHALL write through **only when a row was affected** — a requeue that lost its race changed nothing in PostgreSQL, and publishing the caller's in-memory `queued` job would contradict the winner.

#### Scenario: A requeue moves the job and writes its dispatch together

- **GIVEN** a persisted `VideoJob` in `processing` status at a known epoch
- **WHEN** the requeue is called with that epoch
- **THEN** it reports a row affected, the job's status is `queued`, its epoch is one greater, and exactly one unpublished `video_job_outbox` row exists carrying the current generation's `event_type` and that job's `job_id`, `user_id`, `source_key`, and `content_hash`

#### Scenario: A requeue whose epoch is stale changes nothing

- **GIVEN** a persisted `VideoJob` in `processing` status whose epoch has advanced since the caller observed it
- **WHEN** the requeue is called with the observed epoch
- **THEN** it reports no row affected, the job's status and epoch are unchanged, and no outbox row was written

#### Scenario: A requeue of a job that has since finished changes nothing

- **GIVEN** a persisted `VideoJob` that reached `completed` after the caller observed it as `processing`
- **WHEN** the requeue is called
- **THEN** it reports no row affected and the job is still `completed`

#### Scenario: A failed outbox insert leaves the job processing

- **GIVEN** a requeue whose `video_jobs` update succeeds but whose outbox insert fails
- **WHEN** the call returns an error
- **THEN** the job's persisted status is still `processing` at its original epoch — the transaction rolls back both writes

#### Scenario: The cached decorator writes through only on a won requeue

- **GIVEN** a cached `VideoJob` in `processing` status
- **WHEN** the requeue succeeds and reports a row affected
- **THEN** a subsequent `FindByID` served from cache returns `queued`; and when it reports no row affected, the cache entry is left unchanged

### Requirement: The Repository Enumerates Processing Jobs for the Sweeper

`domain.VideoJobRepository` SHALL expose a method returning a bounded batch of jobs currently in `processing` status, and `postgres.Repository` SHALL implement it with the status filter **in the query**, ordered deterministically, taking an explicit limit.

Successive calls SHALL NOT be able to return the same bounded prefix forever. A fixed `ORDER BY id LIMIT n` starves recovery: if the first `n` `processing` rows belong to healthy long-running extractions whose leases keep being renewed, every cycle examines those same rows and an abandoned job outside that prefix is never reached, for as long as those extractions last. The scan SHALL therefore advance across cycles — a keyset cursor carried between sweeps, or an ordering that puts the least recently examined rows first — so that recovery latency is bounded by the number of `processing` rows and the sweep interval, not by when unrelated jobs happen to finish.

The filter SHALL NOT be applied in Go over a broader read. The result set this feeds is scanned on a timer for the life of the deployment, and a scan whose cost grows with total job history is the failure mode `videojob-outbox-relay`'s claim index exists to avoid. An index supporting the predicate SHALL exist for the same reason.

It SHALL NOT be exposed through any HTTP route: it is not owner-scoped and returns other users' jobs by construction.

`CachedVideoJobRepository` SHALL pass it straight through without caching, as it already does for the other multi-row reads.

#### Scenario: Only processing jobs are returned

- **GIVEN** jobs in `pending`, `queued`, `processing`, `completed`, and `failed` status
- **WHEN** the method is called
- **THEN** only the `processing` ones are returned

#### Scenario: The batch is bounded

- **GIVEN** more `processing` jobs than the requested limit
- **WHEN** the method is called with that limit
- **THEN** at most that many jobs are returned

#### Scenario: An abandoned job outside the first batch is still reached

- **GIVEN** more `processing` jobs than the batch limit, where every job within one batch's worth is healthy and leased, and an abandoned one sorts after them
- **WHEN** the sweeper runs repeatedly while those healthy jobs keep running
- **THEN** the abandoned job is eventually returned by a scan and recovered, rather than waiting for the healthy jobs to finish

#### Scenario: Jobs of every owner are returned

- **GIVEN** `processing` jobs belonging to two different users
- **WHEN** the method is called
- **THEN** both users' jobs are returned, because recovery is not owner-scoped

## MODIFIED Requirements

### Requirement: Update Persists a VideoJob's Transitioned State

`Repository.Update` SHALL persist an already-loaded `VideoJob`'s current `status`, `frame_count`, `error_reason`, and `storage_key` to its existing `video_jobs` row, identified by its unchanging `id`, **by the fence epoch its caller claimed with, and by the row still being `processing`**. It SHALL NOT write a `video_job_outbox` row.

That exclusion is now load-bearing rather than incidental. Two repository methods write outbox rows — `Create` for `video_job.created` and `Enqueue` for the job-dispatch generation's queued event, with the requeue path above as the third — and `Update` is deliberately not among them. It is the path `CompleteJob` and `FailJob` both take, so emitting an event from it would make event production a status-dependent side effect of a general-purpose write, and would settle the shape of `VideoJobCompleted`/`VideoJobFailed` as a by-product rather than as Phase 7's own decision. `Update` SHALL NOT be given an outbox write to avoid adding a dedicated method.

`Update` SHALL be **fenced**: its predicate SHALL include the caller-supplied epoch, and affecting no row SHALL be reported as a distinct exported sentinel — the row exists and the transition was legal, but the caller no longer owns the job. This is a deliberate reversal of this requirement's previous "`Update` SHALL remain unconditional" clause, and the reason it reversed is that recovery now exists: a worker presumed dead can return mid-run, and the only thing that can stop it committing over its successor is a predicate in the same statement as the write.

**The predicate SHALL also require the stored status to be `processing`**, and that conjunct SHALL NOT be dropped as redundant with the epoch. It is what makes a terminal write *exclusive* rather than merely *ordered*: the epoch advances only on a requeue, so two actors can legitimately hold the same epoch for one job — a live but leaseless worker and the sweeper that decided to abandon it both act at the epoch they observed. Both would pass an epoch-only predicate, both would commit, and the second would overwrite the first. With the status conjunct the first write leaves the row terminal, the second matches no row, and exactly one actor may then perform the cleanup that follows a terminal state. An argument that the aggregate's own transition check prevents this SHALL NOT be substituted: both actors evaluate that check against copies loaded before either write.

This is safe precisely because `Update` performs only `processing → completed` and `processing → failed`. `Enqueue` owns `pending → queued`, `ClaimForProcessing` owns `queued → processing`, and the requeue method above owns `processing → queued`; no caller of `Update` writes from any other status.

The fence SHALL NOT be confused with the claim. `ClaimForProcessing` decides who *starts* a job; `Update` decides who may *finish* one. `StartProcessing` SHALL NOT be routed through `Update`.

The two ways `Update` can affect no row SHALL be distinguishable to the caller's log, through the same follow-up lookup `ClaimForProcessing` already uses to separate a missing row from a lost claim: a superseded epoch means the job was taken over, while a matching epoch on a terminal row means another actor at the same epoch finished first. Both dispositions are identical — reject, keep the source object, clear no idempotency key, perform no cleanup — so a single sentinel MAY carry both, but a log that cannot tell them apart makes an abandonment race indistinguishable from a takeover.

#### Scenario: Update persists a transitioned job

- **GIVEN** a `VideoJob` was previously persisted via `Create` and has since had a transition method applied to it in memory
- **WHEN** `Repository.Update` is called with that job and the epoch its row carries
- **THEN** a subsequent `Repository.FindByID` for its ID returns a job matching the updated `status`, `frame_count`, `error_reason`, and `storage_key`

#### Scenario: Update refuses a write carrying a superseded epoch

- **GIVEN** a persisted `VideoJob` whose `lease_epoch` has advanced since a caller read it
- **WHEN** `Repository.Update` is called with that caller's epoch
- **THEN** it returns the fence sentinel and every column of the row is unchanged

#### Scenario: Two actors at the same epoch cannot both commit a terminal state

- **GIVEN** a persisted `processing` `VideoJob` and two actors that both observed it at the same epoch — a leaseless worker still running and a sweeper that has reached the abandonment bound
- **WHEN** both call `Repository.Update` with that epoch, one writing `completed` and the other `failed`
- **THEN** exactly one affects a row, the other is refused, and the persisted job carries only the winner's outcome

#### Scenario: Update does not write an outbox row

- **GIVEN** a previously persisted `VideoJob`
- **WHEN** `Repository.Update` is called with it
- **THEN** no new `video_job_outbox` row is committed as a result of that call

### Requirement: ClaimForProcessing Persists the Processing Transition Only If the Job Is Still Queued

`Repository.ClaimForProcessing` SHALL persist a `VideoJob`'s `queued → processing` transition through a single statement whose predicate includes the stored status (`… WHERE id = $1 AND status = 'queued'`), and SHALL report to its caller whether a row was affected **and, when one was, that row's `lease_epoch`**. Affecting no row SHALL be reported as a distinct outcome, not as success and not as a not-found error — the row exists; another consumer simply reached it first.

The epoch SHALL be read by the claiming statement itself, not by a preceding or following query. A value read before the claim can be stale by the time the claim lands — a sweep can requeue the job in between — and the winner would then hold an epoch that fences its own terminal write; a value read afterwards is a second statement another writer can interleave with.

The predicate SHALL name `queued` and nothing else. It SHALL NOT be widened to admit a `processing` row whose lease has lapsed, however tempting that is as a route to recovery: the lease is Redis-backed and fails open, so a lease-store outage would license two workers to claim one live job. `videojob-lease-recovery` returns an abandoned job to `queued` instead, after which this predicate applies unchanged.

It SHALL be a single statement. A read-then-write, a transaction that selects and then updates, or a check performed in Go SHALL NOT be substituted: the guarantee is that the database evaluates the predicate and applies the update atomically, and any decomposition reintroduces the race the method exists to close.

It SHALL NOT write a `video_job_outbox` row, SHALL NOT advance `lease_epoch`, and SHALL NOT lock the row beyond the statement's own duration. The caller goes on to run an extraction lasting minutes; a claim that held a transaction open across it would be unusable.

`CachedVideoJobRepository` SHALL implement this method and SHALL write through **only when a row was affected**. A lost claim changed nothing in PostgreSQL, so writing the in-memory job to the cache would publish a state the authoritative store does not hold.

#### Scenario: A queued job is claimed

- **GIVEN** a persisted `VideoJob` in `queued` status
- **WHEN** `ClaimForProcessing` is called for it
- **THEN** it reports the row as affected together with that row's epoch, and a subsequent `FindByID` returns the job in `processing` status

#### Scenario: The claim does not advance the epoch

- **GIVEN** a persisted `VideoJob` in `queued` status at a known epoch
- **WHEN** `ClaimForProcessing` succeeds
- **THEN** the epoch it reports and the epoch stored on the row are both that same value

#### Scenario: A job that is not queued is not claimed and is not modified

- **GIVEN** a persisted `VideoJob` in `processing`, `completed`, `failed`, or `pending` status
- **WHEN** `ClaimForProcessing` is called for it
- **THEN** it reports no row affected, and a subsequent `FindByID` returns the job with its status and every other column unchanged

#### Scenario: A processing job with a lapsed lease is still not claimable

- **GIVEN** a persisted `VideoJob` in `processing` status whose lease has expired
- **WHEN** `ClaimForProcessing` is called for it
- **THEN** it reports no row affected — recovery is the requeue path's job, not this predicate's

#### Scenario: Two concurrent claims on one job produce exactly one winner

- **GIVEN** a persisted `VideoJob` in `queued` status
- **WHEN** two `ClaimForProcessing` calls for that ID execute concurrently
- **THEN** exactly one reports a row affected and the other reports none

#### Scenario: An unknown ID is reported as not found, not as a lost claim

- **GIVEN** no `video_jobs` row matches a given ID
- **WHEN** `ClaimForProcessing` is called with it
- **THEN** the caller can distinguish this from a lost claim, so a dispatch naming a nonexistent job is not mistaken for a duplicate
