## Why

`video_job_outbox` has held an unpublished row for every job created since Phase 3, waiting for a relay that did not exist. `add-rabbitmq-infrastructure` supplied the broker connection and the topology; nothing publishes into them. Meanwhile the worker Phase 6 is building toward needs `(jobID, sourceKey)` to call `ProcessVideoJob`, and the source key exists today only as a local variable inside `handleVideoUpload` — it embeds a generated `uploadID`, so it is not reconstructible from any column, and `video_jobs.storage_key` holds the **result** key set at `CompleteJob`.

Three things therefore have to land together, because none of them is useful or even coherent alone: the source key has to become durable state, the `pending → queued` transition has to move to where it can write an outbox row transactionally, and the relay has to exist to carry those rows to the broker.

## What Changes

### The source key becomes part of the aggregate

- `VideoJob` gains a **source** `StorageKey`, distinct from the result `StorageKey` it already has. `NewVideoJob`, `RestoreVideoJob`, and `CreateVideoJobInput` all carry it; `video_jobs` gains a `source_key TEXT NOT NULL DEFAULT ''` column.
- `Enqueue()` SHALL reject a job whose source key is empty: a job with no stored source cannot be queued for processing. That is the invariant, and it is enforced **only on the transition** — `RestoreVideoJob` deliberately does not pair `SourceKey` with status the way it pairs `StorageKey` with `completed` and `ErrorReason` with `failed`. Pairing it would turn a pure add-column migration into a data hazard: any pre-existing row sitting in `queued` or `processing` (a crash or client disconnect mid-`/upload` leaves one) would become unloadable, and `FindByID` would return a domain error instead of a job.
- `POST /api/video-jobs` creates jobs with no source object at all. They therefore carry an empty source key and cannot be enqueued, which is the same "stays `pending` forever" guarantee that endpoint already documents — now enforced by the domain rather than by the absence of a caller.

### The enqueue transition moves to the API and writes its own event

- `POST /upload` calls `EnqueueVideoJob` itself, immediately after `CreateVideoJob`. This is where `docs/domain-model.md`'s use-case table has always put it — actor "API (post-upload)", post-condition "`queued`; message published" — and where the transition can be committed in the same transaction as the event describing it.
- New repository port method `Enqueue`, persisting the `pending → queued` update **and** a `video_job.queued` outbox row in one transaction. `Update`'s documented "writes no outbox row" contract is untouched, which matters because `CompleteJob`/`FailJob` go through it and Phase 7 will want their own events there on its own terms. `CachedVideoJobRepository` implements the new method, write-through like `Update`.
- The persisted `event_type` is `video_job.queued`, following the `video_job.created` constant already in `internal/video/infrastructure/postgres`, and shared as one constant between the insert and the relay's filter so the two cannot drift apart silently.
- `ProcessVideoJob` drops its own `EnqueueVideoJob` call in the same change — leaving it would ask the domain for a rejected `queued → queued` transition on the very next line. Its sequence becomes `StartProcessing` → fetch → extract → store.

### The relay

- New `OutboxRepository` in `internal/video/infrastructure/postgres`: claims unpublished rows of one event type with `SELECT … FOR UPDATE SKIP LOCKED` and marks them published. The event-type filter is not an optimization — the table already holds an unpublished `video_job.created` row for every job ever created, and an unfiltered claim would re-read that backlog on every poll and starve the rows that matter. A supporting partial index comes with it.
- New `Relay` in `internal/video/infrastructure/messaging`, composing that repository with a `Publisher` over the `internal/platform/rabbitmq` connection. It declares the topology on every successful dial, then publishes to `JobDispatchTopology()` with delivery mode 2 and **no** per-message `expiration`, **mandatory**, on a channel in confirm mode — and stamps `published_at` only for messages the broker both acknowledged **and** did not return. Acknowledgement alone is not enough: RabbitMQ acks a mandatory publish it could not route, after returning it, so stamping on the ack would record a dispatch that reached no queue.
- `cmd/api` starts the relay as a goroutine and stops it on shutdown. `RABBITMQ_URL` becomes **required configuration** at startup; the connection itself is owned by the relay and retried rather than fatal. See `design.md` for why this is neither MinIO's fail-closed posture nor Redis's fail-open one.

### What this deliberately does not do

Nothing consumes the queue. Every message this relay publishes names a job that the same request has already driven to `completed` or `failed`, with its source object deleted — `POST /upload` still processes in-request. **Published** residue is inert by construction: the cutover consumes a different queue, so nothing can ever read those messages. It does not drain on its own either, because the job queue carries no message TTL; it sits until the cutover deletes that queue, and if it reaches the queue's maximum length first, `reject-publish` stalls this relay with rows unstamped — nothing lost, uploads unaffected, resuming on retirement.

**Unpublished** rows are a different matter, and a separate queue does not isolate them. A row that was never successfully published — nacked against a full queue, or written while the broker was unreachable — is still claimable, so a relay later switched to the cutover's generation would deliver it there: a dispatch for a job already `completed` with its source deleted. This change cannot fix that on its own, because the cutover is what switches generations; what it can do is not pretend otherwise. The requirement is recorded for that change: it must define a cutoff for pre-existing unpublished rows before it starts publishing into the new generation, and `video_job_outbox.occurred_at` is what makes a cutoff expressible.

## Capabilities

### New Capabilities
- `videojob-outbox-relay`: the transactional-outbox relay — claiming unpublished rows, publishing them to the broker, and marking them published — plus its concurrency guard, its failure handling, and its lifecycle inside `cmd/api`.

### Modified Capabilities
- `videojob-lifecycle`: `CreateVideoJob` accepts and persists a source key; `EnqueueVideoJob` persists through the new transactional method rather than `Update`, and rejects a job with no source key.
- `videojob-persistence`: `video_jobs` gains `source_key`; the repository gains `Enqueue`, which writes a second outbox event type; `Update`'s no-outbox contract is restated as deliberate rather than incidental.
- `videojob-execution`: `ProcessVideoJob` no longer calls `EnqueueVideoJob`; its sequence and the handler's ordering change accordingly.
- `videojob-messaging`: the persistent-publish requirement stops saying no publisher exists and points at the relay that now satisfies it.
- `rabbitmq-infrastructure`: two passages. The "No composition root opens a connection yet" scenario — which that spec already declared this change must modify — and the testing requirement's claim that no entrypoint's `TestMain` needs a broker.

## Impact

- **Schema migration**, additive only: `ALTER TABLE video_jobs ADD COLUMN source_key TEXT NOT NULL DEFAULT ''` and a partial index on `video_job_outbox`. No backfill, and no existing row becomes unloadable — that is what keeps the `Enqueue`-only invariant above from being a nicety.
- **New required configuration**: `RABBITMQ_URL` must be set at startup. `docker-compose.yml` and CI gain it for the `app` and `app-test` services.
- **`cmd/api` opens an AMQP connection for the first time**, so its `TestMain` gains the variable as a prerequisite alongside `ffmpeg` and MinIO.
- No HTTP contract changes. `POST /upload` still blocks and still returns a finished result; the job simply passes through `queued` on its way, and an outbox row and a broker message are produced as a side effect. `GET /api/video-jobs` may now show a job in `queued` for the moments before processing starts.
