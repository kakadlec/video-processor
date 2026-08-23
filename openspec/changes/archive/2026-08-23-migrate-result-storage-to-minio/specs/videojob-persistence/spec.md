## MODIFIED Requirements

### Requirement: PostgreSQL Repository Implements VideoJobRepository

`internal/video/infrastructure/postgres.Repository` SHALL implement `domain.VideoJobRepository`'s `Create`, `FindByID`, `FindByUserID`, and `FindCompletedByUserID(ctx, userID)` against a `video_jobs` table, using parameterized queries and reconstructing `*domain.VideoJob` via `domain.RestoreVideoJob` from stored rows.

`FindCompletedByUserID` SHALL restrict to `completed` jobs **in the query** and SHALL return all of them, taking no offset or limit. It orders identically to `FindByUserID`: `CreatedAt` descending with `VideoJobID` ascending as a tie-breaker.

The absence of pagination is deliberate and is the reason this method exists separately from `FindByUserID`. Its only caller is `GET /api/status`, which accepts no pagination parameters and whose filesystem-backed predecessor returned every zip the caller owned. Reusing `FindByUserID` and filtering afterwards would be wrong twice over: a page of recent `pending`/`failed` jobs would displace a user's completed results out of the listing, and any limit at all would make older results permanently unreachable through the only listing endpoint the frontend consumes. Filtering on status in SQL is what makes returning the full set reasonable — the rows returned are exactly the rows rendered.

`internal/video/infrastructure/cache.CachedVideoJobRepository` SHALL pass `FindCompletedByUserID` straight through to the decorated repository without caching it, exactly as it already does for `FindByUserID` — the status cache is keyed by individual job ID and has nothing to offer a multi-row listing.

#### Scenario: A created job round-trips through FindByID

- **GIVEN** a `VideoJob` persisted via `Repository.Create`
- **WHEN** `Repository.FindByID` is called with that job's ID
- **THEN** it returns a `*domain.VideoJob` with the same `ID`, `UserID`, `OriginalFilename`, `StorageKey`, `FrameCount`, `ErrorReason`, `Status`, and `CreatedAt`

#### Scenario: FindByID reports not-found for an unknown ID

- **GIVEN** no `VideoJob` exists for a given ID
- **WHEN** `Repository.FindByID` is called with that ID
- **THEN** it returns `domain.ErrVideoJobNotFound`

#### Scenario: FindByUserID orders by CreatedAt descending

- **GIVEN** `VideoJob`s persisted for the same `UserID` with distinct `CreatedAt` values
- **WHEN** `Repository.FindByUserID` is called
- **THEN** the returned jobs are ordered newest-`CreatedAt`-first

#### Scenario: FindByUserID orders and paginates via the stored index

- **GIVEN** multiple `VideoJob`s persisted for the same `UserID`, some with equal `CreatedAt` values
- **WHEN** `Repository.FindByUserID` is called with an offset and limit
- **THEN** the returned jobs are ordered by `CreatedAt` descending with `VideoJobID` ascending as a tie-breaker, bounded by the given offset and limit — matching the ordering `videojob-lifecycle`'s `ListUserJobs` requirement documents

#### Scenario: FindCompletedByUserID returns only completed jobs

- **GIVEN** a `UserID` with jobs in `pending`, `processing`, `failed`, and `completed` statuses
- **WHEN** `Repository.FindCompletedByUserID` is called
- **THEN** only the `completed` jobs are returned

#### Scenario: Non-completed jobs do not hide completed ones

- **GIVEN** a `UserID` whose most recently created jobs are all non-`completed`, with `completed` jobs older than them
- **WHEN** `Repository.FindCompletedByUserID` is called
- **THEN** the completed jobs are returned, rather than an empty result

#### Scenario: All completed jobs are returned, with no implicit limit

- **GIVEN** a `UserID` with more `completed` jobs than `ListUserJobs`' maximum page size
- **WHEN** `Repository.FindCompletedByUserID` is called
- **THEN** every one of them is returned

#### Scenario: FindCompletedByUserID is scoped to its user

- **GIVEN** `completed` jobs belonging to two different users
- **WHEN** `Repository.FindCompletedByUserID` is called for one of them
- **THEN** only that user's jobs are returned
