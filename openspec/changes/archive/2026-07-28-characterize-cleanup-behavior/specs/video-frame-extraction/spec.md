## ADDED Requirements

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
