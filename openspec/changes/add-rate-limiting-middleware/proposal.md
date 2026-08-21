## Why

`POST /upload` runs a synchronous, unbounded `ffmpeg` extraction per request, and there is currently no limit on how many requests a single authenticated user can issue in a given window — a single user (or a compromised token) can hold multiple concurrent `ffmpeg` processes and exhaust server resources. This is the second of Phase 4's three Redis-backed reliability features (`docs/roadmap.md`), following `add-upload-idempotency-keys`, and is already described at the requirement level in `openspec/specs/ddd-architecture/spec.md`'s "Redis Responsibilities Are Additive" requirement ("Rate limiting rejects excess requests") — this change implements that scenario.

## What Changes

- New `internal/platform/ratelimit` package: a `Limiter` backed by `internal/platform/redis`, implementing a fixed-window request counter with an atomic Redis Lua script (increment + conditional expire), mirroring the atomicity pattern already used by `internal/video/infrastructure/idempotency`'s `Finalize`/`Clear`.
- New `Config`/`LoadConfigFromEnv()` reading two *optional* environment variables with defaults: `RATE_LIMIT_MAX_REQUESTS` (default 60) and `RATE_LIMIT_WINDOW_SECONDS` (default 60) — unlike `REDIS_ADDR`, absence is not a startup failure.
- New Gin middleware (`cmd/api/ratelimit.go`) mounted on the existing authenticated `videoRoutes` group in `setupRouter`, immediately after `identity.requireBearerAuth()`, keyed on the authenticated `UserID`. Covers every route in that group: `POST /upload`, `POST /api/video-jobs`, `GET /api/video-jobs`, `GET /api/video-jobs/:id`, `GET /download/:filename`, `GET /api/status`, and the `/uploads`/`/outputs` static mounts.
- A rejected request receives `429 Too Many Requests` with an English-language JSON body and a `Retry-After` header giving the number of seconds until the current window resets.
- Reuses the Redis client already opened for idempotency in `setupVideo` — no second Redis connection.
- Out of scope: `/api/auth/register` and `/api/auth/login` (unauthenticated — no `UserID` to key on; brute-force protection for those routes is a separate, not-yet-proposed concern).

## Capabilities

### New Capabilities
- `rate-limiting`: per-authenticated-user request rate limiting on the video-processing HTTP routes, backed by Redis, rejecting excess requests with `429` before they reach any handler.

### Modified Capabilities
(none — this introduces a new capability; it does not change the documented behavior of any existing capability)

## Impact

- New code: `internal/platform/ratelimit/*.go`, `cmd/api/ratelimit.go`.
- Modified: `cmd/api/main.go` (`setupRouter` wiring), `cmd/api/video.go` or `main.go` (wherever the Redis client construction is shared/reused — no new connection).
- New optional environment variables: `RATE_LIMIT_MAX_REQUESTS`, `RATE_LIMIT_WINDOW_SECONDS`.
- No database schema changes. No breaking changes to existing routes' request/response shape (only an added possible `429` response and `Retry-After` header on routes that previously had no rate limit).
- Docs to update at finalization: `docs/architecture.md`, `docs/operations.md`, `docs/roadmap.md`.
