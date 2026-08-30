## Context

`POST /upload` today does everything in one request: stores the source, creates the job, enqueues it (writing an outbox row the relay publishes), then runs `ProcessVideoJob` inline, calls `CompleteJob`, deletes the source, and returns the result. The queue is published to and consumed by nobody.

This change inverts that. The handler stops after enqueueing; a new `cmd/worker` picks the job up from the broker. Everything the worker needs already exists — `ProcessVideoJob` takes a source `StorageKey` rather than a path precisely so a process with a different filesystem can run it, and the queued event's payload already carries `(job_id, user_id, source_key, occurred_at)`.

Three constraints shape the design and none of them is negotiable:

- **The relay is at-least-once.** A publish can succeed before `published_at` commits, so the same message can arrive twice. `Repository.Update` is a read-then-unconditional-write, which means two deliveries would both pass `queued → processing` and both run `ffmpeg`.
- **A rolling deploy overlaps generations.** During it, a replica still processing inline coexists with a worker consuming the queue.
- **`handleVideoUpload` stops learning outcomes.** Anything it owns today that depends on how the job ends has to be re-homed.

## Goals / Non-Goals

**Goals:**

- `POST /upload` returns without waiting for `ffmpeg`.
- No duplicate delivery can run `ffmpeg` twice or corrupt job state.
- No deploy ordering has to be gotten right by hand for correctness.
- The idempotency contract survives the handler losing sight of the outcome.
- The obligation `videojob-outbox-relay` recorded against this change is discharged explicitly.

**Non-Goals:**

- **Recovering a job whose worker died mid-`ffmpeg`.** That job sits in `processing` and stays there. `add-worker-job-lock` (6.4) adds the lease and the fenced takeover; this change deliberately ships the gap rather than a half-lease.
- **A dead-letter reconciler.** Messages accumulate in `video.jobs.dead` to be looked at. `add-rabbitmq-infrastructure` deferred this until something consumed; it is still not this change's.
- **Worker horizontal scaling policy.** The design must be safe with N workers; choosing N is operations.
- **Retiring `POST /api/video-jobs`.** It keeps having no processing trigger.

## Decisions

### Decision 1: One change, not two

A `cmd/worker` merged while `POST /upload` still processed inline would process every upload twice — once in the request, once from the queue. There is no ordering of two changes that avoids it, because the hazard is the overlap itself. The consumer and the handler's response contract therefore land together, and the change is large as a consequence.

### Decision 2: The claim is a conditional UPDATE in PostgreSQL

`StartProcessing` persists through `UPDATE video_jobs SET status = 'processing' … WHERE id = $1 AND status = 'queued'`. Zero rows affected means someone else won; the use case returns a distinct sentinel error and touches nothing.

Alternatives considered:

- **A Redis lease around pickup.** That is 6.4, and it is a *liveness* mechanism — it distinguishes a job someone is working on from one abandoned by a dead worker. It cannot be the correctness primitive here because it fails open, like every other Redis-backed feature in this system; a lease outage would then permit exactly the double-`ffmpeg` this decision exists to prevent.
- **An optimistic `version` column.** Equivalent safety, an extra column, and a compare-and-swap whose predicate says less than the status one does. The status *is* the claim.
- **`SELECT … FOR UPDATE` held across processing.** Holds a database transaction for the duration of an `ffmpeg` run. Rejected on sight.

The predicate is `status = 'queued'` and not something broader, and that is what makes the residual gap in Non-Goals real: a `processing` row cannot be re-claimed. Widening it is 6.4's job, and doing it here without fencing would let two workers write over each other.

### Decision 3: A separate repository method — `ClaimForProcessing`, not a conditional `Update`

The port gains `ClaimForProcessing`, named in full because `postgres.OutboxRepository.Claim` already exists in this context and claims something else entirely. `Update` is also `CompleteJob`'s and `FailJob`'s path. Making it conditional on the source status would put a claim predicate on transitions that are not claims, and would decide the concurrency semantics of the completion and failure writes in advance and by accident — the same argument that gave `Enqueue` its own method in 6.2. `CachedVideoJobRepository` implements the new method too, and **must not write through on a lost claim**: nothing changed in PostgreSQL, so populating the cache would publish a state the database does not hold.

### Decision 4: The generation moves to `.v2`, justified by the deploy window

`docs/roadmap.md` justifies the new generation by the toxic residue 6.2 left on `video.jobs.queued.v1`. **That justification is stale and this change corrects it.** With Decision 2 in place, residue is self-neutralizing: every message on `.v1` names a job the inline flow already drove to `completed` or `failed`, the claim returns zero rows, and the message is dead-lettered without side effects.

What a shared queue would not survive is the deploy window:

```
replica A (old, inline)              worker (new)
  Enqueue ──▶ relay ──▶ queue ────────▶ consume
  ProcessVideoJob                        claim OK — the job really is queued
    StartProcessing → LOST               │
  handler returns 500                    │ Get(source), ffmpeg …
  defer Delete(source) ──────────────────X  source deleted mid-run
                                         └─ FailJob
```

The atomic claim prevents double processing but not this: the loser's cleanup destroys the winner's input. Separate generations make the overlap impossible — old replicas publish to `.v1`, which nothing consumes; new replicas publish to `.v2`, which only the worker consumes. Versioning only the queue does not isolate anything, because a `direct` exchange delivers to every queue bound with the matching key.

### Decision 4a: The generation lives in the `event_type` too, not only in the exchange

Versioning the exchange alone does **not** achieve the isolation Decision 4 needs, and this is the correction that decision turned out to require. The crossing happens in the database, before anything is published:

```
        video_job_outbox  (one table, every replica's relay reads it)
                  │
      ┌───────────┴───────────┐
  relay (old)              relay (new)
  claims event_type=…      claims event_type=…
      │                        │
      ▼                        ▼
  video.jobs.v1            video.jobs.v2
```

`OutboxRepository.Claim` filters on `event_type` and nothing else. With one string across both generations, a new replica's relay claims an old replica's row and publishes it to `.v2` — the worker picks it up and the source-deletion race is back, unchanged — while an old replica's relay claims a new replica's row and publishes it to `.v1`, where nothing consumes it and the job waits in `queued` forever.

So the generation moves onto the `event_type` string as well: `video_job.queued.v2`. Since `videojob-messaging` pins the routing key equal to the event type, the routing key follows. The old relay's filter is a literal it will simply never match, and **that is the property that matters** — the old relay is already deployed and cannot be taught a new predicate, which is what rules out every fix that lives on the reading side.

Alternatives considered:

- **A `dispatch_generation` column on `video_job_outbox`, with the claim scoped to it.** Semantically the cleanest — the event *is* `video_job.queued` regardless of which topology carries it — and it does not work. The already-running old relay has no such predicate, so it would still claim new rows and publish them into the dead generation.
- **Deploy without overlap: stop every API replica, then start the new build.** Correct, and it is exactly the "documented sequence someone has to get right" that this change's whole generation scheme exists to avoid.
- **Accept the stranding and let 6.4 recover it.** 6.4 recovers a job stuck in `processing`. A job stranded in `queued` with its only message on a dead queue is not in that class and would need a different mechanism.

The cost is real and worth naming: 6.1 made the event type and the routing key one vocabulary deliberately, so bumping a generation now renames a persisted domain event. That coupling is the price of the single-string design, not a new mistake.

This also changes what Decision 6's migration is doing, and that decision is amended rather than left implying more than it delivers.

### Decision 5: Retiring `.v1` is an operator action, not a code path

The job queue carries no message TTL by design, so `.v1`'s residue does not drain. Nothing in this change deletes it either: a delete-on-startup would race a not-yet-redeployed replica still publishing into it. `docs/operations.md` gains the post-deploy step — delete `video.jobs.v1` and `video.jobs.queued.v1` once every replica is on the new build — and the change ships correct whether or not it is performed. An undeleted `.v1` is a bounded, idle queue.

### Decision 6: The outbox cutoff is a migration that stamps rows published

With Decision 4a in place, the *mechanism* excluding pre-existing rows is the event-type scoping: the new relay's claim cannot match `video_job.queued`, so those rows are unreachable to it regardless of when they were written. The migration is the *record*, not the mechanism, and stating it the other way round would overclaim.

It still earns its place. A migration stamps `published_at` on unpublished rows carrying the **previous** generation's event type, so their exclusion is a fact on each row rather than an inference a future reader has to reconstruct from two string literals — and so an old relay still running does not keep re-reading a set that will never shrink. It must be bounded to the previous generation's event type: one written against "any unpublished dispatch row" would stamp live work as published and drop it silently.

Letting Decision 2 dead-letter them was considered and rejected before Decision 4a existed; it is now moot, but the reason stands for the general case. "Most of them fail a predicate downstream" is an accidental boundary, and it would fill the dead-letter queue with a batch of expected garbage, degrading the one place operators look for anomalies.

### Decision 7: The idempotency clear moves to the worker, keyed by job ID

The handler cannot clear a failed job's key any more. Chosen: `video_jobs` gains `content_hash`, the queued payload carries it, and `IdempotencyStore` gains `ClearByJob(ctx, key, jobID)` — the finalized value is already `final:<token>:<jobID>`, so a compare-and-delete on the job ID is exactly as safe as one on the token (a key reclaimed by a newer request holds neither).

Alternatives considered:

- **Persist `idempotency_key` and `idempotency_token` on the job** (the roadmap's "cheap path"). Works, and stores a possession capability in a table that outlives the key's 24-hour window and is readable by everything that reads jobs — to buy nothing the job ID does not already buy.
- **Move the decision to the read side**: treat a duplicate whose job is `failed` as fresh. It renames the requirement (the word is "Immediately"), and `Reserve` is `SET NX`, so it needs a key-stealing path that the token protocol was written to prevent.

The worker clears on the failure path only. Success finalizes nothing new — the key already points at the job, which is the correct answer for a duplicate.

### Decision 8: Prefetch 1, manual ack after the terminal write commits

`basic.qos(prefetch: 1)` and acknowledgement after `CompleteJob`/`FailJob` has committed. Prefetch above 1 buffers messages inside a worker that can die; until 6.4 exists, every buffered message's job would be stranded in `queued` with its delivery lost — the opposite of what prefetch buys, given that the unit of work is minutes of `ffmpeg`, not microseconds. Acking before the write would lose the dispatch on a crash in between.

Message classes and their disposition:

| Class | Disposition | Why |
|---|---|---|
| Processed to `completed`/`failed` | `ack` | Terminal; the `video_jobs` row is the record |
| Unparseable payload | `nack`, no requeue → DLQ | Requeueing loops forever on a message that will never parse |
| Unknown job ID | `nack`, no requeue → DLQ | Same |
| Claim lost (not `queued`) | `nack`, no requeue → DLQ | A duplicate, or 6.4's stranded case; either way this worker must not touch it |
| Broker/infra error mid-run | `nack`, no requeue → DLQ | The job is already `failed` by `ProcessVideoJob`; a requeue could not claim it anyway |
| Terminal write will not commit after bounded retry | `nack`, no requeue → DLQ, **source kept** | The job is stuck in `processing` with a durable result; the bytes and the dead-lettered message are what a recovery works from |

Nothing is acked away silently: an acked message is gone from the broker, and the dead-letter queue is what keeps the anomalies enumerable.

### Decision 9: Source-object ownership follows the claim

The worker deletes the source object after a **committed** terminal outcome, success or failure alike — but only if it won the claim, and only if the terminal state actually committed. Two guards, and the second is the one easy to get wrong.

A deferred delete registered at claim time looks like the careful choice — it covers a panic — and it is the dangerous one. A panic mid-extraction, a `CompleteJob` write that will not commit, or an expired shutdown deadline all leave the job in `processing`, and the source bytes are the only thing from which such a job can ever be finished. Deleting them converts a job 6.4's fenced takeover could have recovered into one that can only be failed. The leak is the better outcome: a leaked object is reclaimable by the lifecycle rule; a deleted one is not.

A worker that lost the claim deletes nothing either, or it would destroy the winner's input — the same failure Decision 4 designs against, reintroduced from inside.

`handleVideoUpload`'s unconditional `defer` therefore becomes conditional: it still deletes on every path that fails *before* the enqueue commits (storage failure, `CreateVideoJob` failure, `EnqueueVideoJob` failure, a duplicate-content conflict), and stops deleting once the job is queued, because the object now belongs to whoever claims it.

The consequence, stated rather than discovered: a job that is never picked up leaks its source object permanently. `docs/operations.md`'s `uploads/`-prefix lifecycle rule stops being a backstop and becomes the only guarantee.

### Decision 10: `cmd/worker` wires its own composition root

The worker needs PostgreSQL, MinIO, Redis, and RabbitMQ. It does **not** need identity, the HTTP router, the rate limiter, or the outbox relay — the relay stays in `cmd/api` where 6.2 put it, and running it in both would double the polling load for no gain.

It gets its own `main.go` rather than a mode flag on `cmd/api`, so the hardened runtime image exposes two binaries with genuinely different configuration surfaces (`IDENTITY_*` is not required to start a worker). Shared wiring is not extracted in this change: the two roots overlap in the Video Processing adapters only, and factoring that out is a refactor with its own blast radius.

### Decision 11: Polling interval is specified against the rate-limit budget

The `202` names `GET /api/video-jobs/:id`. `app.js` polls it starting at **2 s**, backing off ×1.5 to a **10 s ceiling**. The per-user budget is 60 requests / 60 s and is shared by the upload, every poll, and the eventual download issuance.

A fixed 2 s poll costs 30 requests/minute sustained — half the budget for one job, and over it for two. With the backoff, a job settles at 6 requests/minute, which leaves room for concurrent uploads and for the browser's other calls. The ramp keeps short jobs feeling immediate, which is what a fixed long interval would cost.

## Risks / Trade-offs

- **The generation scheme now depends on two string literals matching across three call sites** (the outbox insert, the relay's claim, and the routing key) → They are one shared constant, `videojob-outbox-relay` requires it, and `TestRoutingKeyMatchesTheOutboxEventType` already pins two of the three; extend it to the third.
- **A job whose terminal write never commits sits in `processing` with a durable result and a dead-lettered message** → Bounded retry first, then logged with the job ID and result key so the orphan is enumerable, and the source object is deliberately kept so a recovery has something to work from. Reconciling it stays out of scope, as it was before this change.
- **A worker dies mid-`ffmpeg`; the job is stranded in `processing` forever** → Accepted and bounded: it fires on a crash, not on every upload; the symptom is operator-visible; the message is in the DLQ. 6.4 closes it, and Non-Goals says so rather than implying this change is complete.
- **A job whose message never reaches a worker leaks its source object** → Decision 9 states it; the `uploads/` lifecycle rule becomes the guarantee; `docs/operations.md` is updated to stop calling it a backstop.
- **`.v1` is never deleted** → The system stays correct; a bounded idle queue remains. Documented as a post-deploy step with the reason it cannot be automated.
- **The frontend polls harder than the budget allows in a multi-tab session** → Two tabs polling one job each settle at 12 requests/minute combined, well inside 60. A user who opens many tabs hits `429`, which the polling loop must treat as a back-off signal rather than a job failure — a task, not a hope.
- **The `202` breaks any non-bundled client** → Genuinely breaking, marked as such, and the reason Phase 6 exists. `ddd-architecture` already anticipated the response-schema change at this endpoint.
- **Two composition roots drift apart** → Accepted for now; the overlap is the Video Processing adapters, and the integration suite exercises both.

## Migration Plan

1. Deploy the new build. Old replicas write outbox rows under the old event type, which only their own relays claim, publish to `.v1`, and finish their in-flight inline work; new replicas write under the new event type, publish to `.v2`, and the worker consumes it. No row crosses.
2. The cutoff migration runs at startup with the others, stamping pre-existing unpublished `video_job.queued` rows published.
3. Once every replica is on the new build, delete `video.jobs.v1` and `video.jobs.queued.v1`.

Rollback is a redeploy of the previous build: it publishes to and consumes nothing on `.v2`, resumes inline processing, and leaves any `queued` jobs stranded — which is the same stranded class 6.4 addresses, not a new one. Jobs enqueued to `.v2` before the rollback are recoverable by rolling forward again.

## Open Questions

None blocking. One is deferred by design: whether the dead-letter queue ever warrants a reconciler, which stays unanswerable until there is operational data on what actually lands there.
