# Tasks

Implementation order follows the dependency chain: the domain's value objects and aggregate first (they carry every validation rule the rest defers to), then the persistence adapter that stores them, then the application use cases, then the HTTP wiring, then tests, then configuration and documentation.

## 1. `internal/notification/domain`

- [x] 1.1 `user_id.go`: a `UserID` value object for this context, mirroring `internal/video/domain/user_id.go`. It exists because Notification may not import Identity, not because the two differ.
- [x] 1.2 `event_type.go`: `EventTypeVideoJobCompleted = "video_job.completed.v1"` and `EventTypeVideoJobFailed = "video_job.failed.v1"`, plus a `ParseEventType` over exactly that closed set returning a typed error for anything else. Comment why the literals are duplicated rather than imported, and name the `cmd/api` test that pins them.
- [x] 1.3 `channel.go`: `ChannelWebhook = "webhook"` and a `ParseChannel` accepting only it. State in a comment that `email` is deliberately absent until `add-notification-email-delivery` ships the adapter — a rejected value is better than a stored preference nothing honours.
- [x] 1.4 `destination.go`: a `Destination` parsed from an absolute URL whose scheme is `http` or `https`. Reject relative, empty, and other schemes. Comment that `http` is accepted for the TLS-less compose stack, per `design.md`.
- [x] 1.5 `secret.go`: a `Secret` with a 16-byte minimum. Give it no `String()`/`GoString()`/`MarshalJSON` that could put the value into a log line or a response by accident — make the leak require deliberate effort.
- [x] 1.6 `notification_preference.go`: three shapes, distinct on purpose (`design.md`). A **write intent** over `(UserID, EventType, Channel, Enabled, Destination)` with an *optional* `Secret` — omission is a legitimate request, so the constructor must not demand one. A **stored preference** that always carries a secret, with a restore function for the repository, matching how `video_job.go` separates `NewVideoJob` from `RestoreVideoJob`. A **read view** carrying `HasSecret bool` and no secret field at all.
- [x] 1.6a Do **not** put "a create requires a secret" in the intent's constructor: the use case cannot know create-from-update without the pre-read the persistence design avoids, so the constructor would either reject valid updates or admit an aggregate violating its own invariant. The rule is enforced by the repository's row count.
- [x] 1.7 `repository.go`: the `PreferenceRepository` port — a `Set` taking the write intent and returning the stored view plus a typed `ErrSecretRequired` when the intent omitted a secret and no preference existed, and a list-by-user. Document that `Set` is atomic on either branch and reads no row, since the port is where that contract is visible to the application layer.
- [x] 1.8 `dependency_rules_test.go` at `internal/notification/`, copied from `internal/identity/dependency_rules_test.go`, with `video-processor/internal/video` and `video-processor/internal/identity` added to the forbidden prefixes — the cross-context rule this context is built under is exactly what should fail the build if broken.

## 2. `internal/notification/infrastructure/postgres`

- [x] 2.1 `config.go`: `Config`/`LoadConfigFromEnv` reading `NOTIFICATION_POSTGRES_DSN`, with an `ErrDSNRequired` naming the variable. Same shape as the two existing contexts'.
- [x] 2.2 `db.go`: `Open` over the same driver and pool settings the other two use. Read both existing `db.go` files first and match whichever they agree on rather than inventing settings.
- [x] 2.3 `schema.sql`: `notification_preferences` with a composite primary key `(user_id, event_type, channel)`, `enabled BOOLEAN NOT NULL`, `destination TEXT NOT NULL`, `created_at`/`updated_at TIMESTAMPTZ NOT NULL`, and `secret TEXT NOT NULL CONSTRAINT notification_preferences_secret_not_empty CHECK (secret <> '')` — **no** `DEFAULT ''`, per `design.md`: the constraint is what refuses a create with no secret, and a default would swallow it. `CREATE TABLE IF NOT EXISTS` only — there is no pre-existing table to `ALTER`.
- [x] 2.4 `migrate.go`: embedded-schema `Migrate` in a transaction holding `pg_advisory_xact_lock` on a constant this context owns, per `design.md`. `CREATE TABLE IF NOT EXISTS` alone is idempotent but does **not** serialize two first-time creates — two replicas starting together can race to a catalog uniqueness violation, which here means a replica that will not boot. Deliberately stricter than the identity and video adapters; do not "fix" it back to match them.
- [x] 2.4a Test it: run two `Migrate` calls concurrently against a database with no such table and assert both return nil and the table exists once.
- [x] 2.5 `repository.go`: `Set` branches on whether the **intent carries a secret**, not on what the database holds — both statements are in `design.md`, verified against PostgreSQL 16. Secret present: `INSERT ... ON CONFLICT DO UPDATE` overwriting `secret` with `EXCLUDED.secret`. Secret omitted: an `UPDATE ... RETURNING` that never names the `secret` column; zero rows affected is the create-with-no-secret case and returns `ErrSecretRequired`.
- [x] 2.5a Do **not** encode an omitted secret as `''` in an inserted tuple. PostgreSQL evaluates `NOT NULL`/`CHECK` against the proposed row *before* it detects the uniqueness conflict, so such a statement aborts and the `DO UPDATE` clause never runs — this was the flaw in the first version of this proposal and the reason the design has two statements.
- [x] 2.5b Both statements `RETURNING ... secret <> '' AS has_secret`, and the list query projects the same. **Never** select `secret` on any read path — the value must not be loadable where a response is built.
- [x] 2.6 List ordering is `ORDER BY event_type, channel`, so the response is deterministic rather than dependent on physical row order.
- [x] 2.6a Adapter test against a real database, covering the four verified paths: create with a secret; update omitting one (secret intact, `has_secret` still true); create omitting one (zero rows → `ErrSecretRequired`, nothing stored); secret replaced through the insert path. Then two concurrent creates of one triple converging on one row. A fake repository cannot cover these — the row count is the enforcement.
- [x] 2.7 `repository_test.go` gated on `NOTIFICATION_POSTGRES_TEST_DSN`, skipping cleanly when unset, following `internal/identity/infrastructure/postgres/repository_test.go` exactly — including the truncate between cases.

## 3. `internal/notification/application`

- [x] 3.1 `SetPreference`: parse every field through the domain, build the write intent, surface the repository's `ErrSecretRequired` as a validation failure rather than an unexpected one — the use case does not pre-read to decide create-vs-update, per `design.md` — and stamp `UpdatedAt` (and `CreatedAt` on insert) from an injected `Clock`, call the repository, return the stored view. Reuse the `Clock` port shape both existing contexts already have rather than calling `time.Now` in the use case.
- [x] 3.2 `ListPreferences`: return the caller's preferences. The `UserID` is a parameter, never read from anywhere ambient.
- [x] 3.3 Neither use case returns the secret in its result type. Give the result a `HasSecret bool` and no secret field at all, so the handler cannot serialize one.
- [x] 3.4 Table-driven unit tests over a fake repository for both use cases: unknown event type, unknown channel, bad destination, short secret, missing secret on create, omitted secret on update, and the owner-scoping of the list.

## 4. `cmd/api` wiring

- [x] 4.1 New `cmd/api/notification.go` following `identity.go`/`video.go`: a `notificationModule`, a `setupNotification(ctx)` that loads the config, opens, migrates, pings, and returns the module plus its `*sql.DB`, and a `registerRoutes(group)`.
- [x] 4.2 `handleListPreferences` for `GET /api/notification-preferences` — reads the authenticated `UserID` from the gin context under `authenticatedUserIDKey`, never from the request.
- [x] 4.3 `handleSetPreference` for `PUT /api/notification-preferences` — binds the body, ignores any `user_id` it carries, maps a domain validation error to `400` and an unexpected repository error to `500`. English strings, per the language policy; the `web/` pt-BR exception does not reach `cmd/api/*.go`.
- [x] 4.4 The secret field is accepted as a pointer or an explicit "present" flag so that *omitted* and *empty* are distinguishable — the spec rejects an explicit empty string and preserves on omission, and a plain `string` field collapses the two.
- [x] 4.5 `setupRouter` takes the notification module and mounts a group carrying `identity.requireBearerAuth()` and `rateLimitMiddleware(limiter)`, in that order. A separate group from `videoRoutes`, per `design.md` — the middleware pair is the invariant, not the group.
- [x] 4.5a Add `PUT` to the global CORS middleware's `Access-Control-Allow-Methods`, which still advertises only `POST, GET, OPTIONS` (`cmd/api/main.go:143`). Without it a browser preflight rejects the write on a correctly-mounted route, and nothing else in this diff would prompt the change.
- [x] 4.6 `main()` calls `setupNotification`, and shutdown closes its pool alongside the existing two, after the server stops and the relay goroutine is joined. Do not reorder the existing sequence; it is load-bearing and documented.
- [x] 4.7 A setup failure is fatal and names the missing variable, matching `setupIdentity`/`setupVideo`.

## 5. Tests

- [x] 5.1 `cmd/api/notification_test.go` driving the real handlers through `httptest.NewServer`, the way `identity_test.go` and `video_test.go` do: register, log in, then exercise both routes with the issued token.
- [x] 5.2 `401` on both routes with no token, a malformed token, and an expired one.
- [x] 5.3 An empty read is `200` with an empty collection, not `404`.
- [x] 5.4 A write followed by a read returns the preference, with `has_secret: true` and **no** secret field anywhere in the body. Assert on the raw response bytes, not on a decoded struct — a decoded struct cannot show a field the struct does not declare.
- [x] 5.5 Two users' preferences do not leak across the owner boundary, in either direction, on either route.
- [x] 5.6 A body carrying another user's `user_id` writes for the caller and leaves the other user's preferences untouched.
- [x] 5.7 Writing one triple leaves a second triple's preference intact.
- [x] 5.8 An update omitting the secret keeps `has_secret: true`; an update with an explicitly empty secret is `400`.
- [x] 5.9 Rejected values — unknown event type (including the unversioned `video_job.completed`), channel `email`, a relative destination, an `ftp://` destination, a 15-byte secret — are each `400` and store nothing.
- [x] 5.9a An `OPTIONS` preflight for the write route answers `204` with `PUT` present in `Access-Control-Allow-Methods`.
- [x] 5.10 The rate-limit middleware is actually mounted: exceed the limit against a preference route and assert `429` + `Retry-After`, mirroring how `ratelimit_test.go` covers the video routes.
- [x] 5.11 `TestNotificationEventTypesMatchTheEmittedTerminalEventTypes` in `cmd/api`: a table asserting each `notification/domain` constant equals the corresponding `video/infrastructure/postgres` exported constant, in the spirit of `TestRoutingKeyMatchesTheOutboxEventType`.
- [x] 5.12 `cmd/api/main_test.go`'s `TestMain` gate: decide whether `NOTIFICATION_POSTGRES_DSN` joins the required set it already exits 1 without. It should — the composition root will not boot without it, so a suite that skipped would report green while covering none of these routes.

  **Decided the other way, and the reasoning is recorded at `cmd/api/notification_test.go`'s `inMemoryPreferenceRepository`.** No test in this package calls `setupIdentity`, `setupVideo`, or `setupNotification` — every one builds its modules by hand and hands them to `setupRouter` — which is why CI's `Test` step sets neither `IDENTITY_POSTGRES_DSN` nor `VIDEO_POSTGRES_DSN` either. Real MinIO is the sole exception, for a reason that does not apply here: presigning and `Stat` are route behaviour, whereas the adapter's two-statement branch is invisible to these handlers, which only forward `ErrSecretRequired` as a `400`. A real pool would also flake — the adapter suite `TRUNCATE`s `notification_preferences` on every `testDB` call, `go test ./...` runs packages in parallel, and CI points both DSNs at one database.

## 6. Configuration

- [x] 6.1 `docker-compose.yml`: `NOTIFICATION_POSTGRES_DSN` on `app` and `app-test`, plus `NOTIFICATION_POSTGRES_TEST_DSN` on the services that run tests, following the existing DSN pairs exactly. **Not** on `worker`, which registers nothing.
- [x] 6.2 `.github/workflows/ci.yml`: add the variable wherever the existing DSNs are set, so `Build & Test` still boots. **What that turned out to be is `NOTIFICATION_POSTGRES_TEST_DSN` alone**, following 5.12: the `Test` step sets the three `*_TEST_DSN` variables and no runtime DSN, because nothing in the suite opens a pool from one. Adding `NOTIFICATION_POSTGRES_DSN` there would set a variable no test reads.
- [x] 6.3 Confirm `go vet ./...` and `gosec` are clean — in particular that no handler or log line can reach the secret, which is the one finding this change could plausibly produce.

## 7. Verification

- [x] 7.1 `go test ./... -v` passes locally with `ffmpeg`, MinIO, and the PostgreSQL test DSNs set, so the new adapter is exercised rather than skipped. Use `docker compose run --build --rm app-test go test ./... -v` if the local environment lacks any of them.
- [x] 7.2 End-to-end non-regression: upload → poll → download still works, and `setupRouter`'s new signature broke nothing.
- [x] 7.3 Start `cmd/api` with `NOTIFICATION_POSTGRES_DSN` unset and confirm it exits with an error naming the variable.
- [x] 7.4 Start `cmd/worker` **without** the variable and confirm it is unaffected.
- [x] 7.5 Confirm nothing under `internal/video`, `internal/identity`, or `cmd/worker` changed.

## 8. Finalization (separate PR)

- [x] 8.1 Check off sections 1–7.
- [x] 8.2 `CLAUDE.md`: the route list in the Architecture section, `setupRouter`'s signature and the composition-root paragraph, the documented shutdown ordering, the required-at-startup variables, and a gotcha bullet for the preference secret's non-disclosure posture.
- [x] 8.3 `docs/architecture.md`: the Notification context becomes a wired one — its package layout, its own pool, and the two routes.
- [x] 8.4 `docs/domain-model.md`: the `NotificationPreference` aggregate, its closed value sets, and that an absent preference means not subscribed.
- [x] 8.5 `docs/operations.md`: `NOTIFICATION_POSTGRES_DSN` in the environment table, and the sensitivity of the `secret` column — it is plaintext by necessity, so backups and database access carry it.
- [x] 8.6 `docs/roadmap.md`: flip this row to archived with links to the promoted specs.
- [x] 8.7 `README.md`: the two new routes, if it lists routes.
- [x] 8.8 `npx --yes @fission-ai/openspec validate add-notification-domain-and-preferences --strict --no-interactive` passes, then `/opsx:archive`.
