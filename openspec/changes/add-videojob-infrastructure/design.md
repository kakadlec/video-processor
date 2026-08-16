## Context

`internal/identity/infrastructure/postgres` (Phase 2) is the only precedent for a PostgreSQL adapter in this repo: `Config`/`LoadConfigFromEnv` (env-var DSN, fails fast if unset), `Open` (bare `sql.Open`, no connectivity check), `Migrate` (embeds `schema.sql`, runs `CREATE TABLE IF NOT EXISTS` idempotently on every startup — no external migration tool), and `Repository` (parameterized queries, a `*IDParser` port to reconstruct the domain ID type from the stored string, `RestoreX`-style reconstruction of the aggregate). This change follows that shape exactly for `video_jobs`, and adds one new element that Identity didn't need: a transactional outbox table, `video_job_outbox`.

`docs/architecture.md`'s Infrastructure Components table already documents PostgreSQL as "Authoritative state store for users (jobs/outbox tables land in Phase 3)" — a single shared instance/database across contexts, not one database per context. `add-videojob-domain-and-application`'s archived `design.md` named this change explicitly: "PostgreSQL adapter, `video_jobs`/`outbox` schema or migration — `add-videojob-infrastructure`."

## Goals / Non-Goals

**Goals:**
- Implement `domain.VideoJobRepository` against PostgreSQL, reconstructing `VideoJob` aggregates via `RestoreVideoJob` exactly as the identity adapter reconstructs `User` via `RestoreUser`.
- Create the `video_jobs` and `video_job_outbox` schema, migrated idempotently at startup (once a composition root calls `Migrate`, which happens in a later change — see Non-Goals).
- Make `Repository.Create` transactionally consistent: the job row and its creation-event outbox row commit together or not at all.

**Non-Goals:**
- Wiring this repository into `main.go`/`identity.go` or any HTTP handler. No composition root calls `postgres.Open`/`Migrate`/`NewRepository` for `video` yet — that's `wire-videojob-http-endpoints`. Until then this package is built and tested (via `go test`, gated by an env-var-conditioned integration test exactly like identity's) but not exercised by the running application.
- Publishing outbox rows to RabbitMQ or any broker. `published_at` exists as a column for a future relay (Phase 6) to fill in; nothing reads or writes it besides the `NULL` default from `Create`.
- Modeling `VideoJobQueued`/`Started`/`Completed`/`Failed` as outbox events. Those require state-transition use cases (`EnqueueVideoJob`, `StartProcessing`, `CompleteJob`, `FailJob`) that don't exist yet (later phases per `docs/roadmap.md`) and an `Update`/transition method on `VideoJobRepository` that isn't in the current interface. Only `VideoJobCreated` is in scope, because `Create` is the only repository method that exists today.
- A separate PostgreSQL database for `video`. Reuses the same instance/database as `identity`, matching `docs/architecture.md`'s documented target.

## Decisions

**`Repository.Create` writes `video_jobs` and `video_job_outbox` in one `database/sql` transaction, not via a separate `OutboxWriter` port.** Alternative considered: define a `domain.OutboxWriter` port and inject it as a second dependency into `application.CreateVideoJob`, so the use case explicitly emits the event. Rejected for this change: `CreateVideoJob` has no notion of domain events today (the aggregate doesn't emit them; `docs/domain-model.md` lists `VideoJobCreated` as "Planned (Phase 3+)" but no phase change has modeled it as a first-class emitted event object yet). Introducing an event type and a port for a single call site, when every field the outbox row needs (`job_id`, `user_id`, `original_filename`, `occurred_at`) is already visible to `Repository.Create` from the `*domain.VideoJob` it's given, is speculative machinery this change doesn't need. If a later phase adds more event-emitting use cases, promoting this to an explicit application-level port is a small, well-motivated follow-up — not a rewrite.

**The outbox payload shape matches `docs/domain-model.md`'s documented `VideoJobCreated` JSON fields exactly** (`type`, `job_id`, `user_id`, `original_filename`, `occurred_at`), so a future publisher can serialize the stored `payload jsonb` directly onto the wire without transformation.

**`video_job_outbox.id` is a plain `github.com/google/uuid` value generated inline in the infrastructure package, not a domain-level identifier.** It's a row identifier for an infrastructure-only table, not a concept `internal/video/domain` needs to know about (no port, no `VideoJobID`-style value object) — same reasoning `identity_users` never needed one beyond its own primary key.

**One PostgreSQL instance/database, new env vars (`VIDEO_POSTGRES_DSN`/`VIDEO_POSTGRES_TEST_DSN`) rather than reusing `IDENTITY_POSTGRES_DSN`.** Each bounded context owns its own config surface (mirrors `identity`'s `Config`/`LoadConfigFromEnv` exactly, just a different env var name) even though, today, both resolve to the same physical connection string in `docker-compose.yml`/`ci.yml`. This keeps the two contexts' infrastructure packages decoupled — a future split to separate databases per context changes only the env var value, not any Go code — while `docker-compose.yml`/CI simply point both at the same instance for now, matching the documented single-instance target.

**No external migration tool (`golang-migrate`, `goose`, etc.); embedded idempotent DDL, same as identity.** `schema.sql` uses `CREATE TABLE IF NOT EXISTS`/`CREATE INDEX IF NOT EXISTS`, safe to run on every startup. Consistent with the existing precedent; introducing a second migration mechanism for one more table would be inconsistent, not more robust.

**Index `(user_id, created_at DESC, id ASC)` on `video_jobs`.** Directly supports `FindByUserID`'s documented ordering (`videojob-lifecycle` spec: `CreatedAt` descending, `VideoJobID` ascending tie-break) as a single index scan rather than a sort.

## Risks / Trade-offs

- **[Risk]** Outbox rows accumulate unpublished forever until the Phase 6 RabbitMQ relay exists → **Mitigation:** acceptable for this change's scope; the table is inert until a consumer reads it, and `published_at IS NULL` is exactly the query a future relay needs to find unpublished rows. No cleanup/retention logic is added prematurely.
- **[Risk]** Two contexts' Postgres config pointing at the same instance today, but each with its own env var, could drift silently if one is updated and not the other in `docker-compose.yml`/CI → **Mitigation:** both files keep the two DSNs adjacent with a comment noting they're intentionally identical for now; low-cost, matches how identity's own `IDENTITY_POSTGRES_DSN`/`IDENTITY_POSTGRES_TEST_DSN` pair is already documented inline.
- **[Trade-off]** This change is untestable end-to-end (no composition root wires it in) until `wire-videojob-http-endpoints` lands → accepted, matching how `add-videojob-domain-and-application` shipped fully fake-backed with no real dependents either; the repo's Phase 3 decomposition deliberately sequences infra before wiring.

## Migration Plan

Single, additive PR: new `internal/video/infrastructure/{postgres,idgen}` packages, new `video_jobs`/`video_job_outbox` schema (embedded, applied only when a future composition root calls `Migrate` — not yet), `docker-compose.yml`/`ci.yml` env var additions. No existing Go file is modified. No data migration (no prior `video` persistence exists). Rollback is a normal `git revert` — nothing runtime-visible depends on this code existing yet.
