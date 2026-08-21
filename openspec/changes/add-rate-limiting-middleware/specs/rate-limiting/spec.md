## ADDED Requirements

### Requirement: Authenticated Video Routes Are Rate Limited Per User

`cmd/api` SHALL apply a Redis-backed, per-authenticated-user request rate limit to every route in the `videoRoutes` group (everything gated by `identity.requireBearerAuth()`: `POST /upload`, `POST /api/video-jobs`, `GET /api/video-jobs`, `GET /api/video-jobs/:id`, `GET /download/:filename`, `GET /api/status`, and the `/uploads`/`/outputs` static mounts). A request that exceeds the configured limit within the current window SHALL be rejected with `429 Too Many Requests` before any handler-specific logic (including `ffmpeg` invocation) runs. Unauthenticated routes (`/api/auth/register`, `/api/auth/login`, `/`, static assets) are out of scope.

#### Scenario: Request within the limit succeeds

- **GIVEN** an authenticated user who has made fewer requests than `RATE_LIMIT_MAX_REQUESTS` within the current window
- **WHEN** they make another request to any route in `videoRoutes`
- **THEN** the request proceeds to its handler normally, with no `429`

#### Scenario: Request exceeding the limit is rejected

- **GIVEN** an authenticated user who has already made `RATE_LIMIT_MAX_REQUESTS` requests within the current window
- **WHEN** they make one more request to any route in `videoRoutes`
- **THEN** the response is `429 Too Many Requests` with an English-language JSON error body and a `Retry-After` header giving the number of seconds until the window resets, and no handler-specific logic runs

#### Scenario: Different users are limited independently

- **GIVEN** two different authenticated users, one of whom has exceeded their own limit
- **WHEN** the other user (who has not exceeded their limit) makes a request
- **THEN** that request succeeds — one user's rate-limit state never affects another user's

#### Scenario: Limit resets after the window elapses

- **GIVEN** an authenticated user who was rejected with `429` in the current window
- **WHEN** the configured window duration elapses and they retry
- **THEN** the request succeeds (the counter for the new window starts fresh)

### Requirement: Rate Limit Thresholds Are Configurable With Safe Defaults

`internal/platform/ratelimit.LoadConfigFromEnv` SHALL read `RATE_LIMIT_MAX_REQUESTS` and `RATE_LIMIT_WINDOW_SECONDS` from the environment, applying documented defaults (60 requests, 60 seconds respectively) when either is unset or empty — unlike `REDIS_ADDR`, their absence SHALL NOT be a startup failure.

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

### Requirement: Limiter Failure Fails Open

If `internal/platform/ratelimit.Limiter.Allow` itself fails (e.g. a transient Redis error) rather than returning a normal allow/deny result, `cmd/api`'s rate-limit middleware SHALL allow the request to proceed (fail open) and log the error, rather than rejecting an otherwise-valid request due to an unrelated infrastructure hiccup.

#### Scenario: Redis error does not block the request

- **GIVEN** the Redis client used by the rate limiter returns an error (e.g. connection failure) when `Allow` is called
- **WHEN** an authenticated user makes a request to a rate-limited route
- **THEN** the request proceeds to its handler as if the rate limit check had passed, and the error is logged
