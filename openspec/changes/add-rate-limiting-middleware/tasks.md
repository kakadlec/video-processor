## 1. Platform package

- [ ] 1.1 `internal/platform/ratelimit/config.go`: `Config{MaxRequests int, WindowSeconds int}` + `LoadConfigFromEnv() (Config, error)` reading `RATE_LIMIT_MAX_REQUESTS` (default 60) and `RATE_LIMIT_WINDOW_SECONDS` (default 60); returns an error on a malformed (non-integer) value **or** a zero/negative value for either var (a non-positive `WindowSeconds` would disable limiting outright; a non-positive `MaxRequests` would reject every request) — never on an unset one.
- [ ] 1.2 `internal/platform/ratelimit/limiter.go`: `Limiter` wrapping a `*redis.Client` (from `internal/platform/redis`) and a `Config`; `NewLimiter(client *redis.Client, cfg Config) *Limiter`; `Allow(ctx context.Context, key string) (allowed bool, retryAfter time.Duration, err error)` running a Lua script via `client.Eval` that does `INCR key`, sets `EXPIRE key WindowSeconds` only when the post-increment count is `1`, and reads the key's remaining lifetime with `PTTL` (milliseconds, not `TTL`'s whole seconds — `TTL` can report `0` while sub-second time remains) in the same round trip; `allowed` is `count <= MaxRequests`, `retryAfter` rounds the returned milliseconds up to the next whole second (never truncates to `0`) when denied.
- [ ] 1.3 Unit tests for `LoadConfigFromEnv` (defaults, explicit overrides, malformed-value error, zero-value error, negative-value error) — no live Redis needed.
- [ ] 1.4 Integration tests for `Limiter.Allow` against `REDIS_TEST_ADDR` (skip with a clear message if unset, matching `internal/platform/redis`'s own test pattern): under-limit requests all allowed; the request that crosses `MaxRequests` is denied; `retryAfter` is positive (rounded up, never `0`) and roughly bounded by `WindowSeconds`; two different keys are tracked independently; after the window elapses (use a short `WindowSeconds` in the test), the same key is allowed again.

## 2. Transport (`cmd/api`)

- [ ] 2.1 `cmd/api/ratelimit.go` (new file, mirroring the `identity.go`/`video.go` split): a small `videoRateLimiter` interface (`Allow(ctx, key) (bool, time.Duration, error)`) so tests can substitute a fake, matching how `videoModule` depends on `videodomain.IdempotencyStore` rather than a concrete store; `rateLimitMiddleware(limiter videoRateLimiter) gin.HandlerFunc` — reads `authenticatedUserID(c)` (already guaranteed present, since this only mounts behind `requireBearerAuth`), wraps the request context in a short, fixed `context.WithTimeout` (independent of the shared Redis client's own timeout/retry policy — see design.md's "Fail-open only helps... if the failure surfaces quickly" risk) before calling `limiter.Allow(ctx, key)` with a key formatted consistently with `IdempotencyKey.String()`'s style (e.g. `"ratelimit:" + userID.String()`); on `allowed=false`, sets the `Retry-After` header (whole seconds, already rounded up by `Limiter.Allow` — never `0`) and responds `429` with an English-language JSON body (`{"error": "rate limit exceeded, try again later"}`), aborting the chain; on an `Allow` error (including the timeout firing), logs it and calls `c.Next()` (fail open, per design.md's "Limiter Failure Fails Open Within A Bounded Latency" requirement).
- [ ] 2.2 `cmd/api/main.go` or `cmd/api/video.go` (wherever `setupVideo`'s Redis client construction lives): expose or return the already-open `*redis.Client` so it can be reused for the limiter, and construct the `ratelimit.Limiter` from `ratelimit.LoadConfigFromEnv()` alongside it — no second Redis connection opened.
- [ ] 2.3 `setupRouter` (`cmd/api/main.go`): register `rateLimitMiddleware(limiter)` on the `videoRoutes` group, immediately after `identity.requireBearerAuth()` and before the `/uploads`/`/outputs` static sub-groups and the other video route registrations.
- [ ] 2.4 HTTP tests for `rateLimitMiddleware` in `cmd/api` (using a fake `videoRateLimiter`, no live Redis needed): a denied result returns `429` with the English-language JSON body and a `Retry-After` header, and the downstream handler does not run; an allowed result reaches the handler normally; an `Allow` error (and a simulated timeout) still reaches the handler (fail open); two different authenticated users produce two distinct rate-limit keys passed to `Allow`; a request to an unauthenticated route (e.g. `/api/auth/login`) never invokes the limiter at all, confirming router placement keeps it scoped to `videoRoutes`.

## 3. Dependency rules

- [ ] 3.1 Confirm (or extend, if a dependency-rules test enumerates allowed importers explicitly) that `internal/platform/ratelimit` may import `internal/platform/redis`, and that nothing in `internal/video/domain`, `internal/video/application`, or `internal/identity/*` imports `internal/platform/ratelimit`.

## 4. Local dev & CI infrastructure

- [ ] 4.1 `docker-compose.yml`: add `RATE_LIMIT_MAX_REQUESTS`/`RATE_LIMIT_WINDOW_SECONDS` to the `app`/`app-test` services' environment only if a non-default value is needed for tests to run predictably (e.g. a low `RATE_LIMIT_MAX_REQUESTS` would break other integration tests that make several requests per test run) — otherwise leave unset and rely on the shipped defaults, and note that decision in the PR description.
- [ ] 4.2 `.github/workflows/ci.yml`: add `REDIS_TEST_ADDR`'s existing value as needed for the new `internal/platform/ratelimit` integration tests (likely already sufficient, since it reuses the existing CI `redis` service from `add-redis-infrastructure` — confirm rather than assume).

## 5. Verification

- [ ] 5.1 `go vet ./...` passes.
- [ ] 5.2 `go test ./... -v` passes locally via `docker compose run --build --rm app-test go test ./... -v`.
- [ ] 5.3 Manual smoke test: with a low `RATE_LIMIT_MAX_REQUESTS` override, issue more than the limit's worth of requests to `GET /api/status` as the same authenticated user in quick succession, confirm a `429` with `Retry-After` appears once the limit is crossed, and confirm a second user's requests succeed unaffected.

## 6. Finalization (separate PR, per repo-workflow)

- [ ] 6.1 Promote this change's `rate-limiting` delta spec into `openspec/specs/rate-limiting/spec.md` (new), then archive the change folder.
- [ ] 6.2 Update `docs/architecture.md`: "Infrastructure Components" table (Redis row: note the second Phase 4 feature — rate limiting — is now implemented), "Target Package Topology" tree (add `internal/platform/ratelimit/`), Request flow section (videoRoutes gains the rate-limit middleware step), Routes/environment-variable notes (`RATE_LIMIT_MAX_REQUESTS`/`RATE_LIMIT_WINDOW_SECONDS`, both optional with defaults).
- [ ] 6.3 Update `docs/operations.md`: new optional environment variables and their defaults; Redis section notes 2 of 3 Phase 4 features shipped (idempotency keys, rate limiting), status cache remains planned.
- [ ] 6.4 Update `docs/roadmap.md`: add this change as a new row under the Phase 4 Change Backlog section (below `add-upload-idempotency-keys`), archived status with links; update the "Current State" prose paragraph for Phase 4 accordingly.
- [ ] 6.5 Update `CLAUDE.md` only if its existing Architecture/Notable-constraints sections make a claim this change invalidates (confirm rather than assume).
