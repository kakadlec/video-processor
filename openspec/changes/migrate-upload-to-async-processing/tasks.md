# Tasks

Sections 1–10 are implementation-scoped and ship in the implementation PR. Section 11 is finalization-only and ships in its own PR, per `repo-workflow`'s PR-separation rule — no `openspec/` edits, doc updates, or task checkoffs travel with the code.

## 1. Domain and ports

- [ ] 1.1 `internal/video/domain/video_job.go`: add a `contentHash` field with an accessor, carried by `NewVideoJob` and `RestoreVideoJob`. Add **no** cross-field validation pairing it with status — same reasoning as `sourceKey` in the previous change: the column ships with an empty default and pre-existing rows must stay loadable.
- [ ] 1.2 `internal/video/domain/repository.go`: add `ClaimForProcessing(ctx, job) (bool, error)` to `VideoJobRepository`. The bool reports whether the conditional write affected a row. Name it `ClaimForProcessing`, not `Claim` — `postgres.OutboxRepository.Claim` already exists in this context and means something different.
- [ ] 1.3 `internal/video/domain/idempotency_store.go`: add `ClearByJob(ctx, key, jobID) (bool, error)`. Document why it exists alongside the token-based `Clear` rather than replacing it: a token is unique to one request and is the stronger check, so a caller that still holds one keeps using `Clear`. `ClearByJob` is for a process that legitimately never held the token.
- [ ] 1.4 New exported sentinel `ErrJobClaimLost` (or similar) in `internal/video/domain` for a claim that found the job no longer `queued`. It must be distinguishable from `ErrVideoJobNotFound` — a dispatch naming a nonexistent job is a different anomaly from a duplicate, and both are dead-lettered for different reasons.

## 2. Persistence

- [ ] 2.1 `internal/video/infrastructure/postgres/schema.sql`: `ALTER TABLE video_jobs ADD COLUMN IF NOT EXISTS content_hash TEXT NOT NULL DEFAULT ''`, in the file's existing idempotent-DDL style. Additive, no backfill — the digest could in principle be recomputed from the stored object, but pre-existing jobs' source objects are already deleted.
- [ ] 2.2 `schema.sql`: the cutoff migration — `UPDATE video_job_outbox SET published_at = now() WHERE event_type = 'video_job.queued' AND published_at IS NULL`. Guard it so it runs once (a one-shot migration marker, or a bound on `occurred_at` recorded at first execution) and cannot suppress rows written afterwards. Comment it with what it is: a one-time boundary discharging `videojob-outbox-relay`'s expiring requirement, **not** a standing filter — a version of this that kept matching would silently disable dispatch forever.
- [ ] 2.3 `repository.go`: round-trip `content_hash` through `Create`, `FindByID`, `FindByUserID`, and `FindCompletedByUserID`. A pre-migration row must load with an empty value, not error.
- [ ] 2.4 `repository.go`: `ClaimForProcessing` — one statement, `UPDATE video_jobs SET status = 'processing' WHERE id = $1 AND status = 'queued'`, returning rows-affected. Not a transaction, not a `SELECT` then an `UPDATE`, and no row lock held past the statement: the caller runs a multi-minute extraction next. Distinguish "no row matched because the status differs" from "no row matched because the id does not exist" — a second lookup on zero rows affected is the straightforward way, and the comment should say why the distinction matters (dead-letter classification).
- [ ] 2.5 `internal/video/infrastructure/cache/repository.go`: add `ContentHash` to `cachedJobRecord`, `newCachedJobRecord`, and `toVideoJob`. Same non-optional plumbing argument as `SourceKey`: the record mirrors the column set exactly, and a dropped content hash leaves a failed job's idempotency key unclearable.
- [ ] 2.6 `cache/repository.go`: implement `ClaimForProcessing`. Write through **only when a row was affected**. A lost claim changed nothing in PostgreSQL, and writing the caller's in-memory `processing` job would overwrite the winner's entry on behalf of the loser. Do not pass it through uncached.
- [ ] 2.7 `internal/video/infrastructure/idempotency/redis_store.go`: implement `ClearByJob` as a Lua script comparing the stored value's job-ID suffix (`final:<token>:<jobID>`) and deleting only on a match, matching the existing `clearScript`'s shape. An unfinalized `reserved:` value must never match. An absent key reports `false`, not an error.

## 3. Application layer

- [ ] 3.1 `start_processing.go`: persist via `ClaimForProcessing` instead of `Update`. On no-row-affected, return the lost-claim sentinel — do not retry, do not call `FailJob`, do not mutate the job.
- [ ] 3.2 `process_video_job.go`: propagate the lost-claim sentinel unchanged from its `StartProcessing` call. It already returns early on a `StartProcessing` error; the task is to make sure that path stays free of `FailJob` and of the source download, and to say so in the doc comment — the "uniform error handling" refactor that turns this into a failure is exactly what the spec forbids.
- [ ] 3.3 `create_video_job.go`: `CreateVideoJobInput` gains `ContentHash`, passed through to `NewVideoJob`. Empty stays valid — `POST /api/video-jobs` has no content.
- [ ] 3.4 New use case or store call for clearing a failed job's key by job reference, wherever it composes most cleanly for the worker to invoke after a `failed` outcome. Derive the key with the **same** function `handleVideoUpload` uses (`domain.NewIdempotencyKey`), not a re-implementation.

## 4. Messaging: generation and consumer

- [ ] 4.1 `internal/video/infrastructure/messaging/topology.go`: `ExchangeJobs` becomes `video.jobs.v2` and `QueueJobs` `video.jobs.queued.v2`. Dead-letter names unchanged. Rewrite the comment block: the reason for a generation is the rolling-deploy window in which an old inline replica's cleanup would delete the source out from under a new worker's extraction — **not** stale residue, which the conditional claim already neutralizes. The stale reason is what `docs/roadmap.md` currently gives, and leaving it in the code would propagate it.
- [ ] 4.2 `internal/video/infrastructure/messaging/consumer.go` (new): a `Consumer` over an `internal/platform/rabbitmq` connection — declares the topology, sets `Qos(prefetch: 1)`, consumes with manual ack, and hands each delivery to a callback that returns an ack/reject decision. Keep it free of use-case knowledge, mirroring how `Publisher` is free of outbox knowledge; the decision table lives in `cmd/worker`.
- [ ] 4.3 If `internal/platform/rabbitmq` needs a helper for consuming (QoS, `Consume`, or a channel-lifecycle wrapper), add it there — and check it against that package's two enforcement tests: no import outside `internal/platform/`, and no context name (`video.jobs`, `video_job.`) in non-test source.
- [ ] 4.4 The payload struct the consumer unmarshals must stay in step with `postgres.videoJobQueuedPayload`, which now carries the content hash as well. There is no compiler link between the two — extend `TestRoutingKeyMatchesTheOutboxEventType`'s sibling coverage, or add a test that round-trips the producer's marshalled payload through the consumer's decoder.

## 5. `cmd/worker`

- [ ] 5.1 `cmd/worker/main.go`: composition root wiring the Video Processing PostgreSQL pool (+ migrations), MinIO source and result storage, Redis (idempotency store and the status-cache decorator), and the broker. **No identity, no router, no rate limiter, no outbox relay.** Fail fast and exit non-zero on unreachable PostgreSQL or MinIO; dial the broker with retry rather than treating it as a startup gate.
- [ ] 5.2 The per-message decision table from `design.md` Decision 8, one place, explicit: processed to terminal → ack; unparseable payload, unknown job, lost claim, or infra error mid-run → reject **without requeue**. No path acks a message it did not process.
- [ ] 5.3 On a terminal outcome for a job it claimed: `CompleteJob` on success only (never `FailJob` — `ProcessVideoJob` already did it), then delete the source object, then — if the job ended `failed` — clear its idempotency key. Delete the source from a `defer` registered the moment the claim is won, so a panic cannot skip it, and **only** on that path: a lost claim must delete nothing.
- [ ] 5.4 Graceful shutdown on `SIGINT`/`SIGTERM`: cancel the consumer so no new delivery is taken, wait for the in-flight job to reach terminal and be acked, then close the broker, PostgreSQL, and Redis handles. Bound the wait; on expiry, log the in-flight job ID and exit anyway. Note in a comment why abandoning is worse than waiting — the redelivery cannot re-claim a `processing` row, so an abrupt exit strands the job.
- [ ] 5.5 Redial and redeclare the topology on every successful dial, the same posture as the relay: nothing else declares it, so a recreated broker would otherwise leave the consumer bound to nothing.

## 6. `cmd/api`: the 202 handler

- [ ] 6.1 `cmd/api/video.go`: `handleVideoUpload` stops calling `processVideoJob`, `completeJob`, and `failJob`. After a successful `EnqueueVideoJob` it returns `202` with the job ID and the status URL (`/api/video-jobs/<id>`). Response strings in English per the language policy; the pt-BR strings already there stay.
- [ ] 6.2 The source-object `defer` becomes conditional: delete only if the job was **not** successfully enqueued. Register it as one guarded deferred call, not per-path deletes. Comment the reason — after the enqueue the bytes are the worker's input, and deleting them would destroy a running extraction.
- [ ] 6.3 Pass the content hash into `CreateVideoJobInput`. It is already computed on the upload pass; this only stores it.
- [ ] 6.4 The duplicate path returns the same acknowledgement shape naming the existing job, with no outcome branch. Delete the three-way `completed`/`failed`/`processing` translation — it described a response that no longer exists. Keep the duplicate's own source-object deletion.
- [ ] 6.5 `newVideoModule`/`setupVideo`: drop the now-unused dependencies from the API module rather than leaving them wired. If `ProcessVideoJob`, `CompleteJob`, or `FailJob` no longer have an API-side caller, they leave `videoModule` — a field kept "in case" is how the double-processing hazard comes back.
- [ ] 6.6 `GET /api/status` and `GET /download/:filename` are unchanged. Confirm that deliberately rather than by omission: both read `completed` jobs, and a job now becomes `completed` in a different process.

## 7. Frontend

- [ ] 7.1 `cmd/api/web/app.js`: submit, read the `202`, then poll `GET /api/video-jobs/:id`. Start at 2 s, back off ×1.5 to a 10 s ceiling. On `429`, honour `Retry-After`, lengthen the interval, and keep polling — never report the job as failed.
- [ ] 7.2 Render `queued`/`processing` as an in-progress state and `failed` with the job's reason. User-facing copy stays in pt-BR per the language policy exception; the surrounding JS comments, if any, in English.
- [ ] 7.3 On `completed`, refresh the results listing and use the existing `/download/<key>` issuance flow. Do not change the download path — the presigned-URL contract is untouched by this change.

## 8. Compose, image, CI

- [ ] 8.1 `Dockerfile`: build both binaries in the builder; the runtime stage carries both plus `ffmpeg`.
- [ ] 8.2 `docker-compose.yml`: a `worker` service from the same image, running the worker binary, with the Video Processing environment (no `IDENTITY_*`), depending on postgres/redis/minio/rabbitmq, and no ports.
- [ ] 8.3 `.github/workflows/ci.yml`: confirm the existing services suffice for the new tests, and that `go vet ./...` and `go build ./...` cover `cmd/worker`.

## 9. Tests

- [ ] 9.1 `ClaimForProcessing` against real PostgreSQL: claims a `queued` row; refuses `pending`/`processing`/`completed`/`failed` without modifying them; two concurrent claims produce exactly one winner; an unknown id is distinguishable from a lost claim.
- [ ] 9.2 `CachedVideoJobRepository.ClaimForProcessing`: writes through on a win, writes **nothing** on a loss. The loss case is the one that matters — assert the cache entry is byte-identical afterwards.
- [ ] 9.3 `ClearByJob` against real Redis: deletes a key finalized to the given job; leaves one finalized to another job; leaves an unfinalized reservation; reports `false` with no error for an absent key.
- [ ] 9.4 `ProcessVideoJob`: a lost claim returns the sentinel with no download, no `ffmpeg`, no `FailJob`, and no state change.
- [ ] 9.5 Worker end-to-end against the real broker (skipping cleanly without `RABBITMQ_TEST_URL`, like the sibling packages): a published message drives a job to `completed` with the result stored and the source deleted; an undecodable source drives it to `failed` with the source deleted and the idempotency key cleared; a duplicate delivery is dead-lettered without a second extraction; an unparseable message and an unknown job are dead-lettered.
- [ ] 9.6 `cmd/api` handler tests: `POST /upload` returns `202` with a job ID and status URL and does not invoke `ffmpeg`; the source object still exists after the response; a `CreateVideoJob`/`EnqueueVideoJob` failure still deletes it; a duplicate returns the same shape naming the existing job.
- [ ] 9.7 The generation bump is asserted, not assumed: `JobDispatchTopology()` returns the `.v2` names, and a message published to a `.v1`-shaped exchange is not delivered to the `.v2` queue.
- [ ] 9.8 The cutoff migration: pre-existing unpublished `video_job.queued` rows are stamped; a row written afterwards is not; `video_job.created` rows are untouched; the migration is safe to re-run.

## 10. Quality gates

- [ ] 10.1 `go vet ./...` and `go test ./... -v` pass locally (or via `docker compose run --build --rm app-test go test ./... -v`). Confirm the section 9 broker-backed tests reported `PASS`, not `SKIP`.
- [ ] 10.2 `gosec ./...` and `govulncheck ./...` pass.
- [ ] 10.3 Manual full-flow non-regression, which `ddd-architecture` makes a merge gate for this change: upload through the web UI, watch it poll, download the zip. Record the sequence in the PR.
- [ ] 10.4 Manual deploy-window check: with a message on the `.v1` queue from before this change, start the new stack and confirm the worker consumes nothing from it and no job is affected.

## 11. Finalization (separate PR, per repo-workflow)

- [ ] 11.1 Check off sections 1–10 above.
- [ ] 11.2 Promote the delta specs: `videojob-worker` (new), and the modified `videojob-execution`, `videojob-lifecycle`, `videojob-persistence`, `videojob-status-cache`, `videojob-messaging`, `videojob-outbox-relay`, `upload-idempotency`, `videojob-source-storage`, `video-frame-extraction`, `video-processing-access`, `ddd-architecture`, `videojob-http-api`, `container-image`, and `rate-limiting`. Audit every promoted requirement's prose against its predecessor, not just its scenario list — a wholesale block replacement loses paragraphs silently, which is the failure this project has caught twice.
- [ ] 11.3 **Two requirements are renamed under `## MODIFIED`, which the promotion step will not infer.** `videojob-outbox-relay`: "Pre-Existing Unpublished Rows Are Bounded by the Cutover, Not by This Change" → "…Bounded by an Explicit Cutoff". `videojob-source-storage`: "Every Request Deletes Its Own Source Object" → "A Source Object Is Deleted By Whichever Component Owns Its Job's Outcome". Confirm after promotion that each canonical spec holds the new title **once** and the old title not at all — a promotion that appends instead of replacing leaves both standing.
- [ ] 11.4 Two capabilities use `## REMOVED` + `## ADDED` rather than a rename, because the requirement's intent reversed: `video-processing-access` ("Existing synchronous processing remains functional") and `videojob-execution` ("POST /upload Completes The Job Only After Its Result Is Durably Usable"). Confirm both are gone from their canonical specs and that every clause of the second landed in `videojob-worker`.
- [ ] 11.5 **Purpose sections are not carried by deltas and are false after promotion.** Fix all five by hand: `videojob-messaging` ("nothing consumes it yet — that is `migrate-upload-to-async-processing`'s"); `videojob-outbox-relay` ("Nothing consumes those messages yet"); `videojob-execution` ("No queue or worker is in scope here", and "wired into `cmd/api`'s `POST /upload` handler"); `videojob-source-storage` ("has every request attempt to delete its own source object"); and check `video-processing-access`'s and `videojob-http-api`'s.
- [ ] 11.6 Run `npx --yes @fission-ai/openspec validate --all --strict --no-interactive` and fix every error before archiving.
- [ ] 11.7 `/opsx:archive`.
- [ ] 11.8 `docs/architecture.md`: the request pipeline splits into the API's acknowledgement path and the worker's processing path; the topology tree gains `cmd/worker` and `messaging/`'s consumer; the "Processing is synchronous and in-request" prose goes.
- [ ] 11.9 `docs/operations.md`: a `worker` service and its configuration surface (no `IDENTITY_*`); the post-deploy step deleting `video.jobs.v1`/`video.jobs.queued.v1` and why it cannot be automated; the `uploads/` lifecycle rule promoted from "recommended backstop" to the only guarantee, with the never-dispatched leak named; a stuck-`processing` job as an operator-visible symptom with the dead-letter queue as where to look.
- [ ] 11.10 `docs/domain-model.md`: `ContentHash` in the `VideoJob` field table; `StartProcessing`'s actor becomes the worker and its pre-condition records the claim; `CompleteJob`'s actor likewise; the `VideoJobQueued` payload gains the content hash.
- [ ] 11.11 `docs/flows.md`: the synchronous upload diagram is replaced by the acknowledge-poll-download flow plus the worker's own sequence.
- [ ] 11.12 `docs/development.md` and `README.md`: running the stack now means running a worker too; the test prerequisites gain whatever section 9 requires.
- [ ] 11.13 `CLAUDE.md`: the `/upload` pipeline description, the "Processing is synchronous and in-request … nothing consumes that queue" paragraph, the shutdown-ordering note (now two composition roots), and the commands block.
- [ ] 11.14 `docs/roadmap.md`: flip this row to `archived` with links to the archive folder and every promoted spec. **Also correct this row's own text**: it justifies the new generation by toxic residue, which the conditional claim neutralizes — the reason is the deploy-window source-deletion race. And it offers two options for the idempotency clear; record which was taken and that a third was chosen instead.
