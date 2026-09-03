## Context

`cmd/worker` runs one extraction at a time and drives each dispatch to a terminal state. `ClaimForProcessing` — `UPDATE video_jobs SET status = 'processing' WHERE id = $1 AND status = 'queued'` — is the correctness primitive that makes at-least-once dispatch safe: two deliveries race on one statement and exactly one runs `ffmpeg`.

What it cannot do is distinguish a job someone is actively working on from one abandoned by a worker that died mid-extraction. Both read `processing`. The abandoned one is stranded permanently, and the shipped code says so out loud, in `domain.VideoJobRepository.ClaimForProcessing`'s doc comment ("deliberately not a recovery primitive"), in `cmd/worker`'s `drainTimeout` comment, in `CLAUDE.md`, and in `docs/operations.md`'s triage query.

Two constraints shape everything below.

**The broker cannot be the recovery trigger.** When a worker process dies, its connection drops and RabbitMQ requeues the unacked delivery *immediately*. The redelivery therefore arrives seconds after the crash, while any lease set at pickup is still live — and `handle` rejects it to the dead-letter exchange, because `Reject` never requeues. By the time a lease could be observed as lapsed, the message is gone. Any design that waits for the broker to redeliver recovers nothing.

**Redis fails open here, on purpose, everywhere.** The idempotency store, the rate limiter, and the status cache all degrade to a slower-but-correct system when Redis is unreachable. A recovery mechanism that put Redis in the job-pickup decision would break that property: "cannot reach Redis" would have to mean either "never recover" or "assume every lease lapsed", and the second permits two workers on one live job.

## Goals / Non-Goals

**Goals:**

- A job abandoned by a dead worker returns to processing on its own, within a bounded time, with no operator action.
- A resurrected worker can never write `completed` or `failed` over the outcome of the worker that took its job — under any Redis failure mode.
- `ClaimForProcessing` keeps its unconditional, Redis-independent predicate.
- Repeated abandonment terminates instead of looping.
- Discharge `ddd-architecture`'s deferred fourth Redis responsibility and `redis-infrastructure`'s matching disclaimer.

**Non-Goals:**

- **Jobs stranded in `queued`.** A job whose dispatch was lost (relay never published, message dead-lettered, `.v1` residue) is a different class with a different fix; nothing here scans `queued` rows.
- **Resuming a partial extraction.** Recovery re-runs the job from its source object. `ffmpeg` output is not checkpointed and this change does not make it so.
- **Bounding concurrent extractions.** Prefetch is 1 and scaling is by process count; the lease is not a semaphore.
- **A general-purpose distributed lock in `internal/platform/`.** What ships is keyed by `VideoJobID` and belongs to the Video Processing context, like the idempotency store.
- **Any change to `cmd/api`.** It wires no lease and runs no sweeper.

## Decisions

### 1. A periodic sweeper is the recovery trigger

The sweeper is a goroutine in `cmd/worker`, sibling to the consumer in the same way the outbox relay is a sibling in `cmd/api`. Each cycle it reads a bounded batch of `processing` rows, asks the lease store whether each is still held, and requeues those that are not.

*Alternatives considered.* **Broker redelivery** — impossible, per Context. **Dead-letter-with-TTL bounced back onto the job queue** — the delay would have to exceed every plausible extraction to be correct, which is precisely the number nobody can pick, and it turns the dead-letter queue from a place where abandoned messages are enumerable into a retry conveyor. **Operator replay of the DLQ** — that is today's behaviour with extra steps, and the roadmap row exists to remove it.

*Where it runs.* `cmd/worker`, not `cmd/api`: a deployment with no worker has nothing to recover, and the lease knowledge is the worker's. Multiple worker replicas each run one, which is safe by decision 2's conditional update.

*What it reads through, and what it writes through.* The **scan** runs against the undecorated PostgreSQL repository, following the precedent `GET /download/:filename` set: a decision that grants one process authority over another's job is a correctness decision, and a correctness decision does not read a cache. The status cache keys per job ID and caches no listings, so the scan could not be served from it anyway — but the epoch the sweeper carries into the requeue must come from the same authoritative read the conditional update is predicated on.

The **requeue** goes through the cached decorator, with that authoritative epoch. Those are not in tension: the read must bypass the cache, the write must not. A requeue written straight to PostgreSQL would leave a `processing` entry in the cache for its whole TTL, and `GET /api/video-jobs/:id` would keep reporting `processing` for a job that has already been re-dispatched — hiding exactly the recovery this change exists to make visible. `videojob-status-cache` requires that write-through, and routing the sweeper around the decorator would leave the requirement implemented but unreachable.

The same read/write split extends to the three use cases that decide ownership — `StartProcessing`, `CompleteJob`, `FailJob` — which take an undecorated reader and a cached writer. `EnqueueVideoJob` keeps its cached read: a submitter transitioning a job it created moments earlier is not an ownership decision, and no requeue can reach a `pending` row. The split keeps Redis availability out of correctness: even with ordered write-through, a failed cache write and failed fallback invalidation can leave an older entry standing, and a previous release's record may carry no epoch at all.

Write-throughs are ordered with a Redis compare-and-set on `(epoch, status rank)`. This was initially rejected as unnecessary for ownership correctness, but review exposed a separate observable race: a requeue commits its outbox row before its cache write, so a successor can claim and complete while that write is delayed; a late unconditional `SET` then replaces `completed` with `queued` for the full cache TTL. The CAS admits a greater epoch and, within one epoch, only forward status progress (`pending < queued < processing < terminal`). Differing terminal outcomes share one rank and cannot replace one another. Ownership reads still bypass the cache because ordering does not make a failed Redis operation authoritative.

### 2. Recovery requeues (`processing → queued`); the claim predicate is not widened

The requeue is one transaction: `status → queued`, `lease_epoch + 1`, and a `video_job.queued.v2` outbox row — the same row `Enqueue` writes, published by the same relay, consumed by the same worker path. It is conditional on `status = 'processing' AND lease_epoch = <the epoch the sweep observed>`, so two sweepers race on one statement and exactly one wins, exactly as `ClaimForProcessing` does.

This is a deliberate departure from the roadmap row, which specifies widening `ClaimForProcessing` to admit a lapsed-lease `processing` row and adding a `processing → processing` self-transition.

*Why the departure.* Widening the predicate makes the claim *read the lease*. The lease is Redis-backed and fails open; a Redis outage would then license two workers to claim one live job — the exact double-`ffmpeg` the conditional claim exists to prevent, promoted from wasted work to two concurrent writers. Keeping the claim literal confines Redis to the recovery path, where "cannot tell" has a safe answer (decision 5).

Two further benefits fall out. `StartProcessing`'s lost-claim discrimination — it lets `job.StartProcessing()` fail and then inspects `job.Status()` to tell a lost claim from a genuine defect — survives untouched; a `processing → processing` edge would have made that in-memory transition start *succeeding* for duplicate deliveries and quietly reshaped the function. And recovery reuses dispatch end-to-end rather than inventing a second path into the worker.

*The cost, stated.* `processing → queued` is the first backwards edge in this state machine, and `validTransitions`' comment currently reads "No backwards transitions". It becomes one narrow, named exception, specified as a requirement rather than smuggled in. An observer polling `GET /api/video-jobs/:id` can see the status move backwards exactly once per abandonment; `app.js`'s `pollJob` already renders any non-terminal status as "na fila" and keeps polling, so no frontend change is needed.

### 3. The fence is a PostgreSQL column, not a Redis token

`video_jobs` gains `lease_epoch BIGINT NOT NULL DEFAULT 0`. `Update` — the write behind `CompleteJob` and `FailJob` — becomes conditional on the epoch its caller claimed with **and on the row still being `processing`**, returning `domain.ErrJobFenced` when no row matches.

The status conjunct is not redundant, and dropping it as "the epoch already covers that" is the mistake worth naming here. The epoch orders *takeovers*; it does not make a terminal write *exclusive*, because it advances only on a requeue. Two actors can therefore legitimately hold the same epoch for one job — a worker that is still running but leaseless, and the sweeper that has decided to abandon it — and under an epoch-only predicate both writes match and the second overwrites the first, with both actors then believing they own the cleanup. The aggregate's own `processing → failed` check does not save this: each actor evaluates it against a copy loaded before either wrote. Adding `AND status = 'processing'` makes the database decide, and it is free: `Update` performs only `processing → completed|failed`, since `Enqueue`, `ClaimForProcessing`, and the requeue own every other edge.

*Alternative considered: a dedicated `Abandon` repository method* with its own conditional statement. Rejected — it is a fifth port method, a fifth fake in every stub, another write-through case in the cache decorator, and a repository method with no use case behind it for the sweeper to reach past the application layer. Tightening the predicate on the method that already exists gets the same exclusivity for one line of SQL.

The roadmap row justifies failing open with "the PostgreSQL claim still prevents state corruption". That holds only while recovery does not exist. Once a job can be requeued and re-claimed, a resurrected worker A must be prevented from committing over holder B, and the only thing that can do that is a predicate in the same statement as the write. A fencing token held in Redis would be advisory: A would consult it, get an error or a stale answer, and write anyway.

*Alternative considered: no Redis at all* — put `lease_expires_at TIMESTAMPTZ` on `video_jobs` and let the sweeper's predicate be `lease_expires_at < now()`. This is genuinely simpler and would work. It was rejected because `ddd-architecture` commits to a Redis-backed worker lock as the fourth Redis responsibility and `redis-infrastructure` disclaims it pending this change; discharging those requirements by deleting them is a larger and less honest delta than implementing them. It is the obvious fallback if the lease store ever becomes a liability.

### 4. The epoch is won atomically and carried, never re-read

`ClaimForProcessing` returns the row's `lease_epoch` via `RETURNING` on the claiming statement itself, so the claimer learns its epoch from the statement that granted it. Reading it from a prior `FindByID` would be wrong: between that read and the claim, a sweep could requeue and bump, and the worker would run a job it legitimately owns while holding an epoch that fences its own terminal write.

The epoch then travels `StartProcessing` → `ProcessVideoJobResult` → `CompleteJobInput`/`FailJobInput`. It is **not** re-read inside those use cases — they load the aggregate through `FindByID`, which by then may carry the new holder's epoch, and a fence checked against that value fences nothing. `ProcessVideoJob`'s own `failWith` is fenced for the same reason: a resurrected worker must not be able to fail a job someone else is completing.

*Consequence.* Only the requeue increments the epoch, so `lease_epoch` reads as "how many times this job has been abandoned and re-dispatched" — which decision 6 uses directly.

### 5. Acquire fails open; takeover fails closed

Two Redis interactions with opposite postures, and the asymmetry is the point.

- **Acquiring or renewing a lease**: on error, log and proceed. The job is protected by the Redis-independent conditional PostgreSQL claim and terminal fence, so a leaseless extraction is correct; it is merely invisible to the sweeper, and the worst case is one duplicated extraction that the fence resolves.
- **Deciding a lease has lapsed**: on error, *do not requeue*. "Cannot tell" is not evidence of expiry. Declining preserves exactly today's behaviour — the job stays stranded until Redis returns — instead of inverting a Redis outage into a licence to take over every live job in the system at once.

Renewal and release are conditional on the stored value still naming this worker's epoch, so a worker that has already been taken over can neither extend nor delete the lease it no longer owns.

Acquisition is conditional too, on a weaker predicate: replace an absent lease or one at an *older or equal* epoch, refuse one at a *newer* epoch. Winning the claim and setting the lease are two steps, and a claimant that stalls between them can be swept, requeued, and superseded; an unconditional `SET` would then replace the rightful holder's lease with a stale epoch, stop that holder's renewals, and get its live job requeued a second time. `SET NX` is the wrong tool for the same job — it also refuses an *older* lease, which would leave a legitimate new holder running invisibly to the sweeper until the dead holder's entry expired. Compare-and-set on "not newer" is the middle ground that keeps both properties.

A refused renewal is ambiguous between an absent key and a newer holder. The heartbeat therefore invokes the same conditional acquire: it recreates an absent lease at the equal epoch and continues renewing, while a newer stored epoch refuses the acquire and proves the run was superseded. Stopping on the renewal result alone would leave a live extraction permanently leaseless after one Redis outage.

### 6. Repeated abandonment ends in `failed`

The sweeper requeues a job at most `maxRequeues` times (3), read straight off `lease_epoch`. Beyond that it fails the job through the same fenced `FailJob`, deletes the source object, and clears the idempotency key — the worker's own terminal-failure disposition, performed by the sweeper because there is no delivery in hand to carry it.

Without a bound the sweeper is an unbounded retry loop wrapped around whatever killed the worker. A video that reliably OOMs the process would be re-dispatched forever and would take down every replica in turn. The failure reason is a fixed English string carrying no infrastructure detail, matching `storeFailureReason`/`fetchFailureReason`.

### 7. A short lease with heartbeat renewal, not a generous fixed TTL

The lease TTL is short (90s) and renewed from a goroutine (every 30s) for as long as the extraction runs; the sweep interval is 60s. Renewal stops the moment the outcome is committed, and the lease is released explicitly.

*Alternative considered: a fixed TTL longer than any plausible extraction.* No goroutine, no renewal, but the number is unpickable — extractions scale with video length — and every abandoned job then waits the whole worst-case TTL before recovery. The heartbeat costs one goroutine per in-flight job, of which there is at most one (prefetch is 1).

### 8. `ProcessVideoJob` acquires and renews the lease; the worker releases it

The lease must start the instant the claim is won, and the only code that knows that instant is `ProcessVideoJob` — `StartProcessing` is its own first step, and its caller does not see the claim until the whole extraction has returned. It therefore takes the lease port as a dependency, acquires at the claim, renews from a goroutine for the duration of the extraction, and stops renewing when it returns.

Release belongs to the worker, after the terminal write commits, because that is where the job's outcome is decided; the release is conditional on the epoch, so it does not matter that a different component performs it. Between `ProcessVideoJob` returning and `CompleteJob` committing, the lease is unrenewed but unexpired — that gap is the bounded terminal-write retry (four attempts with short backoff), comfortably inside a 90-second TTL. If it were ever exceeded, a sweep could requeue the job and the fence would refuse the late write, which is the correct outcome rather than a hazard.

*Alternatives considered.* **A callback parameter** (`onClaimed(epoch)`) handed to `Execute`, so the worker owns both ends — rejected as a control-flow inversion no other use case in this codebase has, to save one dependency. **Splitting the claim out of `ProcessVideoJob`** so the worker calls `StartProcessing` itself — cleaner on paper, but `videojob-execution` specifies the claim as this use case's first step and several requirements are written against that sequence; the refactor is larger than the change it would serve.

### 9. The result object is not fenced, and that is accepted rather than unnoticed

The fence guards the row. A superseded worker's extraction can still finish and `Put` `frames_<jobID>.zip` after its successor did, before its terminal write is refused — so the stored bytes are the last run's while the row's `frame_count` is the holder's.

This is safe because of two conditions, and it is worth stating them because the safety is entirely in them and not in the fence: the source object is immutable for the life of the job (written once by `/upload`, deleted once a terminal state commits), and every worker that can be running a given job runs the same `ffmpeg` build. Identical input plus identical toolchain means the two runs produce equivalent frames and an equal count, so "whichever finished last" is not a meaningful distinction. The second condition is what promotes the rolling-deploy drain (see Risks) from advice to precondition.

*Alternative considered: epoch-qualify the result key* (`frames_<jobID>_e<N>.zip`). Not blocked by the no-`/` constraint — that shape satisfies it. It is rejected on cost: `handleDownload` parses the job ID out of the requested key and matches it against the row's recorded `storage_key`, so this reaches into `cmd/api/video.go`, contradicts this change's "`cmd/api` unchanged" scope, and leaves a dual-format parse to carry permanently for every object already stored flat. Priced and declined for a hazard whose realized harm is a rewrite with equivalent bytes. If either condition above ever stops holding — a mutable source, a per-worker extraction parameter, replicas on different toolchains — this becomes the required fix rather than an alternative.

### 10. The lease port belongs to the Video Processing context

`domain.JobLeaseStore` with a Redis adapter under `internal/video/infrastructure/lease/`, keyed `videojob:lease:<jobID>` — the same shape and the same namespace convention as `internal/video/infrastructure/idempotency` and `cache`'s `videojob:status:`. It is not `internal/platform/`: it is keyed by a `VideoJobID` and encodes this context's fence semantics, and `ddd-architecture` confines `internal/platform/` to plumbing no bounded context owns.

## Risks / Trade-offs

- **A sweep observes a job in the window between its claim committing and its lease being set** → That single observation records only an in-memory mark; it writes nothing. The lease is acquired as the first act after the claim commits, so two observations substantially narrow the ordinary round-trip gap, but they are not a proof: a process suspended between claim and acquisition across enough scan rotations can still be swept and fenced when it resumes. The residual is sharper at the requeue bound, where recovery commits `failed` and deletes the source; a prolonged pre-acquire stall can therefore end the job rather than merely duplicate it, and no fence undoes that terminal cleanup. Rejected mitigations: acquiring the lease *before* the claim (puts Redis back in the pickup path, decision 2's whole objection) and confirming only at the bound (the same statefulness for a narrower guarantee). For a job revisited every cycle, confirmation costs two observations separated by one sweep interval. Because each cycle scans a bounded batch with a rotating cursor, larger `processing` backlogs can add full scan rotations; that latency and a small in-memory, per-replica mark set are accepted. The marks remain unshared and unpersisted and are not what makes concurrent sweepers safe.
- **Redis is flushed or loses keys while extractions run** → Each heartbeat attempts to reacquire its missing lease. Until that succeeds the job looks abandoned, but the sweeper requires two successful not-held observations; a query error clears the earlier mark for that queried job, so its first later absence cannot complete the pair. Marks outside the failing keyset batch are not globally invalidated and can survive the outage, preserving the prolonged-stall takeover risk above. If a job is still requeued, the cost is duplicated work bounded by `maxRequeues`, with the fence preventing corruption except that abandonment at the bound can deliberately commit failure and source cleanup. This is the same class of degradation the status cache and idempotency store already accept.
- **The rolling deploy that introduces this** → A job in flight is held by a previous-build worker that sets no lease, honours no fence, and writes through the unconditional `Update` it was compiled with. If the sweeper requeues that job and a new worker takes it over, the old worker's write is not refused by anything, so it can land on top of the new holder's terminal state — the recorded outcome would then be the loser's. The aggregate check does not prevent it: the old worker loaded `processing` before the new holder committed. **Draining old workers before starting new ones is therefore a precondition of this deploy, not a mitigation**, and it is the one operational instruction this change adds. `run` already waits up to `drainTimeout` on SIGTERM, so the cost is rollout ordering, not code. Once drained, the exposure is one duplicated extraction for jobs running at the cutover, resolved by the fence.
- **Rolling *back* to the previous build** → The same hazard, in the other direction and easy to miss because the deploy "already worked once". A new worker left running keeps sweeping, and it can requeue a job an old worker is holding — an old worker that sets no lease, so it looks abandoned, and whose unconditional terminal write can then overwrite whichever successor takes the job. **Draining every new worker, which is what stops every sweeper, is a precondition of the rollback too.** Neither direction tolerates a mixed fleet.
- **The sweeper competes with the consumer for the same PostgreSQL pool** → Bounded batch (50) on a query filtered by `status`, at a 60s cadence. A partial index on `status` where it is `processing` keeps it from scanning the table as job history grows, following the outbox index's precedent.
- **`Update` becoming conditional changes an operation every terminal write depends on** → Its failure mode is a new sentinel, not a silent no-op: zero rows affected is `ErrJobFenced`, distinct from `ErrVideoJobNotFound`, and the cache decorator must not write through on it. This is the single riskiest edit in the change and the one with the most direct test obligation.
- **A poison job now consumes 4 extractions instead of 1 before failing** → Accepted. The alternative is either an unbounded loop or no recovery at all; `maxRequeues` is the dial.

## Migration Plan

1. **Schema** — `lease_epoch BIGINT NOT NULL DEFAULT 0` declared inline in `CREATE TABLE` and added by `ALTER TABLE … ADD COLUMN IF NOT EXISTS`, following `source_key` and `content_hash` exactly. Purely additive, no backfill: a pre-existing `processing` row holds epoch `0`, which is exactly right — it has been abandoned zero times and is the first thing the sweeper recovers. Both binaries run the same migration and either may run it first, as today.
2. **Deploy** — `cmd/api` is unaffected and can go at any point. For workers, drain before replacing (see Risks). After two confirmed observations, startup sweeps will requeue every genuinely-stranded `processing` row that predates this change, which is the intended one-time recovery; expect a burst of dispatches proportional to the stranded backlog, and check `docs/operations.md`'s triage query beforehand to know its size.
3. **Rollback** — redeploy the previous build. The column stays and is ignored: the old `updateJobStatement` names no epoch and writes unconditionally, the old claim does not bump anything, and a job the new sweeper requeued is an ordinary `queued` job the old worker processes normally. Recovery simply stops happening again.

## Open Questions

- `maxRequeues = 3`, TTL 90s / renew 30s / sweep 60s / batch 50 are chosen for a hackathon-scale deployment, not measured. They are constants, not configuration — no new environment variables — on the same reasoning as the status cache's fixed 5-minute TTL. If a deployment needs to tune them, promoting them to env vars is a later, additive change.
- Whether the sweeper should also emit a metric or a structured log per requeue is deferred to Phase 8's observability work; for now it logs the job ID, the epoch, and the outcome.
