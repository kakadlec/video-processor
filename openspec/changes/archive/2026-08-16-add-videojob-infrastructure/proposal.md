## Why

`internal/video/domain.VideoJobRepository` has no concrete implementation yet — `add-videojob-domain-and-application` (Phase 3, archived) built the `VideoJob` aggregate and its use cases entirely against fakes. Nothing yet persists a `VideoJob` to PostgreSQL, and no schema exists for the `video_jobs`/outbox tables `docs/architecture.md` already documents as landing in Phase 3. This is the named next Change Backlog row (`docs/roadmap.md`), a dependency of `wire-videojob-http-endpoints`.

## What Changes

- New `internal/video/infrastructure/postgres/` package: `Config`/`LoadConfigFromEnv` (reads `VIDEO_POSTGRES_DSN`), `Open`, `Migrate` (embedded idempotent `schema.sql`), and `Repository` implementing `domain.VideoJobRepository` (`Create`, `FindByID`, `FindByUserID`) — mirrors `internal/identity/infrastructure/postgres/` exactly.
- New `internal/video/infrastructure/idgen/` package implementing `domain.VideoJobIDGenerator`/`domain.VideoJobIDParser` via UUID v4 — mirrors `internal/identity/infrastructure/idgen/`.
- New `video_jobs` table (mirrors the `VideoJob` aggregate's fields) with an index supporting `FindByUserID`'s documented `CreatedAt DESC, VideoJobID ASC` ordering.
- New `video_job_outbox` table (transactional outbox: `id`, `event_type`, `payload jsonb`, `occurred_at`, `published_at` nullable). `Repository.Create` writes the `video_jobs` row and a `video_job.created` outbox row in one SQL transaction, so the two are never inconsistent. No publisher/relay yet (RabbitMQ lands in a later phase) — rows sit unpublished.
- Both tables live in the same PostgreSQL database/instance Identity already uses (not a separate database) — `docker-compose.yml` and `.github/workflows/ci.yml` gain `VIDEO_POSTGRES_DSN`/`VIDEO_POSTGRES_TEST_DSN` pointing at that same instance.
- No `main.go`/`identity.go` composition-root wiring — this change is infrastructure-only. Wiring the HTTP routes and instantiating this repository happens in `wire-videojob-http-endpoints`.

## Capabilities

### New Capabilities

- `videojob-persistence`: PostgreSQL-backed `VideoJobRepository` implementation, its schema, and the transactional-outbox write behavior on job creation.

### Modified Capabilities

(none — `ddd-architecture`'s "Infrastructure packages implement domain interfaces" and "Composition root is the only DI boundary" scenarios are already generic enough to cover this context's infrastructure package without a wording change.)

## Impact

- New files only, under `internal/video/infrastructure/`, plus `docker-compose.yml`/`ci.yml` env var additions. No existing Go source is modified.
- New runtime dependency: PostgreSQL connectivity for the `video` context (already a dependency of the repo via `internal/identity/infrastructure/postgres`; no new library).
- `go.mod`/`go.sum` unchanged (reuses `github.com/jackc/pgx/v5` and `github.com/google/uuid`, both already dependencies).
