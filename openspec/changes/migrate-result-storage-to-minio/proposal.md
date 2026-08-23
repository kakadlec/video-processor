## Why

`add-minio-infrastructure` shipped `internal/video/infrastructure/storage` — configuration, client construction, health check, bucket provisioning — but nothing in the application reads a single one of the five `VIDEO_MINIO_*` variables, and no artifact lives in a bucket. Result zips are still written to `outputs/` on the container's local filesystem, where they die with the container and cannot be shared across replicas. That local directory is also the reason ownership is tracked by `.owner` sidecar files: with no record of who produced a zip other than a second file sitting next to it, `GET /download/:filename` and `GET /api/status` had nowhere else to look. The `VideoJob` row in PostgreSQL has carried both the owning `UserID` and the result `StorageKey` since Phase 3 — the sidecars have been redundant since then, and moving the artifact out of the filesystem is the point at which they can finally go.

This is the change that turns the Phase 5 adapter from dormant plumbing into the durable home for completed results, and the first one that puts MinIO on the request-serving critical path.

## What Changes

- Add a `ResultStorage` port to `internal/video/domain` (`Put`/`Open`/`Stat`) and its MinIO-backed adapter to `internal/video/infrastructure/storage`, fulfilling the commitment `minio-infrastructure`'s spec already records: "the `StoragePort` adapter that carries artifacts into and out of the bucket is added by a later Phase 5 change, in this same package."
- `domain.FrameExtractor.ExtractFrames` returns the **local path of the zip it produced** instead of a `StorageKey`. `ProcessVideoJob` uploads that file through `ResultStorage`, mints the `StorageKey`, and removes the local copy. The `ffmpeg` adapter stops writing to `outputs/` and stops knowing that object storage exists.
- `GET /download/:filename` streams the object out of MinIO. Entitlement comes from the `VideoJob` row — the job named by the key must belong to the caller and be `completed` — not from a sidecar file.
- `GET /api/status` lists the caller's `completed` jobs from PostgreSQL and attaches each result's `size` and `created_at` from a per-object `StatObject`. The JSON response keeps its exact current shape, including `created_at` continuing to mean the artifact's last-modified time rather than the job's creation time.
- Retire the `/outputs` static mount, the `outputs/` directory, and every `.owner` sidecar call site for outputs. `createDirs` stops creating `outputs/`.
- **BREAKING**: the five `VIDEO_MINIO_*` variables become required at startup. `setupVideo` loads the config, opens the client, pings it, and ensures the bucket, failing fatally on any of the four — fail-closed, unlike Phase 4's Redis features, because a result that cannot be stored cannot be delivered. An existing deployment that sets only `IDENTITY_*`/`VIDEO_POSTGRES_DSN`/`REDIS_ADDR` stops booting until the operator supplies MinIO configuration.
- **BREAKING**: `GET /download/:filename` and `GET /api/status` no longer have an unauthenticated behavior. Both handlers currently branch on `if authenticated` and fall back to serving or listing everything; with ownership derived from the job row there is no listing without a user. The branch and the spec sentence that describes it both go.
- Add the missing assertion for the scheme-prefixed endpoint (`http://host:port`) that `minio-infrastructure`'s "Endpoint validation is the client library's, not this adapter's" scenario specifies but no test covers — recorded as a known gap when that change was finalized.

## Capabilities

### New Capabilities

- `videojob-result-storage`: the `ResultStorage` domain port and its MinIO adapter — object key derivation, upload on completion, streamed retrieval, existence/size lookup, and the fail-closed startup wiring that makes the bucket a hard dependency of `cmd/api`.

### Modified Capabilities

- `video-frame-extraction`: the zip's storage location, what "the only durable artifact" means, and the source and entitlement rules for `GET /api/status` and `GET /download/:filename`.
- `videojob-execution`: `FrameExtractor`'s return contract changes from a `StorageKey` to a local zip path; `ProcessVideoJob` gains the upload step; and the durability precondition `handleVideoUpload` must satisfy before calling `CompleteJob` stops being "recording the output artifact's ownership" and becomes "the result is in the bucket."
- `videojob-persistence`: the repository port gains `FindCompletedByUserID`, which filters by status in SQL so pagination applies to completed rows rather than to a mixed-status page.
- `rate-limiting`: its route enumeration names the `/outputs` static mount, which this change removes.
- `container-image`: the runtime stage's pre-created directories and the external-contract requirement both name `outputs/`.
- `upload-idempotency`: one scenario describes the original request's untouched artifacts as living in `uploads/`/`outputs/`.
- `minio-infrastructure`: its "MinIO Configuration Is Not Required At Application Startup" requirement is **removed** — it described a deliberately transitional state that ends here.

Three capabilities were checked and deliberately **not** modified:

- `ddd-architecture` — object keys stay flat (`frames_<jobID>.zip`), so `StorageKey`'s "set only on completion, atomically with `FrameCount`" semantics are untouched.
- `identity-authentication` — its "Authentication protects video-processing access" requirement specifies that ownership derives from the authenticated `UserID` rather than from caller-controlled fields, and says nothing about *where* that ownership is recorded. Replacing the sidecar with the job row satisfies it unchanged; its "Users cannot access another user's artifacts" scenario stays true.
- `videojob-http-api` — `GET /api/video-jobs/:id`'s `storage_key` is specified as an opaque value with no stated relationship to a filesystem location.

## Impact

**Code.** `internal/video/domain` (new port, `FrameExtractor` signature), `internal/video/application/process_video_job.go` (upload step), `internal/video/infrastructure/ffmpeg` (writes to `temp/`, returns a path), `internal/video/infrastructure/storage` (new adapter), `cmd/api/main.go` (`createDirs`, `setupRouter`, `handleDownload`/`handleStatus` move onto `videoModule`), `cmd/api/video.go` (`setupVideo` wiring, the ownership-failure block collapses).

**Tests.** `cmd/api/video_test.go` and `main_test.go` assert heavily on `outputs/` filesystem state and will need reworking against a bucket. `TestHandleVideoUpload_ReserveError_OwnershipFailureStillSkipsClear` encodes the mechanism being deleted and must be rewritten around upload failure, not silently dropped.

**Operations.** `docker-compose.yml`'s `app` service gains `VIDEO_MINIO_*` and `depends_on: minio`, both deliberately withheld by `add-minio-infrastructure` while nothing read them. `cmd/api`'s tests need a real bucket, so `app-test` and CI's `Test` step gain the variables the module reads. `docs/operations.md`'s env table flips five rows from "not read at startup yet" to required.

**Not in scope.** Uploaded videos stay in `uploads/`, and the `.owner` sidecar helpers stay with them — `recordArtifactOwner`, `artifactOwner`, `removeArtifactOwner`, `rejectOwnerSidecarRequests`, and `requireArtifactOwnership` all survive this change and are retired by `migrate-upload-storage-to-minio`. Presigned URLs are `add-presigned-download-urls`. Nothing here touches the frontend: `app.js` keeps posting to `/upload`, reading `zip_path`, and rendering `size`/`created_at` from `/api/status` unchanged.

`container-image`'s "Unchanged External Contract" requirement claims the application can start with no environment variables at all — already false since Phase 2 made `IDENTITY_*` mandatory, and further from true after this change. It is corrected here rather than left to accumulate, and this is called out so the fix is not mistaken for a silent scope expansion.
