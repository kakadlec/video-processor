## Context

`internal/platform/redis` (shipped by `add-redis-infrastructure`) provides bare connection plumbing (`Config`/`LoadConfigFromEnv`, `Open`, `Ping`, `Close`) with no feature built on it yet beyond `add-upload-idempotency-keys`, which added `internal/video/infrastructure/idempotency` scoped to `POST /upload` only. This change adds the second of Phase 4's three Redis-backed features: request rate limiting, scoped more broadly — to every route in `cmd/api/main.go`'s authenticated `videoRoutes` group, not just one handler — since the canonical requirement (`openspec/specs/ddd-architecture/spec.md`, "Rate limiting rejects excess requests") describes it as protecting "a user['s] ... next request" in general, not one specific endpoint.

`setupVideo` (`cmd/api/video.go`) already opens a Redis client (`internal/platform/redis.Open`) for the idempotency store. This change reuses that same client rather than opening a second connection.

## Goals / Non-Goals

**Goals:**
- Reject excess requests from a single authenticated user with `429` before any handler logic (including `ffmpeg` invocation) runs.
- Reuse the existing Redis connection and follow the same atomic-Lua-script pattern `idempotency.RedisStore` already established, for consistency and to avoid introducing a second concurrency-control idiom.
- Make the limit/window configurable without requiring new mandatory startup configuration.

**Non-Goals:**
- Rate limiting `/api/auth/register`/`/api/auth/login` (unauthenticated, no `UserID` to key on) — a separate future change if brute-force protection is needed there.
- Per-IP or global (cross-user) rate limiting — this change is per-authenticated-user only, matching the idempotency feature's scoping precedent.
- A distributed lock for worker job pickup — that is Phase 6, unrelated to this change (see `ddd-architecture`'s "Redis Responsibilities Are Additive" requirement).
- Sliding-window or token-bucket precision — a fixed window is accepted as a known trade-off (see Risks below).

## Decisions

### 1. Fixed-window counter, not sliding window or token bucket

A fixed window (`INCR` a per-window-bucket key, `EXPIRE` it on first increment) is the simplest correct implementation and satisfies the canonical requirement's scenario ("a user exceeds the configured request rate ... rejects it with HTTP 429"), which does not mandate smooth/precise rate shaping. Token bucket or sliding-window log would give smoother behavior at the window boundary but add meaningfully more Redis state and complexity for a hackathon-scoped deliverable. Alternative considered: sliding-window counter (weighted average of current and previous window) — rejected for now as unnecessary precision; can be a future refinement if the fixed-window boundary-burst behavior proves to be a real problem.

### 2. Atomicity via Lua script, mirroring `idempotency.RedisStore`

`Allow` runs a single Lua script (via `client.Eval`) that does `INCR key` then, only if the result equals `1` (i.e., this call created the key), sets `EXPIRE key windowSeconds`. Returning the post-increment count and remaining TTL in one round trip avoids a race between a plain `INCR` and a separate `EXPIRE`/`TTL` call, and matches the atomicity precedent already established by `idempotency.RedisStore`'s `Finalize`/`Clear` scripts — reviewers already know this pattern.

### 3. New `internal/platform/ratelimit` package, not `internal/video/*`

Rate limiting is not a `VideoJob`-domain concept (unlike `IdempotencyKey`, which is content-addressed and tied to job creation) — it's a generic HTTP-layer request-shaping concern that happens to apply to the video routes today but conceptually could apply to any authenticated route. Placing it in `internal/platform/` (alongside `internal/platform/redis`) keeps it usable without forcing a fake domain object into `internal/video/domain`, and keeps `internal/video/domain`/`internal/video/application` dependency-rule-clean (neither may import it — it's wired only from `cmd/api`, same enforcement style as `internal/video/infrastructure/idempotency`).

### 4. Config: two optional env vars with defaults, not required-at-startup

`RATE_LIMIT_MAX_REQUESTS` (default 60) and `RATE_LIMIT_WINDOW_SECONDS` (default 60) follow the *pattern* of `internal/platform/redis.LoadConfigFromEnv` (a `Config` struct + `LoadConfigFromEnv` function) but deliberately do **not** follow its fail-fast-on-missing behavior: `REDIS_ADDR` is a hard dependency (no connection without it), but a rate limit has a sensible universal default, so requiring operators to set two more env vars just to run the app at all would add friction with no safety benefit. `LoadConfigFromEnv` returns `(Config, error)` for symmetry with the existing pattern, but the only error case is a malformed (non-integer) value for either var — not an absent one.

### 5. Middleware placement: after `requireBearerAuth`, before route-specific groups

Mounted directly on `videoRoutes` (the group `setupRouter` already creates in `cmd/api/main.go`, gated by `identity.requireBearerAuth()`), immediately after that auth middleware and before the `/uploads`/`/outputs` static sub-groups and the specific route registrations. This guarantees `authenticatedUserID(c)` is always populated (same precondition `requireArtifactOwnership` already relies on) and applies uniformly to every route in the group without needing to repeat registration per-route.

### 6. 429 response shape

English-language JSON body (`{"error": "rate limit exceeded, try again later"}`, per `CLAUDE.md`'s language policy for new Go code) plus a `Retry-After` header (integer seconds, computed from the Lua script's returned TTL) so well-behaved clients can back off correctly. This mirrors the existing JSON-error-body convention already used elsewhere in `cmd/api` (e.g. `handleDownload`'s `{"error": "File not found"}`), just with an added standard HTTP header.

## Risks / Trade-offs

- **[Risk]** Fixed-window counters allow up to `2x` the configured limit across a window boundary (e.g., a burst at the end of one window plus a burst at the start of the next). → **Mitigation**: acceptable for this change's goal (preventing sustained abuse / accidental resource exhaustion, not precise traffic shaping); documented as a known limitation, revisit only if it proves to be a real-world problem.
- **[Risk]** A `Limiter.Allow` call that errors (e.g., Redis transiently unreachable) could either fail open (allow the request) or fail closed (reject it) — an availability/safety trade-off. → **Mitigation**: fail open (allow the request, log the error) — consistent with idempotency's own precedent of treating a non-fatal store failure as "proceed anyway, log it" (design.md Decision 7 of `add-upload-idempotency-keys`) rather than making an unrelated infrastructure hiccup take down otherwise-healthy request handling.
- **[Risk]** Sharing the Redis client opened by `setupVideo` couples this feature's availability to `setupVideo`'s own startup sequencing. → **Mitigation**: acceptable — both features already require `REDIS_ADDR` at startup; there is no scenario where one is configured/reachable and the other isn't.

## Migration Plan

No data migration. Deploying this change adds a new failure mode (`429`) to previously-unlimited routes; rollback is a plain revert (remove the middleware registration) with no state to unwind, since Redis keys are TTL-bound and self-expire.

## Open Questions

None — the canonical requirement, algorithm, package placement, and config approach are all settled above.
