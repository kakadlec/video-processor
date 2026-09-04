## Context

See `proposal.md` — Why. Three constraints from the existing codebase shape everything below.

**The dependency rule is stricter than it looks.** `ddd-architecture`'s "Notification consumes events without coupling to Video Processing internals" is a rule about imports, and it holds at rest, not only while handling an event. So `internal/notification` cannot import `internal/video/infrastructure/postgres` to learn what `video_job.completed.v1` is called, and cannot import `internal/identity/domain` for a `UserID` either — `internal/video/domain/user_id.go` already exists for exactly that reason and is the precedent to copy.

**Per-context persistence is established, not novel.** `internal/identity/infrastructure/postgres` and `internal/video/infrastructure/postgres` are two packages with the same shape: a `Config`/`LoadConfigFromEnv` reading one context-specific variable, an `Open`, an embedded `schema.sql`, and an idempotent `Migrate` called from the composition root. `docker-compose.yml` points both DSNs at one server; the code never knows that. Notification is a third instance of the same pattern, so the only genuinely new decisions here are the aggregate's shape and the route contract.

**`cmd/api` already owns two pools and an ordered shutdown.** The ordering is load-bearing and documented: `server.Shutdown` → cancel the outbox relay → join it → close the pools → close Redis. Notification adds a pool and no goroutine, which is the easy case.

## Goals / Non-Goals

**Goals:**

- A `NotificationPreference` a user can register, read back, and toggle, resolvable by a future consumer from a `(user_id, event_type, channel)` triple alone.
- A signing secret that is storable, usable for HMAC later, and never disclosed.
- Structural parity with the two existing contexts, so the third one is boring to read.

**Non-Goals:**

- No consumer, no delivery, no HMAC computation, no retry policy — `add-notification-webhook-delivery` owns all of it. This change stores the inputs that change will read.
- No `cmd/notifier`, no change to `cmd/worker`, no new AMQP connection. Notification does not touch the broker in this change.
- No frontend. `cmd/api/web/` is untouched; preferences are an API-only surface for now, and no pt-BR UI copy is added.
- No caching. The status cache exists because `GET /api/video-jobs/:id` is polled every two seconds by the browser; a preference read is neither hot nor on any critical path, and a cache decorator here would be unbacked complexity.
- No `UserRegistered` projection. Notification stores a `UserID` it receives from a verified token and never resolves it to a person; the e-mail address that eventually needs projecting arrives with `add-identity-user-registered-event`.

## Decisions

### The triple is the primary key; there is no surrogate ID

`notification_preferences` uses a composite primary key `(user_id, event_type, channel)` rather than a UUID `id` with a unique constraint beside it.

The aggregate has no identity independent of what it configures, nothing references it by ID, and no route addresses it by one — the write route names the triple in its body and the read route returns a set. A surrogate key would add an `idgen` dependency (both other contexts have one) purely to generate values nothing reads. The composite key also gives the upsert its conflict target for free.

*Alternative considered:* UUID primary key + `UNIQUE (user_id, event_type, channel)`. Rejected as the same guarantee with one more column, one more package, and one more thing to keep unique.

### The write is a single `INSERT ... ON CONFLICT DO UPDATE`

The spec requires two concurrent writes of one triple to converge without either caller seeing a constraint violation, and requires an omitted secret to preserve the stored one. Both fall out of one statement:

```
INSERT INTO notification_preferences
       (user_id, event_type, channel, enabled, destination, secret, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $7)
ON CONFLICT (user_id, event_type, channel) DO UPDATE
   SET enabled     = EXCLUDED.enabled,
       destination = EXCLUDED.destination,
       secret      = COALESCE(NULLIF(EXCLUDED.secret, ''), notification_preferences.secret),
       updated_at  = EXCLUDED.updated_at
RETURNING enabled, destination, secret <> '' AS has_secret, created_at, updated_at
```

The `COALESCE(NULLIF(...))` is what makes "omitted means keep" atomic. A read-then-write in Go would need a transaction to be safe and would still be two round trips; here the row never has to be read.

The domain layer, not SQL, is what refuses an *explicitly empty* secret — an empty string reaches the repository only as the encoding of "the caller did not send one," which is why the SQL can treat empty as "keep."

*Alternative considered:* `SELECT` then `INSERT`/`UPDATE` in a transaction. Rejected: more round trips, a serialization concern to reason about, and it puts a credential through the application layer on a path that does not need it.

### A `CHECK` constraint is what makes "a create needs a secret" reachable

The two secret rules pull against each other: a **create** with no secret must be refused, while an **update** with no secret must preserve the stored one. Distinguishing them needs to know whether a row already exists — which is exactly what the single-statement upsert refuses to look up, and what a `NOT NULL DEFAULT ''` column would happily accept, storing `secret = ''` and reporting `has_secret: false`.

The column therefore carries a named constraint instead of a default:

```
secret TEXT NOT NULL
    CONSTRAINT notification_preferences_secret_not_empty CHECK (secret <> '')
```

The insert path violates it precisely when a create omits the secret. The conflict path can never violate it, because `COALESCE(NULLIF(EXCLUDED.secret, ''), notification_preferences.secret)` yields either the submitted secret or the stored one, and the stored one is non-empty by the same constraint. So one statement enforces both rules, and the adapter's only job is to map that one constraint — matched by name, not by SQLSTATE `23514` alone, so a future constraint on another column is not silently folded into the same error — to a typed domain error the handler renders as `400`.

The discriminating case is two concurrent first-writes of the same triple where one omits the secret. Whichever lands second takes the conflict path, finds a stored secret, and preserves it — a correct outcome, because by the time it executes a preference *is* stored and its write genuinely is an update. A read-then-write would decide "create" from a snapshot taken before the other insert committed and would then have to fail on the insert anyway; the constraint is what makes the outcome well-defined either way.

*Alternative considered:* `RETURNING (xmax = 0) AS inserted` inside a transaction, rolling back when it inserted without a secret. Same guarantee, but it adds a transaction to do what the constraint does for free, and leaves the invariant expressible only in Go — a second writer of this table could violate it.

### The secret is column-plaintext, and non-disclosure is the whole control

HMAC signing needs the original bytes, so bcrypt — the tool `internal/identity/infrastructure/password` uses — is structurally unavailable. Encrypting at rest would require a key-management story (a new required variable, rotation, and a decision about what happens to stored rows when the key changes) that is disproportionate to a hackathon deliverable and is not what the delivery change asked for.

What this change does instead, and what the spec pins: the value never leaves the process on any path. The repository's read query projects `secret <> ''` rather than `secret`, so the value is not even loaded on the read path — a response DTO cannot accidentally carry a field the row does not hold. Handlers log the triple, never the body. This is the same posture `add-presigned-download-urls` established for an issued URL: treat it as a credential, never log, echo, or persist it beyond its store.

*Alternative considered:* store `argon2`/`bcrypt` of the secret. Impossible — verification is not the operation; signing is.

### `enabled` is a stored flag, and a disabled preference is retained

Deleting on disable would destroy the destination and the secret, so re-enabling would mean re-registering an endpoint the user already registered. Since the secret is never returned, they could not even reconstruct it from what the API told them. The flag also keeps the write route total: every accepted request is one upsert, with no branch that deletes.

There is deliberately no `DELETE` route in this change. Nothing in the delivery story needs one, and the disable path covers the user-visible need.

### Validation lives in the domain, as value objects

`EventType` and `Channel` are constructed through parsing functions over closed sets, `Destination` over an absolute `http`/`https` URL, `Secret` over a minimum length. Handlers map a construction error to `400` and never re-implement a check. This mirrors `internal/identity/domain`'s `Email` and `internal/video/domain`'s `OriginalFilename`/`StorageKey`, and it is what keeps the closed sets from being duplicated between a handler and a repository.

Minimum secret length is **16 bytes**, stated as a decision rather than derived: it is the shortest length that is not obviously weak for HMAC-SHA256 and does not push a user toward generating a key with tooling they may not have. `http` destinations are accepted alongside `https` because local development and the compose stack have no TLS, and rejecting them would make the feature untestable in the only environment this project runs in; the trade-off is recorded below.

### The cross-context constant pin lives in `cmd/api`

`internal/notification/domain` declares `EventTypeVideoJobCompleted = "video_job.completed.v1"` and its failed counterpart. `internal/video/infrastructure/postgres` already exports `VideoJobCompletedEventType`/`VideoJobFailedEventType`. Neither package may import the other, but `cmd/api` imports both legitimately — it is the composition root, and `ddd-architecture` names it as the only DI boundary.

So a table-driven test in `cmd/api` asserts the pairs are equal, in the same spirit as `TestRoutingKeyMatchesTheOutboxEventType`, which pins the same kind of uncompiled coupling between the outbox and the topology. Placing it here rather than deferring it to the delivery change means the constants cannot drift during the change that introduces the duplication.

*Alternative considered:* a shared constants package under `internal/platform/`. Rejected — `internal/platform` is confined to connection and lifecycle plumbing and is forbidden by test from containing a context name (`video_job.` is literally one of the banned substrings). These strings are an integration contract, not plumbing.

### Route contract

Both routes mount on a new group in `setupRouter` carrying `identity.requireBearerAuth()` and `rateLimitMiddleware(limiter)` — the same two, in the same order, as `videoRoutes`. They are a separate group rather than an addition to `videoRoutes` because that group is documented as "every route that serves or accepts video-processing artifacts," and a preference is not one; the middleware pair is what matters and is preserved.

```
GET /api/notification-preferences
  → 200 {"preferences":[{"event_type":…,"channel":"webhook","enabled":true,
                         "destination":"https://…","has_secret":true,
                         "created_at":…,"updated_at":…}]}
    Empty set is 200 with an empty array, never 404.
    Ordered by (event_type, channel) so the response is deterministic.

PUT /api/notification-preferences
  ← {"event_type":"video_job.failed.v1","channel":"webhook","enabled":true,
     "destination":"https://…","secret":"…"}         secret optional on update
  → 200  same object shape as one element of the read, without the secret
  → 400  unknown event type or channel, bad destination, short secret,
         missing secret on create, malformed body
  → 401  missing or invalid bearer token
  → 429  rate limited
```

`PUT` rather than `POST` because the operation is idempotent on the triple the body names: the same request repeated produces the same stored state. A `user_id` in the body is ignored, not rejected — the authenticated subject is the only source, and rejecting would leak whether a guessed identifier exists.

*Alternative considered:* `PUT /api/notification-preferences/:eventType/:channel`. Rejected — a versioned, dotted event type is a fragile path segment for no gain, and the body already has to carry the rest.

## Risks / Trade-offs

- **A constraint violation is a `400`, not a `500`** → The adapter matches `notification_preferences_secret_not_empty` by name and returns a typed error; every other constraint failure stays a `500`. A rename of the constraint in `schema.sql` without the matching change in the adapter would turn a validation error back into a `500`, so the test for "create without a secret" runs against a real database rather than a fake repository.
- **A plaintext secret in a database column** → Never returned by any route, never logged, not loaded on the read path (`secret <> ''` is projected instead), and confined to a database that already holds password hashes and job records. Recorded in `docs/operations.md` so an operator knows the column's sensitivity. A future change can encrypt at rest without altering the route contract, because the value is already write-only from the API's point of view.
- **`http` destinations are accepted, so a signed payload can travel in clear text** → Accepted deliberately for local development, where no TLS exists. The delivery change is the right place to add an operator-facing switch that restricts destinations to `https` in production, because it is the process that opens the connection.
- **Two independently-declared copies of each event-type string** → Pinned by a composition-root test that fails on either side's rename. Failure mode without it — a consumer resolving preferences against a name no row carries — is silent, which is precisely why the pin is in this change rather than the next.
- **A destination is never verified to exist or to be under the user's control** → Out of scope; there is no delivery yet, so an unreachable URL costs nothing until `add-notification-webhook-delivery`, which owns the retry and failure story. An SSRF-shaped concern (a destination pointing at an internal address) becomes real only when something dials it, and belongs to that change with the dialer it introduces.
- **A third required environment variable at `cmd/api` startup** → Fatal-when-absent matches both existing DSNs and the fail-closed posture used for MinIO; a preference route that cannot reach its database is not usefully degraded. `docker-compose.yml`, `docs/operations.md`, and CI's test environment are updated in the same change so nothing is left failing to start.

## Migration Plan

No data migration. The new table is created by the context's own idempotent `Migrate` at `cmd/api` startup, and no existing table is touched.

Deployment ordering is unconstrained in one direction and constrained in the other: `NOTIFICATION_POSTGRES_DSN` must be present in the environment **before** the new image starts, or `cmd/api` will not boot. `cmd/worker` is unaffected and needs no coordinated deploy.

Rollback is a plain image revert. The previous `cmd/api` ignores both the variable and the table; stored preferences survive untouched and are picked up again on a re-deploy. Nothing consumes the rows yet, so no downstream behavior changes in either direction.
