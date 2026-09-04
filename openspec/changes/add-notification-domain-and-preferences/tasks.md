# Tasks

Implementation order follows the dependency chain: the domain's value objects and aggregate first (they carry every validation rule the rest defers to), then the persistence adapter that stores them, then the application use cases, then the HTTP wiring, then tests, then configuration and documentation.

## 1. `internal/notification/domain`

- [ ] 1.1 `user_id.go`: a `UserID` value object for this context, mirroring `internal/video/domain/user_id.go`. It exists because Notification may not import Identity, not because the two differ.
- [ ] 1.2 `event_type.go`: `EventTypeVideoJobCompleted = "video_job.completed.v1"` and `EventTypeVideoJobFailed = "video_job.failed.v1"`, plus a `ParseEventType` over exactly that closed set returning a typed error for anything else. Comment why the literals are duplicated rather than imported, and name the `cmd/api` test that pins them.
- [ ] 1.3 `channel.go`: `ChannelWebhook = "webhook"` and a `ParseChannel` accepting only it. State in a comment that `email` is deliberately absent until `add-notification-email-delivery` ships the adapter — a rejected value is better than a stored preference nothing honours.
- [ ] 1.4 `destination.go`: a `Destination` parsed from an absolute URL whose scheme is `http` or `https`. Reject relative, empty, and other schemes. Comment that `http` is accepted for the TLS-less compose stack, per `design.md`.
- [ ] 1.5 `secret.go`: a `Secret` with a 16-byte minimum. Give it no `String()`/`GoString()`/`MarshalJSON` that could put the value into a log line or a response by accident — make the leak require deliberate effort.
- [ ] 1.6 `notification_preference.go`: the aggregate over `(UserID, EventType, Channel, Enabled, Destination, Secret, CreatedAt, UpdatedAt)`, with a constructor that enforces "webhook requires a destination and a secret on create" and a restore function for the repository, matching how `video_job.go` separates `NewVideoJob` from `RestoreVideoJob`.
- [ ] 1.7 `repository.go`: the `PreferenceRepository` port — an upsert taking the preference and a "secret omitted" signal, and a list-by-user. Document that the upsert is expected to be atomic and that an omitted secret preserves the stored one, since the port is where that contract is visible to the application layer.
- [ ] 1.8 `dependency_rules_test.go` at `internal/notification/`, copied from `internal/identity/dependency_rules_test.go`, with `video-processor/internal/video` and `video-processor/internal/identity` added to the forbidden prefixes — the cross-context rule this context is built under is exactly what should fail the build if broken.

## 2. `internal/notification/infrastructure/postgres`

- [ ] 2.1 `config.go`: `Config`/`LoadConfigFromEnv` reading `NOTIFICATION_POSTGRES_DSN`, with an `ErrDSNRequired` naming the variable. Same shape as the two existing contexts'.
- [ ] 2.2 `db.go`: `Open` over the same driver and pool settings the other two use. Read both existing `db.go` files first and match whichever they agree on rather than inventing settings.
- [ ] 2.3 `schema.sql`: `notification_preferences` with a composite primary key `(user_id, event_type, channel)`, `enabled BOOLEAN NOT NULL`, `destination TEXT NOT NULL`, `secret TEXT NOT NULL DEFAULT ''`, `created_at`/`updated_at TIMESTAMPTZ NOT NULL`. `CREATE TABLE IF NOT EXISTS` only — there is no pre-existing table to `ALTER`.
- [ ] 2.4 `migrate.go`: embedded-schema `Migrate`, idempotent and safe to run concurrently, mirroring the identity adapter.
- [ ] 2.5 `repository.go`: the `INSERT ... ON CONFLICT DO UPDATE` from `design.md`, with `COALESCE(NULLIF(EXCLUDED.secret, ''), notification_preferences.secret)` carrying the "omitted means keep" rule. The list query projects `secret <> '' AS has_secret` and **never** selects `secret` — the value must not be loadable on the read path.
- [ ] 2.6 List ordering is `ORDER BY event_type, channel`, so the response is deterministic rather than dependent on physical row order.
- [ ] 2.7 `repository_test.go` gated on `NOTIFICATION_POSTGRES_TEST_DSN`, skipping cleanly when unset, following `internal/identity/infrastructure/postgres/repository_test.go` exactly — including the truncate between cases.

## 3. `internal/notification/application`

- [ ] 3.1 `SetPreference`: parse every field through the domain, refuse a create with no secret, stamp `UpdatedAt` (and `CreatedAt` on insert) from an injected `Clock`, call the repository, return the stored view. Reuse the `Clock` port shape both existing contexts already have rather than calling `time.Now` in the use case.
- [ ] 3.2 `ListPreferences`: return the caller's preferences. The `UserID` is a parameter, never read from anywhere ambient.
- [ ] 3.3 Neither use case returns the secret in its result type. Give the result a `HasSecret bool` and no secret field at all, so the handler cannot serialize one.
- [ ] 3.4 Table-driven unit tests over a fake repository for both use cases: unknown event type, unknown channel, bad destination, short secret, missing secret on create, omitted secret on update, and the owner-scoping of the list.

## 4. `cmd/api` wiring

- [ ] 4.1 New `cmd/api/notification.go` following `identity.go`/`video.go`: a `notificationModule`, a `setupNotification(ctx)` that loads the config, opens, migrates, pings, and returns the module plus its `*sql.DB`, and a `registerRoutes(group)`.
- [ ] 4.2 `handleListPreferences` for `GET /api/notification-preferences` — reads the authenticated `UserID` from the gin context under `authenticatedUserIDKey`, never from the request.
- [ ] 4.3 `handleSetPreference` for `PUT /api/notification-preferences` — binds the body, ignores any `user_id` it carries, maps a domain validation error to `400` and an unexpected repository error to `500`. English strings, per the language policy; the `web/` pt-BR exception does not reach `cmd/api/*.go`.
- [ ] 4.4 The secret field is accepted as a pointer or an explicit "present" flag so that *omitted* and *empty* are distinguishable — the spec rejects an explicit empty string and preserves on omission, and a plain `string` field collapses the two.
- [ ] 4.5 `setupRouter` takes the notification module and mounts a group carrying `identity.requireBearerAuth()` and `rateLimitMiddleware(limiter)`, in that order. A separate group from `videoRoutes`, per `design.md` — the middleware pair is the invariant, not the group.
- [ ] 4.6 `main()` calls `setupNotification`, and shutdown closes its pool alongside the existing two, after the server stops and the relay goroutine is joined. Do not reorder the existing sequence; it is load-bearing and documented.
- [ ] 4.7 A setup failure is fatal and names the missing variable, matching `setupIdentity`/`setupVideo`.

## 5. Tests

- [ ] 5.1 `cmd/api/notification_test.go` driving the real handlers through `httptest.NewServer`, the way `identity_test.go` and `video_test.go` do: register, log in, then exercise both routes with the issued token.
- [ ] 5.2 `401` on both routes with no token, a malformed token, and an expired one.
- [ ] 5.3 An empty read is `200` with an empty collection, not `404`.
- [ ] 5.4 A write followed by a read returns the preference, with `has_secret: true` and **no** secret field anywhere in the body. Assert on the raw response bytes, not on a decoded struct — a decoded struct cannot show a field the struct does not declare.
- [ ] 5.5 Two users' preferences do not leak across the owner boundary, in either direction, on either route.
- [ ] 5.6 A body carrying another user's `user_id` writes for the caller and leaves the other user's preferences untouched.
- [ ] 5.7 Writing one triple leaves a second triple's preference intact.
- [ ] 5.8 An update omitting the secret keeps `has_secret: true`; an update with an explicitly empty secret is `400`.
- [ ] 5.9 Rejected values — unknown event type (including the unversioned `video_job.completed`), channel `email`, a relative destination, an `ftp://` destination, a 15-byte secret — are each `400` and store nothing.
- [ ] 5.10 The rate-limit middleware is actually mounted: exceed the limit against a preference route and assert `429` + `Retry-After`, mirroring how `ratelimit_test.go` covers the video routes.
- [ ] 5.11 `TestNotificationEventTypesMatchTheEmittedTerminalEventTypes` in `cmd/api`: a table asserting each `notification/domain` constant equals the corresponding `video/infrastructure/postgres` exported constant, in the spirit of `TestRoutingKeyMatchesTheOutboxEventType`.
- [ ] 5.12 `cmd/api/main_test.go`'s `TestMain` gate: decide whether `NOTIFICATION_POSTGRES_DSN` joins the required set it already exits 1 without. It should — the composition root will not boot without it, so a suite that skipped would report green while covering none of these routes.

## 6. Configuration

- [ ] 6.1 `docker-compose.yml`: `NOTIFICATION_POSTGRES_DSN` on `app` and `app-test`, plus `NOTIFICATION_POSTGRES_TEST_DSN` on the services that run tests, following the existing DSN pairs exactly. **Not** on `worker`, which registers nothing.
- [ ] 6.2 `.github/workflows/ci.yml`: add the variable wherever the existing DSNs are set, so `Build & Test` still boots.
- [ ] 6.3 Confirm `go vet ./...` and `gosec` are clean — in particular that no handler or log line can reach the secret, which is the one finding this change could plausibly produce.

## 7. Verification

- [ ] 7.1 `go test ./... -v` passes locally with `ffmpeg`, MinIO, and the PostgreSQL test DSNs set, so the new adapter is exercised rather than skipped. Use `docker compose run --build --rm app-test go test ./... -v` if the local environment lacks any of them.
- [ ] 7.2 End-to-end non-regression: upload → poll → download still works, and `setupRouter`'s new signature broke nothing.
- [ ] 7.3 Start `cmd/api` with `NOTIFICATION_POSTGRES_DSN` unset and confirm it exits with an error naming the variable.
- [ ] 7.4 Start `cmd/worker` **without** the variable and confirm it is unaffected.
- [ ] 7.5 Confirm nothing under `internal/video`, `internal/identity`, or `cmd/worker` changed.

## 8. Finalization (separate PR)

- [ ] 8.1 Check off sections 1–7.
- [ ] 8.2 `CLAUDE.md`: the route list in the Architecture section, `setupRouter`'s signature and the composition-root paragraph, the documented shutdown ordering, the required-at-startup variables, and a gotcha bullet for the preference secret's non-disclosure posture.
- [ ] 8.3 `docs/architecture.md`: the Notification context becomes a wired one — its package layout, its own pool, and the two routes.
- [ ] 8.4 `docs/domain-model.md`: the `NotificationPreference` aggregate, its closed value sets, and that an absent preference means not subscribed.
- [ ] 8.5 `docs/operations.md`: `NOTIFICATION_POSTGRES_DSN` in the environment table, and the sensitivity of the `secret` column — it is plaintext by necessity, so backups and database access carry it.
- [ ] 8.6 `docs/roadmap.md`: flip this row to archived with links to the promoted specs.
- [ ] 8.7 `README.md`: the two new routes, if it lists routes.
- [ ] 8.8 `npx --yes @fission-ai/openspec validate add-notification-domain-and-preferences --strict --no-interactive` passes, then `/opsx:archive`.
