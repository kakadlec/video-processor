# Architecture

## Current Implementation

The HTTP surface is served by **three** processes, one per bounded context, behind an nginx gateway: `cmd/identity-api` (`/api/auth/*`), `cmd/notification-api` (`/api/notification-preferences`), and `cmd/video-api` (everything else, including the embedded frontend). Two more processes run off the request path: `cmd/worker` consumes the job queue and runs the extraction pipeline, and `cmd/notifier` consumes the terminal-event queue and delivers each outcome through the transport its preference's channel names. Five composition roots, one image.

It got there incrementally. Phase 2 added the first real internal package, `internal/identity`, an explicit DDD slice (domain/application/infrastructure) wired into a composition root rather than a package of its own; Phase 3's `extract-cmd-api-entrypoint` moved the HTTP surface off the repo root into `cmd/api`, and `wire-videojob-http-endpoints` wired `internal/video` in the same way behind a preview `/api/video-jobs` surface. Phase 6's `migrate-upload-to-async-processing` moved `ffmpeg` out of the request entirely — `cmd/worker` became a second composition root and `POST /upload` began answering `202` as soon as the job is queued — and `add-worker-job-lock` completed it with epoch-scoped Redis leases, fenced terminal writes, and a sweeper. Phase 7's `add-notification-webhook-delivery` added `cmd/notifier`. `split-api-by-bounded-context` — cross-cutting rather than a phase of its own — then split the one HTTP process into the three above and put the gateway in front of them; `cmd/api` no longer exists.

**A client sees one origin.** The gateway is the only process that publishes a host port, and it routes by path prefix, so the split is invisible to the browser: `app.js` calls `/api/auth/login`, `/upload` and `/api/notification-preferences` on the same origin and does not know three processes answer them.

```
video-processor/
  cmd/
    identity-api/
      main.go          # Identity composition root: server lifecycle, router, ordered shutdown
      identity.go      # Wires internal/identity; POST /api/auth/register and /api/auth/login. The ONLY process that holds a private key
      identity_test.go # Route, issuance, and configuration-surface tests
    video-api/
      main.go          # Video API composition root: server lifecycle, static asset routes, the dispatch outbox relay, ordered shutdown
      video.go         # Wires internal/video; POST /upload, GET /download/:filename, GET /api/status, /api/video-jobs
      auth.go          # This service's copy of the bearer middleware — a verifier and nothing else
      ratelimit.go     # This service's copy of the per-user rate-limit middleware
      main_test.go     # Integration tests (drive the real handlers via httptest)
      video_test.go    # Video job route/upload/idempotency/setup tests, plus the route-ownership assertions
      auth_test.go     # Bearer-middleware, verifier-setup, and the "only Identity can mint" source scan
      ratelimit_test.go
      web/
        index.html     # Upload form + login/register panel
        styles.css
        app.js
    notification-api/
      main.go          # Notification composition root: server lifecycle, router, rate limiter, ordered shutdown
      notification.go  # Wires internal/notification; GET/PUT /api/notification-preferences
      auth.go          # Its own copy of the bearer middleware — verifier only
      ratelimit.go     # Its own copy of the rate-limit middleware
      main_test.go notification_test.go auth_test.go ratelimit_test.go
    worker/
      main.go          # Worker composition root: consumer lifecycle, disposition table, terminal cleanup
      sweeper.go       # Lease-aware recovery of jobs abandoned in processing
      main_test.go     # createDirs / configuration-surface tests
      worker_test.go   # End-to-end dispatch tests against real PostgreSQL, Redis, MinIO, and a broker
      sweeper_test.go  # Recovery, fencing, scan rotation, and shutdown tests
    notifier/
      main.go          # Notifier composition root: terminal-event consumer, delivery-budget validation, three-valued disposition table, bounded shutdown drain
      main_test.go     # Configuration-surface, disposition-mapping, and drain/close-ordering tests
  internal/
    identity/
      domain/         # User, UserID, ports (repository, password hasher, token issuer/verifier)
      application/    # RegisterUser, AuthenticateUser use cases
      infrastructure/ # PostgreSQL adapter, bcrypt adapter, JWT adapter, UUID generator
    video/
      domain/         # VideoJob aggregate + transition methods, value objects, repository/FrameExtractor ports
      application/    # CreateVideoJob, GetJobStatus, ListUserJobs, EnqueueVideoJob, StartProcessing, CompleteJob, FailJob, ProcessVideoJob use cases
      infrastructure/ # PostgreSQL adapter, UUID generator, ffmpeg-backed FrameExtractor adapter,
                      #   MinIO storage, Redis idempotency/cache/lease, AMQP messaging (topology, Publisher, Relay, Consumer)
    notification/
      domain/         # NotificationPreference, Delivery, own UserID, closed EventType/Channel sets, Destination,
                      #   DestinationPolicy, non-disclosing Secret, TerminalEvent, repository/Deliverer ports
      application/    # SetPreference, ListPreferences, DeliverNotification use cases + DeliveryConfig (the budget and its validator)
      infrastructure/ # Own PostgreSQL adapter (preferences + deliveries, advisory-locked Migrate),
                      #   own copy of the terminal topology/messages + Consumer, and two Deliverers:
                      #   webhook (payload, HMAC signature, guarded dialer) and smtp (plain-text message, deadlined conversation)
    platform/
      redis/ ratelimit/ rabbitmq/   # Cross-cutting connection/lifecycle plumbing owned by no context
  go.mod / go.sum  # Module definition: gin, pgx, golang-jwt, bcrypt, google/uuid, go-redis, minio-go, amqp091-go
  docker/
    nginx/nginx.conf              # The gateway's configuration: one location rule per HTTP service, upstreams held in variables so DNS resolves per request
    postgres-init/                # Creates one database per bounded context on the volume's first init
  Dockerfile       # Multi-stage build: builder -> test -> runtime (non-root); builds and carries all five binaries
  docker-compose.yml # Local dev stack: postgres, redis, minio, rabbitmq, gateway, identity-api, video-api, notification-api, worker, notifier, and the app-test service used to run the suite
  .github/
    workflows/
      ci.yml                      # Build & Test, SAST (gosec), Vulnerability Scan
      release-please.yml          # Automated release management
  docs/            # Project documentation (this directory)
  openspec/        # Spec-driven change governance artifacts
```

### Request pipeline (current)

Every request enters through the gateway, which resolves its upstream per request and forwards the body unbuffered — `client_max_body_size 0` and `proxy_request_buffering off`, so a large upload is neither rejected at the hop nor spooled to the gateway's own disk. From the handler's point of view nothing about the pipeline below changed when the gateway was introduced.

Processing is asynchronous. `POST /upload` stores the bytes, records the job, and answers `202`; a separate process does the work. The two halves are shown separately because they run in different processes, on different filesystems, and can fail independently.

**Video API half — `cmd/video-api`, inside the request:**

```
Browser / client
  │
  ├─ gateway (nginx), default route → video-api:8080
  │
  └─► POST /upload (multipart)
        │
        ├─ Validate extension (.mp4, .avi, .mov, .mkv, .wmv, .flv, .webm)
        ├─ SourceStorage.Put → bucket/uploads/<uploadID>_<filename>, hashing
        │    the stream (SHA-256) in the same pass; nothing touches local disk
        ├─ Delete that source object                (one defer, registered the
        │    moment Put succeeds, and guarded on whether the enqueue
        │    committed: before that the object is this request's, after it
        │    the object is the worker's input and MUST survive. Best effort
        │    — a failure is logged with the key, never fatal)
        ├─ Reserve(userID + content hash)          (Redis, idempotency)
        │    ├─ error (Redis down/erroring) → log, proceed without a
        │    │    reservation (fail-open); Finalize/Clear below are
        │    │    skipped for this request — see fail-open-upload-idempotency
        │    ├─ reserved=false → poll Lookup up to a bounded 2s window; a
        │    │    resolved duplicate returns 202 naming the existing job —
        │    │    byte-shaped like a fresh acknowledgement, so a client needs
        │    │    no duplicate branch — an unresolved one returns 409, and
        │    │    neither calls CreateVideoJob
        │    └─ reserved=true  → proceed below
        ├─ CreateVideoJob                          (application, status: pending,
        │                                            carrying the source key)
        │    └─ on failure: Clear the idempotency reservation if one was
        │         obtained, return 500
        ├─ EnqueueVideoJob                         (pending → queued; the status
        │                                            update AND a
        │                                            video_job.queued.v2
        │                                            outbox row commit in one
        │                                            transaction — the relay
        │                                            publishes it later, out of
        │                                            band)
        │    └─ on failure: Clear the reservation, return 500 — the job row
        │         exists but is unqueued, and the source object's defer
        │         still deletes it
        ├─ Finalize the reservation if one was obtained
        │    → real VideoJobID, 24h TTL (non-fatal if this write
        │      itself fails — see below). After the enqueue, not before:
        │      see the idempotency note below
        └─ 202 { job_id, status: "queued", status_url: "/api/video-jobs/<id>" }
             no frame count, no result key, no download URL — none of them
             exists yet; the client polls status_url for all of it
```

The source object is deliberately **still in the bucket** when this response is returned: it is the worker's input.

**Worker half — `cmd/worker`, out of band:**

```
video.jobs.queued.v2  (prefetch 1, one delivery at a time)
  │
  └─► handle(body)
        ├─ ParseJobQueuedMessage                   (undecodable → Reject → DLQ;
        │    redelivering it would fail identically forever)
        ├─ NewStorageKey(msg.source_key)           (empty/invalid → Reject → DLQ)
        ├─ ProcessVideoJob (job_id, source_key):
        │    ├─ StartProcessing → ClaimForProcessing
        │    │    UPDATE video_jobs SET status='processing'
        │    │      WHERE id=$1 AND status='queued' RETURNING lease_epoch
        │    │    └─ no row affected → ErrJobClaimLost → Reject → DLQ,
        │    │         touching nothing at all (another consumer owns it)
        │    ├─ Acquire Redis lease at the returned epoch; renew every 30s
        │    │    for the run (errors fail open; an absent lease is
        │    │    reacquired, a newer epoch stops the heartbeat)
        │    ├─ SourceStorage.Get → temp/<jobID>_source (no extension; ffmpeg
        │    │    probes content, so no path component comes from a filename)
        │    ├─ Remove temp/<jobID>_source           (ProcessVideoJob's own
        │    │    defer — registered before extraction, so a failed run
        │    │    can't leave the copy behind)
        │    ├─ FrameExtractor.ExtractFrames (infrastructure/ffmpeg):
        │    │    ├─ exec.CommandContext ffmpeg -i <video> -vf fps=1 temp/<jobID>/frame_%04d.png
        │    │    ├─ Glob PNGs from temp/<jobID>/
        │    │    ├─ Write temp/<jobID>.zip          (beside the frame dir, so
        │    │    │    the defer below can't delete it)
        │    │    └─ Remove temp/<jobID>/            (defer, always)
        │    ├─ Remove temp/<jobID>.zip           (ProcessVideoJob's own
        │    │    defer — registered before the store attempt, so a failed
        │    │    upload can't leave the zip behind)
        │    ├─ ResultStorage.Put (infrastructure/storage):
        │    │    └─ FPutObject → bucket/frames_<jobID>.zip
        │    └─ FailJob if fetch, extraction, OR storage failed
        │         (processing → failed), conditional on the claimed epoch
        ├─ ErrJobFenced from failure or completion:
        │    └─ Reject → DLQ; keep source and idempotency key; release no
        │         lease; log held epoch and any result key
        ├─ result.Success == false and `failed` was already present:
        │    └─ Ack without cleanup (this actor applied nothing)
        ├─ result.Success == false and this run applied `failed`:
        │    ├─ Delete the source object
        │    ├─ ClearJobIdempotencyKey (conditional on this job still owning it)
        │    ├─ Release the lease at this run's epoch
        │    └─ Ack
        └─ result.Success == true:
             ├─ CompleteJob (processing → completed at the claimed epoch),
             │    retried 4× with detached context and backoff
             │    └─ still failing → Reject → DLQ, source object and lease KEPT,
             │         result StorageKey logged
             ├─ Delete the source object (gated on this terminal commit)
             ├─ Release the lease at this run's epoch
             └─ Ack
```

`ProcessVideoJob` still leaves a successful job in `processing` for its caller to complete, but the caller is now `cmd/worker` and the split has acquired a real reason. Storing the result is `ProcessVideoJob`'s own step, so a successful return already means the artifact is durable — but the worker has to own the terminal write, because acknowledging the message is what that write licenses. Folding `CompleteJob` inside the use case would put the commit out of reach of the retry-and-dead-letter policy the worker applies to it.

**Claim and recovery are separate correctness decisions.** `ClaimForProcessing` still admits only `queued`, so duplicate dispatch cannot run two extractions and Redis is absent from pickup. Recovery is a periodic sibling goroutine in `cmd/worker`: each 60-second cycle scans at most 50 `processing` rows using a rotating keyset cursor, queries the Redis lease at the row's epoch, and acts only after two successful not-held observations at the same epoch. A query error for that row clears its confirmation and takes over nothing. Recovery conditionally writes `processing → queued`, increments `lease_epoch`, and inserts a fresh `video_job.queued.v2` outbox row in one transaction. The old holder's terminal `Update` requires both its epoch and `status = 'processing'`, so a requeue or another terminal winner fences it. After three requeues, or when a legacy row has no source key, the sweeper commits `failed` and cleans up only if its own write was applied.

**Outbox relay (Phase 6, `add-videojob-source-key-and-outbox-relay`; generation bumped by `migrate-upload-to-async-processing`):** `POST /upload` records a job dispatch; it never talks to the broker. `Repository.Enqueue` commits the `pending → queued` update and a `video_job.queued.v2` outbox row together, and a relay goroutine started by `cmd/video-api/main.go` carries those rows to RabbitMQ afterwards — `internal/video/infrastructure/messaging.Relay`, composing an `OutboxRepository` in `internal/video/infrastructure/postgres` with an AMQP `Publisher`, so neither infrastructure package has to depend on both a database driver and a broker client. Each cycle opens a transaction, claims a bounded batch with `SELECT … FOR UPDATE SKIP LOCKED` filtered to `event_type = 'video_job.queued.v2'` (the table still holds an unpublished `video_job.created` row for every job ever created, and those are internal events that must never reach the job queue), publishes each message mandatory on a confirm-mode channel, stamps `published_at` only for messages the broker both acknowledged **and** did not return, then commits. Holding row locks across a broker round trip is the deliberate cost: committing the claim first would, on a crash before the publish, lose a dispatch silently, whereas this ordering can only ever republish. A nack is not an error — the job queue's `reject-publish` overflow policy nacks when it is full, which is designed back-pressure — so the row simply stays unstamped for the next poll. The relay dials through `internal/platform/rabbitmq.Open` in its own goroutine, declares `JobDispatchTopology()` after **every** successful dial (the worker's consumer declares the same topology on every dial too, so neither process depends on the other having started first, and a reconnect to a recreated broker would otherwise publish into a missing exchange), and retries with backoff. `RABBITMQ_URL` is therefore required at startup while broker *reachability* is not — see [docs/operations.md](operations.md). **`cmd/worker` consumes that queue**, so a published message is now a live trigger. The generation suffix is carried by the exchange, the queue, **and the `event_type` string** — not by the exchange alone. Versioning the exchange isolates nothing on its own: every replica's relay reads the same `video_job_outbox` table, so with one shared event type a redeployed replica's relay would claim a not-yet-redeployed replica's row and publish it into the new generation. What that protects against is the rolling-deploy window, not stale messages: during a deploy where an in-request replica and a worker are both live, both can act on one legitimately-`queued` job — the claim decides who processes it, but the loser's cleanup would then delete the source out from under the winner's running extraction. A migration stamped `published_at` on the previous generation's still-unpublished rows, recording an exclusion the event-type filter already enforces. See `openspec/specs/videojob-outbox-relay/spec.md` and `openspec/specs/videojob-messaging/spec.md` for the full contract.

**Terminal events (Phase 7, `emit-videojob-terminal-events`):** a job's outcome is now announced the same way its dispatch is. `Repository.Update` — the write path behind both `CompleteJob` and `FailJob`, and the sweeper's by way of `FailJob` — opens a transaction, runs the fenced conditional `UPDATE` unchanged, and, **only when that statement affected a row**, inserts a `video_job.completed.v1` or `video_job.failed.v1` outbox row before committing. Nobody can therefore observe a terminal job without the event describing it, or the event without the job. The row-count gate is the whole correctness argument: a write refused by the fence (`ErrJobFenced`) recorded nothing because another actor won the outcome and already recorded it, and a write that finds its own outcome already stored (`applied == false`) recorded nothing because this actor's earlier commit did — so a superseded worker and a sweeper that both finish one job leave exactly one record of how it ended. The application layer gained no use case, port, or parameter for emission; `CompleteJob`/`FailJob` keep their signatures and their `applied` semantics, and `CachedVideoJobRepository` keeps delegating and forwarding `applied` unchanged. The events go out over a **second topology** — exchange `video.jobs.terminal.v1`, one durable queue `video.jobs.terminal.events.v1` bound under both routing keys, sharing the unversioned dead-letter sink — carried by a **second relay that runs in `cmd/worker`**, because the worker and its sweeper are the processes writing those rows, so an outcome does not go unannounced when no API replica is up. The outbox claim is now scoped to an explicit *set* of event types (`event_type = ANY($1::text[])`) and each row is published under the routing key its own `event_type` names; the dispatch relay is a one-element set and behaves exactly as before, and the two sets are disjoint so neither relay's backlog starves the other's. Delivery remains at-least-once — one row can become more than one message — so deduplication is the consumer's obligation. **`cmd/notifier` consumes that queue** as of `add-notification-webhook-delivery` (see the Delivery pipeline above), and discharges the deduplication obligation with a durable delivery record claimed atomically before any request is made. The queue was declared ahead of its consumer because the relay publishes mandatory, and an exchange with no bound queue would return every message unroutable forever. See `openspec/specs/videojob-terminal-events/spec.md` for the full contract.

**Idempotency (Phase 4, `add-upload-idempotency-keys`; fail-open correction, `fail-open-upload-idempotency`):** `POST /upload` deduplicates by content, not just by request. The upload's SHA-256 hash plus the authenticated `UserID` form a Redis-backed `IdempotencyKey` (`internal/video/domain`, implemented by `internal/video/infrastructure/idempotency.RedisStore`). A `Reserve` call wins the race for a given key with a short-lived sentinel; the loser polls `Lookup` for up to a bounded window and returns the winner's job status instead of starting a second `ffmpeg` run. The reservation is `Finalize`d (24h TTL) once `CreateVideoJob` **and** `EnqueueVideoJob` have both succeeded, or `Clear`ed immediately if either of them fails — freeing an immediate retry. Finalizing before the enqueue would advertise a `VideoJobID` for 24 hours to duplicates of content whose job never reached `queued`; the ordering is specified rather than incidental (`upload-idempotency`'s "Reservation Is Finalized To The Real VideoJobID Only By Its Owning Token"), because the other ordering trades one narrow 24h block for another — a worker that fails the job clears the key *by job ID*, and until `Finalize` runs the key is a bare reservation that `ClearByJob` leaves alone by design. **Clearing a failed job's key is now the worker's**, not the handler's: the handler returns before the outcome exists. It rebuilds the key from the job's `UserID` and a new persisted `content_hash` column — the reservation *token* is never persisted, since it is a possession capability of the request that minted it — and reads that hash from the **undecorated** PostgreSQL repository, because a cache record written by a previous release carries no `content_hash` at all and would silently yield an unbuildable key. Two different users uploading identical content each get their own key and their own job (the key includes `UserID`). If `Reserve` itself errors (Redis unreachable/erroring, as opposed to a normal reservation conflict), `handleVideoUpload` fails open: it logs the error and proceeds to `CreateVideoJob` without a reservation, rather than blocking the upload — the same posture already used by rate limiting and the status cache, so a Redis outage degrades deduplication instead of the critical upload path itself. See `openspec/specs/upload-idempotency/spec.md` for the full contract.

**Rate limiting (Phase 4, `add-rate-limiting-middleware`):** every authenticated route is also gated by a per-user, Redis-backed fixed-window rate limiter (`internal/platform/ratelimit.Limiter`, mounted immediately after the auth middleware). Since the split each HTTP service carries its own thin `ratelimit.go` wrapper over that one shared limiter — `cmd/video-api`'s and `cmd/notification-api`'s — and both key the counter identically (`ratelimit:<userID>`), deliberately: the budget is **one budget per user across the system**, so exhausting it on `GET /api/status` refuses the next `GET /api/notification-preferences` from a different process. Namespacing the counter per service would silently multiply every user's allowance by the number of services. A request that crosses the configured limit within the current window is rejected with `429` and a `Retry-After` header before any handler runs, keyed independently per authenticated `UserID`. Thresholds are configurable via optional `RATE_LIMIT_MAX_REQUESTS`/`RATE_LIMIT_WINDOW_SECONDS` (defaults 60 requests / 60 seconds) — unlike `REDIS_ADDR`, neither is required at startup. If the Redis check itself fails or exceeds a short internal timeout, the middleware fails open (allows the request, logs the error) rather than blocking otherwise-healthy traffic on an infrastructure hiccup. That timeout only actually bounds the real Redis call because `internal/platform/redis.Open` sets `ContextTimeoutEnabled: true` on the shared client — go-redis v9 otherwise silently discards a passed context's deadline on every command, a subtlety this change had to fix and cover with a real-client test (`internal/platform/redis/client_test.go`) after an initial fix attempt turned out not to work. Unauthenticated routes are out of scope; `/api/auth/register` and `/api/auth/login` are not merely unlimited but served by a different process, which mounts no limiter at all. The limiter governs *requests to this API*, which since `add-presigned-download-urls` is narrower than it reads: `GET /download/:filename` issues a URL and is limited, but the transfer that URL authorizes goes to MinIO, which this middleware does not sit in front of. Bounding artifact egress is an object-storage concern. See `openspec/specs/rate-limiting/spec.md` for the full contract.

**Status cache (Phase 4, extended by Phase 6 recovery):** `internal/video/infrastructure/cache.CachedVideoJobRepository` provides cache-aside `FindByID` reads for polling. `GetJobStatus` and `EnqueueVideoJob` use those reads; ownership decisions (`StartProcessing`, `CompleteJob`, `FailJob`, download entitlement, and the sweeper scan) bypass them and read PostgreSQL. Every applied transition still writes through the decorator. Miss-repopulation uses `SET NX`, while transition write-through uses an atomic Redis CAS ordered first by `lease_epoch` and then by state progression within the epoch; a delayed requeue write therefore cannot replace a successor's `processing` or terminal record. An ambiguous database error invalidates the entry because it may have occurred after commit; `ErrJobFenced` is a decided zero-row result and leaves the winner's entry intact. Cache failures remain best effort, and entries carry a fixed five-minute TTL. See `openspec/specs/videojob-status-cache/spec.md`.

**Result download (Phase 5, `add-presigned-download-urls`):** `GET /download/:filename` no longer carries result bytes. It authorizes exactly as before — the key must parse to a `VideoJobID`, and that job must exist, belong to the caller, be `completed`, and record that exact key, read from the *undecorated* PostgreSQL repository — then confirms the object exists with one `Stat` and returns `200 {"url", "expires_at"}`. The client redeems that URL against MinIO directly:

```
Browser / client
  │
  ├─► GET /download/frames_<jobID>.zip   (bearer token)
  │     ├─ five entitlement conditions, from the VideoJob row
  │     ├─ ResultStorage.Stat            (signing is offline and would
  │     │    succeed for a key holding no object, so a missing object is
  │     │    refused here rather than surfacing as MinIO's own 404)
  │     ├─ ResultStorage.PresignGet      (5-minute TTL, no network call)
  │     └─ 200 {"url", "expires_at"}     (Cache-Control: no-store)
  │
  └─► GET <url>                          (no Authorization header)
        └─ MinIO streams the ZIP straight to the client; no service of ours is involved
```

Three properties follow from that and are accepted rather than mitigated. An issued URL **cannot be revoked** — deleting the job or changing its owner leaves an outstanding URL working until it expires, which is why the TTL is a five-minute constant rather than configuration. The URL **is a credential**, so it is never logged, echoed into an error, or persisted; failures on this path log the `StorageKey` instead, and every response carries `Cache-Control: no-store`. And result **bytes are no longer rate limited** — see the rate-limiting note above.

The reported `expires_at` is read off the issued URL's own `X-Amz-Date`/`X-Amz-Expires`, not computed as `now + TTL`: the signing library stamps the signature at whole-second precision and truncates the lifetime, so the naive value overstates the window MinIO actually admits — and overstating is the direction that makes a client retry into a `403`. The bound is on **request admission**: a request arriving after the instant is refused, while a transfer already in flight runs to completion.

Because the URL's host is covered by the SigV4 signature, it cannot be rewritten after issuance — hence `VIDEO_MINIO_PUBLIC_ENDPOINT`, the browser-facing address the presigning client is built against. See `openspec/specs/videojob-result-storage/spec.md` and [docs/operations.md](operations.md).

`POST /upload` returns as soon as the job is `queued`; the ZIP is written later, by the worker. The client follows the `status_url` in the `202` body — `GET /api/video-jobs/:id` — until the job is `completed`, then calls `GET /download/:filename`. See `openspec/specs/videojob-execution/spec.md` and `openspec/specs/videojob-worker/spec.md` for the full contract.


### Delivery pipeline — `cmd/notifier` (out of band)

Phase 7's `add-notification-webhook-delivery` gave the terminal-event queue its consumer, and `add-notification-email-delivery` gave it a second channel. The notifier is a third process, from the same image, with the narrowest configuration surface of the five: the Notification context's own DSN, the broker URL, and the relay each channel sends through. It listens on no port, authenticates no caller, stores no artifact, holds no lease, and runs no `ffmpeg`.

```
video.jobs.terminal.events.v1        cmd/notifier/main.go          internal/notification/application
  │  (prefetch 1)                          │                                  │
  ├─ JobCompletedMessage / JobFailedMessage │                                  │
  │      decoded from the context's OWN copy of the message structs           │
  │                                          │                                 │
  │                                    translate user_id ──► notification.UserID
  │                                    (the sanctioned crossing, and the only one)
  │                                          │                                 │
  │                                          └────────► DeliverNotification.Execute
  │                                                            │
  │   ┌────────────────────────────────────────────────────────┘
  │   │
  │   ├─ FindDeliverable(user, eventType)   enabled preferences, secret included —
  │   │                                     the ONE read path allowed to load it
  │   ├─ drop any preference whose CreatedAt is not before the event's OccurredAt
  │   │     (the enrolment boundary: a standing instruction does not announce
  │   │      what happened before it was given)
  │   ├─ ClaimDelivery                      one atomic INSERT ... ON CONFLICT,
  │   │                                     three-valued: granted / already-resolved / held
  │   ├─ Deliverer.Deliver                  up to 3 attempts, 5s each, 2s backoff doubling
  │   │     └─ routed on the preference's channel, in the composition root:
  │   │          webhook ─► guarded dial ─► HMAC-signed POST ─► the registered URL
  │   │          email   ─► deadlined SMTP conversation ─► the configured relay
  │   └─ ResolveDelivery                    fenced on the claim token, retried 3× on failure
  │
  └─► Ack / Reject / Requeue
```

**The disposition set is three-valued, and the third is a deliberate difference from `cmd/worker`.** What a situation is entitled to turns first on *whether this handler has attempted anything yet*:

| Situation | Disposition |
|---|---|
| Delivered; budget exhausted and recorded; nothing to deliver (no matching preference, disabled, before the enrolment boundary); a claim refused as **already resolved**; a resolve refused by the fence; an outcome that could not be recorded after the attempts were made | **Ack** |
| Undecodable body, or an event type this consumer does not recognize | **Reject** → DLQ, never requeued |
| Any repository failure **before an attempt is made** — `FindDeliverable`'s included — or a claim refused as **held by another** | **Requeue**, after a pause |

`cmd/worker` has only the first two, and the difference is not an inconsistency. There, a requeued job meets a row that has moved past `queued` and can only lose the claim again, so redelivery loops rather than recovers. Here this handler has attempted nothing and the blocking condition — a database that is down, a claim another consumer holds — resolves itself, so dead-lettering would discard a user's notification because of a blip. The pause is what keeps that from becoming a hot loop.

`ClaimHeldByAnother` requeueing rather than acking is the counter-intuitive one and is load-bearing: a crashed claimant's redelivery arrives within seconds, far inside the reclaim bound, so an ack there would strand a `pending` row nothing else will ever meet and drop the notification without a trace. The loop terminates — the holder either resolves it (the next refusal reads *already resolved*) or does not (the bound expires and the claim is granted). At prefetch 1 the accepted cost is head-of-line blocking bounded by the reclaim bound, which is why that bound is sized as a small multiple of one claimant's budget rather than a comfortable round number.

**Two identifiers, not one.** `delivery_id` is what the receiver deduplicates on and survives a reclaim; `claim_token` says which grant is current and is reissued on every grant. Resolution is fenced on the token, because the reclaim bound proves a claim is *old*, not that its holder stopped — the same fence `lease_epoch` puts on a terminal `VideoJob` write, for the same reason.

**The destination policy is applied at two points to the destinations that are connection targets, and neither is redundant.** `SetPreference` judges a `webhook` destination when it is registered; the webhook client's `net.Dialer.Control` judges the **resolved address** when a connection is opened. It does not apply to an `email` destination, and that is a narrowing of scope rather than a hole: the policy exists to judge a target the *user* supplied, and the e-mail path opens its connection to the relay this deployment configures, never to the address the registrant gave. The branch is taken on the closed `Channel` set, so a value outside it is refused before either rule is reached and no third path applies neither. A write-time check cannot survive a hostname that resolves elsewhere later, nor a policy tightened after the row was stored; a dial-time check alone silently accepts a destination that will never be delivered to, which to its owner is indistinguishable from one that works. Both halves read one variable through one parser, so neither half can interpret a given value differently from the other — but the two processes read their own environments, and setting the variable on one and not the other is a real deployment error with two shapes: destinations accepted at registration that are refused at every dial, or destinations refused at registration that the notifier would have delivered to. Set it identically on both, or on neither (see [docs/operations.md](operations.md)). The address rule is a permission — only globally reachable unicast — expressed as an explicit prefix table *in addition to* the standard-library predicates, since `IsGlobalUnicast()` answers yes for shared, benchmarking, and reserved space. The transport's `Proxy` is `nil` explicitly: with a proxy in the environment the guarded dial would approve the proxy's address and the proxy would resolve the user's hostname itself, bypassing the whole table.

**A failed delivery changes nothing about the job.** It is not visible to the Video Processing context, does not alter any `VideoJob` row, and does not affect what any HTTP route reports. Both outcomes acknowledge: a third party's endpoint being down is not a defect in this system, and a dead-letter queue filling with one user's broken URL buries the messages that queue exists to surface.

**Two channels, one protocol around them.** The claim, the fence, the budget, the enrolment boundary and the disposition table above are shared verbatim: `DeliverNotification` holds one outbound port and does not branch on the channel, and the routing is a `Deliverer` composed in `cmd/notifier` that dispatches to the webhook client or the SMTP client. Composing it there rather than inside the use case is what keeps the file holding the fencing logic untouched by a second channel — and what makes "the new channel inherits the first one's guarantees" a property of the build rather than a claim. The set is validated at startup: a channel with no implementation refuses to boot, instead of being discovered by the first user who registered a preference on it.

Because a preference is identified per channel, **one event can produce two records with two independent outcomes** — a webhook that fails changes nothing about the e-mail for the same job, and vice versa. That is also why the shutdown drain is one claim hold *per channel* plus its grace: the handler works through a message's preferences one after another, so a drain sized for a single hold would expire during work that is proceeding normally and within budget.

**The e-mail attempt is bounded by a deadline on the connection, not by the dialer.** `net.Dialer` bounds only establishment; once the SMTP client owns the connection, the greeting, EHLO, STARTTLS, AUTH, MAIL, RCPT, DATA and QUIT are blocking reads that observe no context. One deadline is taken before the dial and reused for both halves, because a relay that completes the handshake and then stalls would otherwise hold an attempt past `MaxClaimHold()` and past the reclaim bound validated against it — which is precisely the "two live requests in normal operation" that validation exists to refuse.


### State (current)

Both uploaded source videos and results live in MinIO; user accounts and job rows in PostgreSQL. The only local directory left is scratch:

| Store | Purpose | Durability |
|---|---|---|
| `temp/` (**`cmd/worker`'s filesystem**) | Per-job scratch — the downloaded source copy, the extracted frames, and the zip built from them; always cleaned up (defer). No HTTP service creates or touches it | Ephemeral |
| MinIO bucket, `uploads/` prefix | Uploaded source videos, keyed `uploads/<uploadID>_<filename>`. Transient, with one owner at a time: the request until queueing, then the worker or recovery sweep that applies the terminal outcome. A mid-extraction crash is recovered automatically; an upload never dispatched, dead-lettered before claim, or interrupted after terminal commit but before cleanup can still leak, so the `uploads/`-prefix expiration rule remains the **only** exhaustive reclamation guarantee | Not meant to outlive its job |
| MinIO bucket, flat keys | Durable ZIP results, keyed `frames_<jobID>.zip`; served directly to the client under a presigned URL `/download/:filename` issues. Flat because `app.js` still uses the key verbatim as that route's single path segment — a `/` would percent-encode and break the match, which is exactly why sources may carry a prefix and results may not. The constraint survived the move to presigned URLs unchanged, since the route did | Persistent, external to the container |
| PostgreSQL `users` table | User accounts (normalized email, password hash) | Persistent, external to the container |
| PostgreSQL `video_jobs` table | `VideoJob` rows — created both by `POST /upload` (this pipeline) and by `POST /api/video-jobs` (the preview API below); the two share the same repository and owner-scoping | Persistent, external to the container |

RabbitMQ carries job dispatch — `video.jobs.v2` / `video.jobs.queued.v2`, with an unversioned `video.jobs.dlx` fanout sink. Redis backs idempotency keys, rate limiting, the non-authoritative `VideoJob` status cache, and epoch-scoped worker leases. PostgreSQL remains authoritative for job state, claims, fence epochs, and recovery transitions.

### Routes (current)

| Route | Handler | Description |
|---|---|---|
| `GET /` | inline | Serves embedded `cmd/video-api/web/index.html` (via `go:embed`); always public |
| `POST /api/auth/register` | `handleRegister` | Create a user account |
| `POST /api/auth/login` | `handleLogin` | Authenticate and issue a bearer JWT |
| `POST /upload` | `handleVideoUpload` | Accept multipart video, store it, queue the job, return `202 {job_id, status, status_url}`; requires a bearer token. **No processing happens in the request** — `cmd/worker` does the work |
| `GET /download/:filename` | `(*videoModule).handleDownload` | Authorize, then issue a 5-minute presigned URL for the stored ZIP: `200` with `{"url", "expires_at"}`, never the bytes. Owner-only, entitlement decided from the `VideoJob` row and evaluated *only* here, since an issued URL carries no identity. Every rejection returns a byte-identical 404; every response carries `Cache-Control: no-store` |
| `GET /api/status` | `(*videoModule).handleStatus` | JSON list of the caller's `completed` jobs' results; size and timestamp come from the stored object |
| `POST /api/video-jobs` | `handleCreateVideoJob` | Create a `VideoJob` record (JSON `original_filename`, no file content); requires a bearer token; preview API, see below |
| `GET /api/video-jobs/:id` | `handleGetVideoJobStatus` | Get a `VideoJob`'s status; owner-only (non-owner and nonexistent both 404). This is the endpoint `POST /upload`'s `status_url` names — the async flow's status channel, not just a preview read |
| `GET /api/video-jobs` | `handleListVideoJobs` | Paginated list of the caller's own `VideoJob`s |
| `GET /api/notification-preferences` | `(*notificationModule).handleListPreferences` | JSON list of the caller's own `NotificationPreference`s, ordered by `(event_type, channel)`; `has_secret` instead of the secret. An empty set is `200` with an empty array, never `404`. Owner-scoped from the token |
| `PUT /api/notification-preferences` | `(*notificationModule).handleSetPreference` | Upsert exactly one preference named by `event_type` + `channel` **in the body**; `secret` optional on update, required on create. A `user_id` in the body is ignored. Same object shape as one element of the read, never the secret |

**Which service answers which route** is the gateway's decision, by path prefix: `/api/auth/` → `cmd/identity-api`, `/api/notification-preferences` → `cmd/notification-api`, everything else → `cmd/video-api`. The rules differ in one detail that looks like an inconsistency and is not — `/api/auth/` carries a trailing slash because it has routes beneath it, while `/api/notification-preferences` is the resource itself and has none, so a trailing-slash rule there would match nothing a client sends.

Each service requires only the configuration it uses, and each fails to start without it. There is no unauthenticated fallback mode anywhere.

| Service | Required at startup | Optional |
|---|---|---|
| `cmd/identity-api` | `IDENTITY_POSTGRES_DSN`, `IDENTITY_JWT_PRIVATE_KEY`, `IDENTITY_JWT_KEY_ID`, `IDENTITY_JWT_PUBLIC_KEYS` | — |
| `cmd/video-api` | `IDENTITY_JWT_PUBLIC_KEYS`, `VIDEO_POSTGRES_DSN`, `REDIS_ADDR`, the four `VIDEO_MINIO_*`, `RABBITMQ_URL` | `VIDEO_MINIO_USE_SSL`, `VIDEO_MINIO_PUBLIC_ENDPOINT`, `VIDEO_MINIO_PUBLIC_USE_SSL`, `RATE_LIMIT_*` |
| `cmd/notification-api` | `IDENTITY_JWT_PUBLIC_KEYS`, `NOTIFICATION_POSTGRES_DSN`, `REDIS_ADDR` | `NOTIFICATION_ALLOW_INSECURE_DESTINATIONS`, `RATE_LIMIT_*` |
| `cmd/worker` | `VIDEO_POSTGRES_DSN`, `REDIS_ADDR`, the four `VIDEO_MINIO_*`, `RABBITMQ_URL` | `VIDEO_MINIO_*` optional trio |
| `cmd/notifier` | `NOTIFICATION_POSTGRES_DSN`, `RABBITMQ_URL` | `NOTIFICATION_ALLOW_INSECURE_DESTINATIONS`, the three delivery-budget variables |

Two absences carry weight. **Only `cmd/identity-api` is given a private key**, and the other two HTTP services hold no code path that could construct an issuer even if one were handed to them — `NewVerifier` refuses a private-key PEM outright. And `cmd/worker` reads **no `IDENTITY_*` and no `NOTIFICATION_*` at all**: it makes no access-control decision and registers no preference, so requiring either would misrepresent what the process does. It serves no HTTP and exposes no port; nor does `cmd/notifier`. `RABBITMQ_URL` is required wherever it appears, though a *reachable* broker never is. See [docs/operations.md](operations.md) for the full table.

**`/api/video-jobs` is a preview API, not the async processing flow.** It was wired by Phase 3's `wire-videojob-http-endpoints`, alongside (not replacing) the `/upload` flow above. A `VideoJob` created *through this API* has no processing trigger — `handleCreateVideoJob` never calls `EnqueueVideoJob`/`StartProcessing`/`CompleteJob`/`FailJob` — and none is currently planned for it: `migrate-ffmpeg-execution-to-videojob-application` gave those four use cases a real caller, but it's `POST /upload`, not `POST /api/video-jobs`, so a job created via this API still stays `pending` indefinitely. Giving this preview API its own trigger would need a separate, not-yet-proposed future change. **The asynchronous cutover did not change this**, and the distinction is now easy to get wrong: `POST /upload` is the async submission endpoint, because it is the one that receives the bytes; `POST /api/video-jobs` takes a filename in JSON, has no source key, and `VideoJob.Enqueue` rejects a job without one. No separate `/jobs` endpoint was introduced. A `pending` status here therefore does **not** mean "waiting for a worker" — a job awaiting a worker is `queued`. `GET /api/video-jobs/:id`, on the other hand, is now consumed by the frontend as the upload flow's status channel. See `openspec/specs/videojob-http-api/spec.md` for the full contract.

**`GET /api/video-jobs`/`GET /api/video-jobs/:id` do show non-`pending` jobs, though** — `POST /upload` also calls `CreateVideoJob`, so a user who has uploaded via `/upload` will see those jobs (real `completed`/`failed` status, real `frame_count`/`storage_key`) alongside any still-`pending` jobs they created directly via `POST /api/video-jobs`. Same aggregate, same repository, same owner-scoping — not a bug, see `openspec/specs/videojob-execution/spec.md` for how `/upload` drives that state machine.

CORS headers (`Access-Control-Allow-Origin: *`) are applied globally, advertising `Access-Control-Allow-Methods: GET, POST, PUT, OPTIONS`. `PUT` is there for the preference write and nothing else — without it a browser preflight would reject that write on a correctly mounted route.

### Frontend (current)

The web UI lives in `cmd/video-api/web/index.html`, `cmd/video-api/web/styles.css`, and `cmd/video-api/web/app.js`, embedded into the binary via `go:embed` and served at `GET /`, `GET /styles.css`, and `GET /app.js` respectively. It contains:

- Plain HTML form for file selection, plus a login/register panel (Phase 2)
- CSS in `cmd/video-api/web/styles.css`
- Vanilla JavaScript in `cmd/video-api/web/app.js` using `fetch` to call `POST /upload`, `GET /api/video-jobs/:id`, `GET /api/status`, `POST /api/auth/register`, and `POST /api/auth/login`; the bearer token is kept in `localStorage` and attached as an `Authorization` header on protected requests
- Since the cutover the page **polls**: it submits, reads `status_url` off the `202`, then polls with an interval that starts at 2s and backs off to a 10s ceiling. Those polls share one per-user rate-limit budget with the submission and the download issuance, so the interval is chosen against the default 60/60s rather than picked for responsiveness. A `429` is a back-off signal, not a job failure — `Retry-After` is honoured **uncapped** (the limiter's window is configurable and routinely exceeds the 10s ceiling; capping it would just earn another `429`), while the ordinary interval keeps advancing underneath so one long wait does not become the cadence

There is no separate frontend build, no Node.js toolchain, and no bundler.

---

## Target Architecture (Partially implemented — Phases 1–6 done, Phase 7 in progress)

The hackathon requirements include user authentication, asynchronous processing, notifications, and object storage. The target architecture introduces Domain-Driven Design structure across three bounded contexts, delivered incrementally.

> Identity (Phase 2) and Phase 3 (the `cmd/api` split — that entrypoint is gone, superseded by the three per-context services, the `VideoJob` HTTP surface, and `POST /upload`'s ffmpeg execution migrated into the application layer) are both fully implemented as described below. Phase 4 is done: `internal/platform/redis` provides connection plumbing (`add-redis-infrastructure`), and all three of its planned features are wired in — idempotency keys on `POST /upload` (`add-upload-idempotency-keys`), per-user rate limiting on every authenticated route (`add-rate-limiting-middleware`), and a `VideoJob` status cache (`add-videojob-status-cache`). Phase 5 is done: `add-minio-infrastructure` added `internal/video/infrastructure/storage`, `migrate-result-storage-to-minio` wired it into the API, `migrate-upload-storage-to-minio` moved uploaded source videos there too, and `add-presigned-download-urls` took the API out of the result-byte path — `GET /download/:filename` now issues a bounded URL the client redeems against MinIO directly. Both `outputs/` and `uploads/` are gone along with the whole ownership-sidecar mechanism, MinIO is a fail-closed startup dependency, and `ProcessVideoJob` now takes a storage key rather than a local path — the seam Phase 6's worker needs. Phase 6 is done: `add-rabbitmq-infrastructure` shipped the shared AMQP adapter and job topology; `add-videojob-source-key-and-outbox-relay` made source keys durable and added transactional dispatch; `migrate-upload-to-async-processing` added `cmd/worker` and the `202` acknowledgement; and `add-worker-job-lock` added Redis leases, PostgreSQL fence epochs, and the recovery sweeper. **Processing is asynchronous, and jobs abandoned after a successful claim are recovered by the lease sweeper.** Queued jobs whose dispatch is never published or is dead-lettered before claim remain outside that recovery scope. Phase 7 is in progress and is now end-to-end for the webhook channel: `emit-videojob-terminal-events` made a job's outcome announceable, `add-notification-domain-and-preferences` built the Notification context's first half — the `NotificationPreference` aggregate, its own PostgreSQL adapter and pool, and two owner-scoped routes on the API — and `add-notification-webhook-delivery` closed the loop with `cmd/notifier`, its own composition root that consumes `video.jobs.terminal.events.v1`, resolves the owner's preferences, and delivers each outcome as one HMAC-signed request. **A registered webhook destination is now actually called.** `add-notification-email-delivery` then opened the `Channel` set to `email` and **closed the phase**: an SMTP adapter behind the same `Deliverer` port, a routing `Deliverer` composed in `cmd/notifier`, a destination rule per channel, and the signing-secret invariant made conditional on the channel in the schema, the create path, the aggregate and the delivery read's projection. Its address comes from the preference's own `Destination` rather than from an Identity event, which is why `add-identity-user-registered-event` was dropped from the backlog. Alongside Phase 7, and belonging to no phase, `split-api-by-bounded-context` replaced `cmd/api` with `cmd/identity-api`, `cmd/video-api` and `cmd/notification-api` behind an nginx gateway, moved token signing to RS256 with only Identity holding a private key, and moved the cross-context pins to `internal/contracts`.

### Bounded Contexts

| Context | Responsibility | Status |
|---|---|---|
| **Identity** | User registration, authentication, JWT issuance and verification | Implemented (Phase 2) |
| **Video Processing** | VideoJob lifecycle — creation, queueing, async execution, recovery, result storage | Implemented (Phase 3: lifecycle and HTTP; Phase 5: source/results in MinIO; Phase 6: asynchronous dispatch and `cmd/worker`, plus lease/fence-based recovery for jobs abandoned in `processing`) |
| **Notification** | Delivery preferences — the `NotificationPreference` aggregate, its storage, and its authenticated owner-scoped HTTP surface — plus consuming a job's terminal outcome and delivering it to the destinations its owner registered | Implemented on both channels (Phase 7, complete): preferences are stored and manageable, `cmd/notifier` consumes the terminal-event queue, and each delivery is claimed, bounded and recorded before being sent through the transport its channel names — one signed HTTP request for `webhook`, one plain-text message through the configured relay for `email`. One job can produce two records with two independent outcomes. Being reachable over HTTP does not make it a context others call to trigger a delivery |

### Target Package Topology

```
video-processor/
  cmd/
    api/        # HTTP entrypoint (implemented, Phase 3) — main.go/identity.go moved here
      web/
        index.html  # Extracted from getHTMLForm()
        styles.css
        app.js
    worker/     # Async frame-extraction worker (implemented, Phase 6): own composition root, no HTTP, no IDENTITY_* configuration
      main.go     # Consumer, fenced terminal disposition, lifecycle
      sweeper.go  # Redis-lease-aware recovery and bounded abandonment
    notifier/   # Terminal-event consumer and webhook deliverer (implemented, Phase 7 — add-notification-webhook-delivery): own composition root, no HTTP, and neither IDENTITY_*, VIDEO_*, nor REDIS_ADDR
      main.go     # Consumer, three-valued disposition table, delivery-budget validation, bounded shutdown drain
  internal/
    platform/
      redis/          # Shared Redis connection adapter (implemented, Phase 4) — Config/Open/Ping/Close, wired into the HTTP services by add-upload-idempotency-keys
      ratelimit/      # Redis-backed fixed-window rate Limiter (implemented, Phase 4 — add-rate-limiting-middleware), wired into each HTTP service's own rateLimitMiddleware on every authenticated route
      rabbitmq/       # Shared AMQP connection adapter (implemented, Phase 6 — add-rabbitmq-infrastructure) — Config/Open/Ping/Close plus a generic Topology descriptor and DeclareTopology; names no exchange or queue of its own. Four connection loops call it — cmd/video-api's dispatch relay, cmd/worker's consumer and its terminal relay, and cmd/notifier's consumer — each owning the connection its own process opens
    identity/                        # Implemented (Phase 2); wired into cmd/identity-api, and its verifier half into every service that authenticates
      domain/         # User aggregate, value objects, repository/password/token ports
      application/    # Use cases: RegisterUser, AuthenticateUser
      infrastructure/ # PostgreSQL adapter, bcrypt adapter, JWT adapter, UUID generator
    video/
      domain/         # VideoJob aggregate + transition methods, value objects, events, repository/FrameExtractor ports (all implemented, Phase 3)
      application/    # Use cases: CreateVideoJob, GetJobStatus, ListUserJobs, EnqueueVideoJob, StartProcessing, CompleteJob, FailJob, ProcessVideoJob (all implemented, Phase 3)
      infrastructure/ # PostgreSQL adapter, ffmpeg-backed FrameExtractor adapter (both implemented, Phase 3 — wired into cmd/video-api by wire-videojob-http-endpoints / migrate-ffmpeg-execution-to-videojob-application)
        idempotency/  # Redis-backed IdempotencyStore adapter (implemented, Phase 4 — add-upload-idempotency-keys), wired into cmd/video-api's POST /upload handler
        cache/        # Redis-backed CachedVideoJobRepository decorator (implemented, Phase 4; epoch/status-aware write ordering added in Phase 6)
        lease/        # Epoch-scoped Redis worker lease (implemented, Phase 6 — add-worker-job-lock)
        storage/      # MinIO adapter (implemented, Phase 5) — Config/Open/Ping/EnsureBucket connection plumbing (add-minio-infrastructure) plus ResultStorage, the domain port carrying result artifacts into and out of the bucket (migrate-result-storage-to-minio)
        messaging/    # This context's job-dispatch and terminal-event topologies, the outbox relays that publish into them, and the consumer that reads the dispatch queue (implemented, Phase 6 — add-rabbitmq-infrastructure for JobDispatchTopology(), add-videojob-source-key-and-outbox-relay for Publisher/Relay, migrate-upload-to-async-processing for Consumer; Phase 7's emit-videojob-terminal-events added TerminalEventsTopology(), NewTerminalRelay, and the JobCompleted/JobFailed message contracts): Publisher wraps a confirm-mode channel and publishes mandatory; Relay claims outbox rows of its own event-type set through postgres.OutboxRepository, publishes each under the routing key its event_type names, and stamps published_at — the dispatch relay started as a goroutine by cmd/video-api/main.go, the terminal relay by cmd/worker/main.go, each stopped and joined on shutdown; Consumer dials, redeclares the topology on every dial, sets prefetch 1, and hands each delivery to a Handler that returns Ack or Reject — it knows nothing about jobs, claims, or storage, and cmd/worker holds the whole decision table
    notification/                    # Implemented for the webhook channel (Phase 7 — add-notification-domain-and-preferences + add-notification-webhook-delivery), wired into cmd/notification-api and cmd/notifier
      dependency_rules_test.go  # Fails the build on an import of internal/video or internal/identity, across EVERY package of the context including infrastructure — the cross-context rule this context is built under
      domain/         # NotificationPreference (write intent / stored preference / read view, three shapes on purpose), its own UserID, the closed EventType and Channel sets, Destination, the non-disclosing Secret, and the PreferenceRepository port; plus TerminalEvent, the Delivery record with its three-valued ClaimOutcome, the DeliveryRepository and Deliverer ports, and DestinationPolicy — whose zero value is the restrictive posture, so a composition root that forgets to pass one fails closed
      application/    # Use cases: SetPreference, ListPreferences, DeliverNotification; plus DeliveryConfig, which holds the whole budget (attempts, timeouts, backoffs, reclaim bound) and the validator that refuses a reclaim bound below twice one claimant's maximum hold
      infrastructure/
        postgres/     # This context's own Config/Open/Migrate/PreferenceRepository/DeliveryRepository over NOTIFICATION_POSTGRES_DSN and its own notification_preferences and notification_deliveries tables. Migrate holds a pg_advisory_xact_lock covering both CREATE TABLE IF NOT EXISTS statements and the guarded block that widens the signing-secret CHECK to CHECK (channel <> 'webhook' OR secret <> ''), deliberately stricter than the identity and video adapters. Set is one atomic statement on each of its three branches and reads no row; ClaimDelivery is likewise one atomic INSERT ... ON CONFLICT that both inserts and refuses; every read projects has_secret and never selects the secret except FindDeliverable, the single named delivery-path read — and even there the projection yields one only for a row whose channel signs
        messaging/    # This context's OWN copy of the terminal topology and the two message structs — copied rather than imported, because the cross-context ban is a property of the build — plus a Consumer whose disposition set is Ack/Reject/Requeue. Both copies are pinned field by field by tests in internal/contracts, the one package allowed to import both contexts
        webhook/      # A Deliverer: the envelope (its own top-level version, independent of the event type's .v1), HMAC-SHA256 signing over timestamp and body, an http.Client whose dialer Control applies the destination policy to the resolved address, an explicitly nil Proxy, no redirects, a capped response body, and the shared LoadDestinationPolicyFromEnv parser cmd/notification-api reads too
        smtp/         # The other Deliverer: one plain-text base64-encoded message through the configured relay, no signature and no secret read at all (asserted at the source level). One deadline taken before the dial and reused for the whole SMTP conversation, because net.Dialer bounds only establishment and the steps after smtp.NewClient observe no context; every value bound for a header is re-checked against the printable-ASCII rule before one is written; credentials are never offered over an unencrypted session
```

**Migration strategy:** no big-bang rewrite, at any stage. The repo-root `main.go` became `cmd/api` (Phase 3's `extract-cmd-api-entrypoint`) and stayed functional while each feature phase migrated one slice of the handler into the appropriate use case and wired it back; `split-api-by-bounded-context` then split that one process into three, one context at a time, each behind the same gateway and each landing on `main` on its own before the next began. `cmd/api` was deleted only once nothing was left in it.

### Infrastructure Components

| Component | Role | Status |
|---|---|---|
| nginx gateway | The single ingress: one location rule per HTTP service, the only process publishing a host port | **Implemented** (`split-api-by-bounded-context`) — stock upstream image plus a mounted configuration file, carrying no binary, key, or credential of ours |
| PostgreSQL | Authoritative state store for users, plus `video_jobs`/`video_job_outbox` (Phase 3) and `notification_preferences`/`notification_deliveries` (Phase 7) | **Implemented** (Phase 2 for identity; Phase 3 schema/adapter for video, wired into the Video API by `wire-videojob-http-endpoints` and driven by `POST /upload` since `migrate-ffmpeg-execution-to-videojob-application`; Phase 7 for notification, whose pool is opened and migrated by `cmd/notification-api` **and** `cmd/notifier` alike), required at deployment time — see [docs/operations.md](operations.md) |
| Redis | Idempotency keys, rate limiting, status cache, worker leases | Connection adapter and the first three responsibilities are implemented from Phase 4; `add-worker-job-lock` completed the fourth in Phase 6 with `internal/video/infrastructure/lease`. Redis remains non-authoritative and is not consulted by the PostgreSQL claim or fence |
| MinIO | Object storage for uploads and ZIP results (S3-compatible) | Fully implemented: ZIP results through `internal/video/domain.ResultStorage` (`migrate-result-storage-to-minio`) and uploaded source videos through `SourceStorage` (`migrate-upload-storage-to-minio`), sharing one bucket, separated by key prefix, with configuration required at startup. Results are handed to clients as presigned URLs rather than proxied (`add-presigned-download-urls`), so the API is absent from the transfer path |
| RabbitMQ | Durable async task queue for job dispatch, plus the terminal-event stream | **Implemented, publishing, and consumed** (Phase 6, `add-rabbitmq-infrastructure` + `add-videojob-source-key-and-outbox-relay` + `migrate-upload-to-async-processing`): `internal/platform/rabbitmq` opens, health-checks, and declares a topology; `internal/video/infrastructure/messaging` defines this context's exchange, queue, and dead-letter sink, the outbox relay that publishes `video_job.queued.v2` events into it, and the consumer `cmd/worker` reads them with. `emit-videojob-terminal-events` (Phase 7) added a second exchange and queue (`video.jobs.terminal.v1` / `video.jobs.terminal.events.v1`, bound under `video_job.completed.v1` and `video_job.failed.v1`) and a second relay, running in `cmd/worker`; `add-notification-webhook-delivery` then gave that queue its consumer in `cmd/notifier`, declaring the same topology from the Notification context's own copy of the names. `RABBITMQ_URL` is **required at all three processes' startup**, but a reachable broker is not — each relay and each consumer owns its connection, dials in its own goroutine, redeclares its topology after every dial, and retries with backoff |

The fourth Redis responsibility is implemented as a **lease for liveness**, not a lock around pickup. Concurrent pickup remains prevented by PostgreSQL's literal `status = 'queued'` claim. Redis gives the sweeper advisory evidence about whether a `processing` row still has a live worker, while PostgreSQL's `lease_epoch` and conditional terminal update provide the fence. A lease-query error itself authorizes no takeover, but fail-open acquisition or renewal can leave a live extraction without a matching key; two later successful absence observations may requeue it and let a successor run concurrently. The PostgreSQL fence prevents the superseded run from overwriting the authoritative outcome, but deliberately does not prevent duplicated extraction work.

See [docs/roadmap.md](roadmap.md) for the full phase plan.

### Dependency Rules (Target)

1. `domain` packages MUST NOT import `application`, `infrastructure`, or transport packages.
2. `application` packages depend only on repository/port **interfaces** defined in `domain`.
3. `infrastructure` packages implement interfaces from `domain` and may import third-party drivers.
4. The five entrypoints are the only places where `infrastructure` adapters are instantiated and wired (composition root), and all five now exist. `cmd/identity-api/identity.go` plays that role for Identity's write side (`RegisterUser`/`AuthenticateUser`, and the issuer); `cmd/video-api/video.go` for the use cases the video HTTP surface needs (`CreateVideoJob`/`GetJobStatus`/`ListUserJobs`/`EnqueueVideoJob`) plus the dispatch relay; `cmd/notification-api/notification.go` for Notification's write side (`SetPreference`/`ListPreferences`, plus the write-time half of the destination policy); `cmd/worker/main.go` wires the ones the pipeline needs (`ProcessVideoJob`, which drives `StartProcessing`/`FailJob`, plus `CompleteJob` and `ClearJobIdempotencyKey`); `cmd/notifier/main.go` wires `DeliverNotification` and the dial-time half of the same policy. No binary switches behaviour on a mode flag, and each requires only the configuration it uses.

   Since the split, **no composition root links both Video Processing and Notification** — each serves one of them. Each HTTP root does still import Identity's `domain` verifier port and its JWT adapter, because a middleware cannot authenticate a caller without them; what disappeared is a root holding the Video and Notification copies of the terminal contract at once. That removed the only place the cross-context pins could legitimately live, which is why they moved to `internal/contracts`: a package that declares nothing outside its `_test.go` files and that no package imports, both asserted by tests of its own.
5. No bounded context may import another context's `domain` or `application` packages directly. Each context defines and owns its own local value object for any identifier that crosses a boundary (e.g. `identity.UserID` and `video.UserID` are distinct types) — cross-context communication uses domain events or translation at the composition root, never a package shared between contexts' `domain` layers. There is no `pkg/` directory; a shared kernel was considered for the crossing `UserID` and rejected as tighter coupling than this architecture's context-independence goal justifies (see `add-videojob-domain-and-application`'s `design.md` in `openspec/changes/archive/`).

Rules 1–3 for `internal/identity/{domain,application}`, `internal/video/{domain,application}`, and `internal/notification/{domain,application}` are each enforced by an automated test (`internal/identity/dependency_rules_test.go`, `internal/video/dependency_rules_test.go`, `internal/notification/dependency_rules_test.go`), not just convention. Notification's adds rule 5 to what it checks, and — as of `add-notification-webhook-delivery` — checks it across **every** package of the context rather than its `domain` and `application` alone: `internal/video` and `internal/identity` are forbidden import prefixes everywhere under `internal/notification/`, infrastructure included, which is where the temptation to import the original lives. That is why the context declares its own `UserID`, its own copies of the `video_job.completed.v1`/`video_job.failed.v1` strings, and its own copies of the terminal topology and message structs rather than importing any of them. Each duplication is pinned at the one place allowed to see both sides — `internal/contracts`, which exists for exactly that and holds nothing else: `TestNotificationEventTypesMatchTheEmittedTerminalEventTypes`, `TestNotificationTerminalTopologyMatchesTheEmittedTopology` (field by field, bounds included, because RabbitMQ refuses to redeclare a queue whose arguments differ and a name-only pin would stay green while the notifier could not consume at all), and `TestNotificationTerminalMessagesDecodeTheEmittedPayloads` — in the same spirit as `TestRoutingKeyMatchesTheOutboxEventType`.
