## Why

Phase 3 wired `internal/video`'s domain and application layers into `cmd/api` behind a preview `/api/video-jobs` HTTP API, but the actual `ffmpeg` frame extraction still runs entirely inside `cmd/api/main.go`'s `handleVideoUpload`/`processVideo`, outside any `VideoJob` aggregate. Phase 3's own stated goal — "synchronous `ffmpeg` call migrated from `main.go` into application layer" — is not yet fulfilled, and `internal/video/application`'s four transition use cases (`EnqueueVideoJob`, `StartProcessing`, `CompleteJob`, `FailJob`) remain unbuilt and uncalled, deferred since `add-videojob-domain-and-application`. This is the last `not-started` row for Phase 3.

## What Changes

- Add `Enqueue`, `StartProcessing`, `Complete`, `Fail` transition methods to the `VideoJob` aggregate (`internal/video/domain`), each validated against the existing `JobStatus.CanTransitionTo` state machine.
- Add `Update` to `domain.VideoJobRepository` (and its PostgreSQL implementation) to persist a transitioned job. No transactional-outbox row is added for these transitions — nothing consumes one yet; only `Create` keeps its existing outbox write.
- Add four new application-layer use cases mirroring the transition methods: `EnqueueVideoJob`, `StartProcessing`, `CompleteJob`, `FailJob` — each loads a job by ID, applies one transition, and persists it.
- Add a `FrameExtractor` domain port and a `ffmpeg`-backed `internal/video/infrastructure/ffmpeg` adapter implementing it — the actual `exec.Command("ffmpeg", ...)` call, temp-dir handling, and zip creation, moved out of `cmd/api/main.go` largely as-is.
- Add a `ProcessVideoJob` application-layer orchestration use case that sequences `EnqueueVideoJob` → `StartProcessing` → `FrameExtractor.ExtractFrames` → `CompleteJob`/`FailJob` synchronously, in-process — the piece that "actually fulfills" Phase 3's promise, and the same sequence Phase 6's worker will later run per dequeued message (minus the create/enqueue step, which happens API-side).
- Rewrite `POST /upload` (`handleVideoUpload`, moving from `cmd/api/main.go` to `cmd/api/video.go`) to create a real `VideoJob` via the existing `CreateVideoJob` use case and drive it through `ProcessVideoJob`, instead of calling the old free-standing `processVideo`/`createZipFile`/`addFileToZip` functions, which are removed. **The route's external HTTP contract is unchanged**: same request shape, same `ProcessingResult` JSON response shape and pt-BR messages, same temp-dir-always-cleaned and upload-retained-on-failure behavior — `cmd/api/main_test.go` passes unedited.
- Known, documented consequence: `/upload` now creates `VideoJob` rows in the same table `/api/video-jobs` reads from. `GET /api/video-jobs`/`GET /api/video-jobs/:id` will start showing non-`pending` (`completed`/`failed`) jobs for a user who has used `/upload` — jobs created via `/upload`, not jobs created via `POST /api/video-jobs` itself. `videojob-http-api`'s "no processing trigger" guarantee remains true for jobs created through that specific endpoint; the listing behavior is documented, not suppressed.

## Capabilities

### New Capabilities
- `videojob-execution`: the `ProcessVideoJob` orchestration use case and the `FrameExtractor` port/`ffmpeg` adapter that actually runs frame extraction for a `VideoJob`, synchronously in-process.

### Modified Capabilities
- `videojob-lifecycle`: adds the `VideoJob` transition methods and the `EnqueueVideoJob`/`StartProcessing`/`CompleteJob`/`FailJob` use cases that were explicitly out of scope there until this change.
- `videojob-persistence`: adds `Repository.Update`, with no outbox row (scoped explicitly not to apply to transitions, only to `Create`).
- `videojob-http-api`: adds a scenario documenting that `GET /api/video-jobs` may list non-`pending` jobs originating from `/upload`, without changing any existing requirement's normative text.

`video-frame-extraction` is unaffected: it describes `/upload`'s external HTTP behavior only, none of which changes, and its Purpose text makes no claim about where in the codebase that behavior is implemented — no delta needed.

## Impact

- `cmd/api/main.go`: removes `handleVideoUpload`, `processVideo`, `createZipFile`, `addFileToZip`, the `ProcessingResult`/`VideoRequest` types (moved to `cmd/api/video.go`), and the now-unused `archive/zip`/`os/exec` imports. Route registration for `POST /upload` moves to `video.registerRoutes`.
- `cmd/api/video.go`: gains `handleVideoUpload`, the moved response types, and wiring for the four new use cases, `ProcessVideoJob`, and the `ffmpeg` extractor adapter.
- `internal/video/domain`: `video_job.go` gains transition methods and new sentinel errors; `repository.go` gains `Update`; new `frame_extractor.go` port.
- `internal/video/application`: four new use case files plus `process_video_job.go`.
- `internal/video/infrastructure/ffmpeg`: new package, the `ffmpeg`-shelling adapter.
- `internal/video/infrastructure/postgres/repository.go`: implements `Update`.
- Docs: `docs/flows.md`, `docs/architecture.md`, `CLAUDE.md`, `docs/roadmap.md` updated in the finalization PR to reflect Phase 3 completion and the listing-behavior consequence above.
- No change to `go.mod`, no new external dependency — `ffmpeg` is still shelled out to exactly as before.
