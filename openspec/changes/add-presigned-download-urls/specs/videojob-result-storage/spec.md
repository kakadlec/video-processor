## MODIFIED Requirements

### Requirement: A ResultStorage Domain Port Owns Result Artifact I/O

`internal/video/domain` SHALL define a `ResultStorage` port through which a `VideoJob`'s result artifact is stored, described, and made retrievable, with three operations: storing a local file under a `StorageKey`; reporting a stored artifact's size and last-modified time without reading its contents; and issuing a time-bounded URL that grants unauthenticated read access to one stored artifact. Its MinIO-backed implementation SHALL live in `internal/video/infrastructure/storage`, beside the connection plumbing `minio-infrastructure` defines. The domain and application layers SHALL depend only on the port.

The port SHALL NOT expose an operation that returns a reader over a stored artifact. Nothing in the system reads result bytes through the API after presigned issuance replaces proxied streaming, and a port method with no consumer is an invitation to route those bytes back through a process this design removed from the data path.

The port SHALL expose three sentinel errors owned by the domain, so callers can classify failures without matching on the MinIO client's own error codes outside the adapter: one for a missing artifact, one that every failure to store an artifact wraps, and one that every failure to issue a presigned URL wraps. The latter two exist because the storage client's error text names the endpoint and the bucket, and that text must never reach a user-facing message or a persisted failure reason — a caller that needs to describe a storage failure matches the sentinel and writes its own wording.

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
- **THEN** it matches the domain's presign-failure sentinel, and the error surfaced to the caller carries no endpoint, bucket name, or credential

#### Scenario: No reader-returning operation remains on the port

- **WHEN** the `ResultStorage` port is inspected
- **THEN** it declares no operation returning an `io.Reader`, `io.ReadCloser`, or equivalent stream over a stored artifact, and no handler streams result bytes through `cmd/api`

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


## ADDED Requirements

### Requirement: A Presigned Result URL Grants Bounded, Single-Object, Non-Revocable Access

An issued URL SHALL grant read access to exactly one object — the `StorageKey` it was issued for — and SHALL carry a fixed expiry chosen by the system rather than by the caller. The expiry SHALL be a compile-time constant of five minutes; no environment variable SHALL configure it, matching the fixed TTL `videojob-status-cache` already uses.

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

#### Scenario: A failure on the issuance path logs the key, not a URL

- **GIVEN** issuance fails after the entitlement check passes
- **WHEN** the failure is logged
- **THEN** the log records the requested `StorageKey` and the underlying error, and contains no signed URL or signature parameter

#### Scenario: No issued URL is persisted

- **WHEN** a URL is issued for a job's result
- **THEN** nothing is written to the job's row, and the URL exists only in the response to the request that asked for it

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

## REMOVED Requirements

### Requirement: Opening An Artifact Resolves Its Existence And Size Before Returning

**Reason**: The operation this requirement constrains — opening a stored artifact and returning a reader together with its size — is removed from the `ResultStorage` port. It existed solely so `GET /download/:filename` could set `Content-Length` and fail before writing a response body while proxying bytes through `cmd/api`. That proxying is what this change replaces: the storage service now serves the object directly, sets its own `Content-Length`, and reports its own errors to the client.

**Migration**: The existence check the requirement guaranteed is preserved, not dropped: "Every Download Rejection Is Byte-Identical" now requires the handler to confirm the object exists before issuing a URL, via the port's stating operation, so a missing object is still refused at the endpoint rather than surfacing later. No caller retains a need for a reader; `GET /api/status`, the port's other consumer, already uses stating alone.
