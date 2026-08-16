## ADDED Requirements

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

`GET /api/video-jobs` SHALL require a valid bearer token and return a page of the authenticated user's own jobs via `ListUserJobs`, ordered newest first. `offset` and `limit` query parameters SHALL default to `0` and `20` respectively when absent. An explicitly supplied `limit` outside `1`-`100` or a negative `offset` SHALL be rejected with `400`, not silently clamped.

#### Scenario: Listing with no query parameters uses defaults

- **GIVEN** an authenticated user owns several `VideoJob`s and issues a request with no `offset`/`limit` query parameters
- **WHEN** `GET /api/video-jobs` is called
- **THEN** the response is `200` with up to 20 of the caller's own jobs, newest first, starting from the first

#### Scenario: Explicit out-of-range limit is rejected

- **GIVEN** an authenticated request supplies a `limit` of `0` or greater than `100`
- **WHEN** `GET /api/video-jobs` is called
- **THEN** the response is `400`, and the requested limit is not silently clamped into range

#### Scenario: Listing never includes another user's jobs

- **GIVEN** `VideoJob`s exist for both the authenticated user and at least one other user
- **WHEN** `GET /api/video-jobs` is called
- **THEN** the returned list contains only jobs owned by the authenticated user

### Requirement: Jobs Created Through This API Have No Processing Trigger

A `VideoJob` created via `POST /api/video-jobs` SHALL remain in `pending` status indefinitely within this capability's scope — no code path reachable from these three endpoints calls `EnqueueVideoJob`, `StartProcessing`, `CompleteJob`, or `FailJob`. This capability SHALL NOT be documented or presented as a working end-to-end processing path until a later change explicitly wires a processing trigger.

#### Scenario: A created job's status never advances on its own

- **GIVEN** a `VideoJob` was created via `POST /api/video-jobs` and no other system component acts on it
- **WHEN** its status is queried via `GET /api/video-jobs/:id` at any later time
- **THEN** the status is still `pending`
