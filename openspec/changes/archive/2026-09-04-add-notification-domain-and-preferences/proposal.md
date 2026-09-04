## Why

`emit-videojob-terminal-events` put `video_job.completed.v1` and `video_job.failed.v1` on a durable queue that nothing reads. The consumer that will read it (`add-notification-webhook-delivery`) has to answer one question per delivered event — *where does this user want to be told, and with what secret do I sign it?* — and today there is nowhere for a user to have answered it. This change builds that: the Notification bounded context's first aggregate, its persistence, and the owner-scoped HTTP surface a user registers a webhook endpoint through.

It ships no consumer and delivers nothing. The terminal queue keeps accumulating exactly as it does now; what changes is that a resolvable destination can exist by the time something drains it.

## What Changes

- A third bounded context, `internal/notification/{domain,application}`, with its own `UserID` value object and no import of `internal/video` or `internal/identity` — `ddd-architecture` forbids Notification from reaching into Video Processing to interpret an event, and the same rule applied at rest means the event-type strings it recognizes are its own constants, declared independently of `postgres.VideoJobCompletedEventType`. That duplication is deliberate and is pinned rather than left loose: `cmd/api`, the composition root that legitimately imports both contexts, asserts the constants are equal. Without that test the two literals could drift silently and the delivery change would resolve every event against an event type no preference row names.
- The `NotificationPreference` aggregate, one per `(UserID, EventType, Channel)`. `EventType` and `Channel` are closed sets: the two terminal event types, and `webhook` alone. `email` is not accepted until `add-notification-email-delivery` ships the adapter that honours it — a channel a user can configure and that silently delivers nothing is worse than a rejected value.
- A per-preference **webhook secret**, carried by the aggregate from the start rather than bolted on by the delivery change. Registering a destination without one produces an endpoint that cannot be signed, so the two belong to the same write. Unlike a password it cannot be hashed — HMAC signing needs the original bytes at delivery time — so it is treated as a credential the way `add-presigned-download-urls` treats an issued URL: accepted on write, never returned by any read, never logged. Reads report only whether one is set.
- Its own PostgreSQL persistence: `NOTIFICATION_POSTGRES_DSN`, a third `sql.DB` pool in `cmd/api`, and a `notification_preferences` table created by the context's own idempotent `Migrate`. This follows the per-context split Identity and Video Processing already have — two separate DSNs and two separate pools, whatever single server they happen to point at in `docker-compose.yml` — and is the same split `add-identity-user-registered-event` is already scoped around.
- Two bearer-authenticated, owner-scoped routes: `GET /api/notification-preferences` returns the caller's own set, and `PUT /api/notification-preferences` upserts exactly one preference named by `event_type` and `channel` in the request body. The pair lives in the body rather than the path because a versioned event type (`video_job.completed.v1`) is a poor path segment and buys nothing. Both mount on a group carrying `requireBearerAuth()` **and** the per-user rate-limit middleware, which is the invariant `cmd/api` holds for every authenticated route rather than a property of the video group specifically. `setupRouter` takes a fourth module.
- Absence of a row means **not subscribed**. There is no implicit default and no backfill: a user who has registered nothing receives nothing, which is the only safe reading when the destination is a URL the system was never given.

No change to `POST /upload`, to the worker, to the terminal-event topology, or to any existing route. No new infrastructure dependency — PostgreSQL is already required at `cmd/api` startup twice over.

## Capabilities

### New Capabilities
- `notification-preferences`: the `NotificationPreference` aggregate and its HTTP surface — what identifies a preference, which event types and channels are accepted, the credential posture of the webhook secret, owner scoping on both routes, upsert semantics, and what an absent row means to a future consumer.
- `notification-persistence`: the Notification context's own PostgreSQL adapter and configuration — `NOTIFICATION_POSTGRES_DSN` as a startup requirement of `cmd/api`, the schema and its uniqueness constraint, idempotent migration, and the pool's place in the documented shutdown ordering.

### Modified Capabilities
- `ddd-architecture`: `Three Bounded Contexts With Non-Overlapping Responsibilities` describes Notification purely as a reactive consumer. It gains the half this change builds — the context also owns delivery preferences, their storage, and an authenticated HTTP surface, and having one does not make it a context others call to trigger a delivery.

## Impact

- **Code**: new `internal/notification/{domain,application,infrastructure/postgres}` and `internal/notification/dependency_rules_test.go` (mirroring `internal/identity`'s and `internal/video`'s); new `cmd/api/notification.go` with `setupNotification` and the two handlers; `cmd/api/main.go` (`setupRouter`'s signature, the third pool's construction and its close in the shutdown sequence). Nothing under `internal/video`, `internal/identity`, or `cmd/worker` changes.
- **Data**: one new table, `notification_preferences`, unique on `(user_id, event_type, channel)`, created by the new context's `Migrate` on `cmd/api` startup. No migration to any existing table; no backfill, because a preference nobody expressed cannot be inferred.
- **Configuration**: `NOTIFICATION_POSTGRES_DSN` required at `cmd/api` startup and fatal when absent, matching both existing DSNs. `docker-compose.yml` gains it for `app` and `app-test` (plus the test-DSN counterpart), and not for `worker`, which registers nothing and reads no preference.
- **Security**: a stored secret that is never returned and never logged. `GET` responses carry `has_secret` instead. `gosec` runs on every PR and the secret must not reach a log line or an error message.
- **Docs**: `docs/architecture.md` gains the context and its routes, `docs/domain-model.md` the aggregate, `docs/operations.md` the new variable, `docs/roadmap.md` the row's status, and `CLAUDE.md` the composition-root and route changes.
