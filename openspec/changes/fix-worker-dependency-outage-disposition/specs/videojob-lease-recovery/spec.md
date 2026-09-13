## MODIFIED Requirements

### Requirement: A Sweeper Returns Abandoned Jobs to the Queue

`cmd/worker` SHALL run a periodic sweeper alongside its consumer. Each cycle it SHALL read a bounded batch of jobs in `processing` status, ask the lease store whether each is still held, and for those that are not, requeue them: a single transaction moving the job `processing → queued`, advancing its fence epoch, and writing the job-dispatch generation's queued outbox row — the same row `Enqueue` writes, published by the same relay and consumed by the same worker path.

The requeue SHALL be conditional on the job still being `processing` **and** still carrying the epoch the sweep observed, so that two sweepers in two worker replicas race on one statement and exactly one wins.

**A single unleased observation SHALL NOT be enough to act on a job.** A sweeper SHALL act only on a job it has observed unleased at the *same epoch* on two consecutive successful lease queries, and SHALL discard that mark as soon as a cycle finds the job leased, no longer `processing`, carrying a different epoch, **or cannot query the lease store**. A mark followed by a lease-query error for the same job cannot confirm its first later not-held observation: the lease may have expired while unreachable and the live worker may not yet have renewed it. Two fresh successful observations are required for that job after its failed query. Marks for jobs outside the failing scan batch are not globally invalidated, so one can survive an outage and pair with the first later absence; that weaker per-job reset is an accepted part of the prolonged-stall risk below. A claim commits in PostgreSQL and its lease is acquired in Redis — two stores, two round trips — so every healthy run is briefly `processing` with no lease. Acting on one observation requeues a live extraction, and at the requeue bound below it *fails* that extraction and deletes its source, which no fence can undo. Two observations mitigate the ordinary claim-to-acquire round-trip window but do not eliminate a prolonged process stall: a claimant suspended before acquisition across enough scans can still be requeued and fenced when it resumes. At the requeue bound, that residual can instead commit `failed` and delete the source; this is accepted as treating a process unable to make its first liveness write across multiple observations as abandoned, not as a guarantee that every live process is preserved. For a job revisited every cycle, confirmation requires two observations separated by one sweep interval. The bounded keyset scan means larger `processing` backlogs can add full cursor rotations before the same job is observed again; that backlog-dependent latency is the right trade against terminating a healthy job.

The confirmation state SHALL be worker-local and in-memory, never persisted or shared between replicas: each replica confirms its own observations, and what makes concurrent sweepers safe remains the conditional requeue and the fenced terminal write. A restarted replica simply re-observes, at a cost of one cycle.

Recovery SHALL NOT be built on broker redelivery. When a worker process dies its unacknowledged delivery is requeued by the broker immediately, usually before a successfully acquired lease lapses; regardless of whether any lease exists, the redelivery is dead-lettered because the PostgreSQL row remains `processing` and the claim admits only `queued`. By the time lease absence can authorize recovery there is no message left. A delayed-retry queue SHALL NOT be substituted either: its delay would have to exceed every possible extraction to be correct.

**That clause is about a job already `processing` whose claim was won, and SHALL NOT be read as forbidding the requeue `videojob-worker` performs for a dispatch whose claim outcome was never learned.** The two are different operations. Recovery returns a job whose claim was *won and then abandoned* to the queue, and it is the lease's absence that authorizes it. `videojob-worker`'s requeue puts back a dispatch for which no actor performed any work; nothing in that path consults a lease, advances an epoch, or writes an outbox row.

Its two possible outcomes are covered between the two requirements rather than by either alone, and the second is **this sweeper's ordinary input rather than an exception to it**. If the claim never committed, the row is still `queued` and the redelivery is decided by the same conditional claim the original delivery would have been — a case none of the reasoning above concerns. If the claim committed and its result was lost, the row is `processing` and holds no lease, because a lease is acquired only once a claim is reported won; the redelivery is dead-lettered exactly as this clause describes, and the row is recovered here on the ordinary two-observation path. Stated here because the clause is otherwise the first thing a reviewer will quote against that disposition, and because that second outcome would otherwise look like a gap between the two requirements rather than the seam where they meet.

That second outcome spends one of the bounded requeues below, and that cost is accepted rather than compensated for: a job that exhausts the bound is committed `failed` — visible to its owner, with its idempotency key cleared — which is the outcome this capability already prescribes at the bound and is strictly better than a `queued` row nothing can reach.

**The sweeper SHALL NOT be extended to scan `queued` rows** as an alternative to that disposition. Its authority comes from the lease, and a `queued` job holds none by design — which is exactly what makes a `processing` row without one evidence of abandonment. A `queued` row offers no equivalent signal, because `queued` is the ordinary state of a job waiting for a free worker: a job whose dispatch is en route behind a backlog and a job whose dispatch is gone are indistinguishable to any scan. Acting on age instead would re-dispatch healthy jobs during exactly the backlog that made them look old, and re-dispatching means writing a **new** outbox row, since the original is already stamped published — so the sweeper would become a standing source of duplicate dispatches to cover a failure the consumer's own disposition prevents outright. A separate reconciliation process over aged `queued` rows SHALL NOT be substituted: it is the same heuristic in another process, with a second recovery mechanism's interaction with this one left unreasoned.

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

- **GIVEN** a job in `queued` status whose dispatch was never published, or was dead-lettered for a reason `videojob-worker` still dead-letters
- **WHEN** the sweeper runs
- **THEN** it is not touched — the sweeper scans `processing` rows only, and preventing the stranding is the consumer's disposition's job rather than this one's

#### Scenario: A dispatch requeued on an unknown claim outcome is not a sweeper concern while the row is queued

- **GIVEN** a job still in `queued` status whose dispatch the worker requeued because its repository could not answer
- **WHEN** the sweeper runs
- **THEN** it is not touched, and the job is advanced by the redelivery rather than by any action of the sweeper's

#### Scenario: The same requeue over a claim that had committed is an ordinary abandoned job

- **GIVEN** a job whose claim committed but whose result the worker could not read, leaving the row `processing` with no lease while the worker requeued the dispatch
- **WHEN** two successful sweep cycles confirm that it is not leased at that epoch
- **THEN** it is requeued like any other abandoned job, spending one of the bounded requeues, with no special handling for having arrived by that route
