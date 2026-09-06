## MODIFIED Requirements

### Requirement: Authenticated Video Routes Are Rate Limited Per User

Every HTTP service that serves bearer-authenticated routes SHALL apply a Redis-backed, per-authenticated-user request rate limit to all of them. `cmd/video-api` applies it to everything gated by `requireBearerAuth()` (`POST /upload`, `POST /api/video-jobs`, `GET /api/video-jobs`, `GET /api/video-jobs/:id`, `GET /download/:filename`, and `GET /api/status`); `cmd/notification-api` applies it to `GET` and `PUT /api/notification-preferences`. A request that exceeds the configured limit within the current window SHALL be rejected with `429 Too Many Requests` before any handler-specific logic (including `ffmpeg` invocation) runs. Unauthenticated routes (`/api/auth/register`, `/api/auth/login`, `/`, static assets) are out of scope, and `cmd/identity-api` mounts no limiter at all — it serves no route with an authenticated user to key on.

**The budget is one budget, not one per service.** The services share a Redis instance and the same key format, so a user's `RATE_LIMIT_MAX_REQUESTS` per window is their allowance across the whole system. Namespacing the counter per service would silently multiply every user's allowance by the number of services, which is a behavior change and SHALL NOT be introduced as a side effect of how the processes are divided. This is why a service that owns no cache and no idempotency store still requires `REDIS_ADDR`: the limiter is a genuine dependency of its middleware.

The middleware pair and its order — bearer authentication, then the limiter — SHALL hold on every group that carries it, in every service. The pair is the invariant; the grouping is not.

Neither static mount appears in that enumeration any more, because neither exists: `/outputs` went when results moved to object storage, `/uploads` when source videos followed. Every handler in the group returns JSON; none streams an artifact.

**Status polling is now the dominant consumer of this budget, and the interval SHALL be chosen against it.** Since `POST /upload` became an acknowledgement rather than a result, a client learns an outcome by repeatedly calling `GET /api/video-jobs/:id`, and those polls share one budget with the submission and the eventual download issuance. A polling client SHALL therefore start at an interval no shorter than 2 seconds and SHALL back off to an interval of at least 10 seconds, so that a single tracked job settles well inside the default budget and a user with several jobs or tabs in flight is not rate-limited by ordinary use. A fixed short interval SHALL NOT be used: at the default 60 requests per 60 seconds it would consume half the budget for one job and exceed it for two.

A `429` returned to a poller SHALL be treated as a back-off signal, not as a job failure. The client SHALL lengthen its interval, honour `Retry-After`, and continue polling; it SHALL NOT report the job as failed, and SHALL NOT retry sooner than the header directs. A job's outcome is what the status endpoint reports when it answers, and a throttled poll has reported nothing.

The limit governs **requests to this system's HTTP surface**, and after result downloads became presigned URLs that is narrower than it may read. `GET /download/:filename` issues a URL and is limited; the transfer that URL authorizes happens between the client and the storage service, which this middleware does not sit in front of. A caller held to `RATE_LIMIT_MAX_REQUESTS` issuances per window can still begin that many transfers, and each transfer's bandwidth is unbounded by anything specified here. Bounding artifact egress is an object-storage concern, and no requirement in this capability SHALL be read as constraining it.

#### Scenario: Request within the limit succeeds

- **GIVEN** an authenticated user who has made fewer requests than `RATE_LIMIT_MAX_REQUESTS` within the current window
- **WHEN** they make another request to any bearer-authenticated route on any service
- **THEN** the request proceeds to its handler normally, with no `429`

#### Scenario: Request exceeding the limit is rejected

- **GIVEN** an authenticated user who has already made `RATE_LIMIT_MAX_REQUESTS` requests within the current window
- **WHEN** they make one more request to any bearer-authenticated route on any service
- **THEN** the response is `429 Too Many Requests` with an English-language JSON error body and a `Retry-After` header giving a strictly positive number of whole seconds until the window resets, and no handler-specific logic runs

#### Scenario: The budget is shared across the HTTP services

- **GIVEN** an authenticated user who has exhausted their window against `cmd/video-api`
- **WHEN** they request `GET /api/notification-preferences`, served by a different process
- **THEN** the response is `429 Too Many Requests`, because both services count against the same per-user counter

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
- **THEN** the response is `429 Too Many Requests`, with no object read from storage and no URL issued

#### Scenario: The upload route remains rate limited after its static mount is removed

- **GIVEN** an authenticated user who has exhausted their window
- **WHEN** they `POST /upload`
- **THEN** the response is `429 Too Many Requests`, with nothing stored in the bucket and `ffmpeg` never invoked

#### Scenario: A transfer authorized by an issued URL is outside the limiter's reach

- **GIVEN** an authenticated user who has exhausted their window and holds a presigned URL issued earlier in that window
- **WHEN** they request that URL
- **THEN** the transfer proceeds, because the request goes to the storage service rather than to a route this middleware is mounted on

#### Scenario: Polling a job to completion stays within the default budget

- **GIVEN** an authenticated user with the default limit who submits one video and polls its status until it is `completed`
- **WHEN** the job takes long enough for the polling interval to reach its ceiling
- **THEN** the submission, every poll, and the download issuance together stay under `RATE_LIMIT_MAX_REQUESTS` for each window, and no `429` is returned

#### Scenario: A throttled poll backs off instead of failing the job

- **GIVEN** a polling client that receives `429` with a `Retry-After` header while a job is still `processing`
- **WHEN** it handles that response
- **THEN** it waits at least the indicated number of seconds, lengthens its interval, and resumes polling — it does not report the job as failed and does not retry sooner than `Retry-After` allows

### Requirement: Limiter Failure Fails Open Within A Bounded Latency

If `internal/platform/ratelimit.Limiter.Allow` itself fails (e.g. a transient Redis error) rather than returning a normal allow/deny result, the rate-limit middleware in every service that mounts it SHALL allow the request to proceed (fail open) and log the error, rather than rejecting an otherwise-valid request due to an unrelated infrastructure hiccup. The middleware SHALL bound how long it waits on the `Allow` call with a short, fixed per-request timeout, and the shared Redis client SHALL be configured (`ContextTimeoutEnabled: true`) to actually honor that timeout — a passed context has no effect on go-redis v9's real command I/O without it (`baseClient.context()` substitutes `context.Background()` otherwise) — so a Redis outage degrades to "fail open quickly" rather than "every authenticated request stalls for the client's own default timeout before proceeding."

#### Scenario: Redis error does not block the request

- **GIVEN** the Redis client used by the rate limiter returns an error (e.g. connection failure) when `Allow` is called
- **WHEN** an authenticated user makes a request to a rate-limited route on any service that mounts the middleware
- **THEN** the request proceeds to its handler as if the rate limit check had passed, and the error is logged

#### Scenario: An unresponsive Redis does not stall the request past the bounded timeout

- **GIVEN** the Redis client used by the rate limiter neither succeeds nor errors within the middleware's configured timeout (e.g. a network partition where connections hang rather than fail fast)
- **WHEN** an authenticated user makes a request to a rate-limited route
- **THEN** the middleware's `Allow` call is bounded by that timeout, after which the request proceeds to its handler (fail open) rather than hanging indefinitely — verified against a real (non-fake) Redis client, using a genuinely in-flight blocking command, not only a fake that honors `ctx.Done()` by construction
