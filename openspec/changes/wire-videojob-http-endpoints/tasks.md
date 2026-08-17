## 1. videoModule and setupVideo

- [ ] 1.1 In `cmd/api/video.go`, add `videoModule` (mirrors `identityModule`) wiring `*application.CreateVideoJob`, `*application.GetJobStatus`, `*application.ListUserJobs` to `postgres.Repository`, `idgen.Adapter`, and the existing `systemClock`
- [ ] 1.2 Add `setupVideo(ctx)` mirroring `setupIdentity(ctx)`: `postgres.LoadConfigFromEnv()` (requires `VIDEO_POSTGRES_DSN`), `postgres.Open`, `postgres.Migrate`, `db.PingContext`, fail startup clearly on any error
- [ ] 1.3 `main()` calls `setupVideo(ctx)` alongside `setupIdentity(ctx)`, fails startup (same `log.Fatal` pattern) if either errors

## 2. HTTP routes and handlers

- [ ] 2.1 Rename `setupRouterWithIdentity(identity *identityModule) *gin.Engine` to `setupRouter(identity *identityModule, video *videoModule) *gin.Engine` in `cmd/api/main.go`; update the call site in `main()`
- [ ] 2.2 Register `POST /api/video-jobs`, `GET /api/video-jobs/:id`, `GET /api/video-jobs` inside the existing `videoRoutes` group (already behind `identity.requireBearerAuth()`)
- [ ] 2.3 `handleCreateVideoJob`: bind JSON body, call `CreateVideoJob.Execute` with the authenticated `UserID` (via `authenticatedUserID(c)`, ignoring any body-supplied owner field), map validation errors to `400`, success to `201`
- [ ] 2.4 `handleGetVideoJobStatus`: call `GetJobStatus.Execute` with the authenticated `UserID` and the `:id` path param, map `domain.ErrInvalidVideoJobID` to `400`, `domain.ErrVideoJobNotFound` to `404`, success to `200`
- [ ] 2.5 `handleListVideoJobs`: parse `offset`/`limit` query params (default `0`/`20` when absent), call `ListUserJobs.Execute`, map `ErrLimitOutOfRange`/`ErrOffsetNegative` to `400`, success to `200`
- [ ] 2.6 Define request/response JSON types (`createVideoJobRequest`, `videoJobResponse`, `videoJobStatusResponse`, `videoJobListResponse`, `videoErrorResponse`) per `design.md`'s shapes

## 3. Test call-site updates

- [ ] 3.1 Update every `setupRouterWithIdentity(...)` call site in `cmd/api/main_test.go` and `cmd/api/identity_test.go` to `setupRouter(identity, video)`, using a new `newTestVideoModule(t)` fake for the second argument

## 4. Test double and route tests

- [ ] 4.1 In `cmd/api/video_test.go`, add `inMemoryVideoJobRepository` (fake `domain.VideoJobRepository`, mirrors `inMemoryUserRepository`) and `newTestVideoModule(t) *videoModule`
- [ ] 4.2 Test `POST /api/video-jobs`: success (201, correct body), missing/invalid auth (401), unsupported/empty filename (400), caller-supplied owner field is ignored
- [ ] 4.3 Test `GET /api/video-jobs/:id`: owner success (200), non-owner returns 404 (not 403), nonexistent id returns 404, malformed id returns 400 without querying the repository
- [ ] 4.4 Test `GET /api/video-jobs`: default pagination (offset 0, limit 20), explicit valid pagination, out-of-range limit/negative offset rejected with 400, listing scoped to caller only, newest-first ordering
- [ ] 4.5 Test `setupVideo`: missing `VIDEO_POSTGRES_DSN` returns an error wrapping `postgres.ErrDSNRequired`; unreachable Postgres returns an error (mirrors `TestSetupIdentity_*`)

## 5. Validation

- [ ] 5.1 `go vet ./...` passes
- [ ] 5.2 `go test ./... -v` passes (ffmpeg on `PATH`, or via `docker compose run --build --rm app-test go test ./... -v`)
- [ ] 5.3 `docker compose up --build` starts successfully; manual smoke check that `POST /api/video-jobs` → `GET /api/video-jobs/:id` → `GET /api/video-jobs` behave as designed against the real Postgres container, and that the legacy `/upload` → `/download` flow is unaffected
- [ ] 5.4 `gosec ./...` and `govulncheck ./...` clean, verified by CI's `SAST (gosec)` and `Vulnerability Scan (govulncheck)` required checks

## 6. Finalization-PR doc updates (not implementation scope)

- [ ] 6.1 `CLAUDE.md`: route list in the Architecture section (also fix the pre-existing stale `setupRouter()`/`setupRouterWithIdentity` name)
- [ ] 6.2 `docs/architecture.md`: Routes table (three new rows), "Not yet wired into a composition root" note for PostgreSQL/video
- [ ] 6.3 `docs/development.md`: any reference to the router constructor name, if present
- [ ] 6.4 `docs/domain-model.md`: Cross-Context Contracts / composition-root reference now reflects video being wired
- [ ] 6.5 `docs/flows.md`: note that a job-lifecycle preview API exists under `/api/video-jobs` since Phase 3, distinct from the Phase 6 `/jobs` flow, with no processing trigger
- [ ] 6.6 `docs/operations.md`: `VIDEO_POSTGRES_DSN` goes from "unused" to "required at startup"
- [ ] 6.7 `docs/roadmap.md`: flip this row's Change Backlog status to archived
- [ ] 6.8 Promote `openspec/changes/wire-videojob-http-endpoints/specs/videojob-http-api/spec.md` into `openspec/specs/videojob-http-api/spec.md`
