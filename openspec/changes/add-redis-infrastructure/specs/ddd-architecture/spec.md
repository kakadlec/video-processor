## MODIFIED Requirements

### Requirement: Redis Responsibilities Are Additive, Not a Replacement for PostgreSQL or RabbitMQ

Redis SHALL be used as a mandatory performance and reliability layer with three defined responsibilities in Phase 4: idempotency keys, rate limiting, and a status cache. It SHALL NOT replace PostgreSQL as the authoritative state store, and it SHALL NOT replace RabbitMQ as the durable job queue. A fourth Redis-backed responsibility — a distributed lock coordinating concurrent `cmd/worker` instances picking up jobs — is deferred to Phase 6, since no worker exists to contend over job pickup until then; it is not in scope for Phase 4.

#### Scenario: Idempotency key prevents duplicate job creation

- **GIVEN** a client retries a `POST /upload` with the same content within the idempotency window
- **WHEN** the API processes the retry
- **THEN** Redis returns the existing `VideoJobID` and the handler returns the existing job without creating a duplicate or re-enqueuing

#### Scenario: Rate limiting rejects excess requests

- **GIVEN** a user exceeds the configured request rate
- **WHEN** their next request arrives
- **THEN** the rate-limiting middleware (backed by Redis) rejects it with HTTP 429 before it reaches the handler

#### Scenario: Status cache absorbs repeated polling reads

- **GIVEN** a client polls `GET /jobs/{id}/status` repeatedly
- **WHEN** the job state has not changed since the last DB write
- **THEN** the response is served from the Redis status cache without a PostgreSQL query

#### Scenario: Cache invalidation is tied to state transition writes

- **GIVEN** a job transitions to a new state and that transition is written to PostgreSQL
- **WHEN** the write succeeds
- **THEN** the Redis status cache entry for that job is invalidated or updated atomically with the DB write (or immediately after, within the same request/transaction scope)

#### Scenario: PostgreSQL is authoritative on cache miss

- **GIVEN** the Redis status cache has no entry for a job
- **WHEN** a status request arrives
- **THEN** the application falls back to PostgreSQL, returns the correct current state, and may repopulate the cache

#### Scenario: Distributed worker lock is deferred, not dropped

- **GIVEN** Phase 6 introduces `cmd/worker` and RabbitMQ-driven job pickup
- **WHEN** more than one worker instance runs concurrently
- **THEN** a Redis-backed distributed lock (proposed as part of Phase 6, not Phase 4) SHALL prevent two workers from processing the same job

### Requirement: Monorepo Package Topology Is the Target Structure

The repository SHALL evolve toward a monorepo topology with `cmd/api` and `cmd/worker` as separate entrypoints sharing `internal/` packages. This topology SHALL NOT require a big-bang rewrite; `main.go` MAY remain functional during incremental migration. Cross-cutting **infrastructure** plumbing that no single bounded context owns (e.g. a shared Redis connection used by more than one context) lives under `internal/platform/`, distinct from any bounded context's own `internal/<context>/infrastructure/`. This is infrastructure sharing only — it does NOT permit sharing `domain` or `application` logic between contexts, which remains forbidden by the "No direct cross-context domain imports" scenario above.

#### Scenario: API and worker share domain and application packages

- **GIVEN** `cmd/api` and `cmd/worker` both exist in the repository
- **WHEN** they both need to work with `VideoJob`
- **THEN** they both import from `internal/video/domain` and `internal/video/application` — the domain logic is not duplicated

#### Scenario: Each cmd entrypoint produces an independent deployable binary

- **GIVEN** the monorepo topology is in place
- **WHEN** `go build ./cmd/api` and `go build ./cmd/worker` are run
- **THEN** each produces an independent binary that can be containerized and deployed separately

#### Scenario: cmd/api is the actual HTTP composition root

- **GIVEN** `main.go`, `identity.go`, and their test files have been moved into `cmd/api/`
- **WHEN** the server is built or run
- **THEN** `go build -o app ./cmd/api` and `go run ./cmd/api` produce the same HTTP behavior the repo-root `main.go` produced before the move, and no `main.go` exists at the repo root anymore

#### Scenario: Shared infrastructure with no owning context lives under internal/platform

- **GIVEN** a piece of infrastructure (e.g. a Redis connection) is used by more than one bounded context or by transport-level middleware with no bounded-context relationship at all
- **WHEN** it is added to the codebase
- **THEN** it lives under `internal/platform/`, not under any single `internal/<context>/infrastructure/`, and it contains only connection/lifecycle plumbing — never domain or application logic for a specific context's use case
