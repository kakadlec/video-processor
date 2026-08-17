## Context

`internal/video/domain` and `application` currently implement only the read/create slice (`CreateVideoJob`, `GetJobStatus`, `ListUserJobs`) wired into `/api/video-jobs` by `wire-videojob-http-endpoints`. `cmd/api/main.go`'s legacy `POST /upload` still does its own thing entirely: `handleVideoUpload` saves the file, calls `processVideo` (a free function that shells to `ffmpeg`, globs frames, zips them via `createZipFile`/`addFileToZip`), and returns a `ProcessingResult` JSON body — none of it touches `VideoJob` at all. This change cuts that call over to run through the `VideoJob` application layer, and, in the process, builds the four transition use cases (`EnqueueVideoJob`, `StartProcessing`, `CompleteJob`, `FailJob`) that `add-videojob-domain-and-application` deliberately deferred.

## Goals / Non-Goals

**Goals**
- `POST /upload`'s frame extraction runs through a real `VideoJob` lifecycle: `pending → queued → processing → completed`/`failed`.
- `EnqueueVideoJob`/`StartProcessing`/`CompleteJob`/`FailJob` exist as independently usable application-layer use cases (not just steps buried in one handler), since Phase 6's worker will call the same shape (`StartProcessing → extract → Complete/Fail`) after dequeuing a message.
- `cmd/api/main.go` no longer contains any `ffmpeg`/zip logic — that code moves into `internal/video/infrastructure`.
- `/upload`'s external HTTP contract (request shape, response JSON shape, pt-BR messages, temp-dir-always-cleaned, upload-retained-on-failure) is unchanged. `cmd/api/main_test.go` passes with zero edits — this is the single tightest constraint on every decision below.

**Non-Goals**
- No queue, no worker, no async processing — still fully synchronous, in-process, in-request. That's Phase 6.
- No change to `/api/video-jobs`'s own behavior or its "no processing trigger" guarantee for jobs *created through that endpoint*. This change does not call `EnqueueVideoJob` et al. from `handleCreateVideoJob` — only from the new `/upload` path.
- No transactional-outbox events for the new transitions. `videojob-persistence`'s outbox requirement is scoped explicitly to `Repository.Create`; nothing consumes a transition event yet, and speculatively adding one now is scope creep ahead of Phase 6 actually needing it.
- No change to MinIO/Redis/RabbitMQ — still local filesystem (`uploads/`, `temp/`, `outputs/`), per Phase 5/6.

## Decisions

### 1. `ProcessVideoJob` is a fifth application-layer use case, not handler-side glue — but it stops short of calling `CompleteJob`

`ProcessVideoJob` holds `*EnqueueVideoJob`, `*StartProcessing`, `*FailJob`, and `domain.FrameExtractor` as dependencies, and sequences them in `Execute(ctx, jobID, videoPath)`: enqueue, start processing, extract. Alternative considered: have `cmd/api/video.go`'s handler call the individual use cases directly. Rejected — Phase 3's own promise is "ffmpeg call migrated *into the application layer*"; if `cmd/api` does the sequencing, the process logic never actually left the transport layer, just changed which functions it calls. Building `ProcessVideoJob` now also means Phase 6's worker can reuse most of this shape (`StartProcessing → extract → Fail-on-error`) instead of re-deriving it.

`CompleteJob` is deliberately **not** called by `ProcessVideoJob` on extraction success — see decision 8 below for why, and the consequence: on success, `ProcessVideoJob` leaves the job in `processing`, not `completed`. The caller (`handleVideoUpload`) calls `CompleteJob` itself once it's confirmed the result is durably usable.

### 2. `FrameExtractor` port lives in `domain`, implemented by a new `internal/video/infrastructure/ffmpeg` package

```go
// internal/video/domain/frame_extractor.go
type FrameExtractor interface {
    ExtractFrames(ctx context.Context, jobID VideoJobID, videoPath string) (storageKey StorageKey, frameCount int, imageNames []string, err error)
}
```

The adapter reproduces `processVideo`/`createZipFile`/`addFileToZip`'s exact logic (temp dir under `temp/<jobID>/`, `exec.CommandContext(ctx, "ffmpeg", "-i", videoPath, "-vf", "fps=1", "-y", pattern)` with its `#nosec G204` suppression carried over unchanged, glob PNGs, zip into `outputs/frames_<jobID>.zip` with the same path-prefix guards `createZipFile`/`addFileToZip` had, `defer os.RemoveAll(tempDir)`), replacing the old ad hoc `requestID := uuid.NewString()` correlation with the real `VideoJobID` — so temp/output artifacts are now keyed by the same ID `GetJobStatus`/`GET /api/video-jobs/:id` report on. `imageNames` (the frame basenames) is returned alongside `storageKey`/`frameCount` since the extractor already computes it while zipping — threading it through avoids the handler reopening the zip just to rebuild the `images` JSON field.

`exec.CommandContext` (not plain `exec.Command`) is used specifically so the port's `ctx context.Context` parameter is load-bearing: a canceled context (client disconnect, server shutdown) kills the `ffmpeg` subprocess instead of leaving it running to completion in the background, consuming CPU/disk for an abandoned upload. Verified by a dedicated test that blocks `ffmpeg` on a named pipe (so the assertion doesn't depend on real decode speed) and confirms cancellation both stops the process and still runs the temp-dir cleanup.

### 3. `Repository.Update`, no new outbox row

```go
Update(ctx context.Context, job *VideoJob) error
```

implemented as a single `UPDATE video_jobs SET status=$1, frame_count=$2, error_reason=$3, storage_key=$4 WHERE id=$5`. `videojob-persistence`'s existing outbox requirement text ("`Repository.Create` SHALL insert... a `video_job_outbox` row") is scoped to `Create` only — `Update` is not held to it, and no delta changes that scoping.

### 4. `handleVideoUpload` moves to `cmd/api/video.go` as a `*videoModule` method

It needs `m.createVideoJob` and `m.processVideoJob`, which only `videoModule` holds. Moving it (rather than passing use cases into `main.go`) is the coherent reading of "retire the legacy in-`cmd/api` exec path" — after this change, `main.go` is router wiring, static file serving, `/download`+`/api/status`, and the ownership-sidecar helpers (`recordArtifactOwner` etc., still shared across handlers regardless of which file defines them, since both are package `main`). `ProcessingResult` and `VideoRequest` (the latter already dead code — grepped, zero references outside its own declaration — deleted, not moved) move to `video.go` with it.

### 5. `handleVideoUpload` uses `authenticatedUserID`'s `ok` check the same way its three siblings in `video.go` do

`handleCreateVideoJob`/`handleGetVideoJobStatus`/`handleListVideoJobs` all sit behind the same `requireBearerAuth()`-gated `videoRoutes` group as `/upload`, yet all three still do `userID, ok := authenticatedUserID(c); if !ok { 401 }` rather than assuming authentication. The *old* `handleVideoUpload` didn't — it had `if authenticated { record owner }` and silently proceeded owner-less otherwise, a leftover from before `requireBearerAuth()` covered this route unconditionally. Since `CreateVideoJob` hard-fails on an empty `UserID`, the new handler needs to make an explicit choice either way; it adopts the same 401-on-`!ok` pattern as its three new siblings for consistency within the same file, rather than inventing a third convention. This is unreachable in production routing either way (the middleware already blocks it), so it changes no observable behavior — confirmed no test exercises `/upload` without a bearer token.

### 6. `/upload` response message stays Portuguese, built at the HTTP boundary

`ProcessVideoJob`'s own errors and the `FrameExtractor` adapter's errors are plain English (per this repo's language policy — new Go code, non-UI). `handleVideoUpload` builds the existing pt-BR `ProcessingResult.Message` strings itself from `ProcessVideoJobResult.Success`/`FrameCount`/`FailureReason`, exactly as `processVideo` did inline before — the pt-BR wording is user-facing UI copy returned to the existing frontend/API contract, which is exempted from the English-new-code policy.

### 7. `GET /api/video-jobs` listing now mixes both origins — documented, not filtered

Once `/upload` also calls `CreateVideoJob`, a user's job list legitimately contains jobs created via `/upload` (which will show `completed`/`failed`, with real `frame_count`/`storage_key`) alongside any jobs created directly via `POST /api/video-jobs` (which still only ever show `pending` — that endpoint still calls nothing beyond `CreateVideoJob`). No filter is added to hide either kind from the other — they're the same aggregate, same table, same owner-scoping rule already enforced by `ListUserJobs`. This is called out explicitly in `videojob-http-api`'s delta (new scenario, no requirement-text change) and in `docs/flows.md`/`docs/architecture.md`.

### 8. `CompleteJob` moves to the caller, after output artifact ownership is durably recorded

Original design had `ProcessVideoJob` call `CompleteJob` itself as the last step on extraction success, before returning to `handleVideoUpload`, which then did its own post-processing (delete the original upload, record the output zip's ownership sidecar) — unchanged from `processVideo`'s old structure. That ordering has a real bug: if `recordArtifactOwner("outputs", ...)` fails *after* the job is already `completed`, the handler deletes the now-unowned zip and returns a failure response, but the `VideoJob` stays `completed` forever with a `StorageKey` pointing at a file that no longer exists — and there's no way to correct it, because `completed` is terminal in the canonical state machine (`CanTransitionTo` returns false for every edge out of `completed`, including to `failed`).

Fixed by moving the `CompleteJob` call out of `ProcessVideoJob` and into `handleVideoUpload`, called only after `recordArtifactOwner("outputs", ...)` succeeds. If it fails instead, the job is still `processing` — a state `FailJob` *can* validly leave — so the handler calls `FailJob` with a reason describing the ownership failure, keeping the job's persisted status accurate. The residual risk (a crash or DB failure between recording ownership and the `CompleteJob` call itself leaves the job stuck in `processing` forever, since there's no worker to reconcile it) is accepted for the same reason the rest of this phase accepts synchronous, no-queue processing: Phase 6 replaces this with real orchestration.

- **Frontend impact**: none — `cmd/api/web/app.js` still only calls `/upload`/`/api/status`, unaffected by the internal refactor or by `/api/video-jobs` rows changing status, since the frontend never reads that endpoint.
- **Test double drift**: `cmd/api/video_test.go`'s `newTestVideoModule(t)` is reachable from `cmd/api/main_test.go`'s `/upload` tests (via `startTestServer`). It must wire the **real** `ffmpeg` adapter (tests already require `ffmpeg` on `PATH` per the repo's existing test-suite precondition) — a fake `FrameExtractor` is only appropriate for `video_test.go`'s own `/api/video-jobs` route tests and for `internal/video/application`'s new use-case-level unit tests (their own fakes, mirroring `fakes_test.go`'s existing pattern), never for `newTestVideoModule`.
- **`inMemoryVideoJobRepository`** (in `cmd/api/video_test.go`) needs an `Update` method to keep implementing `domain.VideoJobRepository`.
- **gosec**: the `#nosec G204` on the `ffmpeg` `exec.CommandContext` call and the path-prefix guards in the zip-writing code must both be carried into the new `ffmpeg` package verbatim — dropping either fails the required `SAST (gosec)` check.
- **`Fail(reason)` empty-string trap**: `RestoreVideoJob` enforces `failed ⇔ non-empty ErrorReason`. If `FrameExtractor.ExtractFrames` ever returns an error whose `.Error()` is empty, `ProcessVideoJob` substitutes a fixed fallback reason string before calling `FailJob`, so the aggregate never becomes un-persistable.

## Migration Plan

Single PR sequence (propose → implementation → finalization), no phased rollout — this is a synchronous, in-process, backward-compatible-at-the-HTTP-boundary internal refactor with no data migration (no existing `video_jobs` rows need backfilling; `Update` is additive to the schema's existing columns).

## Open Questions

None — all decisions above were resolved against the existing code and specs before writing tasks.md.
