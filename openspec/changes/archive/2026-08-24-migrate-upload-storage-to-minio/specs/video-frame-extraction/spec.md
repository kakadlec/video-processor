## MODIFIED Requirements

### Requirement: Original Upload Cleanup After Successful Processing

On successful processing, the system SHALL delete the stored source object, leaving the zip object in the configured MinIO bucket as the only durable artifact.

The source no longer lives on the local filesystem: it is an object under the `uploads/` key prefix of the same bucket (see `videojob-source-storage`), and the transient local copy `ProcessVideoJob` downloads for `ffmpeg` is removed under `temp/` as part of the same sequence.

#### Scenario: Uploaded file removed after success

- **WHEN** a video upload is processed successfully
- **THEN** no object exists under that request's source key, and the transient local copy under `temp/` no longer exists, after the request completes

## ADDED Requirements

### Requirement: Source Object Removed On Processing Failure

When frame extraction or result storage fails, the system SHALL delete the stored source object rather than retaining it. With storage reachable, a failed request leaves behind neither a source object in the bucket nor a local copy under `temp/`.

The deletion is an obligation to attempt, not a guarantee of absence — `videojob-source-storage`'s "Every Request Deletes Its Own Source Object" owns the full semantics, including what happens when the attempt itself fails. The point of this requirement is the reversal of intent: failure is no longer a reason to keep the source.

This inverts the behavior the removed "Uploaded File Retained On Processing Failure" requirement documented. Retention was a known leak, tolerable only because a local file is reclaimed when its container is replaced; the same leak in object storage would be durable and unbounded, with nothing in this system to reap it.

A consequence, accepted deliberately: a failed job cannot be retried from the original bytes, because they are gone. A retry is a fresh upload. `upload-idempotency` already clears a failed job's key immediately so such a retry is not blocked.

#### Scenario: Failed processing leaves no source object

- **WHEN** a video upload with a valid extension but content `ffmpeg` cannot decode is processed
- **THEN** the response reports `success: false`, and no object exists under that request's source key after the request completes

#### Scenario: Failed result storage also removes the source

- **GIVEN** frames were extracted successfully but the result zip cannot be stored
- **WHEN** the request completes
- **THEN** the job is `failed` and no object exists under that request's source key

## REMOVED Requirements

### Requirement: Uploaded File Retained On Processing Failure

**Reason**: The requirement existed to document a known cleanup gap — a failed run left its upload in `uploads/` indefinitely — and said so explicitly, describing itself as tracked "ahead of the planned refactor, not fixed by this change". This change is that refactor. Uploads no longer live on the local filesystem at all, and the source object is deleted on failure as well as on success, so there is no retained artifact left to specify.

**Migration**: The opposite behavior is specified by this capability's new "Source Object Removed On Processing Failure" requirement, and by `videojob-source-storage`'s "No Source Object Outlives Its Request", which enumerates every exit path the deletion must cover. `main_test.go`'s `TestProcessing_Failure_LeavesUploadedFileBehind` asserts the removed behavior and is inverted rather than deleted, so the reversal is pinned by a test instead of silently uncovered.

Operationally, an operator who relied on inspecting a failed upload on the host filesystem loses that. The job's persisted `ErrorReason` and the server's logs remain, and `ProcessVideoJob` already logs storage failures with the underlying error.
