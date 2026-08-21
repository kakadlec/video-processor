## Context

`internal/platform/redis` (shipped by `add-redis-infrastructure`) provides bare connection plumbing. Two features already consume it: `internal/video/infrastructure/idempotency` (scoped to `POST /upload`'s content-hash dedup) and `internal/platform/ratelimit` (cross-cutting, mounted on every authenticated route). This change adds Phase 4's third and final Redis responsibility — a status cache for `VideoJob` lookups — closing out the set of scenarios `openspec/specs/ddd-architecture/spec.md`'s "Redis Responsibilities Are Additive" requirement describes.

The `domain.VideoJobRepository` interface (`internal/video/domain/repository.go`) is small and already the sole dependency of every use case in `internal/video/application` (`GetJobStatus`, `ListUserJobs`, `EnqueueVideoJob`, `StartProcessing`, `CompleteJob`, `FailJob`). Its four state-transition use cases all converge on a single write method, `Update`, after loading the job through `FindByID`. This shape makes a decorator around the repository — rather than a change to any use case — the natural implementation seam: one wrapper gets cache-aside reads and write-through invalidation for free across every code path that touches a `VideoJob`, with zero changes to `internal/video/application` or `internal/video/domain`.

`postgres.Repository.scanJobRow` (the existing PostgreSQL adapter) shows the exact reconstruction discipline a cached value must also follow: each stored field is re-validated through its domain constructor (`domain.NewUserID`, `domain.NewOriginalFilename`, `domain.NewStorageKey` — skipped when empty — `idParser.ParseVideoJobID`) before `domain.RestoreVideoJob` assembles the aggregate. A cache deserializer that skipped this and only trusted the JSON blob would let a corrupted or stale cache entry produce an invalid aggregate silently; mirroring the same validation on the way out of Redis keeps that impossible.

## Goals / Non-Goals

**Goals:**
- Serve repeated `GetJobStatus` polling reads for an unchanged job from Redis instead of PostgreSQL.
- Keep the cache always consistent with the latest write via write-through, not merely time-bound via TTL.
- Reuse the existing shared Redis client (`setupVideo`'s connection) — no new required env var, no second connection.
- Require no changes to `internal/video/domain` or `internal/video/application` — the decorator satisfies the existing `domain.VideoJobRepository` interface unchanged.

**Non-Goals:**
- Caching `FindByUserID` (the `GET /api/video-jobs` list endpoint). The canonical requirement's scenarios all describe single-job status polling; a list cache would need per-user invalidation fanning out from every job's own transition (harder to keep correct) for a read path with no documented hot-polling behavior like single-status lookups have. A future change can revisit this if list-endpoint load ever becomes a real problem.
- A configurable TTL via environment variable. Unlike `add-rate-limiting-middleware`'s `Config`/`LoadConfigFromEnv`, this change's correctness comes from write-through-on-every-write, not from TTL tuning — a fixed constant is simpler and there's no operator-facing trade-off to expose.
- A distributed cache-invalidation protocol for multiple `cmd/api` instances or a future `cmd/worker` (Phase 6). Every write to a `VideoJob` already goes through this same decorator process-locally today (no worker exists yet); cross-process invalidation is out of scope until Phase 6 introduces concurrent writers, at which point the existing single-process write-through stops being sufficient on its own — noted as a risk below, not solved here.

## Decisions

### 1. Decorator around `domain.VideoJobRepository`, in `internal/video/infrastructure/cache` — not `internal/platform`

Unlike rate limiting (a generic HTTP-layer concern applicable to any authenticated route), this is a `VideoJob`-specific concern: it caches values of the `domain.VideoJobRepository` port that only this bounded context defines. Placing it in `internal/video/infrastructure/cache` mirrors `internal/video/infrastructure/idempotency`'s precedent — a new adapter satisfying an existing domain interface, wired in only from `cmd/api`, with the same dependency-rule enforcement (`internal/video/domain`/`internal/video/application` may not import it).

Alternative considered: caching inside `GetJobStatus.Execute` itself (application-layer cache). Rejected — it would only cover the one use case that calls `FindByID` for a read, missing the four transition use cases' own `FindByID` calls before their `Update`, and would require `internal/video/application` to depend on a cache port, which the domain has no independent reason to need (`VideoJobRepository` already exists as its sole persistence port).

### 2. Cache-aside on `FindByID`, write-through on `Update`

`FindByID`: Redis `GET` on a per-job key first. Hit → deserialize (revalidating every field through the domain constructors, per `scanJobRow`'s pattern above) and return without a PostgreSQL round trip. Miss, or a deserialization/Redis error → fall back to the inner repository's `FindByID` and repopulate the cache with the result before returning — satisfying the canonical "PostgreSQL is authoritative on cache miss" scenario, and failing open on any cache-layer problem rather than surfacing it as a lookup failure.

`Update`: call the inner repository's `Update` first — the PostgreSQL write must succeed before anything else happens, since PostgreSQL remains the authority. Once it succeeds, `SET` the cache entry to the job's new serialized state (the `*VideoJob` in hand already has it — no extra read needed) rather than just deleting the old entry. This satisfies the canonical "cache invalidation is tied to state transition writes" scenario more strongly than a bare invalidate-and-let-next-read-repopulate would: the very next poll after a transition is already a cache hit with the fresh value, instead of a guaranteed miss.

If the cache `SET` itself fails (a transient Redis error), `Update` still returns success — the PostgreSQL write already committed, which is what correctness depends on. The error is logged, matching the fail-open posture `add-upload-idempotency-keys`'s design.md already established for non-fatal Redis-layer failures ("proceed anyway, log it").

`Create` and `FindByUserID` pass straight through uncached: a freshly created job gets naturally cache-aside-populated by its first `FindByID`/`GetJobStatus` call, and the list endpoint is out of scope (Non-Goals).

### 3. Repository-level cache value, not a `GetJobStatusResult` DTO

The cached payload must carry everything `domain.RestoreVideoJob` needs — `id`, `userID`, `originalFilename`, `storageKey`, `frameCount`, `errorReason`, `status`, `createdAt` — not just the `Status` field `GetJobStatusResult` exposes, because the same cache entry also serves `FindByID` calls made by the four transition use cases (which need the full aggregate to call `job.Enqueue()`/`StartProcessing()`/etc.). JSON-encode an internal struct mirroring `postgres.Repository`'s own column set; no new serialization dependency, matching every other Redis value format already used in this codebase (plain strings/prefixed strings for idempotency, Lua-script-returned integers for rate limiting — JSON is the natural choice for a multi-field record, and `encoding/json` is already in the standard library this project uses elsewhere).

### 4. Key format and fixed TTL

Key: `"videojob:status:" + jobID.String()` — same `"<domain>:<purpose>:<id>"` shape `add-rate-limiting-middleware`'s `"ratelimit:" + userID.String()` already established.

TTL: a fixed 5-minute constant, applied on every `SET` (both the cache-aside repopulation on miss and the write-through on `Update`). Its only job is bounding the lifetime of an entry in scenarios the write-through path doesn't cover — e.g. a future write path that bypasses this decorator, or a Redis restart that somehow retains a value without its expiry (defense in depth, not a correctness requirement given every current write goes through `Update`). Five minutes is comfortably longer than a realistic polling interval but short enough that a hypothetically-stale entry self-heals quickly; not exposed as configuration (see Non-Goals).

### 5. Wiring: `setupVideo` wraps the repository once, before constructing use cases

`cmd/api/video.go`'s `setupVideo` already opens the shared `*redis.Client` (for idempotency) and constructs `postgres.Repository`. This change adds one line: wrap the repository with `cache.NewCachedVideoJobRepository(repo, redisClient)` and pass the wrapped value to every use case constructor that currently takes the raw repository (`NewGetJobStatus`, `NewListUserJobs`, `NewEnqueueVideoJob`, `NewStartProcessing`, `NewCompleteJob`, `NewFailJob`) — all of them already declare their dependency as the `domain.VideoJobRepository` interface, so none of their signatures change.

## Risks / Trade-offs

- **[Risk]** Write-through correctness relies on every write to a `VideoJob` going through this one decorator instance. If a future change (e.g. Phase 6's `cmd/worker`) adds a second process that writes to PostgreSQL directly or through its own decorator instance, this process-local write-through no longer guarantees the cache reflects the latest write — a stale read becomes possible. → **Mitigation**: out of scope today (no second writer exists — Phase 6 is unstarted); flagged as a Non-Goal above so the gap is visible when that phase is scoped, rather than silently inherited.
- **[Risk]** A Redis outage during `Update`'s write-through `SET` could, in principle, be mistaken for a cache staying wrongly populated with pre-transition data if the `SET` is skipped without also invalidating (e.g. a bare `DEL` fallback). → **Mitigation**: on a `SET` failure, also attempt a best-effort `DEL` of the same key so a partially-failed write-through degrades to a cache miss (falls back to PostgreSQL, which has the correct data) rather than silently leaving a stale hit in place; both failures are logged and neither blocks the use case's success path.
- **[Risk]** Caching the full aggregate (not just `Status`) means a schema-shape change to `VideoJob` (e.g. a new field) requires updating this cache's (de)serialization in lockstep with `postgres.Repository`'s column mapping — two places instead of one. → **Mitigation**: acceptable; the same is already true of `postgres.Repository` versus the domain constructors it calls, and the codebase already tests that kind of round-trip explicitly (mirrored here too — see tasks.md).
- **[Risk]** Fixed TTL means an operator can't tune cache memory/staleness trade-offs without a code change. → **Mitigation**: accepted per Non-Goals — this change's correctness doesn't depend on the TTL value, so there is no operational trade-off worth exposing as configuration yet.

## Migration Plan

No data migration — the cache is purely additive and self-populating (cache-aside on first miss). Deploying this change adds a new Redis key namespace (`videojob:status:*`) with a bounded TTL; rollback is a plain revert (stop wrapping the repository in `setupVideo`), with no state to unwind since every cache entry expires on its own.

## Open Questions

None — placement, algorithm, key/TTL scheme, and wiring are all settled above.
