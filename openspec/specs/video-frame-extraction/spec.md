# video-frame-extraction Specification

## Purpose
TBD - created by archiving change add-integration-tests. Update Purpose after archive.
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
The system SHALL package all extracted frames into a single downloadable `.zip` file stored under `outputs/`, containing exactly the extracted PNG frames.

#### Scenario: Zip contains all extracted frames
- **WHEN** frame extraction succeeds for an upload
- **THEN** the response includes a `zip_path`, and downloading that path via `GET /download/:filename` returns a zip archive whose entry count matches the reported `frame_count`

### Requirement: Original Upload Cleanup After Successful Processing
On successful processing, the system SHALL delete the original uploaded video file, leaving the zip in `outputs/` as the only durable artifact.

#### Scenario: Uploaded file removed after success
- **WHEN** a video upload is processed successfully
- **THEN** the temporary uploaded file under `uploads/` no longer exists after the request completes

### Requirement: Processed Files Listing
The system SHALL expose `GET /api/status`, returning the list of zip files currently present in `outputs/`, including filename, size, creation time, and a download URL for each.

#### Scenario: Newly created zip appears in status listing
- **WHEN** an upload has been successfully processed
- **THEN** `GET /api/status` includes an entry whose `filename` matches the returned `zip_path`

### Requirement: Processed File Download
The system SHALL expose `GET /download/:filename`, serving the matching file from `outputs/` when it exists, and responding `HTTP 404` with a Portuguese error message when it does not.

#### Scenario: Existing zip is downloadable
- **WHEN** a client requests `GET /download/:filename` for a zip that exists in `outputs/`
- **THEN** the server responds `HTTP 200` with the zip's binary content

#### Scenario: Nonexistent file returns 404
- **WHEN** a client requests `GET /download/:filename` for a file that does not exist in `outputs/`
- **THEN** the server responds `HTTP 404`

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

