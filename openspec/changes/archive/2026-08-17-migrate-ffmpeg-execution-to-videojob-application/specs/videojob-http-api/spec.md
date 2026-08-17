## MODIFIED Requirements

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
