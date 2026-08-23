# videojob-result-storage Specification

## Purpose

Define how a `VideoJob`'s result artifact — the zip of extracted frames — is stored, authorized, and served. It covers the `ResultStorage` domain port and its MinIO adapter, the object key convention, the fail-closed startup wiring that makes the bucket a hard dependency of `cmd/api`, and the two HTTP endpoints that read results back (`GET /download/:filename` and `GET /api/status`).

Connection plumbing for MinIO — configuration, client construction, health check, bucket provisioning — belongs to `minio-infrastructure`; this capability builds on it. The extraction that produces the zip belongs to `videojob-execution`, and the aggregate's own `StorageKey`/status invariants to `ddd-architecture`.

## Requirements

### Requirement: A ResultStorage Domain Port Owns Result Artifact I/O

`internal/video/domain` SHALL define a `ResultStorage` port through which a `VideoJob`'s result artifact is stored and retrieved, with three operations: storing a local file under a `StorageKey`, opening a stored artifact for reading together with its size in bytes, and reporting a stored artifact's size and last-modified time without reading its contents. Its MinIO-backed implementation SHALL live in `internal/video/infrastructure/storage`, beside the connection plumbing `minio-infrastructure` defines. The domain and application layers SHALL depend only on the port.

The port SHALL report a missing artifact through a single sentinel error owned by the domain, so callers can distinguish "not stored" from "storage failed" without matching on the MinIO client's own error codes outside the adapter.

#### Scenario: Domain and application layers do not import the adapter

- **GIVEN** the `ResultStorage` adapter exists under `internal/video/infrastructure/storage`
- **WHEN** `internal/video/dependency_rules_test.go` runs
- **THEN** it passes, with no package under `internal/video/domain` or `internal/video/application` importing the adapter

#### Scenario: A missing artifact is reported as the domain's sentinel

- **GIVEN** no object is stored under a given `StorageKey`
- **WHEN** the artifact is opened or stated through the port
- **THEN** the returned error matches the domain's not-found sentinel via `errors.Is`, and carries no MinIO-specific error type or code to the caller

#### Scenario: A storage failure is distinguishable from a missing artifact

- **GIVEN** the MinIO endpoint is unreachable
- **WHEN** the artifact is opened or stated through the port
- **THEN** the returned error does not match the not-found sentinel

### Requirement: Opening An Artifact Resolves Its Existence And Size Before Returning

The `ResultStorage` adapter SHALL confirm the object exists and determine its size before returning a reader to the caller, rather than returning a lazily-evaluated stream whose failure would surface only after the caller has begun writing a response. The returned size SHALL be the object's byte length, so an HTTP caller can set `Content-Length` before streaming.

#### Scenario: Opening a stored artifact yields a reader and its exact size

- **GIVEN** an artifact of known byte length stored under a `StorageKey`
- **WHEN** it is opened through the port
- **THEN** the call returns a reader and a size equal to that byte length, and reading the reader to completion yields exactly those bytes

#### Scenario: Opening a missing artifact fails before any reader is returned

- **GIVEN** no object is stored under a given `StorageKey`
- **WHEN** it is opened through the port
- **THEN** the call returns the not-found sentinel and no usable reader, rather than a reader that fails on first read

### Requirement: The Result Object Key Is Derived From The VideoJobID And Contains No Path Separator

The object key for a `VideoJob`'s result SHALL be `frames_<jobID>.zip`, derived from the job's ID by a single function in `internal/video/domain` that both the storing and the retrieving side use. The key SHALL contain no `/` character.

The flat shape is a hard constraint, not a stylistic preference: the key is returned to the browser as `POST /upload`'s `zip_path` and used verbatim as the `:filename` path segment of `GET /download/:filename`. A `/` inside it percent-encodes to `%2F`, which is decoded back into the request path and prevents the single-segment route parameter from matching.

`internal/video/domain` SHALL also expose the inverse: recovering a `VideoJobID` from a well-formed key, and failing clearly for a key that does not have that shape.

#### Scenario: The key round-trips through its derivation and parse

- **GIVEN** a `VideoJobID`
- **WHEN** its result key is derived and then parsed back
- **THEN** the recovered `VideoJobID` equals the original, and the key contains no `/`

#### Scenario: A malformed key is rejected rather than guessed at

- **GIVEN** a key that does not have the `frames_<jobID>.zip` shape, or whose embedded identifier is not a valid `VideoJobID`
- **WHEN** it is parsed
- **THEN** parsing returns an error and no `VideoJobID`

### Requirement: ProcessVideoJob Stores The Extracted Zip And Removes The Local Copy

`ProcessVideoJob` SHALL, after `FrameExtractor.ExtractFrames` returns successfully, store the extracted zip through `ResultStorage` under the key derived from the job's ID, and SHALL remove the local zip file afterwards whether storing succeeded or failed. On a storage failure it SHALL call `FailJob` and report the failure to its caller, exactly as it already does for an extraction failure — a result that could not be stored is not a result.

The local zip SHALL be written under `temp/`, not `outputs/`; no directory named `outputs` is created, written to, or read by any part of the system after this change.

#### Scenario: A successfully stored result leaves no local zip behind

- **GIVEN** a video `ffmpeg` can decode
- **WHEN** `ProcessVideoJob.Execute` completes successfully
- **THEN** the object exists in the bucket under the job's derived key, and no zip file remains under `temp/`

#### Scenario: A storage failure fails the job and still cleans up

- **GIVEN** frame extraction succeeded but storing the zip fails
- **WHEN** `ProcessVideoJob.Execute` returns
- **THEN** the job's persisted status is `failed` with a non-empty `ErrorReason`, the result reports failure to the caller, and no zip file remains under `temp/`

### Requirement: MinIO Configuration Is Required At Application Startup

`cmd/api` SHALL, during `setupVideo`, load the MinIO configuration, construct the client, confirm connectivity with a real round trip, and ensure the configured bucket exists — failing fatally and refusing to start if any of the four steps fails.

Startup SHALL use `minio-infrastructure`'s existing `LoadConfigFromEnv` unchanged, and therefore inherits its contract exactly: `VIDEO_MINIO_ENDPOINT`, `VIDEO_MINIO_ACCESS_KEY`, `VIDEO_MINIO_SECRET_KEY`, and `VIDEO_MINIO_BUCKET` are required, while `VIDEO_MINIO_USE_SSL` remains **optional** — unset means `false`, and a present-but-unparseable value is still an error. What changes here is only *when* that loader runs: it becomes a startup precondition rather than something no composition root calls. This capability SHALL NOT make `VIDEO_MINIO_USE_SSL` mandatory; doing so would require modifying the configuration contract, the loader, and its tests, none of which are in this change's scope.

This posture is fail-closed, deliberately unlike every Redis-backed feature in this codebase (rate limiting, upload idempotency, and the job status cache all fail open). Those degrade to a slower but correct system when Redis is unavailable; a bucket that cannot be written leaves a completed job with nowhere to put its result, so there is nothing to degrade to.

#### Scenario: A missing MinIO variable prevents startup

- **GIVEN** any of the four required `VIDEO_MINIO_*` variables is unset or empty
- **WHEN** `cmd/api` starts
- **THEN** it exits with an error naming the missing variable, and does not begin serving requests

#### Scenario: VIDEO_MINIO_USE_SSL stays optional

- **GIVEN** the four required variables are set and `VIDEO_MINIO_USE_SSL` is unset
- **WHEN** `cmd/api` starts
- **THEN** it starts normally with TLS disabled, exactly as `minio-infrastructure` already specifies for that variable

#### Scenario: An unreachable MinIO endpoint prevents startup

- **GIVEN** every `VIDEO_MINIO_*` variable is set but the endpoint is unreachable
- **WHEN** `cmd/api` starts
- **THEN** it exits with an error, rather than starting and failing on the first upload

#### Scenario: The bucket is provisioned at startup

- **GIVEN** a reachable MinIO instance whose configured bucket does not yet exist
- **WHEN** `cmd/api` starts
- **THEN** the bucket is created and the server starts serving requests

### Requirement: Result Download Is Authorized From The VideoJob Row

`GET /download/:filename` SHALL serve the stored object only when all of the following hold: the requested filename parses to a `VideoJobID`, a `VideoJob` with that ID exists, its `UserID` equals the authenticated caller's, its status is `completed`, and its recorded `StorageKey` equals the requested filename. Authorization SHALL NOT consult any on-disk ownership record.

The route SHALL have no unauthenticated behavior. It is gated by `requireBearerAuth()`, so an authenticated `UserID` is always present; the handler SHALL NOT retain a fallback branch that serves artifacts when no identity is authenticated.

#### Scenario: The owner of a completed job downloads its result

- **GIVEN** a `completed` `VideoJob` owned by the authenticated caller, whose result is stored
- **WHEN** they request `GET /download/:filename` with that job's storage key
- **THEN** the response is `HTTP 200` with the object's exact bytes, a `Content-Length` matching its size, and the same `Content-Disposition`/`Content-Type` headers the route sent before this change

#### Scenario: Comparing the requested key against the job's own recorded key

- **GIVEN** a key that parses to a real `VideoJobID` but does not equal that job's recorded `StorageKey`
- **WHEN** it is requested by the job's own owner
- **THEN** the response is `HTTP 404`

### Requirement: Every Download Rejection Is Byte-Identical

`GET /download/:filename` SHALL respond `HTTP 404` with the same generic `File not found` JSON body for every rejection: an unparseable key, a job that does not exist, a job owned by someone else, a job that is not `completed`, a key that does not match the job's own, and an object missing from the bucket. No storage-layer error text SHALL reach the response body; adapter errors SHALL be logged instead.

A caller SHALL NOT be able to distinguish these cases by status code, body, or headers, so the endpoint cannot be used to probe for the existence of other users' artifacts.

#### Scenario: Another user's result is indistinguishable from a missing one

- **GIVEN** a `completed` job owned by user A
- **WHEN** user B requests its storage key with a valid bearer token
- **THEN** the response is `HTTP 404` with a body byte-identical to the response for a key that belongs to no job at all

#### Scenario: A storage outage does not leak through the response body

- **GIVEN** the MinIO endpoint is unreachable
- **WHEN** an owner requests their own completed job's result
- **THEN** the response body contains no MinIO error text, endpoint, or bucket name

### Requirement: The Result Listing Is Sourced From The Job Repository And The Bucket

`GET /api/status` SHALL list the authenticated caller's `completed` `VideoJob`s, retrieved by a repository query that filters on status in SQL, and SHALL report for each one a `filename` equal to the job's `StorageKey`, a `download_url` of `/download/<key>`, and a `size` and `created_at` obtained from the stored object.

That query SHALL NOT be paginated. `GET /api/status` accepts no pagination parameters and today returns every zip the caller owns through an unbounded `outputs/*.zip` glob; any hidden limit would silently make a user's older results unreachable through the only listing endpoint the frontend consumes, which is a regression rather than a refinement. Filtering on status in SQL is what makes an unpaginated query the right shape here — it returns exactly the rows the endpoint renders, instead of a page that non-completed jobs can crowd out. Introducing real pagination is a reasonable follow-up, but it requires a matching `app.js` change and therefore belongs to a change that touches the frontend, not to this one.

`created_at` SHALL be the stored object's last-modified time, preserving the field's existing meaning as the artifact's timestamp rather than silently changing it to the job's creation time.

An entry whose object cannot be stated — missing, or a storage error — SHALL be omitted from the listing rather than failing the request, mirroring the behavior the filesystem-backed implementation had for an unreadable file. The response SHALL keep its existing `{"files": [...], "total": N}` shape, with `total` counting only listed entries.

The route SHALL have no unauthenticated behavior, for the same reason as the download route.

#### Scenario: A completed job's result appears in the listing

- **GIVEN** an upload that has been processed successfully
- **WHEN** its owner requests `GET /api/status`
- **THEN** the response includes an entry whose `filename` equals the `zip_path` the upload returned, with a non-zero `size` and a non-empty `created_at`

#### Scenario: The listing is scoped to the authenticated owner

- **GIVEN** completed jobs belonging to two different users
- **WHEN** one of them requests `GET /api/status` with a valid bearer token
- **THEN** the response lists only that caller's own results, and `total` counts only those

#### Scenario: Non-completed jobs never displace completed results

- **GIVEN** a user whose most recent jobs are all `failed` or `pending`, with `completed` jobs older than them
- **WHEN** they request `GET /api/status`
- **THEN** their completed results are listed, rather than being displaced by non-completed jobs

#### Scenario: Every completed result is listed, regardless of how many there are

- **GIVEN** a user with more completed jobs than any page size the job-listing API uses
- **WHEN** they request `GET /api/status`
- **THEN** every one of their completed results whose object can be stated is listed, matching the unbounded behavior the filesystem-backed endpoint had

#### Scenario: An unreachable object is omitted, not fatal

- **GIVEN** a `completed` job whose stored object has been deleted from the bucket
- **WHEN** its owner requests `GET /api/status`
- **THEN** the response is `HTTP 200`, that job is absent from `files`, and `total` does not count it

### Requirement: The outputs Directory And Its Ownership Sidecars Are Retired

The system SHALL NOT create, serve, or read an `outputs/` directory. The `/outputs` static route SHALL be removed from the router, and no `.owner` sidecar file SHALL be written, read, or deleted for a result artifact.

The sidecar helpers themselves SHALL remain in the codebase for the `uploads/` directory, which still uses them; only the result-artifact call sites are removed by this change.

#### Scenario: No outputs directory is created at startup

- **WHEN** `cmd/api` starts
- **THEN** it creates `uploads/` and `temp/`, and does not create `outputs/`

#### Scenario: The outputs route no longer exists

- **WHEN** an authenticated client requests any path under `/outputs/`
- **THEN** the router has no such route

#### Scenario: A completed job writes no ownership sidecar

- **WHEN** an upload is processed successfully
- **THEN** no `.owner` file is written for the result artifact, and the job's `UserID` is the only record of who owns it

#### Scenario: Upload ownership sidecars still work

- **GIVEN** the `uploads/` directory still stores source videos
- **WHEN** an upload is saved
- **THEN** its `.owner` sidecar is written and enforced on the `/uploads` static route exactly as before this change

### Requirement: The Result Storage Adapter Is Tested Against A Real MinIO Instance

This capability's adapter tests SHALL exercise storing, opening, and stating artifacts against a real MinIO instance rather than a mock, using the same `VIDEO_MINIO_TEST_*` variables `minio-infrastructure` established, and SHALL skip with a clear message when those variables are unset.

`cmd/api`'s own tests, which exercise `POST /upload` end to end, SHALL run against a real bucket and SHALL NOT skip when it is unconfigured: that suite requires MinIO the way it already requires `ffmpeg`, failing loudly instead, since a silently-skipped suite would report green while covering none of the behavior this capability adds. `docker-compose.yml`'s `app-test` service and CI's test step SHALL supply the configuration those tests need.

Every test that provisions a bucket SHALL remove that bucket and its objects when it finishes, including on failure — the local MinIO service stores its data in a named volume, so anything left behind accumulates across later runs.

#### Scenario: Adapter tests skip without a configured instance

- **GIVEN** the `VIDEO_MINIO_TEST_*` variables are unset
- **WHEN** the storage package's tests run
- **THEN** they skip with a message naming the missing configuration

#### Scenario: The application's test suite fails rather than skipping without MinIO

- **GIVEN** the runtime `VIDEO_MINIO_*` variables are unset
- **WHEN** `cmd/api`'s test suite starts
- **THEN** it exits non-zero with a message naming what is missing, rather than skipping its result-storage coverage

#### Scenario: The end-to-end upload path is exercised against a real bucket

- **GIVEN** a configured MinIO instance
- **WHEN** `cmd/api`'s upload tests run
- **THEN** they upload a real video, and assert the result is retrievable through `GET /download/:filename` and listed by `GET /api/status`
