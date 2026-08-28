## Why

Phase 6 turns `POST /upload` into a non-blocking endpoint served by a separate `cmd/worker`, and every step of that depends on a durable broker this repository has never connected to. `openspec/specs/ddd-architecture/spec.md` already names RabbitMQ as the durable job queue Redis must not replace, and `video_job_outbox` has been sitting in the schema since Phase 3 waiting for a relay to publish from — but nothing in the codebase can open an AMQP connection, so none of Phase 6's three remaining changes has anything to build against.

This mirrors `add-redis-infrastructure` and `add-minio-infrastructure`: the connection adapter lands first, alone, consumed only by its own tests, and the feature changes that need it follow. `docs/roadmap.md`'s Phase 6 section carries the full decomposition and the three dependents.

## What Changes

- New `internal/platform/rabbitmq/` package: `Config`/`LoadConfigFromEnv` (reads `RABBITMQ_URL`), `Open`, `Ping`, `Close`, and `DeclareTopology`. Bare connection plumbing and topology only — no publisher, no consumer, no outbox relay. Those belong to `add-videojob-source-key-and-outbox-relay` and `migrate-upload-to-async-processing`.
- The package sits in `internal/platform/`, alongside `redis` and `ratelimit`, rather than under `internal/video/infrastructure/` where MinIO lives. Phase 7's Notification context subscribes to this same broker, so the connection genuinely belongs to no single bounded context — the case `ddd-architecture`'s "Monorepo Package Topology" requirement already reserves `internal/platform/` for. MinIO sits in the video context for the complementary reason: every one of its consumers is Video Processing.
- `Open` connects, unlike `redis.Open`. There is no lazy AMQP client: the protocol requires a connection handshake before anything is usable, so `Open` returns an `error` and a live connection or nothing. This divergence from the Redis precedent is forced by the protocol, not chosen — and it is why `Ping` cannot be a mirror of Redis's either (see `design.md`).
- `DeclareTopology` declares, idempotently, the exchange, the job queue, a dead-letter exchange, and the dead-letter queue — with **both** queues bounded by the topology itself: a message TTL and a max length on each. A queue whose overflow policy dead-letters into an unbounded destination has relocated its growth rather than capped it, and the next change publishes into this topology a full merge before any consumer exists.
- New dependency: `github.com/rabbitmq/amqp091-go` (the RabbitMQ-maintained successor to `streadway/amqp`).
- `docker-compose.yml` gains a `rabbitmq` service and `.github/workflows/ci.yml`'s `test` job gains a matching one, consumed only by this package's own tests through `RABBITMQ_TEST_URL`. Unlike MinIO, RabbitMQ works as a CI `services:` container — its image needs no command arguments.
- No `cmd/api` or `cmd/worker` wiring, no new required startup configuration, and no behavior change to any existing endpoint.

## Capabilities

### New Capabilities
- `rabbitmq-infrastructure`: the shared, low-level AMQP connection adapter (`internal/platform/rabbitmq`) — configuration, connection lifecycle, health check, and idempotent topology declaration. No feature-specific publishing or consuming behavior.

### Modified Capabilities

None. Every existing RabbitMQ mention in `openspec/specs/` is forward-looking (`ddd-architecture`'s "Redis... SHALL NOT replace RabbitMQ as the durable job queue", the deferred worker lock, and the documentation-accuracy requirement), and all of them stay true: this change puts no queue in the running application's path. The first canonical statement that has to move is `videojob-execution`'s, and that belongs to the change that actually publishes.

## Impact

- New files only, under `internal/platform/rabbitmq/`, plus `docker-compose.yml` and `.github/workflows/ci.yml` service and environment additions. No existing Go source is modified.
- New third-party dependency `github.com/rabbitmq/amqp091-go` in `go.mod`/`go.sum`. Not yet an application runtime dependency: `cmd/api` never opens an AMQP connection in this change, so nothing in the running application depends on RabbitMQ being reachable. A broker (compose locally, a service container in CI) is a dependency of this package's own test suite only.
- `go test ./...` gains a fourth infrastructure prerequisite alongside PostgreSQL, Redis, and MinIO. `docker compose run --build --rm app-test go test ./... -v` continues to supply all of them; `docs/development.md` is updated in this change's finalization PR, per the repository's PR-separation rule.
