## Why

`POST /upload` (`cmd/api/video.go`'s `handleVideoUpload`) is fully synchronous and blocks for the entire `ffmpeg` extraction — a slow operation for real videos. A client that times out or drops connection mid-processing has no way to tell the difference between "still running" and "lost," so it retries with the same file. Today that retry runs `ffmpeg` a second time and creates a second `VideoJob` row for what is really one submission. `openspec/specs/ddd-architecture/spec.md`'s "Redis Responsibilities Are Additive" requirement already documents the target behavior ("Redis returns the existing `VideoJobID` and the handler returns the existing job without creating a duplicate or re-enqueuing"), and `add-redis-infrastructure` (Phase 4) shipped the connection this feature needs — this is the first Phase 4 feature to actually consume it.

## What Changes

- `handleVideoUpload` computes a SHA-256 hash of the uploaded video's content while streaming it to `uploads/` (via `io.TeeReader` on the existing `io.Copy` — no extra I/O pass), then derives a per-user Redis key `idempotency:{userID}:{hash}`.
- Before creating a `VideoJob`, the handler atomically reserves that key in Redis (`SET NX` with a `"processing"` sentinel value and a short TTL bounding request-handling time). If the reservation fails because a request for the same user+content is already in flight, the handler returns `409 Conflict` without touching the filesystem further or creating a job.
- Once `CreateVideoJob` succeeds, the handler overwrites the sentinel with the real `VideoJobID` and extends the key's TTL to a fixed 24-hour idempotency window (matching the common industry default, e.g. Stripe's idempotency-key window).
- A duplicate arriving after that point (key holds a real `VideoJobID`) short-circuits: the handler deletes the redundant file it just saved (safe — each request writes to its own `uploadID`-prefixed path, so this never touches the original request's file) and returns the existing job's current status, without re-running `ffmpeg` or creating a new `VideoJob` row.
- If the job later fails (`FailJob`), the handler deletes the idempotency key immediately rather than waiting out the 24h TTL, so a legitimate retry after a transient `ffmpeg` failure is treated as a fresh attempt, not permanently blocked.
- `internal/platform/redis` (from `add-redis-infrastructure`) is wired into `cmd/api`'s composition root for the first time: `REDIS_ADDR` becomes required at startup, alongside the existing `IDENTITY_POSTGRES_DSN`/`VIDEO_POSTGRES_DSN`.

## Capabilities

### New Capabilities
- `upload-idempotency`: the Redis-backed idempotency-key mechanism for `POST /upload` — key derivation, atomic reservation, TTL/expiry behavior, and the duplicate-request response contract.

### Modified Capabilities
(none — `ddd-architecture`'s existing "Idempotency key prevents duplicate job creation" scenario already describes this behavior at the level that spec operates at; this change implements it rather than changing its requirements.)

## Impact

- New `domain.IdempotencyStore` port in `internal/video/domain` and a Redis-backed adapter in `internal/video/infrastructure/idempotency`, following the same port/adapter shape as `domain.VideoJobRepository`/`internal/video/infrastructure/postgres`. New `domain.IdempotencyKey` value object.
- `cmd/api/video.go`: `handleVideoUpload` gains the reserve/check/finalize steps described above, calling the new port directly. `cmd/api`'s composition root (`setupVideo`) gains a Redis client dependency; `REDIS_ADDR` becomes a required startup env var for `cmd/api`, same class of failure as a missing `VIDEO_POSTGRES_DSN` today.
- `docker-compose.yml`'s `app`/`app-test` services (not just `internal/platform/redis`'s own tests) need `REDIS_ADDR`/dependency-on-`redis` wiring for the first time, since this is the first change that actually runs the application against Redis.
- No change to `POST /api/video-jobs` (the preview API) — it still has no processing trigger and is out of scope, per existing documented behavior.
- No database schema change; no change to `VideoJob`'s domain state machine.
