# Tasks

Implementation order follows the dependency chain: the domain pieces first (they carry the policy and the record every other layer defers to), then persistence, then the context's own copy of the wire contract, then the outbound adapter and the use case that drives it, then the third composition root and the image, then the write-time half of the destination policy on `cmd/api`.

Each numbered group is sized to be one implementation PR. Group 7 is finalization and belongs to the change's closure PR, not to any implementation PR.

## 1. `internal/notification/domain`

- [ ] 1.1 `terminal_event.go`: the context's own model of a consumed terminal outcome — event type, job id, user id, occurred-at, and the outcome-specific fields (frame count and result key for a completion, reason for a failure). It is built from a decoded message at the composition root; it is not the message type itself, so nothing downstream of it knows a wire format exists.
- [ ] 1.2 `destination_policy.go`: a `DestinationPolicy` deciding whether a destination may be registered and whether a resolved address may be dialled. Restrictive by default: scheme must be `https`; hostname must not be `localhost`; a literal or resolved address must not be loopback, private (RFC 1918 and the IPv6 equivalents), link-local, unspecified, or multicast — link-local is what covers `169.254.169.254`. One boolean relaxes both the scheme rule and the address rule together, per `design.md`; do not split it into two knobs.
- [ ] 1.2a Give the policy two entry points, one over a `Destination` (registration) and one over a `net.IP` (dial), so the dial-side caller cannot accidentally check the hostname instead of the address it resolved to. Table-driven tests over both, including a hostname that is valid and an address that is not.
- [ ] 1.3 `delivery.go`: the delivery record — its identity (`UserID`, `EventType`, `Channel`, job id), a stable `DeliveryID`, a status (`pending`, `delivered`, `failed`), an attempt count, a claimed-at and a resolved-at, and the last observed reason. The reason is free text written by this system, never a response body echoed back from a third party.
- [ ] 1.4 `delivery_repository.go`: the port — `ClaimDelivery(ctx, identity, now, staleBefore) (Delivery, bool, error)` where the boolean reports whether the claim was granted, and `ResolveDelivery(ctx, deliveryID, status, attempts, reason, now) error`. Document on the port that claiming is one atomic statement that reads no row first, and why (`design.md`): the guarantee has to hold across processes, and a check followed by an insert loses that race.
- [ ] 1.5 Extend `repository.go` with `FindDeliverable(ctx, userID, eventType) ([]*NotificationPreference, error)`, returning the aggregate with its `Secret`, for `enabled` preferences only. Document it as the one read path permitted to load the secret and name the test that pins that. Leave `ListByUser` and its `has_secret` projection untouched.
- [ ] 1.6 `deliverer.go`: the outbound port — `Deliver(ctx, preference, event, deliveryID) error` — so the application layer never names `net/http`. Its error type distinguishes a policy refusal from a transport failure from a non-`2xx`, because the recorded reason has to say which.
- [ ] 1.7 Extend `internal/notification/dependency_rules_test.go` to walk **every** package under `internal/notification`, not just `domain` and `application`. Infrastructure keeps its existing allowances (`database/sql`, `net/http`), but the cross-context prohibition applies to all of them — the infrastructure package added in group 3 is exactly where importing `internal/video` would be tempting.

## 2. `internal/notification/infrastructure/postgres`

- [ ] 2.1 `schema.sql`: add `notification_deliveries` with primary key `(user_id, event_type, channel, job_id)`, a `delivery_id UUID NOT NULL`, `status TEXT NOT NULL`, `attempts INT NOT NULL`, `claimed_at TIMESTAMPTZ NOT NULL`, `resolved_at TIMESTAMPTZ`, and a nullable `reason TEXT`. `CREATE TABLE IF NOT EXISTS` only; do not `ALTER` `notification_preferences`.
- [ ] 2.2 The claim is **one statement**: `INSERT ... ON CONFLICT (user_id, event_type, channel, job_id) DO UPDATE SET claimed_at = $now, attempts = 0 WHERE notification_deliveries.status = 'pending' AND notification_deliveries.claimed_at < $stale RETURNING delivery_id`. Zero rows means the claim was refused. The `DO UPDATE` must not overwrite `delivery_id`, so a reclaim keeps the identifier the receiver may already have deduplicated on.
- [ ] 2.2a Verify against a real PostgreSQL that the conflict clause's `WHERE` behaves as intended for all four cases before relying on it: no row, a resolved row, a fresh `pending` row, a stale `pending` row. The equivalent assumption was wrong once already in this context (`2.5a` of the previous change).
- [ ] 2.3 `ResolveDelivery`: an `UPDATE ... WHERE delivery_id = $1` setting status, attempts, reason, and `resolved_at`. It does not need a fence — nothing races it, because only the holder of a granted claim calls it.
- [ ] 2.4 `FindDeliverable`: `SELECT event_type, channel, enabled, destination, secret, created_at, updated_at WHERE user_id = $1 AND event_type = $2 AND enabled` — the only query in this package that names `secret`. Restore full aggregates through `RestoreNotificationPreference`.
- [ ] 2.5 `migrate.go` needs no structural change — the embedded schema grows — but confirm the advisory-locked transaction still covers both `CREATE TABLE IF NOT EXISTS` statements as one unit.
- [ ] 2.6 Adapter tests gated on `NOTIFICATION_POSTGRES_TEST_DSN`, following the existing file exactly: the four claim cases from 2.2a, a concurrent double claim converging on one grant, resolve-then-claim being refused, and `FindDeliverable` returning the secret while `ListByUser` still does not.
- [ ] 2.7 A test asserting the delivery table holds no secret column and that no statement in this package selects `secret` outside `FindDeliverable` — a source-level assertion, the way the dependency rules are enforced, since a runtime test cannot see a query that was never run.

## 3. `internal/notification/infrastructure/messaging`

- [ ] 3.1 `topology.go`: the context's own `TerminalEventsTopology()` over `internal/platform/rabbitmq.Topology`, declaring exchange `video.jobs.terminal.v1`, routing keys `video_job.completed.v1` and `video_job.failed.v1`, queue `video.jobs.terminal.events.v1`, dead-letter exchange `video.jobs.dlx`, queue `video.jobs.dead`, and the same bounds `videojob-terminal-events` pins. Comment that this is a deliberate copy and name the test that pins it.
- [ ] 3.2 `terminal_events.go`: `JobCompletedMessage`/`JobFailedMessage` and their parse functions, field tags identical to the published payloads. Same comment, same named test.
- [ ] 3.3 `consumer.go`: a consumer over the terminal topology. Prefetch 1; declares the topology on every dial; dials with bounded backoff; returns only after the delivery in hand reaches a disposition. Three dispositions — `Ack`, `Reject`, `Requeue` — where `Requeue` pauses before taking the next delivery. Do not copy `internal/video/infrastructure/messaging.Consumer` by import; it is a different disposition set and a different package, and the import is forbidden.
- [ ] 3.4 `cmd/api`'s pinning test grows: assert the copied topology's exchange, queue, dead-letter names, and both routing keys equal `internal/video/infrastructure/messaging.TerminalEventsTopology()`'s, and that a payload written by `internal/video/infrastructure/postgres` decodes into the copied message structs with every field populated. Put it beside `TestNotificationEventTypesMatchTheEmittedTerminalEventTypes`, whose comment already explains why this is the one place allowed to import both.
- [ ] 3.5 Consumer tests gated on `RABBITMQ_TEST_URL`, skipping cleanly when unset, following `internal/video/infrastructure/messaging`'s: each disposition produces the expected broker-side outcome, and a redeclare after a deleted exchange resumes consumption.

## 4. `internal/notification/infrastructure/webhook` and the delivery use case

- [ ] 4.1 `payload.go`: the delivered envelope — delivery id, event type, occurred-at, and a `data` object carrying the job id plus the outcome's own fields. No `user_id`, no secret, no relay bookkeeping. Version it in its own right, independently of the event type's `.v1`.
- [ ] 4.2 `signature.go`: HMAC-SHA256 over `<unix timestamp>.<body>`, hex-encoded, rendered as `sha256=<hex>`. This is the only function in the codebase that calls `Secret.Reveal()`; a test asserts that.
- [ ] 4.3 `client.go`: the `Deliverer` implementation. An `http.Client` whose transport carries a `net.Dialer` with a `Control` function applying the destination policy to the **resolved** address, a `CheckRedirect` that refuses every redirect, a per-attempt timeout, and a response body read under a small cap and discarded. Headers per `design.md`. Returns the typed errors 1.6 defines.
- [ ] 4.3a Test the dial-side policy with a hostname that resolves to a loopback address, asserting no connection is made — a test that passes because the request merely failed proves nothing, so assert on the policy error, not on the request outcome.
- [ ] 4.4 `application/deliver_notification.go`: given a `TerminalEvent`, load deliverable preferences, drop any whose `CreatedAt` is not before the event's `OccurredAt`, and for each remaining one claim, attempt within the budget, and resolve. Returns a disposition-shaped result the composition root maps to `Ack`/`Reject`/`Requeue` — the use case decides *what happened*, the composition root decides *what to tell the broker*, matching how `cmd/worker` splits those two.
- [ ] 4.4a Attempts, backoff, and the per-attempt timeout come from injected configuration with the documented defaults (3 attempts, 5s). Time comes from the existing `Clock` port; do not call `time.Now` in the use case.
- [ ] 4.4b A claim refusal is a success for the caller, not an error. A repository error while claiming or resolving is the only thing that produces the requeue disposition.
- [ ] 4.5 Table-driven tests over fakes for: event before enrolment, disabled preference, no preference, claim refused, `2xx` on the first attempt, `5xx` on every attempt exhausting the budget, a policy refusal recorded with its reason, and a repository error producing the requeue disposition. Assert the delivery id is identical across attempts.
- [ ] 4.6 A test asserting the log output of a full delivery contains neither the secret nor the request body.

## 5. `cmd/notifier`, the image, and compose

- [ ] 5.1 `cmd/notifier/main.go`: `run(ctx)` under `signal.NotifyContext(SIGINT, SIGTERM)`, following `cmd/worker/main.go`'s shape. Load the Notification DSN and `RABBITMQ_URL` first, before any I/O, and fail fast naming a missing one. Open the pool, `Migrate`, ping.
- [ ] 5.2 The handler: decode by event type, build the domain `TerminalEvent` (translating the message's `user_id` into the context's own `UserID` here, which is the sanctioned crossing), call the use case, map its result to a disposition. An undecodable body and an unrecognized event type `Reject`; a repository failure `Requeue`; everything else `Ack`.
- [ ] 5.3 Shutdown: stop consuming, join the consumer so the in-flight delivery reaches a disposition under a bounded drain, then close the pool. The handler runs on `context.WithoutCancel`, so a signal never aborts an outbound request or prevents an outcome being recorded.
- [ ] 5.4 `Dockerfile`: build `/app/notifier` in the `builder` stage and copy it into `runtime` alongside the other two. No new port, no new package.
- [ ] 5.5 `docker-compose.yml`: a `notifier` service on the same image with `command: ["/app/notifier"]`, carrying `NOTIFICATION_POSTGRES_DSN`, `RABBITMQ_URL`, and the destination-policy relaxation set for local development. `depends_on` postgres and rabbitmq, and neither minio nor redis — it uses neither. Comment why the relaxation is set here and must not be set in production.
- [ ] 5.6 `cmd/notifier/main_test.go`: the composition root's own tests, following `cmd/worker`'s — a missing variable is fatal, the disposition mapping is exhaustive over the handler's inputs, and shutdown joins before closing.

## 6. `cmd/api`: the write-time half of the destination policy

- [ ] 6.1 `setupNotification` loads the policy from the environment and passes it into `SetPreference`, so the route refuses a destination the notifier could never dial. The variable name and default are the notifier's — one policy, two readers.
- [ ] 6.2 `SetPreference` applies the policy after `NewDestination` and surfaces a refusal as the same `400` any other validation failure produces. The response says the destination was refused; it does not enumerate which rule caught it.
- [ ] 6.3 `cmd/api/notification_test.go` grows: an `http` destination and a private-address destination are both `400` under the default policy and both accepted with the relaxation set, and a preference stored before the policy existed is neither migrated nor deleted.
- [ ] 6.4 `docker-compose.yml`'s `app` and `app-test` services carry the same relaxation the `notifier` service does — without it the existing tests that register an `http` destination would start failing for a reason unrelated to what they test.

## 7. Finalization (closure PR only — not part of any implementation PR)

- [ ] 7.1 `docs/architecture.md`: the third process, the delivery flow from terminal queue to signed request, and the two-point destination policy.
- [ ] 7.2 `docs/domain-model.md`: the delivery record and its claim semantics.
- [ ] 7.3 `docs/operations.md`: the `notifier` service, its required and optional variables, the destination policy and the consequence of relaxing it, and the note that a stored secret is now read at delivery time.
- [ ] 7.4 `docs/development.md`: the third binary in the build and run commands.
- [ ] 7.5 `docs/roadmap.md`: the `add-notification-webhook-delivery` row and the Phase 7 status line.
- [ ] 7.6 `CLAUDE.md`: the third composition root, the new required variables, and the fact that the terminal queue now has a consumer.
- [ ] 7.7 `npx --yes @fission-ai/openspec validate add-notification-webhook-delivery --strict --no-interactive`, then archive.
