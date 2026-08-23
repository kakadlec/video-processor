# video-frame-extraction Specification

## Purpose
Define the video-processing HTTP surface as it behaves today: which uploads are accepted, how frames are extracted and packaged, which artifacts survive a request, and how processed results are listed and downloaded. Access control over those results is specified in `identity-authentication` and `video-processing-access`; this spec covers the processing behavior itself and references ownership only where it changes a response.
## Requirements
### Requirement: Video Upload Validation
The system SHALL accept an uploaded video file only when its filename extension is one of: `.mp4`, `.avi`, `.mov`, `.mkv`, `.wmv`, `.flv`, `.webm`. Requests with an unsupported extension or missing the `video` form field SHALL be rejected with HTTP 400 and a Portuguese-language error message, without invoking `ffmpeg`.

#### Scenario: Supported video format is accepted
- **WHEN** a client uploads a file with an accepted extension (e.g. `.mp4`) to `POST /upload`
- **THEN** the server accepts the file and proceeds to frame extraction

#### Scenario: Unsupported format is rejected
- **WHEN** a client uploads a file with an unsupported extension (e.g. `.txt`)
- **THEN** the server responds `HTTP 400` with `success: false` and a Portuguese message about the unsupported format, and does not create any output zip

#### Scenario: Missing file field is rejected
- **WHEN** a client `POST`s to `/upload` without a `video` form field
- **THEN** the server responds `HTTP 400` with `success: false`

### Requirement: Frame Extraction At One Frame Per Second
For an accepted video upload, the system SHALL invoke `ffmpeg` to extract one frame per second of video as PNG images.

#### Scenario: N-second video yields N frames
- **WHEN** a valid video of approximately N seconds is uploaded
- **THEN** the response reports `frame_count` approximately equal to N (one frame per second of source video)

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

### Requirement: Temporary Frame Directory Cleanup
The system SHALL remove the per-request temporary frame-extraction directory under `temp/` after a request completes, whether frame extraction succeeds or fails.

#### Scenario: Temp directory removed after failed extraction
- **WHEN** a video upload with a valid extension but content `ffmpeg` cannot decode is processed
- **THEN** no leftover per-request directory remains under `temp/` after the request completes

#### Scenario: Temp directory removed after successful extraction
- **WHEN** a video upload is processed successfully
- **THEN** no leftover per-request directory remains under `temp/` after the request completes

### Requirement: Uploaded File Retained On Processing Failure
When frame extraction fails, the system SHALL leave the originally uploaded file in place under `uploads/` rather than deleting it. This is current, existing behavior — tracked here as a known cleanup gap ahead of the planned refactor, not fixed by this change.

#### Scenario: Failed processing leaves the upload behind
- **WHEN** a video upload with a valid extension but content `ffmpeg` cannot decode is processed
- **THEN** the response reports `success: false`, and the corresponding file still exists under `uploads/` after the request completes

