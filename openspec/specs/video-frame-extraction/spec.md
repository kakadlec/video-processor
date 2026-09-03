# video-frame-extraction Specification

## Purpose
Define the video-processing surface as it behaves today: which uploads are accepted, how frames are extracted and packaged, which artifacts survive a job, and how processed results are listed and downloaded. Extraction now happens in `cmd/worker` rather than inside the submitting request, so the cleanup obligations here are stated against a job's terminal state rather than against a response. Access control over those results is specified in `identity-authentication` and `video-processing-access`; this spec covers the processing behavior itself and references ownership only where it changes a response.
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
- **WHEN** a valid video of approximately N seconds is uploaded and its asynchronous job completes
- **THEN** the completed job reports `frame_count` approximately equal to N (one frame per second of source video)

### Requirement: Zip Packaging Of Extracted Frames

The system SHALL package all extracted frames into a single downloadable `.zip` file, containing exactly the extracted PNG frames, and SHALL store that zip as an object in the configured MinIO bucket under the job's derived storage key. The zip SHALL NOT be written to, or served from, a local `outputs/` directory.

#### Scenario: Zip contains all extracted frames

- **WHEN** frame extraction succeeds for an upload and its asynchronous job completes
- **THEN** the completed job exposes a result path, and following the URL `GET /download/:filename` issues for that path yields a zip archive whose entry count matches the job's `frame_count`

#### Scenario: The zip is stored in the bucket, not on local disk

- **WHEN** frame extraction succeeds for an upload
- **THEN** the zip exists as an object in the configured bucket, and the transient zip the worker produced under `temp/` no longer exists after the processing sequence completes

### Requirement: Original Upload Cleanup Is Attempted After Successful Processing

On successful processing, the worker SHALL make one best-effort attempt to delete the stored source object. A successful deletion leaves the zip object as the only durable job artifact, but a deletion error or interruption after the terminal commit MAY leave source residue for the `uploads/` lifecycle rule to reclaim.

The source has not lived on the local filesystem since Phase 5: it is an object under the `uploads/` key prefix of the same bucket (see `videojob-source-storage`). Since the asynchronous cutover the cleanup no longer happens inside the submitting request at all — `cmd/worker` attempts to delete the object after it applies the job's terminal state, and the transient local copy it downloaded for `ffmpeg` is removed from **the worker's** `temp/` directory as part of the same sequence. The submitting API's filesystem is not involved in processing and holds no copy.

"After successful processing" therefore means after the job reaches `completed`, which is observable through the job's status rather than through the upload response. An observer SHALL NOT expect the source object to be gone by the time the submission is acknowledged.

#### Scenario: Successful cleanup removes the uploaded object

- **GIVEN** a video upload that is processed successfully and whose source deletion succeeds
- **WHEN** the worker's post-commit cleanup finishes
- **THEN** no object exists under that job's source key, and no transient local copy remains in the worker's `temp/` directory

#### Scenario: Cleanup failure leaves lifecycle-managed residue

- **GIVEN** a video upload whose job commits `completed`
- **WHEN** source deletion fails or the worker stops between commit and deletion
- **THEN** the job remains completed, no transient local copy survives the processing sequence, and the source object may remain until the `uploads/` lifecycle rule expires it

#### Scenario: The source object still exists while the job is queued

- **GIVEN** a submitted video whose job is `queued` and not yet claimed
- **WHEN** the bucket is inspected
- **THEN** the source object is present, because the component that will delete it has not processed the job yet

### Requirement: Processed Files Listing

The system SHALL expose `GET /api/status`, returning the authenticated caller's `completed` jobs' results with filename, size, creation time, and a download URL for each. The listing SHALL be derived from the caller's own `VideoJob` records — never from an enumeration of stored objects — with size and creation time read from each stored object. A result whose object cannot be read SHALL be omitted rather than shown.

The `download_url` each entry carries SHALL address `GET /download/:filename` on this API, not the storage service. The listing SHALL NOT contain a presigned URL: minting one per entry would start every expiry at listing time and place one credential per completed job into a single response body. The listing names *where to ask*; the asking is what issues a credential.

The endpoint has no unauthenticated behavior: it is reachable only behind bearer authentication, and there is no listing that is not scoped to a single user.

#### Scenario: Newly created zip appears in status listing

- **WHEN** an upload's asynchronous job has completed successfully
- **THEN** `GET /api/status` includes an entry whose `filename` matches that job's result path

#### Scenario: Listing is scoped to the authenticated owner

- **GIVEN** completed jobs produced by two different authenticated users
- **WHEN** one of them requests `GET /api/status` with a valid bearer token
- **THEN** the response lists only that caller's own results, and `total` counts only those

#### Scenario: The listing carries no credential

- **WHEN** an authenticated caller requests `GET /api/status`
- **THEN** every entry's `download_url` addresses this API, and no entry carries a signed URL, signature parameter, or expiry

### Requirement: Processed File Download

The system SHALL expose `GET /download/:filename`, issuing a bounded-lifetime URL that grants access to the stored object when the authenticated caller owns the `completed` `VideoJob` that key belongs to, and responding `HTTP 404` with a generic `File not found` error body otherwise. The endpoint SHALL NOT return the object's bytes itself; the transfer occurs between the client and the storage service, with this API absent from the data path.

Entitlement SHALL be determined from the `VideoJob` record, not from an ownership record stored alongside the artifact. Because an issued URL carries no identity, that determination SHALL be made at issuance and SHALL be the complete authorization decision. The not-found and not-owned responses SHALL be indistinguishable, so a caller cannot use them to probe for artifacts belonging to someone else.

The endpoint has no unauthenticated behavior: it is reachable only behind bearer authentication, and there is no entitlement rule that applies in the absence of an identity.

#### Scenario: Existing zip is downloadable by its owner

- **WHEN** an authenticated client requests `GET /download/:filename` for the storage key of a `completed` job it owns, whose object is present
- **THEN** the server responds `HTTP 200` with a URL for that object and the instant it stops being accepted, and following that URL yields the zip's binary content

#### Scenario: Nonexistent file returns 404

- **WHEN** a client requests `GET /download/:filename` for a key that belongs to no job, or whose object is not in the bucket
- **THEN** the server responds `HTTP 404` and issues no URL

#### Scenario: Another user's zip is indistinguishable from a missing one

- **GIVEN** a `completed` job owned by user A
- **WHEN** user B requests its storage key with a valid bearer token
- **THEN** the server responds `HTTP 404` with the same body it returns for a key that belongs to no job

#### Scenario: The API is not in the transfer path

- **GIVEN** an authenticated owner who has obtained a URL for their own completed result
- **WHEN** the object is transferred
- **THEN** the bytes travel from the storage service to the client, and no response from this API carries them

### Requirement: Temporary Frame Directory Cleanup
The worker SHALL remove each job's temporary frame-extraction directory under `temp/` after its processing sequence completes, whether frame extraction succeeds or fails.

#### Scenario: Temp directory removed after failed extraction
- **WHEN** a video upload with valid extension but content `ffmpeg` cannot decode is processed
- **THEN** no leftover directory for that job remains under the worker's `temp/` after the processing sequence completes

#### Scenario: Temp directory removed after successful extraction
- **WHEN** a video upload is processed successfully
- **THEN** no leftover directory for that job remains under the worker's `temp/` after the processing sequence completes

### Requirement: Source Object Deletion Is Attempted On Processing Failure

When frame extraction or result storage fails and this run applies the job's `failed` outcome, the worker SHALL make one best-effort attempt to delete the stored source object rather than intentionally retaining it. The local copy SHALL still be removed, while a source deletion error MAY leave bucket residue for the `uploads/` lifecycle rule.

The deletion is an obligation to attempt, not a guarantee of absence — `videojob-source-storage` owns the full semantics, including which component is obliged to attempt it and what happens when the attempt itself fails. The point of this requirement is the reversal of intent: failure is no longer a reason to keep the source.

This inverts the behavior the removed "Uploaded File Retained On Processing Failure" requirement documented. Retention was a known leak, tolerable only because a local file is reclaimed when its container is replaced; the same leak in object storage would be durable and unbounded, with nothing in this system to reap it.

Two consequences, both accepted deliberately. The system does not retry a failed job from the original bytes; a retry is a fresh submission, which the worker attempts to unblock promptly by clearing the failed job's idempotency key. And a job that fails *before* any component claims it — one whose dispatch was never delivered — is not covered by this requirement at all: nothing processes it, so nothing deletes its source, and the object-storage lifecycle rule is what reclaims it.

#### Scenario: Successful cleanup removes the source after extraction failure

- **GIVEN** source deletion succeeds
- **WHEN** a submitted video with a valid extension but content `ffmpeg` cannot decode is processed and its failure write applies
- **THEN** the job's status is `failed` with a recorded reason, and no object exists under its source key

#### Scenario: Successful cleanup removes the source after result-storage failure

- **GIVEN** frames were extracted successfully, the result zip cannot be stored, and source deletion succeeds
- **WHEN** the failure write applies and post-commit cleanup finishes
- **THEN** the job is `failed` and no object exists under its source key

#### Scenario: A job that was never claimed is not covered

- **GIVEN** a job whose dispatch was never delivered to any consumer
- **WHEN** the bucket is inspected
- **THEN** its source object is still present, and only the storage lifecycle rule will reclaim it
