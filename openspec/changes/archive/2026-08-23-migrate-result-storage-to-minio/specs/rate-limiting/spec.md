## MODIFIED Requirements

### Requirement: Authenticated Video Routes Are Rate Limited Per User

`cmd/api` SHALL apply a Redis-backed, per-authenticated-user request rate limit to every route in the `videoRoutes` group (everything gated by `identity.requireBearerAuth()`: `POST /upload`, `POST /api/video-jobs`, `GET /api/video-jobs`, `GET /api/video-jobs/:id`, `GET /download/:filename`, `GET /api/status`, and the `/uploads` static mount). A request that exceeds the configured limit within the current window SHALL be rejected with `429 Too Many Requests` before any handler-specific logic (including `ffmpeg` invocation) runs. Unauthenticated routes (`/api/auth/register`, `/api/auth/login`, `/`, static assets) are out of scope.

The `/outputs` static mount is absent from that enumeration because it no longer exists: result artifacts moved to object storage and are reachable only through `GET /download/:filename`, which is itself in the group and therefore still limited.

#### Scenario: Request within the limit succeeds

- **GIVEN** an authenticated user who has made fewer requests than `RATE_LIMIT_MAX_REQUESTS` within the current window
- **WHEN** they make another request to any route in `videoRoutes`
- **THEN** the request proceeds to its handler normally, with no `429`

#### Scenario: Request exceeding the limit is rejected

- **GIVEN** an authenticated user who has already made `RATE_LIMIT_MAX_REQUESTS` requests within the current window
- **WHEN** they make one more request to any route in `videoRoutes`
- **THEN** the response is `429 Too Many Requests` with an English-language JSON error body and a `Retry-After` header giving a strictly positive number of whole seconds until the window resets, and no handler-specific logic runs

#### Scenario: Different users are limited independently

- **GIVEN** two different authenticated users, one of whom has exceeded their own limit
- **WHEN** the other user (who has not exceeded their limit) makes a request
- **THEN** that request succeeds — one user's rate-limit state never affects another user's

#### Scenario: Limit resets after the window elapses

- **GIVEN** an authenticated user who was rejected with `429` in the current window
- **WHEN** the configured window duration elapses and they retry
- **THEN** the request succeeds (the counter for the new window starts fresh)

#### Scenario: Retry-After rounds a sub-second remainder up, never down to zero

- **GIVEN** a denied request whose underlying window has less than one second of real time remaining before it expires
- **WHEN** the rate-limit middleware computes the `Retry-After` header
- **THEN** the value is rounded up to at least `1`, never `0` — a `0` would incorrectly tell the client to retry immediately against a window that is, in fact, still active

#### Scenario: The download route remains rate limited after the static mount is removed

- **GIVEN** an authenticated user who has exhausted their window
- **WHEN** they request `GET /download/:filename`
- **THEN** the response is `429 Too Many Requests`, with no object retrieved from storage
