## 1. Cache package (`internal/video/infrastructure/cache`)

- [ ] 1.1 `internal/video/infrastructure/cache/repository.go`: `CachedVideoJobRepository` struct wrapping an inner `domain.VideoJobRepository` and a `*redis.Client`; `NewCachedVideoJobRepository(inner domain.VideoJobRepository, client *redis.Client) *CachedVideoJobRepository`. Implements `domain.VideoJobRepository` in full (`Create`, `FindByID`, `FindByUserID`, `Update`).
- [ ] 1.2 A small internal serializable record type mirroring `postgres.Repository.scanJobRow`'s column set (`id`, `userID`, `originalFilename`, `storageKey`, `frameCount`, `errorReason`, `status`, `createdAt` as RFC3339), JSON-encoded/decoded. Deserialization re-validates every field through its domain constructor (`domain.NewUserID`, `domain.NewOriginalFilename`, `domain.NewStorageKey` — skipped when empty, matching `scanJobRow`'s own empty-storage-key handling — the repository's `VideoJobIDParser`) before calling `domain.RestoreVideoJob`, exactly mirroring `postgres.Repository`'s own reconstruction discipline so a cache hit can never produce an aggregate that bypasses domain invariants.
- [ ] 1.3 `FindByID`: Redis `GET` on `"videojob:status:" + id.String()` first. Hit → deserialize per 1.2 and return, no PostgreSQL query. Miss, Redis error, or deserialization error (log the error in the latter two cases) → call `inner.FindByID`, then best-effort `SET NX` (not a plain `SET` — see the race caught by Copilot's review of PR #154, documented in design.md Decision 2) the result into the cache with the fixed TTL (task 1.6) before returning it — a `SETNX` failure here is logged and does not affect the returned result.
- [ ] 1.4 `Update`: call `inner.Update(ctx, job)` first; return its error immediately if it fails, without touching the cache. On success, best-effort **unconditional** `SET` the cache entry to `job`'s new serialized state with the fixed TTL (unlike 1.3's `SETNX` — a write-through always represents the latest confirmed state and must win over anything already cached); if that `SET` fails, attempt a best-effort `DEL` of the same key so the entry degrades to a miss rather than staying stale; log either failure, and return success regardless (the PostgreSQL write already committed).
- [ ] 1.5 `Create` and `FindByUserID`: pass straight through to `inner`, uncached.
- [ ] 1.6 Fixed TTL constant (5 minutes) applied to every cache write (both the `SETNX` repopulation in 1.3 and the `SET` write-through in 1.4) — not read from configuration.
- [ ] 1.7 Unit tests for the internal serialize/deserialize round trip (1.2): a valid record round-trips to an equal `*domain.VideoJob` for each status (including the completed-with-storage-key and failed-with-reason cases); a corrupted/invalid stored value (e.g. bad status string, empty required field) fails deserialization with an error rather than panicking or producing an invalid aggregate.
- [ ] 1.8 Integration tests against `REDIS_TEST_ADDR` (skip with a clear message if unset, matching `internal/platform/redis`'s own test pattern) and a fake/in-memory `domain.VideoJobRepository` standing in for PostgreSQL:
  - `FindByID` cache-miss falls through to the inner repository and repopulates the cache (a subsequent identical call, with the inner repository's fake made to error, still succeeds — proving the second call was actually served from cache, not just "would have worked either way").
  - `Update` writes through so an immediately-following `FindByID` observes the new state via cache.
  - A PostgreSQL (`inner.Update`) failure leaves the previous cache entry untouched and returns the error.
  - `FindByUserID`/`Create` pass through untouched (call counts on the fake confirm no cache interaction), and `Create` does not repopulate the cache.
  - **The miss-repopulation-vs-write-through race itself (Copilot, PR #154):** a `FindByID` miss that has already read a stale value from the inner repository, once a concurrent `Update` has written through a newer value, must not clobber that newer cache entry when its own (`SETNX`-based) repopulation runs afterward.
  - **A genuine Redis error on `GET`** (not just a miss — e.g. a `WRONGTYPE` error from a key holding the wrong data type) falls back to the inner repository exactly like a miss.
  - **A malformed cache entry** (e.g. non-JSON garbage written directly to the key) fails deserialization, falls back to the inner repository, and is replaced by a valid entry (a subsequent call is a clean cache hit, not another fallback).
  - **A cache write failure** (`SETNX` on the miss path, or `SET`/fallback `DEL` on the write-through path — e.g. by pointing the decorator's Redis client at an unreachable address) is logged but never surfaces as an error from `FindByID` or `Update`, whose success still depends only on the inner repository.
  - **Every cache write carries the fixed TTL**: after both a miss-repopulation and a write-through, `TTL`/`PTTL` on the key reports a positive value bounded by the 5-minute constant (task 1.6).

## 2. Wiring (`cmd/api`)

- [ ] 2.1 `cmd/api/video.go`'s `setupVideo`: wrap the constructed `postgres.Repository` with `cache.NewCachedVideoJobRepository(repo, redisClient)` (reusing the already-open shared client — no second Redis connection, no new required env var) before passing it to `NewGetJobStatus`, `NewListUserJobs`, `NewEnqueueVideoJob`, `NewStartProcessing`, `NewCompleteJob`, `NewFailJob` — all six already depend only on the `domain.VideoJobRepository` interface, so no constructor signature changes.
- [ ] 2.2 Confirm (don't assume) that existing `cmd/api` HTTP tests (`video_test.go`, `main_test.go`) still pass unchanged with the wrapped repository in place — they exercise behavior through the same interface, so no test should need to change; if any does, that's a signal the wrapping leaked somewhere it shouldn't have.

## 3. Dependency rules

- [ ] 3.1 Confirm (or extend, if a dependency-rules test enumerates allowed importers explicitly) that `internal/video/infrastructure/cache` may import `internal/video/domain` and `internal/platform/redis`, and that neither `internal/video/domain` nor `internal/video/application` imports `internal/video/infrastructure/cache` — mirroring how `internal/video/infrastructure/idempotency` is already enforced in `internal/video/dependency_rules_test.go`.

## 4. Local dev & CI infrastructure

- [ ] 4.1 Confirm (not assume) `docker-compose.yml`'s `app`/`app-test` services need no new environment variables — this change reuses the existing `REDIS_ADDR`/`REDIS_TEST_ADDR` values already wired for idempotency and rate limiting.
- [ ] 4.2 Confirm `.github/workflows/ci.yml` needs no change — `REDIS_TEST_ADDR: localhost:6379` already covers this change's integration tests, same as the prior two Redis features.

## 5. Verification

- [ ] 5.1 `go vet ./...` passes.
- [ ] 5.2 `go test ./... -v` passes locally via `docker compose run --build --rm app-test go test ./... -v`.
- [ ] 5.3 Manual smoke test: run the real `app` service, create a job via `POST /upload` (or `POST /api/video-jobs`), poll `GET /api/video-jobs/:id` twice in a row before any state change and confirm both responses match (no observable behavior change from the caller's perspective); separately, confirm a poll made immediately after the job's status changes (e.g. once `/upload` completes processing) reflects the new status, not a stale cached one.

## 6. Finalization (separate PR, per repo-workflow)

- [ ] 6.1 Promote this change's `videojob-status-cache` delta spec into `openspec/specs/videojob-status-cache/spec.md` (new), then archive the change folder.
- [ ] 6.2 Update `docs/architecture.md`: "Infrastructure Components" table (Redis row: note all three Phase 4 features are now implemented), Request flow / `VideoJobRepository` wiring note (mention the cache decorator wraps the PostgreSQL repository in `setupVideo`).
- [ ] 6.3 Update `docs/operations.md`: Redis section notes 3 of 3 Phase 4 features shipped (idempotency keys, rate limiting, status cache) — no new environment variables to document, since the TTL is a fixed constant.
- [ ] 6.4 Update `docs/roadmap.md`: add this change as a new row under the Phase 4 Change Backlog section (below `add-rate-limiting-middleware`), archived status with links; update the Phase Summary table's Phase 4 row and the "Current State" prose — Phase 4 likely flips from "Started" to "Done" since this is its third and last planned feature.
- [ ] 6.5 Update `CLAUDE.md` only if its existing Architecture/Notable-constraints sections make a claim this change invalidates (confirm rather than assume).
