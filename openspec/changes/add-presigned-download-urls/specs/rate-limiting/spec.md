## MODIFIED Requirements

### Requirement: Authenticated Video Routes Are Rate Limited Per User

`cmd/api` SHALL apply a Redis-backed, per-authenticated-user request rate limit to every route in the `videoRoutes` group (everything gated by `identity.requireBearerAuth()`: `POST /upload`, `POST /api/video-jobs`, `GET /api/video-jobs`, `GET /api/video-jobs/:id`, `GET /download/:filename`, and `GET /api/status`). A request that exceeds the configured limit within the current window SHALL be rejected with `429 Too Many Requests` before any handler-specific logic (including `ffmpeg` invocation) runs. Unauthenticated routes (`/api/auth/register`, `/api/auth/login`, `/`, static assets) are out of scope.

Neither static mount appears in that enumeration any more, because neither exists: `/outputs` went when results moved to object storage, `/uploads` when source videos followed. Every handler in the group returns JSON; none streams an artifact.

The limit governs **requests to this API**, and after result downloads became presigned URLs that is narrower than it may read. `GET /download/:filename` issues a URL and is limited; the transfer that URL authorizes happens between the client and the storage service, which this middleware does not sit in front of. A caller held to `RATE_LIMIT_MAX_REQUESTS` issuances per window can still begin that many transfers, and each transfer's bandwidth is unbounded by anything specified here. Bounding artifact egress is an object-storage concern, and no requirement in this capability SHALL be read as constraining it.

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
- **THEN** the response is `429 Too Many Requests`, with no object stated in storage and no URL issued

#### Scenario: The upload route remains rate limited after its static mount is removed

- **GIVEN** an authenticated user who has exhausted their window
- **WHEN** they `POST /upload`
- **THEN** the response is `429 Too Many Requests`, with nothing stored in the bucket and `ffmpeg` never invoked

#### Scenario: A transfer authorized by an issued URL is outside the limiter's reach

- **GIVEN** an authenticated user who has exhausted their window and holds a presigned URL issued earlier in that window
- **WHEN** they request that URL
- **THEN** the transfer proceeds, because the request goes to the storage service rather than to a route this middleware is mounted on
