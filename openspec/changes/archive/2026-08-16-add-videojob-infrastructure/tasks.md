## 1. Schema

- [x] 1.1 Write `internal/video/infrastructure/postgres/schema.sql`: `video_jobs` table (id, user_id, original_filename, status, frame_count, error_reason, storage_key, created_at) with a `(user_id, created_at DESC, id ASC)` index
- [x] 1.2 Add `video_job_outbox` table to the same schema file (id, event_type, payload jsonb, occurred_at, published_at nullable)

## 2. ID Generator Adapter

- [x] 2.1 `internal/video/infrastructure/idgen/idgen.go`: `Adapter` implementing `domain.VideoJobIDGenerator` and `domain.VideoJobIDParser` via UUID v4, mirroring `internal/identity/infrastructure/idgen`
- [x] 2.2 Unit tests for the adapter (valid generation, valid/invalid parsing) mirroring `internal/identity/infrastructure/idgen/idgen_test.go`

## 3. PostgreSQL Repository Adapter

- [x] 3.1 `internal/video/infrastructure/postgres/config.go`: `Config`/`LoadConfigFromEnv` reading `VIDEO_POSTGRES_DSN`, mirroring identity's `ErrDSNRequired` fail-fast behavior
- [x] 3.2 `internal/video/infrastructure/postgres/db.go`: `Open(cfg Config) (*sql.DB, error)` mirroring identity's
- [x] 3.3 `internal/video/infrastructure/postgres/migrate.go`: `Migrate(ctx, db) error` embedding `schema.sql`, idempotent `CREATE TABLE/INDEX IF NOT EXISTS`
- [x] 3.4 `internal/video/infrastructure/postgres/repository.go`: `Repository` struct wired with `*sql.DB` and a `domain.VideoJobIDParser`; implement `FindByID` and `FindByUserID` (parameterized queries, `domain.RestoreVideoJob` reconstruction, `domain.ErrVideoJobNotFound` on no rows)
- [x] 3.5 Implement `Repository.Create`: single transaction inserting the `video_jobs` row and a `video_job.created` outbox row (payload fields: `type`, `job_id`, `user_id`, `original_filename`, `occurred_at`, matching `docs/domain-model.md`'s documented `VideoJobCreated` shape); roll back and return the error if either insert fails
- [x] 3.6 `var _ domain.VideoJobRepository = (*Repository)(nil)` compile-time assertion

## 4. Tests

- [x] 4.1 `internal/video/infrastructure/postgres/repository_test.go`: env-var-gated integration tests (`VIDEO_POSTGRES_TEST_DSN`, skip if unset) mirroring identity's `testDB` helper pattern (open, migrate, truncate `video_jobs` and `video_job_outbox` before each test)
- [x] 4.2 Test: `Create` then `FindByID` round-trips all fields correctly
- [x] 4.3 Test: `FindByID` returns `domain.ErrVideoJobNotFound` for an unknown ID
- [x] 4.4 Test: `FindByUserID` scopes to the caller's `UserID`, orders by `CreatedAt` descending (distinct timestamps, not just the tie-break case), breaks ties by ascending `VideoJobID`, and respects offset/limit
- [x] 4.5 Test: `Create` writes a matching `video_job_outbox` row (`event_type = 'video_job.created'`, payload fields including `type`, `published_at IS NULL`)
- [x] 4.6 Test: a `Create` that fails to insert the `video_jobs` row (duplicate ID) leaves no `video_job_outbox` row committed
- [x] 4.7 Test: a `Create` where the `video_jobs` insert succeeds but the `video_job_outbox` insert fails leaves no `video_jobs` row committed either (the direction 4.6 cannot prove, since its failure happens before the outbox insert is attempted) — verify by confirming this test fails against a non-transactional two-Exec implementation before it lands

## 5. Local Dev / CI Wiring (config only, no application wiring)

- [x] 5.1 Add `VIDEO_POSTGRES_DSN`/`VIDEO_POSTGRES_TEST_DSN` to the `app` and `app-test` services in `docker-compose.yml`, pointing at the same instance/database as `IDENTITY_POSTGRES_DSN`/`IDENTITY_POSTGRES_TEST_DSN`, with a comment noting they're intentionally identical for now
- [x] 5.2 Add `VIDEO_POSTGRES_TEST_DSN` to `.github/workflows/ci.yml`'s `Test` step env, pointing at the same CI Postgres service identity already uses

## 6. Validation

- [x] 6.1 `go vet ./...` passes
- [x] 6.2 `go test ./... -v` passes (non-Postgres-gated tests; `ffmpeg` on `PATH` or via `docker compose run --build --rm app-test go test ./... -v`)
- [x] 6.3 `docker compose run --build --rm app-test go test ./... -v` passes with `VIDEO_POSTGRES_TEST_DSN` set, exercising the new integration tests against real PostgreSQL
- [x] 6.4 `gosec ./...` and `govulncheck ./...` clean
