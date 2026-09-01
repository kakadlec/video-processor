## Why

`migrate-upload-to-async-processing` shipped a deliberate, documented gap: `ClaimForProcessing` makes duplicate delivery harmless, but a worker that dies mid-`ffmpeg` leaves its job stored as `processing` with no lease, no timeout, and no edge back to `queued`. Nothing redelivers it, no redelivery could claim it if it arrived, and no mechanism reclaims it. The uploader polls forever, the source object leaks, and an operator's only recourse is the triage query in `docs/operations.md`. This is the last row of Phase 6 and the thing that lets the worker be described as crash-recoverable.

It also discharges the fourth Redis responsibility `add-redis-infrastructure` deferred from Phase 4 to Phase 6 — `ddd-architecture`'s "Distributed worker lock is deferred, not dropped" scenario, and `redis-infrastructure`'s matching disclaimer.

**The roadmap row's mechanism does not work as written, and this proposal corrects it.** The row specifies a lease plus a widened claim predicate plus fencing, and never says where the *second delivery* comes from. Trace a worker crashing mid-extraction against the shipped `messaging.Consumer`: the connection drops, the broker requeues the unacked delivery immediately, and the redelivery arrives seconds later — while the lease it is supposed to find lapsed is still live. `handle` rejects it to the dead-letter queue, and the lease expires minutes later with no message left to redeliver. The only redelivery AMQP ever produces arrives strictly *before* expiry can be observed, so a lease and a fence on their own recover exactly zero jobs. A recovery trigger is the substance of this change; the lease is what tells it when to fire.

## What Changes

### A lease says whether someone is still working on a job

- New `videojob-lease` port and a Redis adapter beside the idempotency store, keyed `videojob:lease:<jobID>` with the claiming worker's fence epoch as its value and a TTL shorter than a plausible extraction.
- `ProcessVideoJob` acquires the lease the moment it wins the PostgreSQL claim and renews it from a heartbeat goroutine for as long as the extraction runs — it is the only component that observes that moment. The worker releases it once the outcome is committed, which is where the outcome is decided; the release is conditional, so performing it elsewhere is safe. Renewal is likewise conditional on the stored value still naming this worker's epoch, so a lease already taken over is never extended by the worker that lost it.
- The lease is a **liveness** signal and nothing else. It is never consulted by `ClaimForProcessing`, which keeps its unconditional `WHERE … AND status = 'queued'` predicate. Redis therefore stays out of the correctness path entirely — the correction below explains why that matters more than it looks.

### Recovery is a sweeper that re-enqueues, not a widened claim predicate

- `cmd/worker` gains a periodic sweeper goroutine, sibling to the consumer in the same way the relay is a sibling in `cmd/api`. Each cycle it reads a bounded batch of `processing` rows, asks the lease store whether each is still held, and for those that are not, **requeues** them: one transaction writing `processing → queued`, an incremented fence epoch, and a `video_job.queued.v2` outbox row — the same row `Enqueue` writes, published by the same relay, consumed by the same worker path. Recovery reuses dispatch rather than inventing a second one.
- The requeue is conditional on `status = 'processing' AND lease_epoch = <the epoch the sweep observed>`, so two sweepers in two worker replicas race on one statement and exactly one wins, exactly as `ClaimForProcessing` does.
- **This replaces the roadmap row's "widen the claim predicate to admit a lapsed-lease `processing` row" with a backwards `processing → queued` edge, and the `processing → processing` self-transition the row anticipated does not appear.** The reason is the one the row itself gets wrong: widening the predicate makes the claim *depend on the lease*, and the lease is Redis-backed and fails open like everything else here. A Redis outage would then permit two workers to claim one live job — the exact double-`ffmpeg` the conditional claim exists to prevent, promoted from "wasted work" to "two writers". Keeping the predicate literal and putting the lease only in the recovery path preserves the claim's unconditional correctness. The cost is a genuine backwards state-machine edge, stated and specified rather than smuggled in as an implementation detail.
- Recovery **cannot** be triggered by the broker's own redelivery, and that is why the sweeper exists rather than a delayed-retry queue: the redelivery is immediate by construction, and a dead-letter-with-TTL bounce would have to guess a delay that exceeds every extraction.

### The fence lives in PostgreSQL, not in Redis

- `video_jobs` gains a `lease_epoch BIGINT NOT NULL DEFAULT 0` column, additive in both the fresh-schema and `ALTER TABLE … IF NOT EXISTS` paths, following `source_key` and `content_hash` exactly. It is incremented by `ClaimForProcessing` and by every requeue.
- `Update` — the write behind `CompleteJob` and `FailJob` — becomes **conditional on the epoch the caller claimed with**, returning a new `domain.ErrJobFenced` when no row matches. The epoch is carried forward from `StartProcessing` through `ProcessVideoJobResult` into `CompleteJobInput`/`FailJobInput`; it is emphatically **not** re-read from the row inside the use case, which would read the new holder's epoch and defeat the fence.
- This is the load-bearing half of the change. `add-worker-job-lock`'s roadmap row justifies failing open with "the PostgreSQL claim still prevents state corruption", which stops being true the moment recovery exists: a resurrected worker whose job was requeued and re-claimed must not be able to write `completed` or `failed` over the new holder's work. A fence token in Redis could not do this — it has to be in the same transaction as the state it guards.
- Fail-open is redefined for a takeover specifically: **without positive evidence that a lease has lapsed, the sweeper does not requeue.** A Redis error means "cannot tell", and declining preserves today's behaviour (the job stays stranded) instead of inverting it into a licence to take over a live job. Acquiring a lease still fails open — that path is protected by the unconditional claim.

### Repeated abandonment ends in a terminal state, not a loop

- `lease_epoch` doubles as the attempt count. A job that has been requeued a bounded number of times — a video that reliably kills the process, say — is not requeued again: the sweeper fails it with a stated reason through the same fenced `FailJob`, and then, **only if that write was its own and it committed**, deletes the source object and clears the idempotency key, matching the worker's own terminal-failure disposition. A job whose `source_key` is empty — the pre-column rows the first sweep is meant to recover — is failed on sight rather than requeued: re-dispatching it would produce a message no worker can act on, and requeueing it forever is the loop this bound exists to prevent.
- Without this, the sweeper is an unbounded retry loop around whatever crashed the worker in the first place, and a single poison job takes down every replica forever.

### The worker's disposition table gains one row

- A terminal write that comes back `ErrJobFenced` rejects the message to the dead-letter queue, logs the job and result key, and **does not delete the source object** — the new holder is reading it right now. That is the same reasoning as the existing "could not be marked completed" path, for a different cause.

## Capabilities

### New Capabilities
- `videojob-lease-recovery`: the lease port and its Redis adapter, the fence epoch and its conditional terminal write, the sweeper's scan/requeue/abandon policy, and the failure posture of each (acquire fails open, takeover fails closed).

### Modified Capabilities
- `videojob-lifecycle`: "JobStatus Transition Validity Is an Independently Testable Pure Function" gains the `processing → queued` requeue edge — the first backwards edge in this state machine, admitted deliberately and only for recovery; and "EnqueueVideoJob, StartProcessing, CompleteJob, and FailJob Persist One State Transition Each" gains the fence epoch that `StartProcessing` returns and `CompleteJob`/`FailJob` are conditional on.
- `videojob-persistence`: the `lease_epoch` column and its additive migration; "Update Persists a VideoJob's Transitioned State" becomes a fenced conditional write; "ClaimForProcessing Persists the Processing Transition Only If the Job Is Still Queued" also stamps the new epoch and returns it; and a new requeue path writes the queued transition and its outbox event transactionally from somewhere other than `Enqueue`.
- `videojob-status-cache`: "Cache Reflects The Latest State Transition Write" — the decorator carries the new repository methods, and a write-through must not happen for a write the fence rejected.
- `videojob-worker`: the worker acquires, renews, and releases the lease around each job; runs the sweeper as a second goroutine with its own shutdown join; "A Message the Worker Cannot Act On Is Dead-Lettered…" gains the fenced-write case; "The Worker Deletes the Source Object, and Only If It Won the Claim" gains the "and only if it still holds the fence" clause; "The Worker Clears a Failed Job's Idempotency Key" gains the sweeper as a second actor.
- `videojob-execution`: "ProcessVideoJob Runs a VideoJob's Start/Extract Sequence Synchronously" — the epoch won at `StartProcessing` is carried through the sequence and into the failure write, so `ProcessVideoJob`'s own `FailJob` is fenced too.
- `ddd-architecture`: "Redis Responsibilities Are Additive" moves from three responsibilities to four and the "Distributed worker lock is deferred, not dropped" scenario is discharged — with the correction that what ships is a lease for liveness plus a PostgreSQL fence, not a lock that gates pickup; and "Valid State Machine Transitions Only" admits the one named backwards edge.

`openspec/specs/redis-infrastructure/spec.md` carries the matching disclaimer in its **overview** rather than in a requirement ("no distributed lock (Phase 6, not Phase 4)"), so it is not expressible as a delta. That one sentence is corrected directly at finalization, and `tasks.md` carries it as an explicit step rather than leaving it to be noticed.

## Impact

- **New**: `internal/video/domain/job_lease.go` (port) and `internal/video/infrastructure/lease/` (Redis adapter); a sweeper in `cmd/worker`; a heartbeat goroutine around each extraction; `domain.ErrJobFenced`.
- **Schema**: `video_jobs.lease_epoch`, additive with a `DEFAULT 0`, no backfill needed — a pre-existing `processing` row holds epoch `0` and is exactly what the first sweep recovers.
- **Changed**: `internal/video/domain/{job_status,repository,video_job}.go`, `internal/video/application/{start_processing,process_video_job,complete_job,fail_job}.go`, `internal/video/infrastructure/{postgres,cache}`, `cmd/worker/main.go`.
- **Not changed**: `cmd/api` wires no lease and runs no sweeper — recovery is the worker's concern, and a deployment with no worker has nothing to recover. `cmd/api/web/app.js` needs no edit either: `pollJob` already treats any non-terminal status as "keep polling" and renders a job that flipped back to `queued` as "na fila".
- **Observable**: a job's reported status can now move backwards, `processing → queued`, exactly once per abandonment. `GET /api/video-jobs/:id` reports it as it reports everything else.
- **Operational**: drain old workers before starting new ones on the deploy that introduces this. A job in flight across the deploy is held by a worker that sets no lease and honours no fence, so the first sweep can requeue it and duplicate one extraction. Bounded, one-shot, and it cannot corrupt state — both writers name the same result key, and the aggregate rejects a terminal transition on an already-terminal job. `docs/operations.md`'s stuck-`processing` triage query and `CLAUDE.md`'s "do not describe the current worker as crash-recoverable" both become stale and are corrected at finalization.
