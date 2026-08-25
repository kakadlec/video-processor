## Context

`POST /upload` currently writes the incoming video to `uploads/<uploadID>_<filename>` with `os.Create` + `io.Copy(out, io.TeeReader(part, hasher))`, records a `.owner` sidecar next to it, and passes that local path to `ProcessVideoJob.Execute(ctx, jobID, videoPath)`. The extractor shells out to `ffmpeg -i <videoPath>`. On success the handler unlinks the file; on failure it does not, which `video-frame-extraction` documents as a known leak.

Two constraints shape everything below.

**`ffmpeg` needs a filesystem path.** It is an external process invoked through `exec.CommandContext`; there is no way to hand it an `io.Reader`. So moving the source into object storage necessarily means a local copy exists during extraction. The design question was never *whether* to have one, only where it lives and who removes it.

**The result migration already set the precedent.** `migrate-result-storage-to-minio` established that the `ffmpeg` adapter writes only under `temp/` and knows nothing about buckets, while `ProcessVideoJob` owns storage calls and the local file's lifetime. `videojob-result-storage`'s spec and `ProcessVideoJob`'s doc comment both argue for that attribution explicitly. This change is the mirror image — pulling bytes down instead of pushing them up — and follows the same split rather than inventing a second one.

## Goals / Non-Goals

**Goals:**

- Make `ProcessVideoJob` addressable by storage key rather than by a filesystem path the calling handler wrote, so Phase 6 can invoke it from `cmd/worker` without a shared filesystem.
- Remove the last local-filesystem artifact store and, with it, the `.owner` sidecar mechanism in its entirety.
- Guarantee that no source object outlives the request that created it, on every exit path.
- Preserve `POST /upload`'s observable success and failure responses byte-for-byte, and preserve `upload-file-validation`'s fast-reject guarantee exactly.

**Non-Goals:**

- Persisting the source key on the `VideoJob` aggregate. Nothing reads it after the request ends, because nothing survives the request.
- Any queue, worker, or asynchrony. The extra round trip this design introduces (up to the bucket, back down for `ffmpeg`) is a cost of the pipeline still being synchronous; Phase 6 removes the download from the request path, not this change.
- Concurrency limiting. Unbounded parallel uploads are a pre-existing property of the synchronous pipeline; this change alters the per-upload memory constant, not the bound.
- Presigned URLs, in either direction.

## Decisions

### 1. `ProcessVideoJob` downloads; the `ffmpeg` adapter does not

`Execute(ctx, jobID, sourceKey)` calls `SourceStorage.Get` into a local path under `temp/`, then passes that path to `FrameExtractor.ExtractFrames` unchanged.

*Alternative considered:* let the `ffmpeg` adapter take a key and fetch the object itself. Rejected for the same reason the result upload was not put there: it would make the extractor depend on object storage, and `videojob-result-storage` canonized the opposite ("the `ffmpeg` adapter writes only under `temp/`"). `FrameExtractor`'s port contract is unchanged by this design — it still takes a local path.

The `defer` that removes the downloaded copy is registered **immediately after `Get` succeeds and before the extraction call**, never after. `migrate-result-storage-to-minio`'s review caught exactly this ordering bug on the zip: a `defer` placed after a call whose error path `return`s never runs.

### 2. The local copy is `temp/<jobID>_source`, with no file extension

Beside the per-job frame directory `temp/<jobID>/`, not inside it — the extractor's own `defer os.RemoveAll(tempDir)` must not be able to delete a file it does not own. Same placement rule as `temp/<jobID>.zip`.

Dropping the extension is deliberate. Carrying the original one would mean deriving a filesystem name from a user-supplied filename, which reintroduces a sanitization obligation this design can simply not have. `ffmpeg` detects input format by probing content, not by extension, and every extension on `isValidVideoFile`'s whitelist is a container with an unambiguous magic signature (`ftyp` for mp4/mov, `RIFF` for avi, EBML for mkv/webm, the ASF GUID for wmv, `FLV` for flv). The name is therefore built entirely from the job's own UUID plus a fixed suffix, and confined with the same `strings.HasPrefix(path, tempDirName+separator)` check the zip path already uses, so gosec's G304 sees a validated path.

*Verification owed at implementation time:* extract from at least one non-mp4 container through the real pipeline to confirm probing without an extension, rather than asserting it from documentation.

### 3. Source keys take an `uploads/` prefix; result keys stay flat

`uploads/<uploadID>_<original-filename>`, mirroring today's local filename so operator-facing naming is unchanged.

The prefix is permitted here for a reason that does **not** generalize: `domain.StorageKey`'s existing comment records that a result key must contain no `/` because `app.js` uses it verbatim as `GET /download/:filename`'s single path segment, where a `%2F` breaks the route match. A source key never becomes a URL path segment — precisely because this change deletes the `/uploads` route. The two facts are load-bearing together, and if any future change re-exposes source objects over HTTP, the prefix has to go with it.

One bucket, not two. A second bucket would mean a fifth required `VIDEO_MINIO_*` variable and a second `EnsureBucket` at startup for no behavioral gain; the prefix already separates the two key spaces, and the two classes' retention differs by code, not by bucket policy.

### 4. `SourceStorage.Put` takes an `io.Reader`; `ResultStorage.Put` takes a path

A deliberate asymmetry. The result zip already exists as a file when it is stored, so `FPutObject` is the natural call. The source has no local existence at all at upload time — the whole point is that it never touches the disk on the way in — so the port takes the reader and the adapter calls `PutObject`. Making the two ports look alike would force one side or the other to materialize a file it does not need.

`Get` takes a destination path (`FGetObject`) rather than returning a reader, because its only consumer needs a file for `ffmpeg`; returning a reader would just move the copy loop into the application layer.

### 5. An explicit `PartSize` on upload

A `multipart.Part` does not report its length, so the size argument is `-1`. Read from the pinned `minio-go v7.3.0` source rather than its documentation: `PutObject` with a negative size and no configured part size routes to `putObjectMultipartStreamNoLength`, which calls `OptimalPartInfo(-1, 0)`. That function substitutes `maxMultipartPutObjectSize` (5 TiB) for the unknown size and divides by `maxPartsCount` (10000), and the caller then allocates `buf := make([]byte, partSize)` — a single ~537 MiB heap buffer per upload, as the function's own comment states.

Setting `PutObjectOptions.PartSize` to 16 MiB (`minPartSize`, comfortably above the 5 MiB floor `OptimalPartInfo` enforces) bounds that buffer at 16 MiB per in-flight upload. With a configured part size and an unknown object size the library caps the object at `partSize × maxPartsCount` = 160 GiB, which this service will never approach.

*Alternative considered:* pass `c.Request.ContentLength`. Rejected — that is the length of the entire multipart body including boundaries and any other parts, not the video part, so it is wrong in a way that would only show up on large uploads.

### 6. The handler owns the source object's lifetime, via one `defer`

`handleVideoUpload` registers a single deferred delete as soon as `Put` returns successfully. That one construct covers every subsequent exit: the idempotency conflict, a `CreateVideoJob` error, a `ProcessVideoJob` error, a processing failure, and the success path — plus a panic.

*Alternative considered:* delete inside `ProcessVideoJob`, which is more Phase-6-shaped since that use case becomes the worker's body. Rejected because `ProcessVideoJob` does not run on the duplicate or `CreateVideoJob`-failure paths, so the handler would still need its own delete calls for those, and the object's lifetime would be owned by two places at once. Split cleanup ownership is how leaks are written. When Phase 6 moves extraction out of the request, the delete moves with it and the handler retains a compensating delete only for the paths that never enqueue.

The delete runs on a detached context (`NewFinalizationContext`), for the reason every other cleanup path in this handler already does: `c.Request.Context()` may already be canceled — a client disconnect can itself be why processing failed — and the cleanup must still succeed or the object leaks precisely in the case the design promises it will not.

A delete of an already-absent key is not treated as an error, so the deferred delete is safe even on paths where an earlier step already removed the object.

### 6a. That deletion is best effort, and the specs say so

One `RemoveObject` call, no retry, no persisted cleanup record. If MinIO is unreachable at exactly that moment — or the detached context's own bound expires — the delete fails, and there is nothing in this system that will come back for the object later. Unlike the local file it replaces, nothing reclaims it when a container is replaced.

So the contract is written as an obligation to *attempt*, not as a guarantee of absence: the handler SHALL attempt deletion on every exit path, SHALL log a failure with the key, and SHALL NOT fail the request because cleanup failed — the response is about the job, not about housekeeping. No requirement in this change asserts that no source object survives its request, because no mechanism here can enforce that.

This is the same defect class as PR #179's "No zip file remains anywhere on the local filesystem", which was globally unenforceable and had to be scoped in review. Catching it in the proposal is the cheaper place.

Two things bound the residual leak. First, its shape: it needs a storage failure at the exact moment of cleanup, and every such failure is logged with the key that leaked, so the set is enumerable from logs rather than invisible. Second, its backstop: an expiration lifecycle rule on the `uploads/` key prefix reclaims anything the application misses. That rule is operator policy rather than application behavior — nothing in the code depends on it — and it is *cheap only because* decision 3 gave source objects a prefix of their own. Result objects, which must never expire, share the bucket but not the prefix. `docs/operations.md` recommends it in the finalization tasks.

*Alternative considered:* retry the delete, or record pending cleanups somewhere durable. Both are the beginning of a reconciler, and a reconciler needs a process that outlives the request — which is Phase 6's worker. Building half of one here would add moving parts without closing the gap.

### 7. Deleting the source on failure removes the documented retention gap

`video-frame-extraction`'s "Uploaded File Retained On Processing Failure" requirement, and the test that pins it, describe behavior this design ends. The requirement is removed rather than rewritten: it exists to document a leak, and the leak is gone.

Carrying it forward would have been actively worse than leaving it alone. A local-disk leak is bounded by the container's lifetime and reclaimed when the container is replaced; the same leak in a bucket is durable, unbounded, and would need either a lifecycle rule or a reaper that nothing in this system has. That asymmetry is what made "delete on failure too" the right call rather than a gratuitous behavior change.

### 8. The duplicate path pays a bucket write it cannot avoid

The idempotency key is derived from the content hash, which is only known after the entire part has been read. So the object is fully written before `Reserve` can report that this content is a duplicate, and `cleanupRedundantUpload` deletes it.

*Alternative considered:* hash first, store second. That requires buffering the whole upload somewhere between the wire and the decision — which is the local file this change exists to delete. The cost is accepted and stated in the proposal rather than engineered around.

## Risks / Trade-offs

**[The video's bytes cross the network twice per request — up to the bucket, then back down for `ffmpeg`.]** → Accepted and temporary. It is the direct consequence of keeping extraction in-request while the source lives remotely, and Phase 6 removes the second leg from the request path by moving extraction to a worker that downloads on its own time. Trading latency now for a use case that is addressable by key is the explicit purpose of the change.

**[`ffmpeg` probing an extension-less file could fail for some container.]** → Mitigated by testing a non-mp4 container end to end during implementation, not by assuming. If probing turns out to need a hint, the fallback is `-f` from a whitelist-validated extension, never a filename-derived path component.

**[Per-upload memory becomes a 16 MiB buffer where it used to be a streaming write to disk.]** → Bounded per upload but not in aggregate, since nothing limits concurrent uploads. This is a smaller regression than the ~537 MiB default it replaces, and concurrency limiting stays a Phase 6 concern. Named here so it is not discovered as a surprise.

**[Operators lose the ability to inspect a failed upload on the host filesystem.]** → By design; there is nothing to inspect once failures stop retaining the source. The job's persisted `ErrorReason` and the server logs remain, and `ProcessVideoJob` already logs storage failures with the underlying error.

**[A failed job can no longer be retried without re-uploading the bytes.]** → An accepted consequence of the transient-source decision. If Phase 6's worker is later given replay-on-retry, it will need the source key persisted on the aggregate and a retention policy to match; deciding that now, with no worker to use it, would add a column that nothing reads.

**[A failed cleanup leaks a source object durably, with nothing to reclaim it.]** → Not fully mitigated, and deliberately not specified away. The deletion is one best-effort call (decision 6a); a storage failure at that instant leaves the object. Every such failure is logged with its key, so the leak is enumerable rather than silent, and an expiration lifecycle rule on the `uploads/` prefix reclaims what the application misses. Closing the gap properly requires a reconciling worker, which is Phase 6.

**[An interrupted `PutObject` can leave an incomplete multipart upload server-side.]** → `minio-go` aborts the multipart upload when its own write fails, so the common case self-heals. A client that disconnects mid-body is covered by the same path, since the copy returns an error. Bucket-level abort-incomplete-multipart lifecycle configuration is not added here — it is operator policy, not application behavior, and no code path depends on it.

**[The change deletes six symbols and a route that tests assert on.]** → `TestProcessing_Failure_LeavesUploadedFileBehind` must be *inverted* into an assertion that nothing remains, not deleted. Silently dropping a test that pins a behavior being reversed is how a regression becomes invisible; the same discipline was applied to the ownership-recording test in the result migration.

## Migration Plan

No data migration. `uploads/` holds only in-flight files by definition, so a deployment cutover has nothing to move; any file left there by an older build is orphaned and can be removed by the operator at will. The directory stops being created, and the bind mount that exposed it is dropped from `docker-compose.yml`.

No new configuration. Source objects share `VIDEO_MINIO_BUCKET` with results, so the required variable set and the fail-closed startup posture established by `migrate-result-storage-to-minio` are unchanged — an operator already running the current release needs no action beyond deploying.

Rollback is a plain image revert. Because source objects never outlive their request, a rolled-back build finds no state in the bucket it does not understand, and the reverted code recreates `uploads/` on startup as it always did.

## Open Questions

None blocking. The one question that gated this design — whether a failed job's source survives for replay — was decided before proposing: it does not, which is what keeps the source key out of the aggregate and the scope confined to a port, a signature, and a set of deletions.
