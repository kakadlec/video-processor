## Context

Two shipped changes left a deliberate gap. `emit-videojob-terminal-events` declared `video.jobs.terminal.events.v1` explicitly ahead of any consumer, so that mandatory publishes would route and the events would accumulate durably rather than be returned unroutable. `add-notification-domain-and-preferences` built the aggregate that answers *where and with what secret*, and stopped there: it stores a `Secret` no read path loads, and a `NotificationPreference` type whose doc comment says outright that it "exists for the delivery change".

So the inputs are all present and none of them has ever been exercised end to end. What this change adds is the process between them, plus the two questions both earlier changes explicitly forwarded here:

- `internal/notification/domain/destination.go` accepts `http` and states that restricting production destinations to `https` "belongs to `add-notification-webhook-delivery`, the change that opens the connection", and the archived `design.md` records the SSRF question as becoming real "only when something dials it".
- The roadmap row states that the retry policy is "a decision rather than inherited", because `cmd/worker`'s dead-letter disposition is calibrated for a claim-fenced job.

Constraints that are not negotiable here: `ddd-architecture` forbids Notification importing Video Processing (enforced by `internal/notification/dependency_rules_test.go`); `videojob-terminal-events` assigns deduplication to whatever consumes its queue; and the Notification context owns its own PostgreSQL pool under `NOTIFICATION_POSTGRES_DSN`.

## Goals / Non-Goals

**Goals:**

- A running `cmd/notifier` turns a terminal event into a signed HTTP `POST` at the destination its owner registered, or into a recorded reason why it did not.
- Deliveries are deduplicated durably, so at-least-once transport does not become at-least-once webhooks.
- A user-supplied URL is treated as hostile input: no internal address, no redirect chase, bounded time and bytes.
- A stored secret is loaded on exactly one path and is not observable anywhere else, including in logs and error text.
- A failing endpoint is an ordinary outcome with a bounded cost, not an incident and not dead-letter traffic.

**Non-Goals:**

- Email delivery. `email` stays a rejected `Channel` until `add-notification-email-delivery` ships an adapter; nothing here loosens `ParseChannel`.
- `UserRegistered` and any local projection of a user's address — `add-identity-user-registered-event` owns that, and webhook delivery needs no address.
- Long-horizon retry (a delay queue, a scheduled redelivery sweeper). The attempt budget is bounded and in-process; see the decision below for why, and for the trigger that would revisit it.
- Any change to a `VideoJob`, to the two topologies, or to what the API returns. A delivery outcome is invisible to Video Processing.
- Delivering the backlog that accumulated before any preference existed. The enrolment boundary makes that a non-event rather than a task.
- Surfacing delivery history over HTTP. The table is written and read by the notifier; exposing it is a later change if anyone asks.

## Decisions

### A third binary, `cmd/notifier`, in the existing image

Delivery runs in its own process, built by the same `builder` stage and shipped in the same `runtime` image as `/app/notifier`, started as the same image with a different command.

*Why not a goroutine in `cmd/api`*: the announcement of an outcome would then depend on an API replica being up — the argument `videojob-terminal-events` already makes for putting the terminal relay in the worker. *Why not in `cmd/worker`*: the worker's process shape is built around one `ffmpeg` at a time (prefetch 1, a 5-minute drain), and delivery is I/O-bound work that scales on a different axis; sharing the process would make one the other's head-of-line blocker. *Why not a separate image*: `container-image` already records the reason — the binaries share every internal package, and two images are a way for two halves of a deploy to be at different commits.

### The wire contract is copied into Notification, and pinned

`internal/notification/infrastructure/messaging` declares its own `TerminalEventsTopology()` and its own `JobCompletedMessage`/`JobFailedMessage`. It may import `internal/platform/rabbitmq` — that package is context-free plumbing by construction, and its own tests forbid it containing a context name — but not `internal/video/infrastructure/messaging`.

The duplication is pinned by a test in `cmd/api`, asserting the copied topology and the copied struct field names equal Video Processing's. `cmd/api` is chosen over `cmd/notifier` because it is the established home for exactly this kind of pin (`TestNotificationEventTypesMatchTheEmittedTerminalEventTypes` already lives there) and because keeping every cross-context pin in one file is what makes it findable; the alternative, pinning inside `cmd/notifier`, would be equally legal, since a composition root may import both. One argument for it was weighed and does not decide the question: `cmd/api`'s `TestMain` requires `ffmpeg`, MinIO, and `RABBITMQ_URL`, which is the heaviest prerequisite gate in the repository, whereas a `cmd/notifier` test would need none of them. That gate would matter if it caused the pin to be skipped, but it does not — `TestMain` exits 1 rather than skipping, so an unsatisfied prerequisite fails the run loudly instead of quietly not exercising the pin. A drift-detector that cannot silently not-run is doing its job wherever it lives, so locality wins. A second consideration points the same way: `cmd/notifier` is the one composition root whose spec says it imports no Video Processing package, and putting the import in its test binary would blur a boundary the change is otherwise sharpening.

`internal/notification/dependency_rules_test.go` currently checks only `domain` and `application`. It is extended to walk **every** package under `internal/notification`, so the new `infrastructure/messaging` cannot quietly import Video Processing to avoid the duplication. Without that extension the rule this decision rests on would not be enforced where it now matters most.

### The enrolment boundary is the preference's `created_at`

An event is delivered to a preference only when `event.occurred_at > preference.created_at`.

This does the job an explicit deploy-date cutoff would have done — `videojob-outbox-relay` carried exactly such a requirement for its own pre-existing rows — without its defect. A date constant discards everything older than the deploy, including events that are legitimately old because the notifier was down; and it needs a second decision, and a second deploy, every time the situation recurs. The preference's own age is self-limiting and states a rule rather than a fact about one afternoon: *a standing instruction does not announce what happened before it was given.*

Consequences accepted deliberately: `created_at` is not touched by the upsert's update branch, so disabling and re-enabling a preference does not re-open the window; and a user who registers a preference one second after their job finished does not get told about that job. Both are the correct reading of "was not subscribed at the time".

*Alternative rejected*: delivering the whole backlog. In today's data it would be nearly harmless — the feature is days old and few preferences exist — which is exactly why it is a bad rule to write down: it is right only until it isn't.

### Delivery records are claimed before the attempt, not written after it

`notification_deliveries`, primary key `(user_id, event_type, channel, job_id)`, holds one row per logical delivery with an `id`, a status, an attempt count, and timestamps.

The row is written **before** the HTTP request, in one statement:

```
INSERT ... ON CONFLICT (user_id, event_type, channel, job_id) DO UPDATE
   SET claimed_at = $now, attempts = 0, id = notification_deliveries.id
 WHERE notification_deliveries.status = 'pending'
   AND notification_deliveries.claimed_at < $stale
RETURNING id
```

Zero rows means someone else already claimed this delivery, or it already reached a terminal outcome; the message is acked and nothing is sent. This is what discharges the obligation `videojob-terminal-events` assigns to its consumer, and it is a claim rather than a post-hoc record because writing afterwards leaves a read-then-act race that two notifier replicas would lose.

The `status = 'pending' AND claimed_at < $stale` conjunct is one extra predicate that closes the only hole a claim-first design has: a process that dies between claiming and recording an outcome would otherwise strand the delivery forever. It is the same shape as the sweeper's takeover of an abandoned job, reduced to a single statement because there is no lease and no epoch — nothing here needs fencing, since a duplicate delivery is undesirable rather than corrupting.

The `id` is a UUID generated at claim and reused across the attempt budget, so the receiver's own dedup key is stable across our retries; the `DO UPDATE ... SET id = notification_deliveries.id` clause is what keeps it stable across a takeover too.

*Alternative rejected*: Redis, the store the upload idempotency key uses. That store is explicitly fail-open (`fail-open-upload-idempotency`), which there degrades to "an extra job" and here would degrade to "an extra webhook" — the exact thing the record exists to prevent. It is also the natural home for the delivery history the retry policy reports into, which Redis is not.

### The retry policy: bounded in-process attempts, always ack

A delivery is attempted up to `NOTIFICATION_WEBHOOK_MAX_ATTEMPTS` times (default 3) with exponential backoff, each attempt bounded by `NOTIFICATION_WEBHOOK_TIMEOUT_SECONDS` (default 5), under a total budget small enough that a dead endpoint costs the queue seconds rather than minutes. A `2xx` records `delivered`; anything else — connection refused, timeout, `4xx`, `5xx` — exhausts the budget and records `failed` with the last observed reason. **Either outcome acks.**

The reasoning is the one the roadmap row demands be stated: `cmd/worker` dead-letters because a redelivered job whose row has moved on can only lose its claim again, so redelivery cannot help. Here redelivery *could* help — but the thing that would need retrying is a third party's availability, which is not this system's failure and not something a dead-letter queue is a useful record of. A DLQ filling with one user's broken endpoint would bury the messages a DLQ exists to surface.

Retryable status codes are not distinguished from permanent ones, deliberately: the attempt budget is small enough that retrying a `404` costs little, and a table mapping status codes to retryability is a source of wrong guesses about endpoints we do not control.

*Alternative rejected*: a delay-queue retry topology (TTL + DLX rebound to the work queue), which would give minutes-to-hours of retry. It is the right answer for a system whose notifications are contractual; it is a new topology, a new generation to version, and new operational surface for a hackathon deliverable whose events remain fully recorded in PostgreSQL either way. **Trigger to revisit**: the first time the `failed` rows in `notification_deliveries` show transient failures materially outnumbering permanent ones.

### Three dispositions, not two

`cmd/worker`'s handler answers `Ack` or `Reject`. This consumer needs a third:

| Situation | Disposition |
|---|---|
| Delivered, or budget exhausted, or nothing to deliver (no preference, disabled, before enrolment, already claimed) | `Ack` |
| Undecodable body; event type this process does not recognize | `Reject` → dead-letter |
| Notification database unreachable while claiming or recording | `Requeue`, after a pause |

The third exists precisely because the reason `cmd/worker` forbids requeue does not hold here. There, a redelivery meets a row that has moved past `queued` and loses the claim again — a loop. Here, the dependency that failed is one that comes back, and the message has not been acted on at all; dead-lettering it would discard a notification for a database blip. The pause before consuming again is what keeps that from becoming a hot loop against a down database.

### Destination policy, applied twice

One policy type in `internal/notification/domain`, consulted in two places:

1. **At write** (`PUT /api/notification-preferences`, `cmd/api`): scheme must be `https`; the host must not be a literal loopback/private/link-local address or `localhost`. A destination that could never be delivered to is refused where the user sees the error — the argument the closed `Channel` set already makes for rejecting `email` rather than storing it and silently doing nothing.
2. **At dial** (`cmd/notifier`): the same policy re-checked against the **resolved IP** inside `net.Dialer.Control`, which is the only place a DNS name that resolves to `169.254.169.254` is actually caught. Redirects are not followed (`CheckRedirect` returns an error), the response body is read under a small cap and discarded, and the whole attempt runs under the per-attempt timeout.

Two layers rather than one, for the reason `Secret`'s own comment gives about `String`/`Format`/`%p`: each closes what the other leaves open. Write-time validation cannot survive DNS rebinding or a policy tightened after the row was stored; dial-time validation cannot tell the user why their URL was refused.

`NOTIFICATION_ALLOW_INSECURE_DESTINATIONS` (default `false`) relaxes both together — `http` and private addresses — and `docker-compose.yml` sets it, because the compose stack has no TLS and a local webhook receiver is a container hostname on a private network. It is one variable rather than two because the two relaxations are wanted in exactly the same situation, and splitting them would invite enabling half of it in production.

**This is a behavior change to an existing write path**: a preference carrying an `http` destination can be stored today and cannot be stored after this change unless the opt-out is set. Rows already stored are not migrated or deleted; they simply do not deliver under the default policy, and the recorded delivery reason says so.

### The secret is loaded by one named port method

`PreferenceRepository` gains `FindDeliverable(ctx, userID, eventType) ([]*NotificationPreference, error)` — returning the aggregate, secret included, for enabled preferences only. `ListByUser` and its `secret <> '' AS has_secret` projection are untouched.

The invariant this modifies is real and load-bearing, so it is narrowed rather than dropped: the requirement becomes "no read path that feeds an HTTP response loads the secret", with one named method excepted, and the exception is defended by a test asserting `cmd/api` contains no call to it. The alternative — a separate repository interface visible only to the notifier — is cleaner in isolation and was rejected as more structure than one method warrants, given the test makes the same assertion directly.

`Secret.Reveal()` is called in exactly one function, the signer. The delivery record stores no secret. Log lines name the triple and the delivery id, never the body and never the destination's query string.

### The webhook payload is Notification's own, versioned

```
POST <destination>
Content-Type: application/json
User-Agent: fiapx-notifier/1
X-FiapX-Event: video_job.completed.v1
X-FiapX-Delivery: <delivery id>
X-FiapX-Timestamp: <unix seconds>
X-FiapX-Signature: sha256=<hex HMAC-SHA256 over "<timestamp>.<body>">

{"id": "...", "type": "video_job.completed.v1", "occurred_at": "...",
 "data": {"job_id": "...", "frame_count": 42, "storage_key": "frames_<id>.zip"}}
```

The envelope is built by Notification rather than forwarded from Video Processing's wire payload. Forwarding would publish an internal contract: every future change to what the outbox writes would become a breaking change for every registered endpoint, and `videojob-terminal-events`' generation suffix would be leaking out of the system it was designed to isolate. `user_id` is omitted from `data` because the receiver is that user; the triple is theirs by construction.

The signature covers `timestamp.body` rather than the body alone, so a captured request cannot be replayed indefinitely against a receiver that checks the timestamp's age. That is a property the receiver has to use, which is why the timestamp is a header rather than only a body field.

## Risks / Trade-offs

- **A failed delivery is not retried beyond the budget** → The outcome is recorded in `notification_deliveries` with its reason, the `VideoJob` is unaffected and still readable through `GET /api/video-jobs/:id`, and the trigger for adopting a delay queue is written down above rather than left to be rediscovered.
- **Head-of-line blocking: one slow endpoint delays every other user's delivery** → The per-attempt timeout and the attempt count are chosen so the worst case is seconds, and prefetch stays at 1 with "scale by adding processes" as the answer, matching `cmd/worker`. Bounded in-handler concurrency was considered and rejected here: it makes shutdown, ack ordering, and the claim's staleness bound all harder to reason about for a gain the timeout budget already bounds.
- **The `https`-only default breaks a destination a user has already stored** → Deliberate, documented in `docs/operations.md`, and visible: the delivery record names the policy as the reason. The alternative, delivering a signed payload over plaintext, sends the receiver a signature an observer can also read the body of.
- **A crash between claiming and recording strands one delivery** → The staleness predicate on the claim lets a redelivery take it over. If no redelivery ever comes, one row stays `pending` and one notification is lost; that is the residual, and it is visible in the table rather than silent.
- **The secret is plaintext in the column and now also in process memory at delivery time** → Unchanged posture from `add-notification-domain-and-preferences`, which records it in `docs/operations.md`; what this change adds is one function that reads it, one test asserting nothing else does, and no new storage of it.
- **Two copies of the terminal wire contract can drift** → Pinned by a test in the one place allowed to see both, exactly as the event-type constants already are. An unpinned copy is the failure mode; a pinned copy is a rule the compiler cannot express.
- **A delivery is only as scoped as the `user_id` in the event** → The event's `user_id` is Video Processing's, translated at the composition root into Notification's own `UserID`, which is the sanctioned crossing under `ddd-architecture`. No preference is ever resolved from anything else in the payload.

## Migration Plan

1. **Schema first, and it is additive.** `notification_deliveries` is created by the Notification context's existing advisory-locked `Migrate`, which `cmd/api` already runs at startup. `cmd/notifier` runs it too, for the same reason both sides declare the AMQP topology: neither process's startup may depend on the other's order.
2. **Deploy `cmd/api` before `cmd/notifier`.** The API deploy brings the table and the write-time destination policy. Until a notifier runs, behavior is exactly today's — the queue accumulates.
3. **First notifier start drains the backlog safely.** Every accumulated event is evaluated against the enrolment boundary, and every event that predates its owner's preference is acked with no delivery and no record. No purge, no cutoff constant, no timed step.
4. **Rollback is stopping the notifier.** Events accumulate again; `notification_deliveries` rows already written stay valid and are re-read as claims if the process comes back. The only irreversible thing is a webhook already sent, which is true of any delivery system.
5. **Nothing is retired.** Unlike the `.v2` dispatch bump, this change supersedes no queue, exchange, or event type, so there is no post-deploy retirement step.

## Open Questions

- Whether the default attempt budget (3 attempts, 5s each) is right is a guess until real endpoints exist; it is configurable precisely so the answer can be found without a change.
- Whether delivery history should ever be exposed over HTTP — currently out of scope, and the table is shaped so that adding an owner-scoped read later requires no migration.
