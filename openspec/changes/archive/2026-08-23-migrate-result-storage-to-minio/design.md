## Context

Today a completed job's zip lives at `outputs/frames_<jobID>.zip`, written by `internal/video/infrastructure/ffmpeg`'s `Extractor` and read back by three separate paths in `cmd/api`: the `/outputs` static mount, `handleDownload`, and `handleStatus`. All three answer "may this caller have this file?" by reading a `<filename>.owner` sidecar written next to the artifact. That mechanism exists because a bare directory carries no ownership metadata; it predates `internal/video/domain.VideoJob` carrying both `UserID` and `StorageKey`.

`add-minio-infrastructure` (archived 2026-08-23) supplied `Config`/`LoadConfigFromEnv`, `Open`, `Ping`, and `EnsureBucket` in `internal/video/infrastructure/storage`, verified against `minio/minio:RELEASE.2025-04-22T22-12-26Z` and `minio-go v7.3.0`, but wired none of it into `cmd/api`. Its spec explicitly defers the artifact-carrying adapter to "a later Phase 5 change, in this same package" — this one.

Two constraints shape everything below, and both were established empirically rather than assumed:

- **`cmd/api/web/app.js` consumes the storage key verbatim.** Line 151 puts `result.zip_path` straight into the download button's `data-download-filename`, and line 83 fetches `'/download/' + encodeURIComponent(filename)`. Any `/` inside the key encodes to `%2F`, which Go decodes into `URL.Path`, which stops Gin's single-segment `:filename` parameter from matching.
- **`app.js` renders `file.size` through `formatFileSize` and `file.created_at` as text** (line 204). Dropping either field from `GET /api/status` renders "NaN undefined" in the UI.

## Goals / Non-Goals

**Goals:**

- The frames zip is durable in MinIO rather than on the container's local disk, so a completed result survives the container and is reachable from any replica.
- Artifact ownership derives from the `VideoJob` row, and the `outputs/` sidecar mechanism is deleted rather than reimplemented against object storage.
- `POST /upload`, `GET /download/:filename`, and `GET /api/status` keep their exact request and response shapes, so the frontend needs no change.
- MinIO is a hard, fail-closed dependency of `cmd/api`, established at startup rather than discovered on the first upload.

**Non-Goals:**

- Uploaded source videos. They stay in `uploads/`, and the five `.owner` helpers stay with them, until `migrate-upload-storage-to-minio`.
- Presigned URLs. Downloads stay proxied through the API; `add-presigned-download-urls` changes that.
- Backfilling zips that already exist in a running deployment's `outputs/` directory (see Migration Plan).
- Any change to the `VideoJob` aggregate's fields or invariants.

## Decisions

### 1. The application layer uploads; the `ffmpeg` adapter returns a local path

`domain.FrameExtractor.ExtractFrames` changes its first return value from `StorageKey` to a local zip path. The `Extractor` writes frames to `temp/<jobID>/` exactly as today, writes the zip to `temp/<jobID>.zip`, and returns that path. `ProcessVideoJob` gains a `domain.ResultStorage` dependency, uploads the file, mints the `StorageKey`, and removes the local copy.

The port already accepts `videoPath string` — a local filesystem path — as an input. Returning one is symmetric with that, not a new abstraction leak.

*Alternatives considered.* **The `Extractor` uploads directly**, which is the smallest diff and lets the existing `defer os.RemoveAll(tempDir)` clean the zip up for free — rejected because a package named `ffmpeg`, whose stated job is shelling out to a transcoder, would then also hold MinIO credentials and perform object I/O. **A `storage`-owned decorator implementing `FrameExtractor` around the `ffmpeg` one**, mirroring `cache.CachedVideoJobRepository` — rejected because the decorator still needs the inner adapter to hand it a path, so it buys the same signature change plus an extra indirection. **Returning an `io.ReadCloser` instead of a path**, so no filesystem detail crosses the boundary — rejected as the most machinery for the least gain: cleanup would have to ride on a custom `Close`, and `PutObject` wants a known size that a bare reader does not carry.

*Consequence worth stating:* `ProcessVideoJob` now calls `os.Remove` on a path a port handed it. That is the application layer touching the filesystem, which is unusual here. It is deliberate — the use case owns the file's lifetime because it is the only component that knows when the upload has succeeded — and it is the one line in this design a reviewer should expect to question.

`videojob-execution`'s "Temporary extraction directory is always removed" scenario stays true unmodified: `temp/<jobID>/` is still removed by the same `defer`. The zip at `temp/<jobID>.zip` is a *new* transient file with its own cleanup owner, and it needs its own scenario rather than an amendment to that one.

### 2. Object keys stay flat: `frames_<jobID>.zip`

The key is byte-identical to today's filename in `outputs/`, so `ProcessingResult.zip_path` and every `/download/:filename` URL are unchanged.

*Alternative considered and rejected on evidence:* **per-user key prefixes** (`users/<userID>/frames_<jobID>.zip`). This was attractive for two real reasons — ownership would be structural (a handler can only ever address `users/<authenticatedUserID>/…`, so cross-user access is unrepresentable rather than merely checked), and `GET /api/status` would collapse to a single prefixed `ListObjects` returning name, size, and last-modified together. It is nonetheless impossible without a frontend change, per the `%2F` finding in Context. Since `add-presigned-download-urls` will revisit the download path anyway, a later change may reopen this; it must reopen `app.js` with it.

### 3. Download resolves the job by parsing the key, then re-verifies against the row

`GET /download/:filename` extracts the `VideoJobID` from the `frames_<uuid>.zip` shape, calls `FindByID`, and serves the object only when **all three** hold: the job's `UserID` equals the authenticated user, the job's status is `completed`, and the job's own `StorageKey` equals the requested key. The third check is what makes parsing safe — a caller who forges a key for someone else's job is rejected by the first check, and a caller who forges a key that no job claims is rejected by the third.

*Alternative considered.* A `FindByStorageKey` repository method. Rejected on two counts: it is a third lookup path to implement across the domain port, the PostgreSQL adapter, and the cache decorator, and it would bypass `CachedVideoJobRepository`'s `FindByID` cache, which `add-videojob-status-cache` built precisely for repeated lookups of a single job.

Key derivation and parsing both live in `internal/video/domain` as functions over `VideoJobID`/`StorageKey`, not in the storage adapter — the convention defines what a job's result *is*, and it must be identical on the write and read sides.

### 4. `GET /api/status` reads ownership from PostgreSQL and size/time from MinIO

The listing is driven by the caller's `completed` jobs. For each one, a `StatObject` supplies `size` and `created_at`.

Taking `created_at` from the object's `LastModified` — not from `VideoJob.CreatedAt()` — deliberately preserves today's semantics, where the field is the zip's filesystem `ModTime`. The job row's creation time would be a quieter but genuine behavior change (it precedes the artifact by however long `ffmpeg` ran), and there is no reason to accept one when `StatObject` returns the right value in the same call that is already needed for `size`.

A `StatObject` that fails or reports a missing object causes the entry to be **omitted**, not to fail the request — mirroring `handleStatus`'s current `os.Stat` error handling, which `continue`s past unreadable files.

*Alternatives considered.* **Storing the size on the `VideoJob` aggregate**, avoiding MinIO on this path entirely — rejected because it drags in a schema migration, a `RestoreVideoJob` invariant, and a `videojob-persistence` change to avoid one round trip per listed item. **One unprefixed `ListObjects` over the whole bucket**, filtered against the caller's key set — one round trip instead of N, but it scans every user's objects, so its cost grows with total system usage rather than with the caller's own result count. The per-item `StatObject` scales with the caller's completed jobs and maps one-to-one onto the `os.Stat` loop it replaces.

### 5. A dedicated, unpaginated repository method and use case for completed jobs

`domain.VideoJobRepository` gains `FindCompletedByUserID(ctx, userID)` — no offset, no limit — implemented in the PostgreSQL adapter and passed straight through by the cache decorator (as `FindByUserID` already is). A new `ListUserResults` application use case composes it with `ResultStorage.Stat`.

*Alternative considered and rejected:* reusing `FindByUserID` and filtering `completed` in the use case. Pagination is applied by the database before the filter, so a page of 20 jobs that happen to be `pending`/`failed` yields an empty listing while completed results exist further down — a bug that only appears for users with many failures.

*The pagination question itself.* `GET /api/status` accepts no pagination parameters, and its filesystem predecessor globbed `outputs/*.zip` with no bound at all. Giving the new query a limit — even a generous one — would silently make a heavy user's older results unreachable through the only listing endpoint `app.js` consumes, which is a regression dressed up as a refinement. So the method is unpaginated, and filtering on status in SQL is what makes that defensible: the rows it returns are exactly the rows the endpoint renders, with nothing fetched to be discarded. Real pagination is worth having, but it needs a matching frontend change and therefore belongs to a change that touches `app.js` — plausibly `add-presigned-download-urls`, which reopens this path anyway.

This is a genuine widening of the persistence port and is declared as a `videojob-persistence` delta rather than smuggled in as an implementation detail.

### 6. `ResultStorage.Open` returns the size alongside the reader

```
Put(ctx, key StorageKey, localPath string) error
Open(ctx, key StorageKey) (io.ReadCloser, int64, error)
Stat(ctx, key StorageKey) (size int64, modifiedAt time.Time, err error)
```

`minio-go`'s `GetObject` returns an `*Object` that is documented as a lazy HTTP stream, so a missing key does not necessarily surface from `GetObject` itself. The adapter therefore calls the returned object's `Stat()` before handing anything back, which both resolves the error eagerly and yields the `Content-Length` the download handler needs. This makes the design correct whether or not `GetObject` itself would have reported the error, and the implementation task verifies the actual behavior against the pinned image rather than trusting the doc comment.

`Put` takes a local path so the adapter can use `FPutObject`, which sets the content length from the file rather than buffering an unsized reader.

A missing object is reported as a single sentinel (`ErrResultNotFound`) so the HTTP layer can map it without matching on `minio.ToErrorResponse` codes outside the adapter.

### 7. Startup is fail-closed

`setupVideo` performs `LoadConfigFromEnv` → `Open` → `Ping` → `EnsureBucket`, and any failure is fatal, matching how `VIDEO_POSTGRES_DSN` is already treated.

This is the opposite of every Phase 4 Redis feature, all of which fail open, and the difference is deliberate: rate limiting, idempotency, and the status cache each degrade to a slower-but-correct system when Redis is unavailable, whereas a bucket that cannot be written means a completed job has nowhere to put its result. There is nothing to degrade to. Recording this explicitly matters because the three most recent changes in this repository all established the opposite posture, and pattern-matching would get it wrong.

### 8. The unauthenticated branches are deleted, not preserved

`handleDownload` and `handleStatus` both currently wrap their ownership check in `if userID, authenticated := authenticatedUserID(c); authenticated`, falling back to serving or listing everything. Both routes have been behind `requireBearerAuth()` since Phase 2, so the fallback is already unreachable; with ownership sourced from a per-user query it is not merely unreachable but unrepresentable. The branches go, and `video-frame-extraction`'s "When no identity is authenticated, every zip in `outputs/` is listed" sentence goes with them.

### 9. Not-found and not-owned stay indistinguishable

Every rejection in `handleDownload` — unparseable key, no such job, wrong owner, job not `completed`, `ErrResultNotFound` from the adapter — returns `HTTP 404` with the same `{"error": "File not found"}` body the handler returns today. No MinIO error text reaches the response body; adapter errors are logged, not rendered.

### 10. Orphan objects are an accepted, pre-existing failure class

If the upload succeeds and `CompleteJob` then fails, the bucket holds an object whose job is stuck in `processing` and which no listing will show. This is the same class of orphan the current code already produces when a zip is written and the subsequent ownership recording fails, so it is not a new failure mode and no reaper is introduced here.

One thing this decision explicitly does *not* preserve: the handler's post-processing failure branch. `ProcessVideoJob` still leaves a successfully-extracted job in `processing` and the handler still calls `CompleteJob`, but the handler no longer has any fallible step of its own between the two — storing the result moved inside `ProcessVideoJob` (Decision 1), and a successful return from it already means the result is durable. The `FailJob` call that used to guard ownership recording is deleted rather than repointed at something else. The split between `ProcessVideoJob` and `CompleteJob` survives only because collapsing it is a separate refactor across this capability's callers and tests, not because a failure branch still needs it.

### 11. The implementation commit carries a breaking-change marker

`feat!:`. Making MinIO configuration a startup precondition stops an existing deployment from booting, which is a breaking operational change regardless of the API surface staying stable.

The nearest precedent, `enforce-mandatory-identity-config`, shipped as `fix:` (`c77f136`) and therefore released as a patch despite making `IDENTITY_*` mandatory. That is treated here as a mistake not to repeat, not as a convention to follow.

### 12. `container-image`'s stale contract sentence is corrected here

That spec's "Unchanged External Contract" requirement asserts the application can start with no environment variables. That has been false since Phase 2 and would be further from true after this change. Correcting it alongside the `outputs/` removals it already needs is cheaper than leaving a known-false normative statement in a canonical spec, and the proposal names it explicitly so it is not read as silent scope creep.

## Risks / Trade-offs

- **MinIO joins the critical path; an outage now takes uploads and downloads down.** → Accepted and deliberate (Decision 7). Mitigated by proving connectivity at startup instead of at first request, so a misconfiguration fails loudly at deploy time rather than as a 500 for the first user.
- **Every request that lists results now performs N network round trips where it performed N `os.Stat` calls, and N is unbounded** — the listing is deliberately unpaginated (Decision 5), so a user with hundreds of completed jobs pays hundreds of `StatObject` calls per poll, and `app.js` polls this endpoint. → Accepted as the lesser evil: the alternative is a limit that silently hides a user's older results, and the current filesystem implementation is unbounded in exactly the same way. The cost scales with one caller's own history, not with total bucket contents. This is the strongest argument for `add-presigned-download-urls` to revisit the path — a single prefixed `ListObjects` plus real pagination would fix both halves at once, but needs the frontend change this scope excludes.
- **The large `cmd/api` test files assert filesystem state that will no longer exist.** → Reworked against a real bucket, which CI and `docker compose` already provide. The specific risk is deleting rather than porting a test: `TestHandleVideoUpload_ReserveError_OwnershipFailureStillSkipsClear` covers a real invariant (a failed finalization step must not clear another request's idempotency reservation) and is rewritten around upload failure.
- **Parsing a job ID out of a filename couples the download path to a naming convention.** → The convention is already public in `zip_path`, and the three-way re-verification in Decision 3 means a wrong parse cannot yield unauthorized access, only a 404.
- **Deleting the `/outputs` static mount removes a route some client might use directly.** → It served exactly the same bytes as `/download/:filename` under the same ownership rule; the frontend never referenced it.

## Migration Plan

There is no data migration. Zips already sitting in a deployment's `outputs/` directory are not copied into the bucket: this is a hackathon deliverable with no production data, the artifacts are reproducible by re-uploading, and a backfill would need to invent object keys for zips whose jobs may not exist. After deploy, previously-completed jobs will report `completed` from PostgreSQL while their objects are absent — `GET /api/status` omits them (Decision 4) and `GET /download/:filename` returns 404 (Decision 9), which is the correct observable outcome for an artifact that is genuinely gone.

Deployment order matters: MinIO must be reachable and the operator must have set the four required `VIDEO_MINIO_*` variables *before* the new image starts, or `cmd/api` exits at startup. Rollback is redeploying the previous image, which reverts to `outputs/` and ignores the MinIO variables entirely; jobs completed while the new image was live are then unreachable, since their artifacts are in the bucket and the old code only looks at the local directory.

## Open Questions

None blocking. Two things are settled here that a later change may legitimately revisit: per-user key prefixes (Decision 2), if `add-presigned-download-urls` touches `app.js` anyway, and the per-item `StatObject` in the listing (Decision 4), if listing cost ever becomes measurable.
