## 1. Domain: VideoJob transitions and FrameExtractor port

- [ ] 1.1 Add `Enqueue()`, `StartProcessing()`, `Complete(storageKey StorageKey, frameCount int)`, `Fail(reason string)` methods to `VideoJob` in `internal/video/domain/video_job.go`, each checked against `status.CanTransitionTo` and mutating in place on success.
- [ ] 1.2 Add `ErrInvalidStatusTransition` and `ErrFailureReasonRequired` sentinel errors.
- [ ] 1.3 Add `Update(ctx context.Context, job *VideoJob) error` to `VideoJobRepository` in `internal/video/domain/repository.go`.
- [ ] 1.4 Add `internal/video/domain/frame_extractor.go` with the `FrameExtractor` port (`ExtractFrames(ctx, jobID, videoPath) (storageKey, frameCount, imageNames, err)`).
- [ ] 1.5 Unit tests: transition methods (valid/invalid edges, aggregate unchanged on rejection) in `video_job_test.go`.

## 2. Application: transition use cases and orchestration

- [ ] 2.1 `internal/video/application/enqueue_video_job.go` — `EnqueueVideoJob` use case + test.
- [ ] 2.2 `internal/video/application/start_processing.go` — `StartProcessing` use case + test.
- [ ] 2.3 `internal/video/application/complete_job.go` — `CompleteJob` use case + test.
- [ ] 2.4 `internal/video/application/fail_job.go` — `FailJob` use case + test.
- [ ] 2.5 `internal/video/application/process_video_job.go` — `ProcessVideoJob` orchestration use case (Enqueue → StartProcessing → extract → Complete/Fail), including the empty-error-message fallback reason, + test with a fake `FrameExtractor`.
- [ ] 2.6 Extend `internal/video/application/fakes_test.go` (or add a sibling fake) as needed for the above.

## 3. Infrastructure: ffmpeg adapter and repository Update

- [ ] 3.1 New `internal/video/infrastructure/ffmpeg/extractor.go` implementing `FrameExtractor`, moving `processVideo`/`createZipFile`/`addFileToZip`'s logic from `cmd/api/main.go` (temp dir under `temp/<jobID>/`, `ffmpeg` exec with `#nosec G204`, glob, zip into `outputs/frames_<jobID>.zip` with path-prefix guards, deferred temp-dir cleanup).
- [ ] 3.2 Implement `Repository.Update` in `internal/video/infrastructure/postgres/repository.go` (no outbox row).
- [ ] 3.3 Tests for both (extractor test can reuse this repo's existing test-video-generation helpers; repository Update test alongside the existing `repository_test.go`, gated the same way on `VIDEO_POSTGRES_TEST_DSN`).

## 4. cmd/api: rewire /upload through the VideoJob application layer

- [ ] 4.1 Extend `videoModule` (`cmd/api/video.go`) with the four new use cases, `ProcessVideoJob`, and wire the `ffmpeg` extractor adapter in `setupVideo`.
- [ ] 4.2 Move `handleVideoUpload` to `cmd/api/video.go` as a `*videoModule` method; rewrite it to call `m.createVideoJob` then `m.processVideoJob`, matching the `authenticatedUserID`/`ok` pattern used by the module's other three handlers.
- [ ] 4.3 Move `ProcessingResult` to `cmd/api/video.go`; delete the dead `VideoRequest` type.
- [ ] 4.4 Remove `processVideo`, `createZipFile`, `addFileToZip`, and the now-unused `archive/zip`/`os/exec` imports from `cmd/api/main.go`; update the `POST /upload` route registration to `video.handleVideoUpload`.
- [ ] 4.5 Update `cmd/api/video_test.go`'s `newTestVideoModule`/`newTestVideoModuleWithRepo` to wire the real `ffmpeg` extractor (not a fake) — it's reachable from `cmd/api/main_test.go`'s `/upload` tests via `startTestServer`.
- [ ] 4.6 Add `Update` to `cmd/api/video_test.go`'s `inMemoryVideoJobRepository` fake.
- [ ] 4.7 Confirm `cmd/api/main_test.go` passes with zero edits.

## 5. Validation

- [ ] 5.1 `go vet ./...`
- [ ] 5.2 `go test ./... -v` (or `docker compose run --build --rm app-test go test ./... -v`)
- [ ] 5.3 `gosec ./...` — confirm zero findings, especially around the moved `exec.Command`/zip path checks.
- [ ] 5.4 Manual smoke test: `docker compose up --build`, upload a video via `/upload`, confirm the response is unchanged, then `GET /api/video-jobs` with the same token and confirm the uploaded job appears as `completed`.

## 6. Finalization-PR doc updates

- [ ] 6.1 `docs/flows.md`: update the "Preview: VideoJob HTTP API" section — the four transition use cases now exist (just not reachable from `/api/video-jobs`'s own routes), and note the listing now mixes `/upload`-originated and `/api/video-jobs`-originated jobs.
- [ ] 6.2 `docs/architecture.md`: update the request pipeline description (frame extraction now runs through `internal/video`), package topology comment for the new `ffmpeg` infra adapter, dependency rule 4, bounded-context status table (Video Processing → fully implemented for Phase 3's scope), Routes table note.
- [ ] 6.3 `CLAUDE.md`: update the Architecture section's numbered route/pipeline description to match.
- [ ] 6.4 `docs/roadmap.md`: flip row 60 to `archived`, update the Phase Summary table and "Current State" Phase 3 paragraph to say Phase 3 is complete.
- [ ] 6.5 Mark all tasks in this file complete, promote delta specs into `openspec/specs/`, archive the change folder.
