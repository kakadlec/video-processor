# videojob-result-storage Specification

## Purpose

Define how a `VideoJob`'s result artifact — the zip of extracted frames — is stored, authorized, and handed back. It covers the `ResultStorage` domain port and its MinIO adapter, the object key convention, the fail-closed startup wiring that makes the bucket a hard dependency of `cmd/api`, and the two HTTP endpoints through which a stored result is reached (`GET /download/:filename`, which authorizes and issues a bounded URL the client redeems against object storage, and `GET /api/status`, which lists them). Neither returns the artifact's bytes: since presigned issuance replaced proxied streaming, no response from this API carries a result.

Connection plumbing for MinIO — configuration, client construction, health check, bucket provisioning — belongs to `minio-infrastructure`; this capability builds on it. The extraction that produces the zip belongs to `videojob-execution`, and the aggregate's own `StorageKey`/status invariants to `ddd-architecture`.

## Requirements

### Requirement: A ResultStorage Domain Port Owns Result Artifact I/O

`internal/video/domain` SHALL define a `ResultStorage` port through which a `VideoJob`'s result artifact is stored, described, and made retrievable, with three operations: storing a local file under a `StorageKey`; reporting a stored artifact's size and last-modified time without reading its contents; and issuing a time-bounded URL that grants unauthenticated read access to one stored artifact. Its MinIO-backed implementation SHALL live in `internal/video/infrastructure/storage`, beside the connection plumbing `minio-infrastructure` defines. The domain and application layers SHALL depend only on the port.

The port SHALL NOT expose an operation that returns a reader over a stored artifact. Nothing in the system reads result bytes through the API after presigned issuance replaces proxied streaming, and a port method with no consumer is an invitation to route those bytes back through a process this design removed from the data path.

The port SHALL expose three sentinel errors owned by the domain, so callers can classify failures without matching on the MinIO client's own error codes outside the adapter: one for a missing artifact, one that every failure to store an artifact wraps, and one that every failure to issue a presigned URL wraps. The latter two exist because the storage client's error text names the endpoint and the bucket, and that text must never reach a user-facing message or a persisted failure reason — a caller that needs to describe a storage failure matches the sentinel and writes its own wording.

The wrapped error itself MAY carry the client's own text, and does: that is what makes it worth logging. The obligation is on the **caller's rendering**, not on the error value — a handler matches the sentinel, logs the wrapped error, and writes its own response. This is the contract the store-failure sentinel has always had, and the presign-failure sentinel inherits it unchanged rather than inventing a stricter one the adapter would have to strip detail to satisfy.

#### Scenario: Domain and application layers do not import the adapter

- **GIVEN** the `ResultStorage` adapter exists under `internal/video/infrastructure/storage`
- **WHEN** `internal/video/dependency_rules_test.go` runs
- **THEN** it passes, with no package under `internal/video/domain` or `internal/video/application` importing the adapter

#### Scenario: A missing artifact is reported as the domain's sentinel

- **GIVEN** no object is stored under a given `StorageKey`
- **WHEN** the artifact is stated through the port
- **THEN** the returned error matches the domain's not-found sentinel via `errors.Is`, and carries no MinIO-specific error type or code to the caller

#### Scenario: A storage failure is distinguishable from a missing artifact

- **GIVEN** the MinIO endpoint is unreachable
- **WHEN** the artifact is stated through the port
- **THEN** the returned error does not match the not-found sentinel

#### Scenario: Every store failure matches the store-failure sentinel

- **GIVEN** storing an artifact fails for any reason
- **WHEN** the caller inspects the returned error with `errors.Is`
- **THEN** it matches the domain's store-failure sentinel, so the caller can recognize the failure without parsing the underlying client's message

#### Scenario: Every issuance failure matches the presign-failure sentinel

- **GIVEN** issuing a presigned URL fails for any reason
- **WHEN** the caller inspects the returned error with `errors.Is`
- **THEN** it matches the domain's presign-failure sentinel, so the caller can recognize the failure without parsing the underlying client's message

#### Scenario: The wrapped error keeps its detail for the log, not for the response

- **GIVEN** an issuance failure whose underlying error names the endpoint and bucket
- **WHEN** the handler renders a response for it
- **THEN** the response body contains none of that text, and the underlying error is logged intact — the detail is stripped at the rendering boundary, not at the adapter

#### Scenario: No reader-returning operation remains on the port

- **WHEN** the `ResultStorage` port is inspected
- **THEN** it declares no operation returning an `io.Reader`, `io.ReadCloser`, or equivalent stream over a stored artifact, and no handler streams result bytes through `cmd/api`

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

The failure reason it records for a storage failure SHALL be its own fixed wording, not the storage error's text. That reason is persisted on the job and echoed to the uploader through `POST /upload` and `GET /api/video-jobs/:id`, and the adapter's error names the endpoint and bucket. Extraction failures continue to echo `ffmpeg`'s own message, as they always have.

The local zip SHALL be written under `temp/`, not `outputs/`; no directory named `outputs` is created, written to, or read by any part of the system after this change.

#### Scenario: A successfully stored result leaves no local zip behind

- **GIVEN** a video `ffmpeg` can decode
- **WHEN** `ProcessVideoJob.Execute` completes successfully
- **THEN** the object exists in the bucket under the job's derived key, and no zip file remains under `temp/`

#### Scenario: A storage failure fails the job and still cleans up

- **GIVEN** frame extraction succeeded but storing the zip fails
- **WHEN** `ProcessVideoJob.Execute` returns
- **THEN** the job's persisted status is `failed` with a non-empty `ErrorReason`, the result reports failure to the caller, and the zip this request produced no longer exists under `temp/`

#### Scenario: A storage failure leaks no infrastructure detail

- **GIVEN** storing the zip fails with an error naming the object-storage endpoint and bucket
- **WHEN** the job is failed and the upload response is written
- **THEN** neither the persisted `ErrorReason` nor the message returned to the uploader contains the storage client's error text, endpoint, or bucket name — the underlying error is logged instead

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

`GET /download/:filename` SHALL issue a presigned URL for the stored object only when all of the following hold: the requested filename parses to a `VideoJobID`, a `VideoJob` with that ID exists, its `UserID` equals the authenticated caller's, its status is `completed`, and its recorded `StorageKey` equals the requested filename. Authorization SHALL NOT consult any on-disk ownership record.

Because an issued URL carries no identity, issuance SHALL be the only point at which ownership is evaluated. The system SHALL NOT rely on any later check between issuing the URL and the object being served.

The route SHALL have no unauthenticated behavior. It is gated by `requireBearerAuth()`, so an authenticated `UserID` is always present; the handler SHALL NOT retain a fallback branch that serves artifacts when no identity is authenticated.

#### Scenario: The owner of a completed job obtains a URL for its result

- **GIVEN** a `completed` `VideoJob` owned by the authenticated caller, whose result is stored
- **WHEN** they request `GET /download/:filename` with that job's storage key
- **THEN** the response is `HTTP 200` with a JSON body carrying a URL for that object and the instant it stops being accepted, and the response body does not contain the object's bytes

#### Scenario: The issued URL yields the artifact without a bearer token

- **GIVEN** a URL issued to the owner of a completed job
- **WHEN** it is requested directly with no `Authorization` header
- **THEN** the response carries exactly the stored object's bytes

#### Scenario: Comparing the requested key against the job's own recorded key

- **GIVEN** a key that parses to a real `VideoJobID` but does not equal that job's recorded `StorageKey`
- **WHEN** it is requested by the job's own owner
- **THEN** the response is `HTTP 404` and no URL is issued

#### Scenario: A rejected request mints nothing

- **GIVEN** any request that fails the entitlement check
- **WHEN** it is handled
- **THEN** no presigned URL is generated for the requested key, so a rejection cannot be turned into a credential by observing timing or downstream effects

### Requirement: Every Download Rejection Is Byte-Identical

`GET /download/:filename` SHALL respond `HTTP 404` with the same generic `File not found` JSON body for every rejection: an unparseable key, a job that does not exist, a job owned by someone else, a job that is not `completed`, a key that does not match the job's own, an object missing from the bucket, and a failure to issue the presigned URL. No storage-layer error text SHALL reach the response body; adapter errors SHALL be logged instead.

A caller SHALL NOT be able to distinguish these cases by status code, body, or headers, so the endpoint cannot be used to probe for the existence of other users' artifacts.

Because signing is performed locally and succeeds for a key that holds no object, the handler SHALL confirm the object's existence before issuing a URL. Without that confirmation a missing object would surface as a `404` from the storage service — at a different origin, with a different body — which is exactly the distinguishable rejection this requirement forbids.

#### Scenario: Another user's result is indistinguishable from a missing one

- **GIVEN** a `completed` job owned by user A
- **WHEN** user B requests its storage key with a valid bearer token
- **THEN** the response is `HTTP 404` with a body byte-identical to the response for a key that belongs to no job at all

#### Scenario: A storage outage does not leak through the response body

- **GIVEN** the MinIO endpoint is unreachable
- **WHEN** an owner requests their own completed job's result
- **THEN** the response body contains no MinIO error text, endpoint, or bucket name

#### Scenario: A completed job whose object was deleted is refused at issuance

- **GIVEN** a `completed` `VideoJob` owned by the caller whose object no longer exists in the bucket
- **WHEN** they request `GET /download/:filename` with that job's storage key
- **THEN** the response is `HTTP 404` byte-identical to every other rejection, and no URL is issued

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

The sidecar helpers no longer remain in the codebase for any directory. They survived this capability's change only because `uploads/` still used them; `migrate-upload-storage-to-minio` moves source videos into the bucket as well, deletes the `/uploads` static route, and removes every sidecar symbol. `videojob-source-storage` owns that retirement — this capability now only asserts that no result artifact has ever had one.

#### Scenario: No outputs directory is created at startup

- **WHEN** `cmd/api` starts
- **THEN** it creates `temp/`, and does not create `outputs/`

#### Scenario: The outputs route no longer exists

- **WHEN** an authenticated client requests any path under `/outputs/`
- **THEN** the router has no such route

#### Scenario: A completed job writes no ownership sidecar

- **WHEN** an upload is processed successfully
- **THEN** no `.owner` file is written for the result artifact, and the job's `UserID` is the only record of who owns it

#### Scenario: No sidecar mechanism remains for any artifact class

- **WHEN** the codebase is inspected for the ownership-sidecar helpers, their suffix constant, and the middleware that enforced them
- **THEN** none is present, and no route or handler depends on a sidecar for entitlement

### Requirement: The Result Storage Adapter Is Tested Against A Real MinIO Instance

This capability's adapter tests SHALL exercise storing, stating, and presigning artifacts against a real MinIO instance rather than a mock, using the same `VIDEO_MINIO_TEST_*` variables `minio-infrastructure` established, and SHALL skip with a clear message when those variables are unset. Opening is no longer among them: the port no longer offers that operation.

Presigning SHALL be verified by **following the issued URL against that instance and comparing bytes**, never by inspecting the URL alone. A signed URL is structurally well-formed whether or not the storage service will honor it, so assertions on its host, path, or parameters cannot establish that it works.

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
- **THEN** they upload a real video, and assert the result is retrievable by following the URL `GET /download/:filename` issues for it, and that it is listed by `GET /api/status`

### Requirement: A Presigned Result URL Grants Bounded, Single-Object, Non-Revocable Access

An issued URL SHALL grant read access to exactly one object — the `StorageKey` it was issued for — and SHALL carry a fixed expiry chosen by the system rather than by the caller. The expiry SHALL be a compile-time constant of five minutes; no environment variable SHALL configure it, matching the fixed TTL `videojob-status-cache` already uses.

The expiry reported to the caller SHALL be **derived from the issued URL's own signed fields**, not computed from the issuing process's clock. The signing library stamps the signature's start instant at whole-second precision and truncates the requested lifetime to whole seconds, so an independently computed `now + TTL` overstates the real admission window by up to a second — measured at 561 ms in one issuance against the pinned stack, and a requested lifetime of five minutes and 500 milliseconds signed as exactly 300 seconds. An expiry that overstates the credential's actual admission window is worse than no expiry field: a client that trusts it retries at an instant the storage service has already stopped accepting.

The guarantee the storage service enforces is that **a request arriving after the expiry instant is refused**. It is NOT that access ceases at that instant: a transfer already in progress when the expiry passes runs to completion, and clock skew between the issuing process and the storage service shifts the effective instant in both directions. Specifications, documentation, and code comments SHALL describe this bound as request admission, never as "the URL stops working" or "no download continues past expiry".

An issued URL SHALL NOT be revocable. Deleting the `VideoJob`, changing its owner, or changing its status does not invalidate a URL already handed out; the expiry is the entire mechanism by which access ends. This is a property of presigned access, and it is the reason the expiry is short.

#### Scenario: A request arriving after the expiry is refused

- **GIVEN** a presigned URL whose expiry instant has passed
- **WHEN** it is requested
- **THEN** the storage service refuses the request and returns none of the object's bytes

#### Scenario: A transfer in flight at the expiry runs to completion

- **GIVEN** a presigned URL requested before its expiry, whose object is large enough that the transfer outlasts the expiry instant
- **WHEN** the response body is read to completion
- **THEN** every byte of the object is received, demonstrating that the bound applies to request admission rather than to transfer duration

#### Scenario: The URL grants access to no other object

- **GIVEN** a URL issued for one job's `StorageKey`
- **WHEN** its path is altered to name a different stored object
- **THEN** the storage service refuses the request

#### Scenario: The expiry is not configurable

- **WHEN** the configuration surface is inspected
- **THEN** no environment variable sets, extends, or shortens the presigned URL's lifetime

#### Scenario: The reported expiry equals the signed admission instant

- **GIVEN** a URL issued for a stored artifact
- **WHEN** the expiry reported to the caller is compared against the instant encoded in the URL's own signature
- **THEN** the two are equal, rather than the reported value being computed independently from the issuing process's clock

### Requirement: The Attachment Filename Travels Inside The Signature

The issued URL SHALL instruct the storage service to return `Content-Disposition: attachment` naming the result's key, carried as a signed request parameter rather than applied by the API. Altering that parameter after issuance SHALL invalidate the URL.

This is not cosmetic. The browser reaches the storage service at a different origin, where the HTML `download` attribute is ignored, so the response header is the only thing that makes the artifact a download rather than a navigation.

#### Scenario: Following the URL yields an attachment

- **GIVEN** a presigned URL issued for a completed job's result
- **WHEN** it is requested
- **THEN** the response carries `Content-Disposition: attachment` naming that job's result key

#### Scenario: Tampering with the disposition invalidates the URL

- **GIVEN** a presigned URL whose disposition parameter is altered after issuance
- **WHEN** it is requested
- **THEN** the storage service refuses the request

### Requirement: An Issued URL Is Treated As A Credential

An issued URL SHALL NOT be written to a log, included in an error message, echoed in a failure response, or recorded on the `VideoJob`. Where the existing handlers log a `StorageKey` on failure, the issuance path SHALL do the same — the key is not a credential and the URL is.

The issuance response SHALL carry `Cache-Control: no-store`. Without it the response is an ordinary authenticated `200` that a private or user-agent cache may retain, which would preserve a working credential past the request that requested it and defeat the short lifetime this design relies on. The header SHALL be set on **every** response the endpoint produces, not only the successful one, so that the rejection responses this capability requires to be byte-identical remain byte-identical to one another.

#### Scenario: A failure on the issuance path logs the key, not a URL

- **GIVEN** issuance fails after the entitlement check passes
- **WHEN** the failure is logged
- **THEN** the log records the requested `StorageKey` and the underlying error, and contains no signed URL or signature parameter

#### Scenario: No issued URL is persisted

- **WHEN** a URL is issued for a job's result
- **THEN** nothing is written to the job's row, and the URL exists only in the response to the request that asked for it

#### Scenario: The issuance response forbids caching

- **WHEN** a URL is issued for a job's result
- **THEN** the response carries `Cache-Control: no-store`

#### Scenario: A rejection carries the same caching directive

- **WHEN** the endpoint rejects a request for any reason
- **THEN** that response carries `Cache-Control: no-store` too, leaving every rejection byte-identical to every other

### Requirement: Presigned Issuance Is Verified By Following The Issued URL

This capability's tests SHALL include at least one that issues a URL against the real test MinIO instance, requests it with no `Authorization` header, and compares the received bytes to the stored object. Assertions on the URL's structure — host, path, expiry parameter, disposition parameter — SHALL be supplementary to that test, never a substitute for it: a structurally correct URL can still be refused, and only following it distinguishes the two.

The suite SHALL additionally cover that a client configured against a public endpoint the test process cannot resolve still issues a URL, since that is the deployment shape the public-endpoint configuration exists to serve and the failure mode it guards against is a network call hidden inside signing.

#### Scenario: The issued URL is followed and its bytes compared

- **GIVEN** an object of known content stored in the test bucket
- **WHEN** a URL is issued for it and requested with no `Authorization` header
- **THEN** the response status indicates success and the received bytes equal the stored object's exactly

#### Scenario: Issuance succeeds against an unresolvable public endpoint

- **GIVEN** a presigning client configured with a public endpoint that does not resolve from the test process
- **WHEN** a URL is issued
- **THEN** issuance succeeds without a network call, and the URL's host is the configured public endpoint
