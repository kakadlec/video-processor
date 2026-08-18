## Context

`openspec/specs/ddd-architecture/spec.md`'s "Redis Responsibilities Are Additive" requirement (written in Phase 1, before any Redis code existed) already describes idempotency keys, rate limiting, and a status cache as target behavior. Nothing in the repository can implement any of that yet — there is no Redis connection anywhere in the codebase. This change adds only the connection plumbing; the three Phase 4 feature changes that consume it are proposed separately, once this lands.

The existing precedent for this kind of infra-first slice is `add-videojob-infrastructure` (Phase 3): it added the PostgreSQL adapter and schema with zero `cmd/api` wiring, and a later change (`wire-videojob-http-endpoints`) connected it to the HTTP layer. This change follows the same shape for Redis.

Unlike PostgreSQL, Redis here has no natural single owning bounded context. Two of the three planned consumers (idempotency on `POST /upload`, the `GetJobStatus` cache) are Video Processing concerns; the third (rate-limiting middleware) is a pure HTTP-transport concern with no domain relationship at all. `docs/architecture.md`/`ddd-architecture` spec explicitly reject a shared *domain* kernel between contexts (each context owns its own `UserID`, etc.) — but they say nothing about shared *infrastructure* plumbing, and a single physical Redis instance is exactly that: one shared technical resource, not a domain concept.

## Goals / Non-Goals

**Goals:**
- A minimal, connectable Redis client adapter, configured from the environment, with a health check.
- `docker-compose.yml` and CI gain a real Redis service so the adapter's own tests run against the genuine thing, not a mock — matching how the PostgreSQL adapter is tested.
- Establish `internal/platform/` as the recognized location for cross-cutting infrastructure that no single bounded context owns, and say so explicitly in the canonical spec, so this isn't a silent precedent future changes have to reverse-engineer.

**Non-Goals:**
- No idempotency store, rate limiter, or cache decorator — those are business logic for specific use cases, owned by whichever context/layer actually needs them, and land in their own future changes.
- No `cmd/api`/`cmd/worker` wiring. Nothing calls this package outside its own tests until a consumer change does.
- No production auth/TLS/cluster configuration. Local dev and CI use an unauthenticated, loopback-only Redis container, matching the existing PostgreSQL service's "fixed, non-secret local-only defaults" posture. Production deployment configuration is `docs/operations.md`'s concern, when this is actually deployed (same split already used for PostgreSQL).
- No abstract cache/lock port defined in any bounded context's `domain` layer. Defining a port before a concrete use case exists to consume it would be speculative — each Phase 4 feature change defines the port it actually needs, in the layer that owns it.

## Decisions

**1. Package location: `internal/platform/redis`, not `internal/video/infrastructure/redis` or an inline client in `cmd/api`.**
Rate-limiting has no relationship to the Video Processing bounded context, so nesting the shared connection under `internal/video/` would make an unrelated context reach into `video/infrastructure` for a client it needs for its own reasons. An inline client instantiated directly in `cmd/api` (no package at all) was also considered — simplest, but it would force every future consumer package (which live in `internal/video/infrastructure/...` or elsewhere) to receive an already-constructed `*redis.Client` with no shared place to put `Config`/health-check code reused by more than one caller, and no home for this package's own connectivity tests independent of `cmd/api`. `internal/platform/` is new but not unprecedented in spirit — `cmd/api`'s own `identity.go`/`video.go` already play a composition-root role per bounded context; `internal/platform` is the analogous home for infrastructure that belongs to no context.

**2. Client library: `github.com/redis/go-redis/v9`.**
It's the de facto standard Go Redis client — context-aware API (matches this codebase's consistent use of `context.Context` through `pgx`/`exec.CommandContext`), actively maintained, and requires no additional wrapping layer. Alternatives considered: `gomodule/redigo` (older, connection-pool API predates Go's `context` idioms) and `redis/rueidis` (newer, RESP3-first, but far less precedent/adoption to lean on for a hackathon-scoped codebase). No strong reason to deviate from the standard choice.

**3. Configuration: single `REDIS_ADDR` env var (`host:port`), not a full connection-string DSN.**
PostgreSQL's `DSN` shape is a Postgres convention (`postgres://user:pass@host:port/db`); Redis's own client library takes `redis.Options{Addr: "host:port"}` natively, with `Password`/`DB`/TLS fields set separately when actually needed. A DSN would just be parsed back apart. `Config` stays a small struct (`Addr string`, room to grow) so a later change can add `Password`/`TLS` fields without breaking this one's shape, mirroring how `postgres.Config` started with just `DSN`.

**4. `Open` returns a bare `*redis.Client`, with no `error` return; no interface/port defined here.**
Every future consumer already needs `context.Context`-based calls (`GET`/`SET`/`EXPIRE`/etc.) that `*redis.Client` provides directly. Wrapping it in an interface now, with no concrete second implementation or test double ever needed at this layer, would be an abstraction with no consumer — each future feature change defines whatever narrower interface its own use case needs (e.g., a `domain.IdempotencyStore` port in `internal/video/domain`), the same way `domain.VideoJobRepository` was defined against the domain's actual needs, not against `*sql.DB`'s shape. Unlike `postgres.Open` (which wraps `sql.Open`, itself capable of returning a driver-registration error), `Open` here only calls `redis.NewClient(&redis.Options{Addr: cfg.Addr})`, which cannot fail — `Config.Addr` is an unparsed string, nothing to validate before constructing the client. Giving `Open` an `error` return it can never actually produce would force every caller to handle an impossible failure; the postgres adapter's shape isn't a reason to copy a return signature that doesn't fit here.

**5. Test env var: `REDIS_TEST_ADDR`, following the `VIDEO_POSTGRES_TEST_DSN` naming precedent.**
`internal/platform/redis`'s own tests (`Open` + `Ping` against a real container) read `REDIS_TEST_ADDR`, set in `docker-compose.yml`'s `app-test` service and `.github/workflows/ci.yml`'s `test` job — exactly parallel to how the Postgres adapter's tests are wired today.

**6. `docker-compose.yml`: no named volume for Redis's data directory.**
PostgreSQL's `postgres_data` volume exists because it's the authoritative store — losing it loses real data. Redis here backs idempotency keys, rate-limit counters, and a status cache, all explicitly documented as non-authoritative (`ddd-architecture` spec: "PostgreSQL is authoritative on cache miss"). Losing Redis's contents on a container restart is a correctness non-event by design, so there's nothing worth persisting to a host volume.

## Risks / Trade-offs

- **[Risk]** `internal/platform/` could become a dumping ground for anything two contexts touch, eroding bounded-context boundaries over time. → **Mitigation:** this change and its spec delta scope it explicitly to shared *infrastructure connections*, not domain or application logic; each Phase 4 feature's actual behavior still lives in the context/layer that owns it, never in `platform`.
- **[Risk]** This package has no consumer immediately after merging — it's inert until a Phase 4 feature change lands. → **Mitigation:** identical shape to `add-videojob-infrastructure`, which shipped the same way and was consumed within the same phase's next changes; not speculative, just sequenced.
- **[Risk]** Adding a `redis` service to CI grows CI runtime and gives CI one more service that can flake. → **Mitigation:** same container-service mechanism already proven stable for `postgres` in this CI; Redis containers start faster than Postgres (no data directory init), so the added time is small.
- **[Risk]** No auth/TLS now means a future change adding production deployment config has to revisit `Config`'s shape. → **Mitigation:** deliberately deferred (see Non-Goals) — `docs/operations.md` doesn't yet describe deploying this service at all, so designing its production auth now would be speculative; `Config` is small enough to extend without breaking `LoadConfigFromEnv`'s existing callers when that day comes.

## Migration Plan

No data migration — this is new infrastructure with nothing depending on it yet. Rollout is: merge → `docker compose up` picks up the new `redis` service automatically → CI's `test` job gains the service. Rollback is a plain revert; no schema, no persisted data, no `cmd/api`/`cmd/worker` behavior change to unwind.

## Open Questions

None blocking. Production Redis deployment target (managed service vs. self-hosted, sizing, persistence policy) is out of scope until a change actually deploys this — the same treatment already given to PostgreSQL in `docs/operations.md`.
