## Context

Three facts constrain this change from outside, and each has already cost a review round somewhere in Phase 6.

**The state machine has no edge out of `queued` except to `processing`.** Anything that moves a message out of the job queue without also moving the row leaves a job reporting `queued` to its owner forever. That is why `add-rabbitmq-infrastructure` shipped the job queue with no message TTL, and it constrains what this change may do when a publish fails.

**`Update` writes no outbox row, deliberately and documented.** `CompleteJob` and `FailJob` go through it, and Phase 7 will want `VideoJobCompleted`/`VideoJobFailed` events on its own terms. Making `Update` outbox-aware to get one event would decide Phase 7's design as a side effect.

**`RestoreVideoJob` enforces cross-field invariants.** `StorageKey`↔`completed` and `ErrorReason`↔`failed` are both paired there, so extending the pattern to a new field looks like the consistent move — and here it is a data hazard. That is decision 1.

## Goals / Non-Goals

**Goals:**

- Make the source key durable state on the `VideoJob`, since the worker cannot reconstruct it.
- Move the `pending → queued` transition to where the event describing it can be written in the same transaction.
- A relay that carries `video_job.queued` rows to the broker exactly once per row under normal operation, at least once under failure, and never silently drops one.
- Safe concurrent operation across multiple `cmd/api` replicas.

**Non-Goals:**

- Consuming anything. No `cmd/worker`, no acknowledgement policy, no processing. The messages this relay publishes are read by nothing.
- Making `POST /upload` non-blocking. It still runs `ProcessVideoJob` in-request and still returns a finished result; only the transition sequence changes.
- Publishing `VideoJobCompleted`/`VideoJobFailed`. Phase 7's, unwritten by any code.
- Retiring or draining `video.jobs.queued.v1`. The cutover owns that.
- A dead-letter reconciler. Nothing dead-letters yet.

## Decisions

### Decision 1: The source-key invariant lives on `Enqueue`, not on `RestoreVideoJob`

A job with no stored source cannot be queued for processing — that is a real domain rule, and `Enqueue()` enforces it.

`RestoreVideoJob` deliberately does **not** pair `SourceKey` with status, breaking the pattern it already applies to `StorageKey` and `ErrorReason`. The reason is the migration: `ALTER TABLE video_jobs ADD COLUMN source_key TEXT NOT NULL DEFAULT ''` gives every existing row an empty source key, and a row can legitimately be sitting in `queued` or `processing` right now — `/upload` drives `pending → queued → processing → completed` inside one request, so a crash or a client disconnect strands one. Pairing the field at reconstitution would make those rows unloadable: `FindByID` would return a domain error rather than a job, `GET /api/video-jobs` would fail for their owner, and the failure would arrive at deploy time with no obvious cause.

Two alternatives were considered. Backfilling is not possible — the key embeds a generated `uploadID` that exists nowhere else. Migrating stranded rows to `failed` would be a data change smuggled into a schema migration, deciding on those users' behalf that their jobs are dead. Enforcing on the transition keeps the migration a pure add-column and puts the rule exactly where it can be checked against the caller that has to satisfy it.

### Decision 2: A dedicated `Enqueue` repository method, not an outbox-aware `Update`

`Enqueue(ctx, job)` persists the `pending → queued` update and inserts the `video_job.queued` outbox row in one transaction, mirroring what `Create` already does for `video_job.created`.

The alternative — teaching `Update` to write an outbox row when the status is `queued` — is shorter and wrong in a way that compounds: `Update` is also `CompleteJob`'s and `FailJob`'s path, so the outbox behavior would become a status-dependent side effect of a general-purpose method, and Phase 7 would inherit whatever shape this change happened to give it. A separate method leaves `Update` exactly as documented and leaves Phase 7 free.

`CachedVideoJobRepository` implements `Enqueue` write-through, the same as `Update`: PostgreSQL first, then an unconditional `SET` of the cache entry. The decorator must not skip it — a job that stayed `pending` in the cache while `queued` in PostgreSQL would make `GET /api/video-jobs/:id` disagree with the row the relay is about to publish.

### Decision 3: The outbox read lives in `postgres`; the relay lives in `messaging`

`OutboxRepository` (`internal/video/infrastructure/postgres`) claims and marks rows. `Relay` (`internal/video/infrastructure/messaging`) composes it with a `Publisher` over the `internal/platform/rabbitmq` connection.

The alternative puts both in `messaging`, which then imports both a database driver and an AMQP client. Splitting them keeps each infrastructure package to one driver, matches where every other PostgreSQL access already lives, and makes the `FOR UPDATE SKIP LOCKED` claim testable with no broker running at all — which matters, because that guard is the part most likely to be wrong and the part hardest to notice being wrong.

### Decision 4: Claim, publish, and stamp inside one transaction

```
BEGIN
  SELECT … WHERE event_type = 'video_job.queued' AND published_at IS NULL
    ORDER BY occurred_at LIMIT n FOR UPDATE SKIP LOCKED
  → publish each, awaiting the broker's confirmation
  UPDATE … SET published_at = now() WHERE id = ANY(acknowledged)
COMMIT
```

This holds row locks across a broker round trip, which is the cost. `SKIP LOCKED` means another replica passes over the locked rows rather than blocking, the batch is bounded, and the confirmation wait is bounded — so the exposure is a small number of rows locked for a short, capped interval.

The alternative — commit the claim first, publish afterwards — releases the locks sooner and loses rows: a crash between the two leaves a row marked published that was never published, and nothing would ever republish it. That is a silent, permanent loss of a job dispatch, which is the one outcome an outbox exists to prevent.

The failure this ordering does allow is the safe one: a crash after the broker acknowledged but before the commit leaves the row unstamped, so a later poll republishes it. That is at-least-once delivery, which the consumer has to tolerate regardless — a redelivery after a nack or a worker crash produces the same duplicate.

### Decision 5: Stamping requires an acknowledgement *and* a routing guarantee, and a nack is not an error

The publishing channel runs in confirm mode, and publishes are **mandatory**. Confirms alone are not enough, and the gap is easy to miss: a confirmation says the *exchange* accepted the publish, not that any queue received it, so a non-mandatory publish to an exchange with no matching binding is acknowledged and discarded. Stamping on that confirmation would record a dispatch that reached no queue — permanently, silently, and in the one component whose entire purpose is that this cannot happen. Mandatory publishing turns the silent discard into a `basic.return`, which the relay correlates with its confirmation and treats as not-published. A message the broker nacks — which is what `reject-publish` returns when the job queue is at its maximum length — leaves its row unstamped, so the next poll retries it. Nothing is lost and nothing is logged as a failure beyond a warning; a full queue is back-pressure, and back-pressure is the designed behavior, not an incident.

The consequence worth stating because it will look alarming: if the job queue fills while nothing consumes it, this relay stalls and unpublished outbox rows accumulate indefinitely. Uploads are unaffected — they still complete in-request — and the backlog drains when the cutover retires that queue. A relay that treated a nack as fatal, or that stamped regardless, would turn that benign state into either a crash loop or silent data loss.

Publishing uses delivery mode 2 and leaves the per-message `expiration` property unset, both required by `videojob-messaging`. The second is easy to omit and invisible when omitted: RabbitMQ honours `expiration` independently of the queue's arguments, so setting one would expire a job message into the dead-letter queue even though the queue deliberately has no TTL.

### Decision 6: `RABBITMQ_URL` is required configuration; the connection is not a startup gate

This is deliberately neither of the two existing postures, and the difference is worth naming because a reviewer will reach for one of them.

- **Config presence is fail-fast.** An unset `RABBITMQ_URL` stops startup, like every other required variable. That catches misconfiguration at deploy time, which is what fail-fast is actually for.
- **Connectivity is not.** The relay owns the connection, dials it in its own goroutine, and retries with backoff on failure or loss.

MinIO is fail-closed because a result that cannot be stored cannot be delivered — the failure is in the request path. The relay is not in any request path: `POST /upload` neither publishes nor waits on the broker, so a broker outage delays dispatch and costs nothing else. And the relay needs reconnection logic regardless, because an AMQP connection can drop at any time and `NotifyClose` has to be handled — once that loop exists, making the *first* dial fatal buys nothing that requiring the variable does not already buy, while coupling API availability to broker availability.

Redis's fail-open is also the wrong analogy: those features degrade a request by skipping an optimization. Here there is no request to degrade; there is a background loop that either runs now or runs shortly.

### Decision 7a: Unpublished rows are not isolated by a separate queue

The cutover consumes a different generation, which isolates every message this relay **published**. It does not isolate rows this relay never managed to publish — nacked against a full queue, or written while the broker was down. Those stay claimable, so a relay later pointed at the new generation would deliver them there: dispatches for jobs already `completed`, with their sources deleted.

This change cannot close that, because switching generations is the cutover's act, and defining the cutoff is therefore the cutover's decision. What this change owes is the honest statement plus the affordance: `video_job_outbox.occurred_at` is recorded on every row, so a cutoff is expressible without a schema change. The claim to avoid making — and which an earlier draft of this proposal did make — is that a separately named queue means no residue can reach the worker.

### Decision 7: Relay lifecycle and pacing

The relay declares the topology on every successful dial, before opening its publishing channel. Nothing else ever declares it — `add-rabbitmq-infrastructure` shipped the descriptor and the declaring function without a caller — so against a fresh broker the exchange simply does not exist, and a publish to a missing exchange closes the channel rather than failing routably. Declaring on connect rather than once at startup is what makes a reconnect correct too: if the broker was recreated while the relay was disconnected, the redeclaration restores the topology instead of publishing into nothing.

One goroutine per `cmd/api` process, started after the module is wired and stopped on shutdown via context cancellation, draining its in-flight transaction first. It polls on a fixed interval with a bounded batch size, both compile-time constants — no new environment variables, matching the status cache's fixed TTL.

Polling rather than `LISTEN`/`NOTIFY`: a notification is lost if no replica is listening at that instant, so a poll would be needed as a backstop anyway, and the poll alone is correct. The interval is chosen so a stalled or restarting replica adds seconds of dispatch latency, not minutes — the relay's latency lands entirely in the user's wait once the cutover ships.

### Decision 8: `cmd/api`'s `TestMain` requires the variable, not a broker

`rabbitmq-infrastructure` says the change that first makes the broker load-bearing for an entrypoint owns tightening that entrypoint's `TestMain`. This is that change, and the honest tightening is narrow: `TestMain` requires `RABBITMQ_URL` to be **set**, matching the startup requirement, and does not require a reachable broker — because `cmd/api` itself does not. A test suite that demanded a live broker would be asserting a stronger contract than the code has.

The relay's own behavior against a real broker is covered by `internal/video/infrastructure/messaging`'s tests, which skip without `RABBITMQ_TEST_URL` like every other adapter suite.

## Risks / Trade-offs

- **The `Enqueue`-only invariant means a `queued` row with an empty source key is representable** → It can only arise from data that predates this change, never from code: `Enqueue` is the sole path into `queued` and rejects it. Such a row stays loadable and is simply never dispatched — it has no `video_job.queued` outbox row (it predates the event) and the relay ignores its `video_job.created` row by design, so no message is produced for it at all. It is stranded rather than mis-dispatched, and recovering one is an operator action (fail it, or re-upload), not something this change automates.
- **Row locks are held across a broker round trip** → Bounded batch, bounded confirm wait, and `SKIP LOCKED` so concurrent replicas step over rather than queue behind. The alternative loses rows permanently, which is worse than a brief lock.
- **A stalled relay is quiet**: publishes nacked, outbox rows accumulating, no user-visible symptom while `/upload` still completes in-request → Accepted for this window and logged with the row id and the queue's state. The cutover, which makes dispatch load-bearing, is where this becomes worth alerting on.
- **Two replicas both polling adds contention** → `SKIP LOCKED` is what makes it correct; the cost is wasted polls, not duplicates.
- **`GET /api/video-jobs` can now briefly show `queued`** where it previously went straight from `pending` to `processing` → Correct behavior, not a regression: the job genuinely is queued. Worth noting because it is an observable change with no HTTP contract change behind it.

## Migration Plan

Additive schema only: one column with a default, one partial index. No backfill, no data migration, no row becomes unloadable. Rolling back the code leaves both in place, harmless — the column goes unread and the index unused.

One ordering constraint: `RABBITMQ_URL` must be set before the new image starts, or the container exits at startup like it does for a missing `VIDEO_MINIO_ENDPOINT`.

## Open Questions

None blocking. Deferred to the changes that own them: whether dispatch latency warrants alerting (the cutover, which makes it user-visible), and whether unpublished-row age warrants a metric (Phase 8's observability work).
