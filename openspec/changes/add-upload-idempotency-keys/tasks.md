## 1. Domain

- [ ] 1.1 `internal/video/domain/idempotency_key.go`: `IdempotencyKey` value object (`UserID` + content hash), constructor validates both are non-empty, `String()` renders `idempotency:{userID}:{hash}`.
- [ ] 1.2 `internal/video/domain/idempotency_store.go`: `IdempotencyStore` port — `Reserve(ctx, key IdempotencyKey) (reserved bool, err error)`, `Finalize(ctx, key IdempotencyKey, jobID VideoJobID) error`, `Lookup(ctx, key IdempotencyKey) (jobID VideoJobID, found bool, err error)`, `Clear(ctx, key IdempotencyKey) error`.
- [ ] 1.3 Unit tests for `IdempotencyKey`'s constructor (mirroring `OriginalFilename`/`StorageKey`'s existing value-object test style).

## 2. Infrastructure

- [ ] 2.1 `internal/video/infrastructure/idempotency/redis_store.go`: `RedisStore` implementing `domain.IdempotencyStore` against `*redis.Client` (from `internal/platform/redis`). `Reserve` uses `SET key "processing" NX EX <sentinel-ttl>` (sentinel TTL: a few minutes, comfortably longer than `CreateVideoJob`'s own latency — pick and document a concrete value, e.g. 5 minutes). `Finalize` uses `SET key <jobID> EX <86400>` (24h, unconditional overwrite — caller already holds the reservation). `Lookup` uses `GET key`, returning `found=false` on a miss and treating the literal `"processing"` sentinel value as `found=false` too (an in-flight reservation is not yet a real job to return). `Clear` uses `DEL key`.
- [ ] 2.2 Integration tests against `REDIS_TEST_ADDR` (skip with a clear message if unset, matching `internal/platform/redis`'s own test pattern): reserve-then-finalize round-trip, reserve-fails-when-already-reserved, lookup-miss, lookup-sentinel-treated-as-miss, clear-then-lookup-miss, TTL is set on finalize (assert via `TTL` command or equivalent).

## 3. Transport (`cmd/api`)

- [ ] 3.1 `cmd/api/video.go`'s `videoModule`/`newVideoModule`: add an `idempotency domain.IdempotencyStore` field, threaded through the constructor like `createVideoJob`/`processVideoJob` etc.
- [ ] 3.2 `setupVideo`: load `internal/platform/redis.LoadConfigFromEnv()` (fails startup on missing `REDIS_ADDR`, same class of error as the existing `VIDEO_POSTGRES_DSN` check), `Open` the client, construct `idempotency.RedisStore`, wire it into `newVideoModule`.
- [ ] 3.3 `handleVideoUpload`: after computing `safeFilename`/opening the destination file, wrap the `io.Copy` source with `io.TeeReader` into a `sha256.New()` hasher; after the copy completes, finalize the hash and build the `IdempotencyKey` from `userID` + hash.
- [ ] 3.4 `handleVideoUpload`: call `idempotency.Reserve`; on `reserved=false`, delete the just-saved file, remove its owner sidecar (mirroring the existing cleanup calls elsewhere in this handler), and respond `409 Conflict` with a Portuguese-language `ProcessingResult{Success: false, Message: "..."}` (per this repo's user-facing-copy language policy).
- [ ] 3.5 `handleVideoUpload`: call `idempotency.Lookup` — if it already holds a `VideoJobID` (finalized, not a fresh reservation this request just made), delete the just-saved file/sidecar, fetch that job's current status via `getJobStatus`, and return it without calling `CreateVideoJob`/`ProcessVideoJob`. This check happens before the `Reserve` call in 3.4 (a finalized key means someone else already succeeded — no need to reserve first).
- [ ] 3.6 `handleVideoUpload`: on successful `CreateVideoJob`, call `idempotency.Finalize` with the new `VideoJobID` before proceeding to `ProcessVideoJob`.
- [ ] 3.7 `handleVideoUpload`: on the `FailJob` path(s) — both `ProcessVideoJob`'s own extraction failure and the existing post-processing `FailJob` call (ownership recording failure) — call `idempotency.Clear` for this request's key.
- [ ] 3.8 HTTP/integration tests in `cmd/api/video_test.go`: duplicate request while first is in-flight (`409`), duplicate after completion (returns existing job, no new `ffmpeg` run — assert via job count or a test double), retry after failure succeeds (creates a new job), two different users with identical content both succeed independently.

## 4. Local dev & CI infrastructure

- [ ] 4.1 `docker-compose.yml`: add `REDIS_ADDR` to the `app` and `app-test` services' environment (pointing at the existing `redis` service, `redis:6379` — added by `add-redis-infrastructure` but not yet consumed by `app`/`app-test`), and add `redis` to both services' `depends_on` with `condition: service_healthy`.
- [ ] 4.2 `.github/workflows/ci.yml`: add `REDIS_TEST_ADDR`'s sibling, `REDIS_ADDR` (pointing at `localhost:6379`, same CI `redis` service `add-redis-infrastructure` already added), to the `Test` step's env — `cmd/api`'s own tests now require it to start.

## 5. Verification

- [ ] 5.1 `go vet ./...` passes.
- [ ] 5.2 `go test ./... -v` passes locally via `docker compose run --build --rm app-test go test ./... -v`.
- [ ] 5.3 Confirm `internal/video/dependency_rules_test.go` still passes — `internal/video/infrastructure/idempotency` may import `internal/platform/redis` and `internal/video/domain`, but nothing in `internal/video/domain`/`internal/video/application` may import it back.
- [ ] 5.4 Manual smoke test (or scripted via `curl`): upload the same file twice in quick succession as the same authenticated user, confirm the second response reflects the first job's status rather than a second `ffmpeg` run (e.g. via timing, or by checking only one `VideoJob` row exists for that content).

## 6. Finalization (separate PR, per repo-workflow)

- [ ] 6.1 Promote this change's `upload-idempotency` delta spec into `openspec/specs/upload-idempotency/spec.md` (new), then archive the change folder.
- [ ] 6.2 Update `docs/architecture.md`: "Infrastructure Components" table (Redis row: move from "connection adapter implemented, features planned" to noting the first feature — idempotency keys — is now implemented), "Target Package Topology" tree (add `internal/video/infrastructure/idempotency/`), Request flow section (`POST /upload`'s step list gains the idempotency check/reserve/finalize steps), Routes/environment-variable notes (`REDIS_ADDR` now required for `cmd/api`, not just `internal/platform/redis`'s own tests).
- [ ] 6.3 Update `docs/operations.md`: `REDIS_ADDR` environment variable entry (required for `cmd/api`, not just optional/test-only), Redis section moves from "features Planned" to noting idempotency keys shipped (2 of 3 features remain planned: rate limiting, status cache).
- [ ] 6.4 Update `docs/roadmap.md`: add this change as a new row under the Phase 4 Change Backlog section (below `add-redis-infrastructure`), archived status with links.
- [ ] 6.5 Update `CLAUDE.md` only if its existing Architecture/Notable-constraints sections make a claim this change invalidates (expected: the "Notable constraints / gotchas" section may need a note that `POST /upload` now has idempotency behavior — confirm rather than assume).
