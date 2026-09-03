## Why

The Notification bounded context (Phase 7) is defined as a downstream consumer of `VideoJobCompleted` and `VideoJobFailed`, but neither event is written anywhere. `CompleteJob` and `FailJob` persist through `Repository.Update`, which `internal/video/domain/repository.go` documents as deliberately not an outbox writer precisely so that this phase would decide the events' shape on its own terms rather than inheriting one by accident. Nothing in Phase 7 can be built until a terminal outcome produces a durable, transactionally-consistent record of itself.

This change makes that record exist and reach the broker. It adds no consumer: the events accumulate on a declared queue until `add-notification-webhook-delivery` reads them.

## What Changes

- `Repository.Update` — the write path behind both `CompleteJob` and `FailJob`, and its only two callers — writes a `video_job_outbox` row in the same transaction as the fenced `UPDATE`, and only when that `UPDATE` affects a row. A write refused by the fence (`ErrJobFenced`) and a write that finds its own outcome already recorded (`applied == false`) each emit nothing, so the lease-recovery sweeper cannot produce a duplicate notification for a job a superseded worker also finished.
- Two new event types, `video_job.completed.v1` and `video_job.failed.v1`, carrying the field sets `ddd-architecture` already pins for `VideoJobCompleted` and `VideoJobFailed`. The generation suffix is on the event type from the start, for the same reason the dispatch generation carries one: `event_type` is the predicate every relay actually claims on.
- A second AMQP topology in `internal/video/infrastructure/messaging` — a terminal-event exchange, one durable queue bound to both routing keys, and the existing dead-letter sink. The queue is declared even though nothing consumes it yet, so that the relay's mandatory publishes are routable and the events wait durably rather than piling up unpublished.
- The outbox claim is scoped to an explicit **set** of event types instead of exactly one, and each row is published under the routing key its own `event_type` names. The queued dispatch becomes a one-element set with byte-identical behavior; the terminal relay claims the two new types with one connection and one poll loop.
- `cmd/worker` gains a relay for the terminal events, alongside its consumer. It is the process that writes those rows — the worker's own terminal writes and the sweeper's abandonment write — so co-locating the relay avoids making notification depend on an API replica being up. Its shutdown joins the relay before the pools close, matching `cmd/api`'s existing ordering.

No HTTP surface changes, no new environment variables, no change to what `POST /upload` returns, and no change to when the worker acknowledges a delivery.

## Capabilities

### New Capabilities
- `videojob-terminal-events`: the completion/failure integration-event contract — what each event carries, the exact conditions under which one is recorded, why a fenced or already-recorded outcome records nothing, the terminal-event topology, and where the relay carrying them runs.

### Modified Capabilities
- `videojob-persistence`: `Update Persists a VideoJob's Transitioned State` currently requires that `Update` write no outbox row. It now writes one transactionally, on exactly the paths where the conditional statement applied.
- `videojob-outbox-relay`: `The Claim Is Scoped to One Event Type` becomes a claim scoped to an explicit set of event types, with the same generation-isolation guarantee, and the published routing key is read from the claimed row rather than fixed per relay.
- `videojob-worker`: the worker's composition root and shutdown sequence gain the terminal relay — a second AMQP connection it owns, cancelled and joined before PostgreSQL and Redis close.

## Impact

- **Code**: `internal/video/infrastructure/postgres` (`repository.go`, `outbox.go`, `schema.sql`), `internal/video/infrastructure/messaging` (`topology.go`, `publisher.go`, `relay.go`), `internal/video/domain/repository.go` (port documentation), `cmd/worker/main.go`. `internal/video/application` is untouched — `CompleteJob`/`FailJob` keep calling `Update` and keep their `applied` semantics.
- **Data**: no schema migration to `video_jobs`. New rows in the existing `video_job_outbox` table under two new `event_type` values; the existing partial index answers them. No pre-existing backlog to retire, because both event types are new.
- **Broker**: one new exchange, one new durable queue, two new bindings, declared on every dial like the dispatch topology. Messages accumulate there until Phase 7's consumer ships.
- **Deployment**: `cmd/worker` opens a second AMQP connection. `RABBITMQ_URL` is already required at its startup; a reachable broker is still not a startup gate.
- **Docs**: `docs/roadmap.md` gains the Phase 7 Change Backlog section, `docs/architecture.md` and `docs/domain-model.md` record that the two integration events are now emitted, and `CLAUDE.md`'s outbox/relay description gains the terminal generation.
