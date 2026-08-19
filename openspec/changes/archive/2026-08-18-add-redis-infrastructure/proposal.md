## Why

Phase 4 (idempotency keys on `POST /upload`, rate-limiting middleware, a status cache for `GetJobStatus`) has no Redis connectivity to build against yet. `openspec/specs/ddd-architecture/spec.md`'s "Redis Responsibilities Are Additive" requirement already documents the target behavior, but nothing in the codebase can satisfy it without a connection to depend on first. This mirrors how `add-videojob-infrastructure` (Phase 3) had to land before any use case could persist a `VideoJob` — the three Phase 4 feature changes that follow this one all need this adapter, not the other way around.

Distributed locking for worker job pickup — the fourth Redis responsibility the canonical spec's wording still implies — is deferred to Phase 6: there is no `cmd/worker` yet for workers to contend over, so a lock has no consumer until then. `docs/roadmap.md`'s Phase Summary reflects this same deferral once this change's finalization PR lands (see `tasks.md`), so the canonical spec delta below and the human-readable roadmap summary change together rather than the summary landing ahead of the decision it's supposed to summarize.

## What Changes

- New `internal/platform/redis/` package: `Config`/`LoadConfigFromEnv` (reads `REDIS_ADDR`), `Open` (returns a configured client — no connectivity check), `Close`, and a `Ping`-based health check. This is bare connection plumbing only — no idempotency store, rate limiter, or cache decorator. Those are owned by whichever future change actually implements each Phase 4 feature (idempotency and status-cache logic under `internal/video/infrastructure/`, since both wrap video-context use cases; rate-limiting middleware directly in `cmd/api`, since it isn't a bounded-context concern).
- `internal/platform/` is a new top-level namespace under `internal/`, sibling to `identity/` and `video/`. It holds genuinely cross-cutting **infrastructure** plumbing (a single shared Redis connection, used by consumers across contexts) — not a shared **domain** kernel. `openspec/specs/ddd-architecture/spec.md`'s existing rejection of a shared kernel (see `add-videojob-domain-and-application`'s design.md) was specifically about domain value objects like `UserID` crossing bounded contexts; it does not speak to shared technical infrastructure, so this does not reopen or contradict that decision.
- New dependency: `github.com/redis/go-redis/v9`.
- `docker-compose.yml` gains a `redis` service (mirroring the existing `postgres` service's shape: pinned image, healthcheck, loopback-appropriate defaults), and `.github/workflows/ci.yml`'s `test` job gains a matching `redis` service. Both are consumed only by `internal/platform/redis`'s own tests via a `REDIS_TEST_ADDR`-style env var — no `cmd/api`/`cmd/worker` composition-root wiring yet, since nothing calls this package outside its own tests until a Phase 4 feature change does.
- `openspec/specs/ddd-architecture/spec.md`: reword the "Redis Responsibilities Are Additive" requirement's "four defined responsibilities" to reflect the three that remain in Phase 4 scope (idempotency, rate limiting, status cache) plus a note that distributed locking is deferred to Phase 6; document `internal/platform/` as the recognized location for cross-cutting infrastructure plumbing in the "Monorepo Package Topology" requirement.

## Capabilities

### New Capabilities
- `redis-infrastructure`: the shared, low-level Redis connection adapter (`internal/platform/redis`) — configuration, connection lifecycle, and health check. No feature-specific behavior.

### Modified Capabilities
- `ddd-architecture`: "Redis Responsibilities Are Additive, Not a Replacement for PostgreSQL or RabbitMQ" requirement's responsibility count and scope (distributed lock deferred to Phase 6); "Monorepo Package Topology Is the Target Structure" requirement gains a scenario documenting `internal/platform/` as the home for shared infrastructure plumbing that no single bounded context owns.

## Impact

- New files only, under `internal/platform/redis/`, plus `docker-compose.yml`/`ci.yml` service and env var additions. No existing Go source is modified.
- New third-party library dependency: `github.com/redis/go-redis/v9`, in `go.mod`/`go.sum`. Not yet an application runtime dependency — `cmd/api`/`cmd/worker` never open a Redis connection in this change, so nothing in the running application depends on Redis being reachable. A Redis instance (local dev via `docker-compose.yml`, CI via a service container) is only a dependency of `internal/platform/redis`'s own test suite until a Phase 4 feature change actually wires the client in.
- No `cmd/api`/`cmd/worker` wiring, no behavior change to any existing endpoint.
