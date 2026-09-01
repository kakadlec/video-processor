# videojob-http-api Specification

## Purpose

Define the HTTP-layer contract for `POST /api/video-jobs`, `GET /api/video-jobs/:id`, and `GET /api/video-jobs` — routes, request/response shapes, bearer-auth and ownership enforcement, and error-status mapping — wrapping the `CreateVideoJob`, `GetJobStatus`, and `ListUserJobs` use cases defined in `videojob-lifecycle`. This capability owns only the `cmd/api` HTTP surface; the use cases it calls, and the pure `JobStatus` transition logic, remain `videojob-lifecycle`'s responsibility. No code path reachable from these endpoints triggers processing — see the "Jobs Created Through This API Have No Processing Trigger" requirement below. `GET /api/video-jobs/:id` does, however, now carry a second role: it is the status channel a `POST /upload` acknowledgement names, so a client polls it to learn an outcome the submission no longer returns.

## Requirements

### Requirement: POST /api/video-jobs Creates a Pending Job Record

`POST /api/video-jobs` SHALL require a valid bearer token, accept a JSON body with an `original_filename` field, and create a `VideoJob` via `CreateVideoJob` using the authenticated `UserID` (never a caller-supplied one). It SHALL NOT accept or store file content — this endpoint registers job metadata only. On success it SHALL return `201` with the job's id, original filename, status (always `pending`), and creation time.

#### Scenario: Authenticated request with a supported filename creates a job

- **GIVEN** a request carries a valid bearer token and a JSON body with `original_filename` ending in a supported extension
- **WHEN** `POST /api/video-jobs` is called
- **THEN** the response is `201` with the created job's id, the same `original_filename`, `status: "pending"`, and a `created_at` timestamp

#### Scenario: Missing or malformed bearer token is rejected

- **GIVEN** a request has no `Authorization` header, or a malformed/expired/invalid one
- **WHEN** `POST /api/video-jobs` is called
- **THEN** the response is `401`, and no `VideoJob` is created

#### Scenario: Unsupported or empty filename is rejected

- **GIVEN** an authenticated request whose `original_filename` is empty or has an unsupported extension
- **WHEN** `POST /api/video-jobs` is called
- **THEN** the response is `400`, and no `VideoJob` is created

#### Scenario: Caller-supplied user identifier is ignored

- **GIVEN** an authenticated request whose JSON body includes any user/owner field
- **WHEN** `POST /api/video-jobs` is called
- **THEN** the created job's owner is the authenticated `UserID` from the bearer token, never a value from the request body

### Requirement: GET /api/video-jobs/:id Returns Status Scoped to the Owner

`GET /api/video-jobs/:id` SHALL require a valid bearer token, look up the job via `GetJobStatus` using the authenticated `UserID`, and return its status, frame count, error reason, and storage key. A job that does not exist and a job owned by a different user SHALL both return `404`, never a distinct `403`. A malformed job id SHALL return `400` before any repository lookup.

#### Scenario: Owner retrieves their own job's status

- **GIVEN** an authenticated user owns a `VideoJob` with a given id
- **WHEN** `GET /api/video-jobs/:id` is called with that id
- **THEN** the response is `200` with the job's status, frame count, error reason, and storage key

#### Scenario: Non-owner request returns 404, not 403

- **GIVEN** a `VideoJob` exists but is owned by a different user than the requester
- **WHEN** `GET /api/video-jobs/:id` is called with that job's id
- **THEN** the response is `404`, identical to the response for a nonexistent id

#### Scenario: Malformed job id returns 400 before any lookup

- **GIVEN** an authenticated request whose `:id` path parameter is not a well-formed job id
- **WHEN** `GET /api/video-jobs/:id` is called
- **THEN** the response is `400`

### Requirement: GET /api/video-jobs Lists the Caller's Own Jobs, Paginated

`GET /api/video-jobs` SHALL require a valid bearer token and return a page of the authenticated user's own jobs via `ListUserJobs`, ordered newest first. `offset` and `limit` query parameters SHALL default to `0` and `20` respectively when absent or present-but-empty (e.g. `?limit=`). A present, non-empty value that is not a valid integer (e.g. `?limit=abc`, `?offset=1.5`) SHALL be rejected with `400`, exactly like an in-range-but-invalid value below — it SHALL NOT be treated as absent. An explicitly supplied `limit` outside `1`-`100` or a negative `offset` SHALL be rejected with `400`, not silently clamped. The returned jobs are not limited to ones created through `POST /api/video-jobs` — `VideoJob`s created through any path (including the legacy `POST /upload` flow) that share the same owning `UserID` appear in this listing too, since they are the same aggregate in the same repository.

#### Scenario: Listing with no query parameters uses defaults

- **GIVEN** an authenticated user owns several `VideoJob`s and issues a request with no `offset`/`limit` query parameters
- **WHEN** `GET /api/video-jobs` is called
- **THEN** the response is `200` with up to 20 of the caller's own jobs, newest first, starting from the first

#### Scenario: Explicit out-of-range limit is rejected

- **GIVEN** an authenticated request supplies a `limit` of `0` or greater than `100`
- **WHEN** `GET /api/video-jobs` is called
- **THEN** the response is `400`, and the requested limit is not silently clamped into range

#### Scenario: Non-integer query value is rejected, not defaulted

- **GIVEN** an authenticated request supplies a non-empty, non-integer `limit` or `offset` (e.g. `limit=abc`, `offset=1.5`)
- **WHEN** `GET /api/video-jobs` is called
- **THEN** the response is `400` — the value is never silently treated as absent and defaulted

#### Scenario: Listing never includes another user's jobs

- **GIVEN** `VideoJob`s exist for both the authenticated user and at least one other user
- **WHEN** `GET /api/video-jobs` is called
- **THEN** the returned list contains only jobs owned by the authenticated user

#### Scenario: Listing includes jobs created outside this API

- **GIVEN** the authenticated user has a `VideoJob` created via `POST /upload` (in `completed` or `failed` status) as well as one created via `POST /api/video-jobs` (in `pending` status)
- **WHEN** `GET /api/video-jobs` is called
- **THEN** the response includes both jobs, each reporting its own actual status

### Requirement: Jobs Created Through This API Have No Processing Trigger

A `VideoJob` created via `POST /api/video-jobs` SHALL remain in `pending` status indefinitely within this capability's scope — no code path reachable from these three endpoints calls `EnqueueVideoJob`, `StartProcessing`, `CompleteJob`, or `FailJob`. This capability SHALL NOT be documented or presented as a working end-to-end processing path until a later change explicitly wires a processing trigger.

**This survives the asynchronous cutover, and the distinction is now easy to get wrong.** `POST /upload` is the asynchronous submission endpoint: it stores bytes, creates a job, enqueues it, and returns a reference to *this* capability's read endpoints. `POST /api/video-jobs` accepts a filename in JSON, has no uploaded bytes, and therefore still has nothing to enqueue — a job it creates carries no source key and `videojob-lifecycle`'s enqueue precondition rejects it. The worker introduced by the cutover SHALL NOT be presented as this endpoint's processing trigger.

A `pending` status observed through `GET /api/video-jobs/:id` therefore SHALL NOT be interpreted as "waiting for a worker". A job awaiting a worker is `queued`.

#### Scenario: A created job's status never advances on its own

- **GIVEN** a `VideoJob` was created via `POST /api/video-jobs` and no other system component acts on it
- **WHEN** its status is queried via `GET /api/video-jobs/:id` at any later time
- **THEN** the status is still `pending`

#### Scenario: A worker never picks up a job created through this endpoint

- **GIVEN** a `VideoJob` created via `POST /api/video-jobs`, and a running worker consuming the job queue
- **WHEN** the worker has drained the queue
- **THEN** that job is still `pending`, no message naming it was ever published, and no extraction ran for it

### Requirement: GET /api/video-jobs/:id Is The Status Endpoint The Upload Response Names

`GET /api/video-jobs/:id` SHALL be the endpoint a `POST /upload` response directs its caller to for the submitted job's outcome, and its response SHALL be sufficient on its own to decide whether to keep polling, stop and download, or stop and report a failure.

This promotes an endpoint that until now only served a preview API into the asynchronous flow's status channel. Its contract does not change: it remains bearer-authenticated and owner-scoped, and returns the same representation for a job created through either entry point. What changes is that a client depends on it, so the statuses it can return SHALL be treated as a public contract — `pending`, `queued`, `processing`, `completed`, and `failed` — and a `failed` job's response SHALL carry the persisted failure reason so a poller can report it without a second call.

A caller SHALL NOT have to distinguish which entry point created a job in order to poll it.

#### Scenario: A submitted job is polled to completion through this endpoint

- **GIVEN** a video submitted through `POST /upload` by an authenticated user
- **WHEN** that user repeatedly requests the status URL the submission returned
- **THEN** each response is this endpoint's owner-scoped representation, the status advances through `queued` and `processing` to `completed`, and no other endpoint is needed to observe the outcome

#### Scenario: A failed job reports its reason to the poller

- **GIVEN** a submitted job whose processing failed
- **WHEN** its owner polls this endpoint
- **THEN** the response reports `failed` and carries the persisted failure reason

#### Scenario: Polling is owner-scoped like every other read here

- **GIVEN** a job submitted by one user
- **WHEN** a different authenticated user polls its status URL
- **THEN** the request is rejected exactly as this capability's existing owner-scoping requires
