# rate-limiting Specification

## Purpose

Define the Redis-backed, per-authenticated-user request rate limiter applied to every route in `cmd/api`'s `videoRoutes` group: threshold/window configuration, the fixed-window counting algorithm's observable behavior, the `429`/`Retry-After` rejection contract, and the fail-open behavior when the Redis-backed check itself is unavailable. This is the second Phase 4 feature (of idempotency keys, rate limiting, status cache) to consume `internal/platform/redis` (`redis-infrastructure`), implementing the "Rate limiting rejects excess requests" behavior `ddd-architecture`'s "Redis Responsibilities Are Additive" requirement already documents at the target-state level.

## Requirements

### Requirement: Authenticated Video Routes Are Rate Limited Per User

`cmd/api` SHALL apply a Redis-backed, per-authenticated-user request rate limit to every route in the `videoRoutes` group (everything gated by `identity.requireBearerAuth()`: `POST /upload`, `POST /api/video-jobs`, `GET /api/video-jobs`, `GET /api/video-jobs/:id`, `GET /download/:filename`, `GET /api/status`, and the `/uploads`/`/outputs` static mounts). A request that exceeds the configured limit within the current window SHALL be rejected with `429 Too Many Requests` before any handler-specific logic (including `ffmpeg` invocation) runs. Unauthenticated routes (`/api/auth/register`, `/api/auth/login`, `/`, static assets) are out of scope.

#### Scenario: Request within the limit succeeds

- **GIVEN** an authenticated user who has made fewer requests than `RATE_LIMIT_MAX_REQUESTS` within the current window
- **WHEN** they make another request to any route in `videoRoutes`
- **THEN** the request proceeds to its handler normally, with no `429`

#### Scenario: Request exceeding the limit is rejected

- **GIVEN** an authenticated user who has already made `RATE_LIMIT_MAX_REQUESTS` requests within the current window
- **WHEN** they make one more request to any route in `videoRoutes`
- **THEN** the response is `429 Too Many Requests` with an English-language JSON error body and a `Retry-After` header giving a strictly positive number of whole seconds until the window resets, and no handler-specific logic runs

#### Scenario: Retry-After rounds a sub-second remainder up, never down to zero

- **GIVEN** a denied request whose underlying window has less than one second of real time remaining before it expires
- **WHEN** the rate-limit middleware computes the `Retry-After` header
- **THEN** the value is rounded up to at least `1`, never `0` — a `0` would incorrectly tell the client to retry immediately against a window that is, in fact, still active

#### Scenario: Different users are limited independently

- **GIVEN** two different authenticated users, one of whom has exceeded their own limit
- **WHEN** the other user (who has not exceeded their limit) makes a request
- **THEN** that request succeeds — one user's rate-limit state never affects another user's

#### Scenario: Limit resets after the window elapses

- **GIVEN** an authenticated user who was rejected with `429` in the current window
- **WHEN** the configured window duration elapses and they retry
- **THEN** the request succeeds (the counter for the new window starts fresh)

### Requirement: Rate Limit Thresholds Are Configurable With Safe Defaults

`internal/platform/ratelimit.LoadConfigFromEnv` SHALL read `RATE_LIMIT_MAX_REQUESTS` and `RATE_LIMIT_WINDOW_SECONDS` from the environment, applying documented defaults (60 requests, 60 seconds respectively) when either is unset or empty — unlike `REDIS_ADDR`, their absence SHALL NOT be a startup failure. Both values, whether defaulted or explicitly set, SHALL be strictly positive integers (`>= 1`); a zero or negative value SHALL be rejected with the same class of error as a non-integer value, since a non-positive `WindowSeconds` would disable limiting outright (the counter key expires immediately) and a non-positive `MaxRequests` would reject every request.

#### Scenario: Both variables unset falls back to defaults

- **GIVEN** neither `RATE_LIMIT_MAX_REQUESTS` nor `RATE_LIMIT_WINDOW_SECONDS` is set
- **WHEN** `LoadConfigFromEnv` is called
- **THEN** it returns a `Config` with `MaxRequests = 60` and `WindowSeconds = 60`, and no error

#### Scenario: Explicit values override the defaults

- **GIVEN** `RATE_LIMIT_MAX_REQUESTS=10` and `RATE_LIMIT_WINDOW_SECONDS=30` are set
- **WHEN** `LoadConfigFromEnv` is called
- **THEN** it returns a `Config` with `MaxRequests = 10` and `WindowSeconds = 30`, and no error

#### Scenario: Malformed value returns an error

- **GIVEN** `RATE_LIMIT_MAX_REQUESTS` is set to a non-integer value (e.g. `"abc"`)
- **WHEN** `LoadConfigFromEnv` is called
- **THEN** it returns an error and no usable `Config`

#### Scenario: Zero or negative value returns an error

- **GIVEN** `RATE_LIMIT_MAX_REQUESTS` or `RATE_LIMIT_WINDOW_SECONDS` is set to `"0"` or a negative integer (e.g. `"-1"`)
- **WHEN** `LoadConfigFromEnv` is called
- **THEN** it returns an error and no usable `Config`

### Requirement: Limiter Failure Fails Open Within A Bounded Latency

If `internal/platform/ratelimit.Limiter.Allow` itself fails (e.g. a transient Redis error) rather than returning a normal allow/deny result, `cmd/api`'s rate-limit middleware SHALL allow the request to proceed (fail open) and log the error, rather than rejecting an otherwise-valid request due to an unrelated infrastructure hiccup. The middleware SHALL bound how long it waits on the `Allow` call with a short, fixed per-request timeout (independent of the shared Redis client's own connection/retry policy), so a Redis outage degrades to "fail open quickly" rather than "every authenticated request stalls for the client's default timeout before proceeding."

#### Scenario: Redis error does not block the request

- **GIVEN** the Redis client used by the rate limiter returns an error (e.g. connection failure) when `Allow` is called
- **WHEN** an authenticated user makes a request to a rate-limited route
- **THEN** the request proceeds to its handler as if the rate limit check had passed, and the error is logged

#### Scenario: An unresponsive Redis does not stall the request past the bounded timeout

- **GIVEN** the Redis client used by the rate limiter neither succeeds nor errors within the middleware's configured timeout (e.g. a network partition where connections hang rather than fail fast)
- **WHEN** an authenticated user makes a request to a rate-limited route
- **THEN** the middleware's `Allow` call is bounded by that timeout, after which the request proceeds to its handler (fail open) rather than hanging indefinitely
