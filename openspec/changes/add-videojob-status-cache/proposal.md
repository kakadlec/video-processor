## Why

`GetJobStatus` (`internal/video/application`) hits PostgreSQL on every call, and `GET /api/video-jobs/:id` is the endpoint a client polls repeatedly while a job is `pending`/`queued`/`processing` — often at a fixed interval until it observes `completed`/`failed`. This is exactly the polling-read load the canonical roadmap (`openspec/specs/ddd-architecture/spec.md`'s "Redis Responsibilities Are Additive" requirement) already anticipates with its "Status cache absorbs repeated polling reads" scenario, one of Phase 4's three planned Redis responsibilities. The other two (idempotency keys, rate limiting) are already shipped; this closes out Phase 4's Redis feature set.

## What Changes

- New `internal/video/infrastructure/cache` package: `CachedVideoJobRepository`, a decorator implementing `domain.VideoJobRepository` around the real (PostgreSQL) repository, backed by the already-shared `*redis.Client` (`internal/platform/redis`, opened once in `cmd/api/video.go`'s `setupVideo` — no second connection, no new required env var).
  - `FindByID`: cache-aside — Redis hit returns without touching PostgreSQL; miss falls back to PostgreSQL and repopulates the cache.
  - `Update`: write-through — PostgreSQL write happens first, then the cache entry is set to the job's new state, keeping the cache always consistent with the latest write rather than merely invalidating it.
  - `Create` and `FindByUserID`: pass straight through, uncached (see Impact/non-goals).
  - A short, fixed TTL (not env-configurable) backstops the entry as a safety net; correctness comes from write-through on every state transition, not from the TTL.
- `cmd/api/video.go`'s `setupVideo` wraps the existing `postgres.Repository` with `CachedVideoJobRepository` before handing it to the use case constructors — `internal/video/application` and `internal/video/domain` are unchanged, since every use case already depends only on the `domain.VideoJobRepository` interface.

## Capabilities

### New Capabilities
- `videojob-status-cache`: Redis-backed, cache-aside/write-through caching of `VideoJob` lookups by ID, with PostgreSQL remaining authoritative on any cache miss.

### Modified Capabilities
(none — this adds a new capability without changing the observable behavior of any existing one; `GET /api/video-jobs/:id` and `GET /api/video-jobs` still return the same data, just sometimes served from cache for the single-job lookup)

## Impact

- **New code**: `internal/video/infrastructure/cache/repository.go` (+ tests). No changes to `internal/video/domain` or `internal/video/application`.
- **Changed wiring**: `cmd/api/video.go`'s `setupVideo` (wraps the repository before passing it to use case constructors).
- **Out of scope (non-goals)**: caching `FindByUserID` (the `GET /api/video-jobs` list endpoint) — only single-job lookups are cached; a configurable TTL via environment variable — a fixed constant is used instead, deliberately simpler than `add-rate-limiting-middleware`'s `Config`/`LoadConfigFromEnv` pattern, since correctness here doesn't depend on the TTL value.
- **Dependencies**: none new — reuses `github.com/redis/go-redis/v9`, already a direct dependency via `internal/platform/redis`.
- **Docs** (finalization PR only): `docs/architecture.md`, `docs/operations.md`, `docs/roadmap.md` (Phase 4 likely flips from "Started" to "Done" — this is the third and last of its three Redis features), `CLAUDE.md` if applicable.
