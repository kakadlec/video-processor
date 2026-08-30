## MODIFIED Requirements

### Requirement: Original Upload Cleanup After Successful Processing

On successful processing, the system SHALL delete the stored source object, leaving the zip object in the configured MinIO bucket as the only durable artifact.

The source has not lived on the local filesystem since Phase 5: it is an object under the `uploads/` key prefix of the same bucket (see `videojob-source-storage`). Since the asynchronous cutover the cleanup no longer happens inside the submitting request at all — the object is deleted by `cmd/worker` once the job it processed reaches a terminal state, and the transient local copy it downloaded for `ffmpeg` is removed from **the worker's** `temp/` directory as part of the same sequence. The submitting API's filesystem is not involved in processing and holds no copy.

"After successful processing" therefore means after the job reaches `completed`, which is observable through the job's status rather than through the upload response. An observer SHALL NOT expect the source object to be gone by the time the submission is acknowledged.

#### Scenario: Uploaded file removed after success

- **WHEN** a video upload is processed successfully
- **THEN** no object exists under that job's source key, and no local copy of it remains under any `temp/` directory, once the job's status is `completed`

#### Scenario: The source object still exists while the job is queued

- **GIVEN** a submitted video whose job is `queued` and not yet claimed
- **WHEN** the bucket is inspected
- **THEN** the source object is present, because the component that will delete it has not processed the job yet

### Requirement: Source Object Removed On Processing Failure

When frame extraction or result storage fails, the system SHALL delete the stored source object rather than retaining it. With storage reachable, a failed job leaves behind neither a source object in the bucket nor a local copy under any `temp/` directory.

The deletion is an obligation to attempt, not a guarantee of absence — `videojob-source-storage` owns the full semantics, including which component is obliged to attempt it and what happens when the attempt itself fails. The point of this requirement is the reversal of intent: failure is no longer a reason to keep the source.

This inverts the behavior the removed "Uploaded File Retained On Processing Failure" requirement documented. Retention was a known leak, tolerable only because a local file is reclaimed when its container is replaced; the same leak in object storage would be durable and unbounded, with nothing in this system to reap it.

Two consequences, both accepted deliberately. A failed job cannot be retried from the original bytes, because they are gone; a retry is a fresh submission, which `upload-idempotency` keeps unblocked by clearing a failed job's key immediately. And a job that fails *before* any component claims it — one whose dispatch was never delivered — is not covered by this requirement at all: nothing processes it, so nothing deletes its source, and the object-storage lifecycle rule is what reclaims it.

#### Scenario: Failed processing leaves no source object

- **WHEN** a submitted video with a valid extension but content `ffmpeg` cannot decode is processed
- **THEN** the job's status is `failed` with a recorded reason, and no object exists under its source key

#### Scenario: Failed result storage also removes the source

- **GIVEN** frames were extracted successfully but the result zip cannot be stored
- **WHEN** the job reaches its terminal state
- **THEN** the job is `failed` and no object exists under its source key

#### Scenario: A job that was never claimed is not covered

- **GIVEN** a job whose dispatch was never delivered to any consumer
- **WHEN** the bucket is inspected
- **THEN** its source object is still present, and only the storage lifecycle rule will reclaim it
