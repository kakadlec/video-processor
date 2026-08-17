## Context

`internal/video/application` already has `CreateVideoJob`, `GetJobStatus`, and `ListUserJobs`, each depending only on `domain.VideoJobRepository`/`domain.VideoJobIDGenerator`/`domain.VideoJobIDParser` ports (see `videojob-lifecycle` spec). `internal/video/infrastructure/postgres.Repository` implements the repository port. `docker-compose.yml` already defines both `VIDEO_POSTGRES_DSN` (the runtime variable this row's `setupVideo` will read) and `VIDEO_POSTGRES_TEST_DSN` (already consumed by `internal/video/infrastructure/postgres/repository_test.go`'s existing skip-unless-set integration test), both explicitly commented as "unused [for the runtime one] until a video composition root calls `postgres.Open`/`Migrate`... wiring lands in `wire-videojob-http-endpoints`". `.github/workflows/ci.yml` defines only `VIDEO_POSTGRES_TEST_DSN` — CI's `go test ./...` step never calls `main()`/`setupVideo`, so it has no need for the runtime `VIDEO_POSTGRES_DSN`. This row is the wiring that makes `VIDEO_POSTGRES_DSN` load-bearing.

`cmd/api/identity.go` is the direct template: `identityModule` struct + `setupIdentity(ctx)` (env-driven, fails startup clearly) + `registerRoutes(router)` + handler methods + a `requireBearerAuth()` middleware already used by every existing video-processing route. Nothing about auth needs to be re-invented — the new endpoints sit inside the same `videoRoutes := r.Group("/"); videoRoutes.Use(identity.requireBearerAuth())` group `handleVideoUpload`/`handleDownload`/`handleStatus` already use.

## Goals / Non-Goals

**Goals:**
- Wire `CreateVideoJob`, `GetJobStatus`, `ListUserJobs` to three new HTTP routes, reusing the existing bearer-auth middleware for ownership.
- `main()` fails startup clearly if `VIDEO_POSTGRES_DSN` is missing/invalid/unreachable, exactly mirroring `setupIdentity`'s existing behavior for `IDENTITY_POSTGRES_DSN`.
- HTTP-level tests for the new routes use an in-memory fake `domain.VideoJobRepository`, not a live PostgreSQL instance — mirroring `newTestIdentityModule`'s `inMemoryUserRepository` pattern, so `go test ./...` without Docker/`VIDEO_POSTGRES_TEST_DSN` still exercises the full HTTP contract.

**Non-Goals:**
- No file upload, no storage wiring (MinIO doesn't exist until Phase 5) — `POST /api/video-jobs` takes a filename string, not multipart video bytes.
- No processing trigger (`EnqueueVideoJob`/`StartProcessing`/`CompleteJob`/`FailJob` don't exist yet) — every job created here stays `pending` forever within this row's scope. `migrate-ffmpeg-execution-to-videojob-application` is a separate, later row.
- No change to the legacy `/upload`/`/download`/`/api/status` flow or to `cmd/api/web/app.js` — the frontend does not consume the new endpoints in this row.
- No change to `internal/video/domain`/`application`/`infrastructure` — every use case and adapter this row calls already exists and is already tested.

## Decisions

**New path namespace `/api/video-jobs`, not `/jobs`.** `docs/flows.md`'s Phase 6 section and API-compatibility table name `POST /jobs`/`GET /jobs/{id}/status` as the canonical async endpoints, where `POST /jobs` accepts a real multipart upload, stores it in MinIO, and enqueues real processing via RabbitMQ. This row's `POST /api/video-jobs` has none of that — it takes a JSON filename string and produces a job that never leaves `pending`. Reusing the `/jobs` name now would make the API surface itself claim capabilities it doesn't have, which is exactly the failure mode the roadmap's own note about this row warns against ("must not be presented or documented as a working end-to-end path"). A distinct namespace makes the distinction structural, not just a caveat in prose that's easy to miss later. Considered reusing `/jobs` and updating `docs/flows.md`'s prose to describe it as "existing since Phase 3, gains real processing in Phase 6" — rejected because the endpoints would have to change shape anyway once Phase 5/6 land (multipart body instead of JSON, storage/queue side effects), so nothing is saved by sharing the name today, and the shared name actively increases the risk of the "looks done" mistake the roadmap flags.

**Three routes, REST-ish but matching the three existing use cases 1:1.** `POST /api/video-jobs` → `CreateVideoJob`, `GET /api/video-jobs/:id` → `GetJobStatus`, `GET /api/video-jobs` → `ListUserJobs`. No additional endpoints (no update/delete/cancel) because no corresponding use case exists — adding routes with no use case behind them would mean stubbing behavior nowhere else in the codebase defines.

**Router constructor renamed `setupRouterWithIdentity` → `setupRouter(identity, video)`.** The old name embeds only one of its now-two dependencies; keeping it and just adding a second parameter would leave a misleading name. This touches every call site in `cmd/api/main.go`, `cmd/api/main_test.go`, `cmd/api/identity_test.go` — mechanical, no behavior change to existing routes.

**Test double: `inMemoryVideoJobRepository`, mirroring `inMemoryUserRepository`.** `cmd/api/identity_test.go`'s `newTestIdentityModule` already establishes the pattern (fake repository, real password/JWT/ID adapters since none do I/O) precisely so HTTP-level route tests don't need live PostgreSQL. The new `newTestVideoModule(t)` in `cmd/api/video_test.go` does the same: fake `domain.VideoJobRepository`, real `idgen.Adapter` (no I/O) and a fixed test clock. `internal/video/infrastructure/postgres/repository_test.go` (already existing, skipped unless `VIDEO_POSTGRES_TEST_DSN` is set) remains the only place the real Postgres adapter is exercised — this row adds no new Postgres-level tests, since `Repository`/`Migrate` are unchanged.

**Pagination defaults: `offset=0`, `limit=20` when query params are absent; explicit values are validated exactly as `ListUserJobs` already validates them (400 on out-of-range), not silently clamped.** Matches `ListUserJobs`'s existing "rejected with an error rather than silently clamped" requirement — a caller-supplied out-of-range value must fail loudly, but an *absent* value isn't a caller error, so it gets a sane default instead of a 400. An explicitly-empty query value (`?limit=`) is indistinguishable from an absent one at the HTTP layer (`gin`'s `c.Query` returns `""` for both) and is treated the same way — defaulted, not rejected. A present-but-non-integer value (`?limit=abc`, `?offset=1.5`) fails integer parsing and is rejected with `400`, same as an out-of-range integer — it is never silently treated as absent.

**Exact JSON shapes** (Go struct field order below matches JSON key order):

```
POST /api/video-jobs
  Request:  { "original_filename": string }
  Response: 201 { "job_id": string, "original_filename": string, "status": string, "created_at": string (RFC 3339) }
            400 { "error": string }   401 { "error": string }

GET /api/video-jobs/:id
  Response: 200 { "job_id": string, "status": string, "frame_count": int, "error_reason": string (omitted if empty), "storage_key": string (omitted if empty) }
            400 { "error": string }   401 { "error": string }   404 { "error": string }

GET /api/video-jobs?offset=&limit=
  Response: 200 { "jobs": [ { "job_id": string, "original_filename": string, "status": string }, … ] }
            400 { "error": string }   401 { "error": string }
```

`jobs` is always present as an array (possibly empty `[]`), never `null` or omitted, so callers don't need a nil-check before iterating.

**Ownership/not-found mapping: reuse `GetJobStatus`'s existing collapse.** `GetJobStatus` already returns the same `ErrVideoJobNotFound` for both "doesn't exist" and "isn't yours" (see `videojob-lifecycle` spec). The HTTP handler maps that one error to one `404` — no separate `403` path to preserve that indistinguishability at the API boundary too, not just inside the use case.

## Risks / Trade-offs

- [Risk] A caller might treat `POST /api/video-jobs` returning `201` as "processing started," same misreading the roadmap already flags for the raw use case. → Mitigation: response body's `status` field is always `"pending"` on creation (never anything implying progress), and the finalization PR's doc updates (`docs/architecture.md`, `docs/flows.md`) state explicitly that these endpoints have no processing trigger yet, next to the route table entries.
- [Risk] Forgetting a doc file that still says `VIDEO_POSTGRES_DSN` is unused, or still names the router constructor `setupRouterWithIdentity`. → Mitigation: `proposal.md`'s Impact section enumerates every affected permanent doc; `tasks.md` tracks each one as a finalization-PR subtask.
- [Risk] `main()` now hard-requires `VIDEO_POSTGRES_DSN` at startup, so any environment that ran `go run ./cmd/api` with only `IDENTITY_POSTGRES_DSN` configured breaks. → Mitigation: this is the same posture `IDENTITY_POSTGRES_DSN` already has (fail clearly, no silent fallback), and `docker-compose.yml`'s `app`/`app-test` services already set `VIDEO_POSTGRES_DSN` — only a bare local `go run` outside Docker is affected, and `docs/development.md`/`docs/operations.md` get this documented in the finalization PR.

## Migration Plan

Single PR-sequence. Implementation PR: add `cmd/api/video.go` (module, `setupVideo`, three handlers, request/response types), add `cmd/api/video_test.go` (fake repository + HTTP-level tests for all three routes, success and error paths), update `cmd/api/main.go` (`main()` calls `setupVideo`, router constructor call updated) and the two existing test files' call sites for the renamed constructor. Verify `go vet ./...` and `go test ./... -v` pass (Docker fallback if `ffmpeg` isn't on `PATH`), then `docker compose up --build` and a manual smoke check that `POST /api/video-jobs` → `GET /api/video-jobs/:id` → `GET /api/video-jobs` all behave as designed against the real Postgres container, and that the legacy `/upload` flow is unaffected. Finalization PR: task checkoffs, spec promotion (new `videojob-http-api` capability into `openspec/specs/`), archive, and the enumerated doc updates.
