# videojob-lease-recovery Specification

## Purpose
Define crash-safe worker recovery through Redis liveness leases, PostgreSQL fence epochs, conditional requeue with transactional re-dispatch, and bounded terminal abandonment.
## Requirements
### Requirement: A Lease Records That Some Worker Is Still Working on a Job

The Video Processing context SHALL define a job-lease port in its domain layer, implemented by a Redis-backed adapter in its infrastructure layer, keyed by `VideoJobID` under this context's own key namespace. The port SHALL expose acquiring a lease for a job, renewing it, releasing it, and asking whether one is held. **Every one of those operations SHALL carry the fence epoch, the query included.** The query SHALL answer "is this job leased at this epoch", not "does a key exist for this job": a stored value naming a different epoch SHALL be reported as not held.

That distinction is not pedantry. Acquisition fails open, so the rightful holder may hold no lease at all; a superseded claimant's late acquire then finds the key absent and writes its own older epoch. A query keyed only on the job would report that row as protected, the sweeper would skip it forever, and the job would be unrecoverable while its only lease belongs to a worker the fence has already locked out.

A lease SHALL carry an expiry, and its value SHALL be the fence epoch its holder claimed with (see below). Renewal and release SHALL be conditional on the stored value still naming that epoch, so a worker whose job has already been taken over can neither extend nor delete the lease its successor holds. Release SHALL be best effort after an eligible terminal outcome: an error SHALL be logged without changing the message disposition, and the key MAY remain until its TTL expires. The epoch identifies a holder unambiguously: only a requeue changes it, and only a requeue can produce a second holder.

**Acquisition SHALL be conditional too**, though on a weaker predicate than renewal: it SHALL replace an absent lease or one naming an *older or equal* epoch, and SHALL refuse to replace one naming a *newer* one. An unconditional write is unsafe because winning the claim and setting the lease are two steps: a claimant that stalls between them can be swept, requeued, and superseded, and its late write would then replace the rightful holder's lease with a stale epoch — after which the holder's own renewals are refused, its live job looks abandoned once that stale entry expires, and it is requeued a second time under a worker that is still running. Refusing on a newer stored value SHALL NOT be reported as an error to the caller: it is the successor's lease, correctly left alone.

A `SET NX` SHALL NOT be substituted for that conditional write. `NX` also refuses to replace an *older* lease, so a claimant that legitimately took over a job would run leaseless until the dead holder's entry expired — invisible to the sweeper for exactly the window recovery depends on.

A refused renewal SHALL NOT by itself be treated as a takeover. The same outcome also means the key is absent, as happens when the initial acquire failed or the lease expired during a Redis outage. The holder SHALL attempt the conditional acquire again: an absent, equal, or older epoch restores its lease and keeps the heartbeat running, while a newer epoch proves a successor exists and stops renewal. This distinction prevents one transient outage from leaving a live extraction permanently invisible to recovery.

The lease SHALL be a **liveness** signal and nothing else. `ClaimForProcessing` SHALL NOT consult it, no HTTP route SHALL consult it, and it SHALL NOT be used to bound how many extractions run concurrently — prefetch and process count do that.

It SHALL NOT be placed in `internal/platform/`. It is keyed by a `VideoJobID` and encodes this context's fence semantics, and `ddd-architecture` confines `internal/platform/` to plumbing no bounded context owns.

#### Scenario: A lease is held for the duration of an extraction

- **GIVEN** a lease store whose acquire and renew operations succeed
- **WHEN** a worker wins a job's claim and begins extracting
- **THEN** the lease store reports that job as held, and continues to report it as held for as long as the extraction runs

#### Scenario: A lease naming an older epoch does not protect the current generation

- **GIVEN** a job requeued and re-claimed at a newer epoch, whose current holder never acquired a lease, and whose stored lease names the previous epoch
- **WHEN** the sweeper asks whether that job is held at the epoch its scan reported
- **THEN** the answer is not-held, and the job is treated as abandoned like any other

#### Scenario: A lease lapses when its holder stops renewing

- **GIVEN** a job whose holder has stopped renewing its lease
- **WHEN** the lease's expiry passes
- **THEN** the lease store reports that job as not held

#### Scenario: A superseded holder cannot renew the lease it lost

- **GIVEN** a job whose lease is held at a newer epoch than the one a resurrected worker claimed with
- **WHEN** that worker attempts to renew or release the lease
- **THEN** the stored lease is unchanged, and the current holder's lease keeps its own expiry

#### Scenario: A stalled claimant cannot overwrite its successor's lease

- **GIVEN** a worker that won a claim at one epoch but had not yet acquired the lease, and a job that has since been requeued and re-claimed at a newer epoch by another worker
- **WHEN** the first worker finally attempts to acquire the lease
- **THEN** the stored lease still names the newer epoch, the current holder's renewals keep succeeding, and the attempt is not reported as an error

#### Scenario: A new holder replaces a dead holder's stale lease

- **GIVEN** a job whose previous holder's lease entry still exists at an older epoch
- **WHEN** a worker that claimed the job at a newer epoch acquires the lease
- **THEN** the acquisition succeeds and the lease store reports the job as held at the newer epoch

#### Scenario: A live holder reacquires a lease that lapsed during an outage

- **GIVEN** a running extraction whose lease key disappeared while Redis was unavailable
- **WHEN** renewal reports the key absent and the holder conditionally reacquires at its epoch
- **THEN** the lease is restored and renewal continues; but if the stored epoch is newer, reacquisition is refused and the old holder stops renewing

#### Scenario: A successful release removes the lease after commit

- **GIVEN** a worker that has committed a job's terminal state and whose release operation succeeds
- **WHEN** it finishes with that job
- **THEN** the lease store reports the job as not held, without waiting for the expiry

#### Scenario: A failed release falls back to expiry

- **GIVEN** a cleanup-eligible terminal outcome whose lease release returns an error
- **WHEN** the worker finishes with that job
- **THEN** it logs the error, preserves its normal message disposition, and the lease may remain until its TTL expires

### Requirement: The Fence Epoch Is Won Atomically and Carried, Never Re-Read

`video_jobs` SHALL carry a monotonically increasing fence epoch per job. `ClaimForProcessing` SHALL return the epoch of the row it claimed, read by the claiming statement itself rather than by a prior lookup, and that value SHALL be carried forward through the processing sequence into whichever terminal write ends it.

Re-reading the epoch inside `CompleteJob` or `FailJob` SHALL NOT be substituted for carrying it. Those use cases load the aggregate before they write, and by then the row may carry a successor's epoch; a fence checked against that value fences nothing, and the check would pass in exactly the case it exists to reject.

Reading it from a lookup that precedes the claim SHALL NOT be substituted either. Between such a read and the claim, a sweep can requeue the job and advance the epoch, and the worker would then run a job it legitimately owns while holding an epoch that fences its own terminal write.

#### Scenario: A claimant learns its epoch from the claiming statement

- **GIVEN** a job in `queued` status
- **WHEN** a worker claims it
- **THEN** the claim reports the row's current epoch, and that epoch is the one the worker carries for the rest of the job

#### Scenario: A stale epoch read before the claim is not used

- **GIVEN** a job that is requeued, and its epoch advanced, between a worker's initial lookup and its claim
- **WHEN** the worker wins the claim and later commits a terminal state
- **THEN** the write succeeds, because the epoch it carries is the one the claim reported and not the one the earlier lookup saw

### Requirement: A Terminal Write Is Conditional on the Fence Epoch

The persistence path behind `CompleteJob` and `FailJob` SHALL apply its write only if the stored row still carries the epoch the caller claimed with **and is still in `processing` status**. When no row matches, it SHALL classify the authoritative stored row: the exact same terminal outcome already stored at the same epoch is an idempotent success with `Applied=false`; a missing row is the not-found error; every other existing state returns the distinct, exported `ErrJobFenced` sentinel. That sentinel SHALL remain distinguishable from not-found and invalid-transition errors: the row exists, but this caller may no longer commit its proposed outcome.

Both conjuncts are load-bearing and neither subsumes the other. The epoch rejects a worker whose job was taken from it. The status makes the write *exclusive*, which the epoch alone cannot: the epoch advances only on a requeue, so a live leaseless worker and the sweeper abandoning it can hold the same epoch, and an epoch-only predicate would let both commit, the second overwriting the first. With the status conjunct exactly one terminal write per job can ever affect a row, and the cleanup that follows a terminal state has exactly one owner.

The fence SHALL live in the same statement as the write it guards. A token held only in Redis SHALL NOT be substituted: consulting it is advisory, and a caller that got an error or a stale answer would write anyway.

This is what makes recovery safe. Once a job can be requeued and re-claimed, a worker that was presumed dead can come back mid-run; without the fence it would commit `completed` or `failed` over the outcome of the worker that took the job from it.

#### Scenario: A superseded worker cannot complete a job it no longer owns

- **GIVEN** a job that was requeued and re-claimed after its original worker stopped renewing its lease
- **WHEN** that original worker attempts to commit `completed`
- **THEN** the write is refused with the fence sentinel, and the job's persisted status, `StorageKey`, `FrameCount`, and `ErrorReason` reflect only the current holder

#### Scenario: A superseded worker cannot fail a job it no longer owns

- **GIVEN** the same job, and an original worker whose extraction errored
- **WHEN** it attempts to commit `failed`
- **THEN** the write is refused with the fence sentinel and the job is not moved to `failed`

#### Scenario: The current holder's terminal write succeeds

- **GIVEN** a job whose current holder claimed it and whose epoch has not advanced since
- **WHEN** that holder commits `completed` or `failed`
- **THEN** the write succeeds and the job reaches that terminal state

### Requirement: A Sweeper Returns Abandoned Jobs to the Queue

`cmd/worker` SHALL run a periodic sweeper alongside its consumer. Each cycle it SHALL read a bounded batch of jobs in `processing` status, ask the lease store whether each is still held, and for those that are not, requeue them: a single transaction moving the job `processing → queued`, advancing its fence epoch, and writing the job-dispatch generation's queued outbox row — the same row `Enqueue` writes, published by the same relay and consumed by the same worker path.

The requeue SHALL be conditional on the job still being `processing` **and** still carrying the epoch the sweep observed, so that two sweepers in two worker replicas race on one statement and exactly one wins.

**A single unleased observation SHALL NOT be enough to act on a job.** A sweeper SHALL act only on a job it has observed unleased at the *same epoch* on two consecutive successful lease queries, and SHALL discard that mark as soon as a cycle finds the job leased, no longer `processing`, carrying a different epoch, **or cannot query the lease store**. A mark followed by a lease-query error for the same job cannot confirm its first later not-held observation: the lease may have expired while unreachable and the live worker may not yet have renewed it. Two fresh successful observations are required for that job after its failed query. Marks for jobs outside the failing scan batch are not globally invalidated, so one can survive an outage and pair with the first later absence; that weaker per-job reset is an accepted part of the prolonged-stall risk below. A claim commits in PostgreSQL and its lease is acquired in Redis — two stores, two round trips — so every healthy run is briefly `processing` with no lease. Acting on one observation requeues a live extraction, and at the requeue bound below it *fails* that extraction and deletes its source, which no fence can undo. Two observations mitigate the ordinary claim-to-acquire round-trip window but do not eliminate a prolonged process stall: a claimant suspended before acquisition across enough scans can still be requeued and fenced when it resumes. At the requeue bound, that residual can instead commit `failed` and delete the source; this is accepted as treating a process unable to make its first liveness write across multiple observations as abandoned, not as a guarantee that every live process is preserved. For a job revisited every cycle, confirmation requires two observations separated by one sweep interval. The bounded keyset scan means larger `processing` backlogs can add full cursor rotations before the same job is observed again; that backlog-dependent latency is the right trade against terminating a healthy job.

The confirmation state SHALL be worker-local and in-memory, never persisted or shared between replicas: each replica confirms its own observations, and what makes concurrent sweepers safe remains the conditional requeue and the fenced terminal write. A restarted replica simply re-observes, at a cost of one cycle.

Recovery SHALL NOT be built on broker redelivery. When a worker process dies its unacknowledged delivery is requeued by the broker immediately, usually before a successfully acquired lease lapses; regardless of whether any lease exists, the redelivery is dead-lettered because the PostgreSQL row remains `processing` and the claim admits only `queued`. By the time lease absence can authorize recovery there is no message left. A delayed-retry queue SHALL NOT be substituted either: its delay would have to exceed every possible extraction to be correct.

The claim predicate SHALL NOT be widened to admit a `processing` row instead. Doing so would make `ClaimForProcessing` read the lease, and the lease is Redis-backed and fails open — a Redis outage would then license two workers to claim one live job, which is the exact hazard the conditional claim exists to close.

#### Scenario: A job abandoned by a dead worker is processed again

- **GIVEN** a job left in `processing` by a worker that died mid-extraction, whose lease has since lapsed
- **WHEN** two consecutive sweep cycles both observe it unleased at the same epoch, and a worker is consuming
- **THEN** the job is returned to `queued`, dispatched again, claimed, and driven to a terminal state without operator action

#### Scenario: A job claimed moments before a sweep is not requeued

- **GIVEN** a job whose claim has committed but whose holder has not yet acquired its lease
- **WHEN** a sweep cycle observes it unleased and the holder acquires and renews before the next cycle
- **THEN** nothing is written on either cycle, the epoch is unchanged, and the extraction runs to completion

#### Scenario: A mark is discarded when the epoch moves under it

- **GIVEN** a job marked unleased at one epoch that another replica then requeues and a worker re-claims
- **WHEN** the next cycle observes it at the advanced epoch
- **THEN** that observation counts as a first one and nothing is written for it on that cycle

#### Scenario: A job whose lease is still held is left alone

- **GIVEN** a job in `processing` whose holder is renewing its lease
- **WHEN** the sweeper runs
- **THEN** the job is not requeued, its status is still `processing`, and its epoch is unchanged

#### Scenario: An outage resets an earlier unleased confirmation

- **GIVEN** a job marked by one successful not-held observation, followed by a lease-store error
- **WHEN** connectivity returns and the next cycle again observes the job not held
- **THEN** that observation starts a fresh confirmation pair and does not requeue the job; only a second successful not-held observation may act

#### Scenario: Two sweepers cannot requeue the same job twice

- **GIVEN** a job in `processing` with a lapsed lease and two worker replicas sweeping concurrently
- **WHEN** both attempt to requeue it
- **THEN** exactly one succeeds, the epoch advances by exactly one, and exactly one queued outbox row is written

#### Scenario: The requeue and its dispatch event commit together

- **GIVEN** a requeue whose outbox insert fails
- **WHEN** the call returns an error
- **THEN** the job is still `processing` with its original epoch — no job is left `queued` with nothing to dispatch it

#### Scenario: A job stranded before this change is recovered without operator action

- **GIVEN** a `video_jobs` row in `processing` written before the fence column existed, holding the column's default epoch and having no lease
- **WHEN** two successful sweep cycles confirm that it is not leased at that epoch
- **THEN** it is requeued like any other abandoned job

#### Scenario: Jobs stranded in queued are not the sweeper's concern

- **GIVEN** a job in `queued` status whose dispatch was never published or was dead-lettered
- **WHEN** the sweeper runs
- **THEN** it is not touched — the sweeper scans `processing` rows only

### Requirement: Repeated Abandonment Ends in a Terminal State, Not a Loop

The sweeper SHALL requeue a given job at most a bounded number of times, counted by its fence epoch. Beyond that bound it SHALL NOT requeue the job again; it SHALL fail the job through the same fenced terminal write, subject to the same two-cycle confirmation as a requeue — the abandonment is the one sweeper action a fence cannot undo, so it is the action that most needs it, with a fixed reason naming no infrastructure detail, and SHALL then make one best-effort attempt to delete the job's source object and conditionally clear its idempotency key — the worker's own terminal-failure disposition, performed here because there is no delivery in hand to carry it.

**A `processing` job whose source key is empty SHALL be failed after the same two-observation confirmation rather than requeued.** Such rows exist only from before the source-key column was added, and they are exactly what a first sweep encounters. Requeueing one is a loop with no exit: the aggregate's requeue transition rejects an empty source key, so the epoch never advances, the bound is never reached, and the abandonment path never fires. Excluding them from the scan instead SHALL NOT be substituted — that leaves them stranded, which is the condition this capability exists to end.

The cleanup that follows an abandonment — deleting the source object and clearing the idempotency key — SHALL be gated on **this** actor's own committed `failed` write, never on the observation that the job is terminal and never on the terminal row merely matching what this actor intended to write. Two sweepers at the bound produce byte-identical intents — same epoch, same `failed`, same fixed reason — so the sweeper SHALL use the applied-versus-already-present outcome `videojob-lifecycle` requires, and SHALL clean up only on *applied*. Exclusivity for the write itself comes from the terminal statement's own predicate, which requires the row to still be `processing`; whichever actor gets there first leaves the row terminal and every other actor's write affects no row. Two sweepers reaching the bound together, or a sweeper racing a leaseless worker that is still running, therefore produce exactly one cleanup. An argument from the aggregate's transition check SHALL NOT be substituted for that predicate: every actor evaluates that check against a copy loaded before any of them wrote.

An unbounded requeue SHALL NOT be shipped. A job that reliably kills the process — an input that exhausts memory, say — would otherwise be re-dispatched forever and would take down each replica in turn.

#### Scenario: A job that keeps killing its worker eventually fails

- **GIVEN** a job that has already been requeued the maximum number of times and is again in `processing` with a lapsed lease
- **WHEN** the sweeper runs
- **THEN** the job is `failed` with a non-empty reason, no further queued outbox row is written for it, and the sweeper makes one best-effort attempt to delete its source object and clear its idempotency key

#### Scenario: A job with no recorded source is failed rather than requeued

- **GIVEN** a `processing` job whose `source_key` is empty, as a row predating that column carries
- **WHEN** the sweeper finds it unleased
- **THEN** it is `failed` on the cycle that confirms it, no further queued outbox row is written for it (the terminal write commits its own `video_job.failed.v1` row, per `videojob-terminal-events`), and it is not seen again by a later sweep

#### Scenario: Only the sweeper that committed the failure cleans up

- **GIVEN** two sweepers that both reach the bound for the same job, writing the identical epoch, status, and fixed reason
- **WHEN** the first commits `failed` and the second finds the job already carrying exactly that outcome
- **THEN** the second's result reports the state as already present rather than applied, and it deletes no object and clears no idempotency key

#### Scenario: The abandonment reason leaks no infrastructure detail

- **GIVEN** a job failed by the sweeper for repeated abandonment
- **WHEN** its `ErrorReason` is read back through the job-status API
- **THEN** it names neither the storage endpoint, nor the bucket, nor the broker, nor Redis

### Requirement: The Result Object Is Not Fenced, and Its Interchangeability Is What Makes That Safe

The fence guards the `video_jobs` row, not the stored result object. A superseded worker's extraction can finish after its successor's and write `frames_<jobID>.zip` a second time before its terminal write is refused, so the bytes under that key are whichever run finished last while the row's `frame_count` is the holder's. This SHALL be treated as an accepted, stated property rather than an unnoticed one, and it is safe only because two conditions hold:

- **The source object is immutable for the life of the job.** `POST /upload` writes it once and never rewrites it, and it is deleted only once a terminal state is committed. Every run of a given job therefore reads identical input bytes.
- **Every worker that can be running a given job runs the same `ffmpeg` build.** Extraction is deterministic in its input, so two runs of one job produce equivalent frames and an equal frame count. This is what makes draining old workers before starting new ones a **precondition** of the deploy rather than an optimisation.

Any change that breaks either condition — a mutable source, a per-worker extraction parameter, a toolchain that varies across replicas — SHALL either re-key results per epoch or fence publication of the object, and SHALL NOT rely on this requirement's argument.

#### Scenario: A late run rewrites the result object without changing the recorded outcome

- **GIVEN** a superseded worker whose extraction finishes after its successor has completed the job
- **WHEN** it stores its result under the job's result key and then attempts its terminal write
- **THEN** the terminal write is refused, the job's `frame_count` and `status` are the successor's, and the object under that key decodes to the same frames the successor produced

### Requirement: Acquiring a Lease Fails Open, Deciding One Has Lapsed Fails Closed

The two lease interactions SHALL have opposite failure postures.

Acquiring or renewing a lease SHALL fail **open**: a lease-store error SHALL be logged and processing SHALL continue. The job is protected from stale PostgreSQL writes by the conditional claim and fence, so an extraction may continue without a lease. It is then invisible to the sweeper: recovery can duplicate its work and fence it on resume, while prolonged invisibility at the requeue bound can instead produce the documented terminal abandonment and source cleanup.

Deciding that a lease has lapsed SHALL fail **closed**: a lease-store error SHALL NOT be read as evidence of expiry, and the sweeper SHALL NOT requeue a job it could not get an answer for. It SHALL also discard any earlier unleased mark for that job, so recovery requires two fresh successful observations for that job after its failed query. Marks for jobs that were not queried in the failing batch are not globally invalidated. Declining the queried job preserves the pre-existing behaviour — it stays stranded until the lease store is reachable — whereas assuming expiry from the query error would turn a Redis outage into a licence to take over live jobs.

This asymmetry SHALL be stated in the implementation rather than left to be inferred, because it is the one place in this system where "fail open" is the wrong default.

#### Scenario: An extraction proceeds when the lease cannot be acquired

- **GIVEN** a worker that won a job's claim and a lease store that errors on acquire
- **WHEN** the worker continues
- **THEN** the extraction runs to a terminal state, and the failure to acquire is logged

#### Scenario: The sweeper requeues nothing while the lease store is unreachable

- **GIVEN** jobs in `processing` and a lease store that errors on every query
- **WHEN** the sweeper runs
- **THEN** no job is requeued, no epoch advances, no outbox row is written, and the condition is logged

#### Scenario: A lease lost to an outage costs work, not correctness

- **GIVEN** a running extraction whose lease was lost because the lease store dropped its keys
- **WHEN** the sweeper requeues that job and another worker claims and completes it
- **THEN** the original worker's terminal write is refused by the fence, it does not delete the source object, and the job carries exactly one outcome
