# videojob-source-storage Specification

## Purpose
Define the `SourceStorage` domain port and its MinIO adapter: how an uploaded source video reaches the bucket without touching local disk, how its key is derived and why that key may carry a prefix when a result key may not, how `ProcessVideoJob` obtains the transient local copy `ffmpeg` needs, and the transient-lifetime contract that has every request attempt to delete its own source object. Result artifacts are `videojob-result-storage`'s concern; the two share one bucket and are separated only by key prefix.

## Requirements

### Requirement: Uploaded Source Videos Are Stored As Objects, Never On Local Disk

`POST /upload` SHALL stream the uploaded video part directly into the configured MinIO bucket through a `SourceStorage` domain port, and SHALL NOT write the uploaded content to the local filesystem at any point on the inbound path. The SHA-256 content hash that `upload-idempotency` derives SHALL be computed from the same single read pass that feeds the upload, not from a second read.

The port SHALL expose `Put`, `Get`, and `Delete`. `Put` SHALL accept an `io.Reader` rather than a local path — the source has no local existence at upload time, and requiring one would reintroduce the file this capability exists to remove. `Get` SHALL write the object to a caller-supplied local path, since its only consumer needs a file for `ffmpeg`.

This requirement constrains the **inbound** path only, observed while the request is in flight. What survives the request is specified separately by "Every Request Deletes Its Own Source Object"; the two are not in tension, because the object's existence mid-request is exactly what makes the deletion afterwards meaningful.

#### Scenario: A successful upload writes no local copy of the source

- **GIVEN** a valid video uploaded to `POST /upload`, observed **while the request is still in flight** — after the object is stored and before the handler's cleanup runs
- **WHEN** the local filesystem is inspected
- **THEN** the object is readable from the configured bucket under the request's source key, and no file containing the uploaded video's bytes exists outside `temp/`

#### Scenario: A storage failure rejects the upload

- **GIVEN** the configured bucket cannot accept the object
- **WHEN** a client uploads a valid video
- **THEN** the response reports failure, no `VideoJob` reaches `completed`, and the error message exposes neither the storage endpoint nor the bucket name

### Requirement: Source Object Keys Are Prefixed And Are Never URL Path Segments

A source object's key SHALL be `uploads/<uploadID>_<original-filename>`, where `uploadID` is generated per request and is independent of the `VideoJobID`. The `uploads/` prefix is permitted **only** because no HTTP route turns a source key into a URL path segment.

This is the deliberate opposite of `videojob-result-storage`'s flat `frames_<jobID>.zip` keys, which must contain no `/` because `cmd/api/web/app.js` uses a result key verbatim as `GET /download/:filename`'s single path segment. Any future change that exposes source objects over HTTP SHALL remove this prefix in the same change, or the route will not match.

#### Scenario: A source key carries the uploads prefix

- **WHEN** a source key is derived for an upload
- **THEN** it begins with `uploads/` and contains the request's `uploadID` and the base name of the uploaded file

#### Scenario: Source and result keys never collide

- **WHEN** an upload is processed successfully
- **THEN** the source key and the result key are distinct, occupy separate prefixes of the same bucket, and neither is derivable from the other by string manipulation alone

### Requirement: The Upload Stream Declares A Bounded Part Size

Because a `multipart.Part` does not report its length, the adapter SHALL upload with an unknown object size and SHALL explicitly configure the client's multipart part size rather than accepting the library default. With an unknown size and no configured part size, `minio-go` allocates a single part buffer of roughly 537 MiB per upload; with no limit on concurrent uploads, that constant is multiplied by every request in flight.

The configured part size SHALL be at or above the client library's minimum and small enough that concurrent uploads do not exhaust memory, while still permitting objects far larger than this service accepts.

#### Scenario: Concurrent uploads do not allocate the default part buffer

- **GIVEN** several uploads are in flight simultaneously
- **WHEN** each is streamed to the bucket
- **THEN** each allocates the explicitly configured part buffer, not the library's unknown-size default

#### Scenario: A multi-part-sized upload still stores correctly

- **WHEN** a video larger than the configured part size is uploaded
- **THEN** the stored object's content is byte-identical to what was sent, and its content hash matches the one derived during the upload

### Requirement: ProcessVideoJob Obtains A Transient Local Copy For ffmpeg

`ProcessVideoJob` SHALL download the source object to a local path under `temp/` before extraction and SHALL remove that copy before returning, on every path — success, extraction failure, and storage failure alike. The removal SHALL be registered before the extraction attempt, never after it, so a failure path that returns early cannot skip it.

The local copy SHALL be named from the job's own identifier plus a fixed suffix and SHALL carry no file extension, so no part of its path is derived from a user-supplied filename. `ffmpeg` detects input format by probing content, and every extension this system accepts identifies a container with an unambiguous signature. The path SHALL be confined under `temp/` by the same prefix check the extractor applies to its own paths.

The `FrameExtractor` port SHALL continue to receive a local file path and SHALL NOT gain any knowledge of object storage.

#### Scenario: The downloaded copy is removed after a successful extraction

- **WHEN** an upload is processed successfully
- **THEN** no file remains under `temp/` for that job after the request completes

#### Scenario: The downloaded copy is removed after a failed extraction

- **GIVEN** a video whose content `ffmpeg` cannot decode
- **WHEN** processing fails
- **THEN** the transient local copy this request downloaded no longer exists under `temp/`

#### Scenario: A download failure fails the job without invoking ffmpeg

- **GIVEN** a source key whose object is absent from the bucket
- **WHEN** `ProcessVideoJob.Execute` is called with it
- **THEN** the job ends in `failed` status with a non-empty `ErrorReason`, and `ffmpeg` is never invoked

#### Scenario: A container other than mp4 extracts without a filename extension

- **WHEN** a video in a non-mp4 supported container is uploaded and extracted from the extension-less local copy
- **THEN** extraction succeeds and reports a frame count, confirming format detection does not depend on the filename

### Requirement: Every Request Deletes Its Own Source Object

Every `POST /upload` request that stores a source object SHALL attempt to delete that object before the request completes, on every exit path: successful processing, processing failure, a duplicate-content conflict, a `CreateVideoJob` error, and any other early return after the object was written. The attempt SHALL be made from a single deferred call registered as soon as the object is stored, not from per-path cleanup calls, so no future exit path can be added without it.

The deletion SHALL be performed on a context detached from the request's own, because a canceled request context may itself be the reason processing failed. Deleting a key that is already absent SHALL NOT be treated as an error.

This is an obligation to **attempt**, deliberately not a guarantee of absence. The attempt is one call with no retry and no persisted cleanup record, so a storage failure at that instant leaves the object behind with nothing in this system to reclaim it. A failure SHALL be logged with the leaked key — so the residual set is enumerable from logs rather than invisible — and SHALL NOT fail the request, which is about the job's outcome rather than about housekeeping. No requirement in this capability asserts that no source object survives its request, because nothing here can enforce that; an expiration lifecycle rule on the `uploads/` key prefix is the recommended operator-side backstop, and a guarantee proper would need the reconciling worker Phase 6 introduces.

#### Scenario: A successful upload deletes its source object

- **WHEN** a video is uploaded and processed successfully, with storage reachable throughout
- **THEN** no object exists under that request's source key after the response is returned, and the result object is the only remaining artifact

#### Scenario: A failed upload deletes its source object

- **GIVEN** a video whose content `ffmpeg` cannot decode
- **WHEN** the request completes with `success: false`
- **THEN** no object exists under that request's source key

#### Scenario: A duplicate's source object is deleted without touching the original's

- **GIVEN** a duplicate request that stored its own source object under its own `uploadID` before discovering the conflict
- **WHEN** the handler cleans up
- **THEN** that request's own source object is deleted and the original request's artifacts are untouched

#### Scenario: A client disconnect still triggers cleanup

- **GIVEN** a request whose context is canceled after its source object was stored
- **WHEN** the handler unwinds
- **THEN** the deletion is still attempted, because the cleanup does not run on the canceled request context

#### Scenario: A cleanup failure is logged, not fatal, and not specified away

- **GIVEN** a request whose source object was stored but whose deletion fails
- **WHEN** the handler unwinds
- **THEN** the response is whatever the job's own outcome dictates — unchanged by the cleanup failure — and the failure is logged with the source key, leaving an object the application will not retry

### Requirement: The uploads Directory And The Ownership Sidecar Mechanism Are Retired

The system SHALL NOT create, serve, or read a local `uploads/` directory. `createDirs` SHALL create only `temp/`. The `/uploads` static route SHALL be removed from the router.

Every `.owner` sidecar symbol SHALL be deleted from the codebase — the write, read, and remove helpers, the suffix constant, the middleware that rejects direct sidecar requests, and the middleware that enforces per-artifact ownership. After this change no artifact class in the system uses ownership sidecars: results derive ownership from the `VideoJob` row, and source objects are never served at all.

#### Scenario: No uploads directory is created at startup

- **WHEN** `cmd/api` starts
- **THEN** it creates `temp/` only, and creates neither `uploads/` nor `outputs/`

#### Scenario: The uploads route no longer exists

- **WHEN** an authenticated client requests any path under `/uploads/`
- **THEN** the router has no such route

#### Scenario: No sidecar is written for any artifact

- **WHEN** a video is uploaded and processed
- **THEN** no `.owner` file is written anywhere, for either the source or the result

### Requirement: The Source Storage Adapter Is Tested Against A Real MinIO Instance

This capability's adapter tests SHALL exercise `Put`, `Get`, and `Delete` against a real MinIO instance rather than a mock, using the same `VIDEO_MINIO_TEST_*` variables `minio-infrastructure` established, and SHALL skip with a clear message when those variables are unset.

Every test that provisions a bucket SHALL remove that bucket and its objects when it finishes, including on failure, since the local MinIO service stores its data in a named volume.

`cmd/api`'s own tests SHALL continue to require MinIO rather than skip, as `videojob-result-storage` already specifies — with the upload path now storing to a bucket as well, a silently-skipped suite would cover even less than before.

#### Scenario: Adapter tests skip without a configured instance

- **GIVEN** the `VIDEO_MINIO_TEST_*` variables are unset
- **WHEN** this package's tests run
- **THEN** they skip with a message naming the missing configuration

#### Scenario: Deleting an absent key is not an error

- **WHEN** `Delete` is called for a key that holds no object
- **THEN** it returns no error, so the handler's deferred cleanup is safe on paths where the object was already removed

#### Scenario: A test that creates a bucket leaves nothing behind

- **WHEN** a test provisions a bucket, whether it then passes or fails
- **THEN** that bucket's objects and the bucket itself are removed before the suite ends
