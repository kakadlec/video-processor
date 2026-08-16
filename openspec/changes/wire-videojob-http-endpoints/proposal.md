## Why

`internal/video/domain` and `internal/video/application` (`CreateVideoJob`, `GetJobStatus`, `ListUserJobs`) exist and are fully tested, and `internal/video/infrastructure/postgres` can persist a `VideoJob`, but nothing in `cmd/api` calls any of it — the composition root only wires `internal/identity`. This is the next Change Backlog row after `add-videojob-infrastructure` and `extract-cmd-api-entrypoint`, both now merged, and it is what actually connects the Video Processing application layer to an HTTP surface for the first time.

## What Changes

- Add a `videoModule` to `cmd/api` (mirroring `identityModule` in `cmd/api/identity.go`), wiring `CreateVideoJob`, `GetJobStatus`, and `ListUserJobs` to the existing `internal/video/infrastructure/postgres.Repository`, `idgen.Adapter`, and a shared clock. `setupVideo(ctx)` mirrors `setupIdentity(ctx)`: requires `VIDEO_POSTGRES_DSN` (already defined in `docker-compose.yml` and `.github/workflows/ci.yml`, unused until now), opens the connection, runs `postgres.Migrate`, fails startup clearly if misconfigured or unreachable.
- Add three new authenticated JSON endpoints under a new `/api/video-jobs` namespace, alongside (not replacing) the legacy `/upload`/`/download`/`/api/status` flow:
  - `POST /api/video-jobs` — creates a job record from `{"original_filename": "..."}` (no file bytes; there is no storage/upload wiring here, only job-record creation). Returns `201` with the job's id, filename, status (`pending`), and creation time.
  - `GET /api/video-jobs/:id` — returns the job's status, frame count, error reason, and storage key, scoped to the requesting user (a non-owner or nonexistent id both return `404`, matching `GetJobStatus`'s existing owner-collapsing behavior).
  - `GET /api/video-jobs` — returns a paginated list of the caller's own jobs (`offset`/`limit` query params, defaulting to `0`/`20`, validated the same way `ListUserJobs` already validates them: `limit` 1-100, `offset` >= 0).
- **Deliberately not `POST /jobs` / `GET /jobs/:id/status`** — `docs/flows.md` names those exact paths as the Phase 6 canonical async endpoint, where `POST /upload`-equivalent accepts a real multipart file, stores it in MinIO, and enqueues real processing. This row's `POST /api/video-jobs` accepts no file at all (`CreateVideoJob` only takes a filename string) and never leaves `pending`, since no later row in this row's own scope triggers processing. Naming it `/jobs` would misrepresent it as that endpoint years before it does what that endpoint is supposed to do; a separate namespace keeps the distinction visible in the API surface itself, not just in prose.
- No processing trigger: a job created via `POST /api/video-jobs` stays `pending` forever until `migrate-ffmpeg-execution-to-videojob-application` (a separate, later Change Backlog row) adds `EnqueueVideoJob`/`StartProcessing`/`CompleteJob`/`FailJob`. This row only adds the three read/create use cases already built by `add-videojob-domain-and-application`.
- No frontend change: `cmd/api/web/app.js` keeps using the legacy `/upload` flow exclusively; the new endpoints are not linked from the UI, so this does not touch the `ddd-architecture` spec's "Frontend as Presentation/Delivery Layer" requirement (no consumed contract changes).
- `main()` now calls `setupVideo(ctx)` in addition to `setupIdentity(ctx)`, and fails startup if either is misconfigured — `go run ./cmd/api` now requires `VIDEO_POSTGRES_DSN` set, same as it already requires `IDENTITY_POSTGRES_DSN`.
- `setupRouterWithIdentity(identity *identityModule) *gin.Engine` is renamed to `setupRouter(identity *identityModule, video *videoModule) *gin.Engine` (mirrors both dependencies explicitly) — updates every call site in `cmd/api/main.go`, `cmd/api/main_test.go`, and `cmd/api/identity_test.go` (a mechanical rename plus a new fake video module for HTTP-level tests, matching the existing `newTestIdentityModule` pattern with an in-memory `domain.VideoJobRepository` fake so these tests don't need a live PostgreSQL instance).

## Capabilities

### New Capabilities

- `videojob-http-api`: HTTP routes, request/response shapes, auth/ownership enforcement, and error-status mapping for `POST /api/video-jobs`, `GET /api/video-jobs/:id`, and `GET /api/video-jobs`, plus the explicit "jobs never leave pending" constraint this row does not resolve. Kept separate from `videojob-lifecycle` because that capability's own Purpose states "No infrastructure, HTTP route... is in scope here" — extending it here would either contradict that statement or force an unrelated Purpose edit; a dedicated capability keeps the HTTP-layer contract (owned by `cmd/api`) separate from the pure use-case contract (owned by `internal/video/application`) it wraps.

### Modified Capabilities

(none — `videojob-lifecycle`'s existing use-case requirements are consumed as-is, not changed; `ddd-architecture`'s "Frontend as Presentation/Delivery Layer" Phase 6 scenario is untouched since this row uses a different path namespace and the frontend doesn't consume these endpoints)

## Impact

- **New**: `cmd/api/video.go` (module + `setupVideo` + handlers), `cmd/api/video_test.go`.
- **Changed**: `cmd/api/main.go` (`main()` calls `setupVideo`; router constructor renamed/re-parameterized), `cmd/api/main_test.go` and `cmd/api/identity_test.go` (call-site updates for the renamed router constructor).
- **Not in scope for the implementation PR** (finalization PR only, per this repo's PR-scope rules) — every permanent doc with a stale route list, composition-root description, or "unused" `VIDEO_POSTGRES_DSN` note once this lands: `CLAUDE.md` (Architecture section's route list, which is also already stale on the `setupRouter`/`setupRouterWithIdentity` name from a prior change), `docs/architecture.md` (Routes table, "Not yet wired into a composition root" note), `docs/development.md` (if it references the router constructor name), `docs/domain-model.md` (Cross-Context Contracts / composition-root reference), `docs/flows.md` (a note that a job-lifecycle preview API exists but is not the Phase 6 flow), `docs/operations.md` (`VIDEO_POSTGRES_DSN` goes from "unused" to "required at startup"), `docs/roadmap.md` (this row's Change Backlog status).
- No change to `docker-compose.yml` or `.github/workflows/ci.yml` — both already define `VIDEO_POSTGRES_DSN`/`VIDEO_POSTGRES_TEST_DSN` in anticipation of this row (see their own comments).
- No change to `internal/video/domain`, `internal/video/application`, or `internal/video/infrastructure/*` — this row only wires existing, already-tested code into HTTP.
