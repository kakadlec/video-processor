## Why

Every piece of the asynchronous pipeline now exists except the thing that makes it a pipeline. `add-rabbitmq-infrastructure` declared the topology, `add-videojob-source-key-and-outbox-relay` made the source key durable and gave the relay something to publish — and nothing consumes it. `POST /upload` still blocks on `ffmpeg` for the whole request, which is the problem Phase 6 exists to solve: a request holds a connection, an `ffmpeg` process, and an unbounded amount of wall-clock time, with no back-pressure and no way to survive a redeploy.

This is deliberately one change rather than two. A `cmd/worker` merged while `POST /upload` still processes in-request would process every upload twice — once inline, once from the queue — so the consumer and the handler's response contract have to land together.

## What Changes

### `cmd/worker` consumes the queue

- New `cmd/worker` entrypoint: dials the broker, declares the topology, consumes the job queue, and for each message runs `ProcessVideoJob` against the message's `(job_id, source_key)` — the seam `migrate-upload-storage-to-minio` gave `ProcessVideoJob` its storage-key signature for, since the worker shares no filesystem with `cmd/api`.
- It calls `CompleteJob` on success **only**. `ProcessVideoJob` already calls `FailJob` itself for the fetch, extraction, and storage failures, so a worker that also called `FailJob` would ask the domain for a rejected `failed → failed` transition.
- Errors arising *before* processing begins — an unparseable message, an unknown job, a job it cannot claim — are rejected **without requeue** and so dead-lettered. Never turned into a second `FailJob`, and never simply acked away: an acknowledged message is gone from the broker, leaving nothing to enumerate afterwards.
- The consumer's prefetch and acknowledgement policy is settled here, as `add-rabbitmq-infrastructure`'s `design.md` deferred it to this change.

### The claim becomes atomic

- `StartProcessing` persists through a **conditional** update (`… WHERE id = $1 AND status = 'queued'`); zero rows affected means another consumer won the race, and the use case returns a plain error without touching `FailJob`. A new repository port method, `ClaimForProcessing`, carries this — spelled out rather than `Claim`, because `postgres.OutboxRepository.Claim` already means something else here — and `CachedVideoJobRepository` carries it too, the same decorator obligation `Enqueue` incurred.
- This is the correctness primitive: the relay is necessarily at-least-once (a publish can succeed before `published_at` commits) and `Update` is a read-then-unconditional-write, so without it two deliveries would both pass `queued → processing` and both run `ffmpeg`.
- It is **not** a recovery primitive, and this change does not pretend otherwise. A worker that dies mid-`ffmpeg` leaves its job in `processing`; the redelivery cannot claim a non-`queued` row and is dead-lettered, and the job stays stranded until `add-worker-job-lock` adds the fenced takeover. The gap is bounded — it fires on a worker crash, not on every upload — the symptom is operator-visible (a job stuck in `processing`), and the dead-letter queue keeps the abandoned messages enumerable meanwhile.

### `POST /upload` stops blocking — **BREAKING**

- The handler stores the source, creates and enqueues the job, and returns **`202`** with the job ID and a status URL. It no longer calls `ProcessVideoJob`, `CompleteJob`, or `FailJob`, and no longer returns a finished result.
- `cmd/api/web/app.js` gains a polling loop against `GET /api/video-jobs/:id`. Its interval must leave headroom under the per-user rate limit (default 60 requests / 60 s), which the upload, every poll, and the eventual download issuance all share — polling being the dominant consumer.
- Ownership of the source object moves from the handler's `defer` to the worker, on success and terminal failure alike. A consequence to state rather than discover: a job never picked up (relay down, worker crash before ack, message dead-lettered) now **leaks** its source object, so `docs/operations.md`'s `uploads/`-prefix lifecycle rule stops being a backstop and becomes the only guarantee.
- The duplicate reply changes shape, not just meaning: an idempotent hit returns the same acknowledgement naming the existing job, with no outcome in it. The old three-way `completed`/`failed`/`processing` translation described a response that no longer exists, and the client learns the difference on its first poll.

### The idempotency clear moves to the worker

`upload-idempotency`'s "A Failed Job Clears Its Idempotency Key Immediately" names `handleVideoUpload` as the actor, and that handler stops learning the outcome once it returns `202`. Without a new owner, failed content would stay deduplicated for the full 24-hour window instead of being immediately retryable.

- `video_jobs` gains a `content_hash` column, and the queued event's payload carries it. The idempotency key is `hash(userID, contentHash)`, so the worker can reconstruct it.
- The `IdempotencyStore` port gains `ClearByJob(ctx, key, jobID)`. The finalized value is already `final:<token>:<jobID>`, so comparing on the `jobID` the worker already holds is exactly as safe as comparing on the token: a key reclaimed by a newer request holds neither that token nor that job ID.
- The ownership **token** is deliberately not persisted. It is a possession capability; putting it in `video_jobs` would make it durable far beyond the key's 24-hour window and readable by anything that reads that table, to buy nothing the job ID does not already buy.

### The topology moves to `.v2`, and `.v1` is retired by hand

- `JobDispatchTopology()` returns `video.jobs.v2` / `video.jobs.queued.v2`, **and the routing key — which `videojob-messaging` pins equal to the persisted outbox `event_type` — becomes `video_job.queued.v2` with it.** The dead-letter exchange and queue stay unversioned, as that spec already requires.
- **Versioning the exchange alone would not have isolated anything**, and that is the sharpest correction in this proposal. Every replica's relay reads one `video_job_outbox` table and claims by `event_type`, so with a shared string a new replica's relay would claim an old replica's row and publish it into `.v2` — the race below, unchanged — while an old replica's relay would claim a new replica's row and publish it into `.v1`, where nothing consumes it and the job waits in `queued` forever. The crossing happens before anything reaches the broker. Bumping the event type is what closes it, and it works because the already-deployed old relay's filter is a literal it can never match. A `dispatch_generation` column would read better and fails for exactly that reason: the old relay has no such predicate. The cost, stated rather than glossed: 6.1 made the event type and the routing key one vocabulary on purpose, so a generation bump now renames a persisted domain event.
- **The reason is the rolling-deploy window, not the residue** — and this corrects `docs/roadmap.md`, which justifies the new generation by residue. With the conditional claim above, residue is self-neutralizing: every message left on `.v1` names a job already `completed`/`failed`, the claim returns zero rows, and the message is dead-lettered harmlessly. What a shared queue would *not* survive is a deploy in which an old replica still processes inline: it enqueues, the relay publishes within seconds, a new worker legitimately claims a genuinely-`queued` job, and the old replica's `defer` then deletes the source object out from under the running `ffmpeg`. Separate generations make that impossible — the old replica publishes to `.v1`, which nothing consumes.
- Retirement of `.v1` is an **explicit deletion performed by an operator after the deploy**, not expiry and not a startup step (a delete-on-startup would race a replica still publishing into it): the shipped job queue carries no message TTL (an expired message would be dead-lettered without touching its `video_jobs` row, against a state machine with no edge out of `queued` except to `processing`), so its residue does not drain on its own.

### The outbox cutoff

`videojob-outbox-relay` carries an expiring requirement obliging the generation-switching change to define a cutoff for pre-existing unpublished `video_job.queued` rows *and* to modify that requirement in the same change. The event-type bump above is the mechanism — the new relay's claim cannot match the old string, which holds even for a row a not-yet-redeployed replica writes *after* the migration, as no timestamp could. A migration additionally stamps `published_at` on unpublished rows carrying the **previous** generation's event type, so the exclusion is a recorded fact rather than an inference from two string literals, and an old relay still running stops re-reading a set that will never shrink. Dispatching them instead would deliver jobs whose source objects the old inline flow already deleted.

## Capabilities

### New Capabilities
- `videojob-worker`: the `cmd/worker` entrypoint — consuming the job queue, its prefetch and acknowledgement policy, the run/reject decision for each message class, source-object ownership, the idempotency clear on failure, and its lifecycle and shutdown.

### Modified Capabilities
- `videojob-execution`: `ProcessVideoJob`'s sequence requirement gains the lost-claim outcome and a worker caller; "POST /upload Completes The Job Only After Its Result Is Durably Usable" is **removed** (its clauses move to `videojob-worker`, since `handleVideoUpload` is no longer an actor in it) and a new requirement defines the `202` response contract.
- `videojob-lifecycle`: "EnqueueVideoJob, StartProcessing, CompleteJob, and FailJob Persist One State Transition Each" — `StartProcessing` persists conditionally, and losing the claim is a distinct, non-failing outcome.
- `videojob-persistence`: the repository gains the conditional start-processing path and the `content_hash` column; the outbox cutoff migration lands here.
- `videojob-status-cache`: "Cache Reflects The Latest State Transition Write" — the decorator carries the conditional method, and must not write through on a lost claim.
- `videojob-messaging`: "The Job-Dispatch Topology Is Pinned" — `.v2` names, with the deploy-window justification and the explicit retirement of `.v1`.
- `videojob-outbox-relay`: the expiring "Pre-Existing Unpublished Rows Are Bounded by the Cutover, Not by This Change" is discharged and replaced by the cutoff this change performs; and "The Claim Is Scoped to One Event Type" gains its second purpose — the filter is what isolates dispatch generations, not just what keeps internal events off the queue.
- `upload-idempotency`: "A Failed Job Clears Its Idempotency Key Immediately" (actor becomes the worker, via `ClearByJob`); "A Finalized Duplicate Returns The Existing Job…" (a duplicate now returns an unfinished job); the key-derivation requirement gains the persisted content hash.
- `videojob-source-storage`: "Every Request Deletes Its Own Source Object" — the owner becomes the worker, and a never-picked-up job leaks its source.
- `video-frame-extraction`: "Original Upload Cleanup After Successful Processing" and "Source Object Removed On Processing Failure" — same actor change.
- `video-processing-access`: "Existing synchronous processing remains functional behind access control" is **removed** — this change is exactly what it forbade — and replaced by the equivalent guarantee stated over the asynchronous path, plus the rule that the worker makes no access-control decision at all.
- `ddd-architecture`: the "POST /upload remains available during async migration" scenario is reconciled against the `POST /api/video-jobs` endpoint that already exists — `/upload` is the async endpoint, `/api/video-jobs` keeps having no trigger; and "Monorepo Package Topology" gains `cmd/worker`.
- `videojob-http-api`: `GET /api/video-jobs/:id` becomes the status URL the `202` names and the frontend polls; "Jobs Created Through This API Have No Processing Trigger" survives and is restated against the new flow rather than left ambiguous.
- `container-image`: a second binary and a `worker` service — "Multi-Stage Image Build" and "Unchanged External Contract".
- `rate-limiting`: "Authenticated Video Routes Are Rate Limited Per User" — polling is now a first-class consumer of the per-user budget, and the interval must be specified against it.

## Impact

- **New**: `cmd/worker/`, a `worker` service in `docker-compose.yml` and a second binary in the `Dockerfile`, a `content_hash` column and the cutoff migration, `IdempotencyStore.ClearByJob`, a conditional start-processing repository method (`ClaimForProcessing`), and an AMQP consumer beside the existing publisher.
- **Renamed on the wire and in the database**: the dispatch event type and routing key become `video_job.queued.v2`. Existing rows keep the old value and are excluded by construction.
- **Changed**: `cmd/api/video.go` (`handleVideoUpload` returns `202`), `cmd/api/web/app.js` (polling), `internal/video/application/{start_processing,process_video_job}.go`, `internal/video/infrastructure/{postgres,cache,messaging,idempotency}`, `internal/video/domain/{repository,idempotency_store,video_job}.go`.
- **Breaking**: `POST /upload` returns `202` with a job reference instead of `200` with a finished result. Any client other than the bundled frontend must be updated.
- **Operational**: `.v1` must be deleted after the deploy; the `uploads/`-prefix lifecycle rule becomes load-bearing rather than a backstop.
- The full-flow non-regression `ddd-architecture` requires (upload → poll → download) is a merge gate here, not optional diligence.
