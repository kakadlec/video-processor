## MODIFIED Requirements

### Requirement: Zip Packaging Of Extracted Frames

The system SHALL package all extracted frames into a single downloadable `.zip` file, containing exactly the extracted PNG frames, and SHALL store that zip as an object in the configured MinIO bucket under the job's derived storage key. The zip SHALL NOT be written to, or served from, a local `outputs/` directory.

#### Scenario: Zip contains all extracted frames

- **WHEN** frame extraction succeeds for an upload
- **THEN** the response includes a `zip_path`, and downloading that path via `GET /download/:filename` returns a zip archive whose entry count matches the reported `frame_count`

#### Scenario: The zip is stored in the bucket, not on local disk

- **WHEN** frame extraction succeeds for an upload
- **THEN** the zip exists as an object in the configured bucket, and no zip file remains anywhere on the local filesystem after the request completes

### Requirement: Original Upload Cleanup After Successful Processing

On successful processing, the system SHALL delete the original uploaded video file, leaving the zip object in the configured MinIO bucket as the only durable artifact.

#### Scenario: Uploaded file removed after success

- **WHEN** a video upload is processed successfully
- **THEN** the temporary uploaded file under `uploads/` no longer exists after the request completes

### Requirement: Processed Files Listing

The system SHALL expose `GET /api/status`, returning the authenticated caller's `completed` jobs' results with filename, size, creation time, and a download URL for each. The listing SHALL be derived from the caller's own `VideoJob` records — never from an enumeration of stored objects — with size and creation time read from each stored object. A result whose object cannot be read SHALL be omitted rather than shown.

The endpoint has no unauthenticated behavior: it is reachable only behind bearer authentication, and there is no listing that is not scoped to a single user.

#### Scenario: Newly created zip appears in status listing

- **WHEN** an upload has been successfully processed
- **THEN** `GET /api/status` includes an entry whose `filename` matches the returned `zip_path`

#### Scenario: Listing is scoped to the authenticated owner

- **GIVEN** completed jobs produced by two different authenticated users
- **WHEN** one of them requests `GET /api/status` with a valid bearer token
- **THEN** the response lists only that caller's own results, and `total` counts only those

### Requirement: Processed File Download

The system SHALL expose `GET /download/:filename`, streaming the stored object when the authenticated caller owns the `completed` `VideoJob` that key belongs to, and responding `HTTP 404` with a generic `File not found` error body otherwise. Entitlement SHALL be determined from the `VideoJob` record, not from an ownership record stored alongside the artifact. The not-found and not-owned responses SHALL be indistinguishable, so a caller cannot use them to probe for artifacts belonging to someone else.

The endpoint has no unauthenticated behavior: it is reachable only behind bearer authentication, and there is no entitlement rule that applies in the absence of an identity.

#### Scenario: Existing zip is downloadable by its owner

- **WHEN** an authenticated client requests `GET /download/:filename` for the storage key of a `completed` job it owns, whose object is present
- **THEN** the server responds `HTTP 200` with the zip's binary content

#### Scenario: Nonexistent file returns 404

- **WHEN** a client requests `GET /download/:filename` for a key that belongs to no job, or whose object is not in the bucket
- **THEN** the server responds `HTTP 404`

#### Scenario: Another user's zip is indistinguishable from a missing one

- **GIVEN** a `completed` job owned by user A
- **WHEN** user B requests its storage key with a valid bearer token
- **THEN** the server responds `HTTP 404` with the same body it returns for a key that belongs to no job
