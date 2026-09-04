# Tasks

Implementation order follows the dependency chain: the payload contract and the transactional write first, then the claim/publish generalization that carries them, then the topology and the worker wiring, then tests.

## 1. Event contract in `internal/video/infrastructure/postgres`

- [x] 1.1 Add `videoJobCompletedEventType = "video_job.completed.v1"` and `videoJobFailedEventType = "video_job.failed.v1"` alongside the existing dispatch constant, and export both the way `VideoJobQueuedEventType` is exported — `messaging` needs them for the topology's bindings and for the routing-key pinning test.
- [x] 1.2 Add `videoJobCompletedPayload` (`type`, `job_id`, `user_id`, `frame_count`, `storage_key`, `occurred_at`) and `videoJobFailedPayload` (`type`, `job_id`, `user_id`, `error_reason`, `occurred_at`) with the same JSON tag style as `videoJobQueuedPayload`.
- [x] 1.3 Add a marshal helper that selects payload and event type from the job's status, generating `occurred_at` once and returning it for reuse in the row. A status that is neither `completed` nor `failed` returns an error.
- [x] 1.4 Generalize `insertQueuedOutboxEvent` into an insert taking the event type, or add a sibling — whichever leaves `Enqueue`/`Requeue`'s shared-payload comment true and unweakened.

## 2. The transactional terminal write

- [x] 2.1 Rewrite `Repository.Update` to open a transaction, run the existing conditional statement unchanged, and commit. Marshal the payload before `BeginTx`, so a marshalling failure cannot abort a transaction that has already written.
- [x] 2.2 Insert the outbox row inside that transaction **only** when `RowsAffected() > 0`; otherwise roll back and hand off to `classifyRefusedUpdate` exactly as today, on the pool.
- [x] 2.3 Refuse a non-terminal status before `BeginTx`.
- [x] 2.4 Update the `Update` documentation on `domain.VideoJobRepository` — the "Unlike Create and Enqueue, it writes no `video_job_outbox` row" clause is now false — and the adapter's own comment. State the row-count gate as the contract, not as an implementation note.
- [x] 2.5 Confirm `cache.CachedVideoJobRepository.Update` needs no change: it delegates and forwards `applied`, and the event is written below it.

## 3. Claim and publish generalization

- [x] 3.1 `OutboxMessage` gains `EventType`; `Claim` takes `eventTypes []string` and filters with `event_type = ANY($1)`. Verify the `text[]` binding survives `database/sql` + the pgx stdlib driver against the real PostgreSQL; fall back to `string_to_array($1, ',')` if it does not, and record which was used in `design.md`'s Open Questions.
- [x] 3.2 `messaging.Message` gains a per-message routing key; `Publisher` publishes each message under its own key instead of the one it was constructed with. Keep the exchange a construction parameter.
- [x] 3.3 `Relay` holds an event-type set and a topology; `NewRelay` keeps its current signature for the dispatch relay and passes a one-element set. Add a constructor for the terminal relay.
- [x] 3.4 Confirm the `ORDER BY occurred_at` claim still reads oldest-first across the set and that the partial index answers the new predicate — check the plan, do not assume.

## 4. Terminal-event topology

- [x] 4.1 Replace `rabbitmq.Topology.RoutingKey` with `RoutingKeys []string` and bind each in `DeclareTopology`, per design decision 5. Reject an empty slice with an error. No name from this context enters `internal/platform/rabbitmq`.
- [x] 4.1a Update the existing dispatch topology to pass a one-element `RoutingKeys`, and add `TerminalEventsTopology` to `topology.go` with the exact names and bounds the spec pins (`video.jobs.terminal.v1`, `video.jobs.terminal.events.v1`, both routing keys), reusing the existing dead-letter exchange and queue.
- [x] 4.2 Give the terminal queue the same `x-max-length` + `reject-publish` bound as the job queue and no message TTL, and comment why the TTL is absent — the reason differs from the job queue's and is worth stating: an expired terminal event is an outcome that is never announced.
- [x] 4.3 Extend `TestRoutingKeyMatchesTheOutboxEventType` to assert the equality for every published event type, over a table rather than one pair.
- [x] 4.4 Assert `TerminalEventsTopology`'s returned descriptor field-by-field against the pinned table, the way the dispatch topology's values are pinned — names, both routing keys, and every argument.

## 5. `cmd/worker` wiring

- [x] 5.1 Construct the terminal relay in `setupWorker` from the already-open pool and the already-loaded `rabbitmq.Config`. No new environment variable.
- [x] 5.2 Start it as a goroutine under its own cancellable context, and in `run`'s shutdown cancel and **join** it before the PostgreSQL pool closes — same position as the sweeper, for the same reason.
- [x] 5.3 Confirm shutdown does not wait for the relay to drain: it stops mid-cycle and uncommitted work rolls back.

## 6. Tests

- [x] 6.1 `postgres`: `Update` commits the row and exactly one terminal outbox row, for `completed` and for `failed`.
- [x] 6.2 `postgres`: a fenced `Update` (advanced epoch) writes no outbox row and leaves the job unchanged.
- [x] 6.3 `postgres`: a repeated identical `Update` reports `applied == false` and leaves exactly one outbox row.
- [x] 6.4 `postgres`: two actors at the same epoch writing different outcomes produce one applied write and one outbox row between them — extend the existing same-epoch scenario rather than writing a parallel one.
- [x] 6.5 `postgres`: a non-terminal status is refused with no write of either kind.
- [x] 6.6 `postgres`: the payloads decode to the pinned field sets, with `occurred_at` equal to the row's column.
- [x] 6.7 `postgres`: `Claim` over a two-element set returns both types oldest-first and claims nothing outside the set — including the permanent `video_job.created` backlog and the dispatch rows.
- [x] 6.8 `messaging`: the publisher publishes each message under its own routing key; a message whose key matches no binding is reported unpublished. Skips cleanly without `RABBITMQ_TEST_URL`, like the existing tests.
- [x] 6.9 `messaging`: a contract test decoding each terminal payload struct against the `postgres` payload it pairs with, in the shape of `TestJobQueuedMessageDecodesTheOutboxPayload`. Add the consumer-side structs this change needs for that test and nothing more.
- [x] 6.10 `messaging`: the terminal topology is declared idempotently on redial, and both routing keys land on one queue.
- [x] 6.10a `platform/rabbitmq`: a descriptor naming two work-queue routing keys routes both to one queue, and a descriptor with an empty set is refused with nothing declared. Test-scoped names, torn down on failure, skipping cleanly without `RABBITMQ_TEST_URL`, like the rest of that package.
- [x] 6.11 `cmd/worker`: a job run to completion by the real handler leaves a terminal outbox row; the platform test asserting `internal/platform/` imports and names nothing context-specific still passes after task 4.1.
- [x] 6.12 Existing dispatch-path tests pass unchanged — that is the evidence the one-element set is behaviour-preserving. Do not adjust them to fit the new signature beyond the mechanical argument change.

## 7. Verification

- [x] 7.1 `go test ./... -v` passes locally with `ffmpeg`, MinIO, and a reachable `RABBITMQ_TEST_URL`, so that the messaging and relay packages are actually covered rather than skipped.
- [x] 7.2 `go vet ./...` clean; `gosec` and `govulncheck` clean via CI's required checks.
- [x] 7.3 End-to-end non-regression with the full stack: upload → poll → download still works, and a completed job leaves one message on the terminal queue and one stamped outbox row.
- [x] 7.4 Kill the worker mid-extraction, let the sweeper abandon the job at the bound, and confirm the terminal `failed` event is written once and published once.
- [x] 7.5 Confirm no `cmd/api` production file changed. `cmd/api`'s tests may change where they construct outbox rows or implement the repository port; that is not a scope violation.

## 8. Finalization (separate PR)

- [x] 8.1 Check off sections 1–7.
- [x] 8.2 `CLAUDE.md`: the outbox/relay bullet says the relay "is the only thing `cmd/api` opens an AMQP connection for" and that `cmd/worker` opens one for its consumer — the worker now opens two. Update the dispatch-generation paragraph to describe two generations, and the worker bullet's shutdown ordering to include the relay.
- [x] 8.3 `docs/architecture.md` and `docs/domain-model.md`: `VideoJobCompleted`/`VideoJobFailed` move from "Planned" to emitted, with the note that nothing consumes them yet. `domain-model.md`'s paragraph stating neither event is written anywhere is now the opposite of true.
- [x] 8.4 `docs/operations.md`: the terminal queue accumulates until Phase 7's consumer ships — document the expected depth, the `reject-publish` back-pressure symptom (rows with `published_at` NULL and a growing queue), and retiring nothing, since no generation is superseded.
- [x] 8.5 `docs/roadmap.md`: flip this row to archived with links, and mark Phase 7 as in progress in the Phase Summary.
- [x] 8.6 `openspec/specs/videojob-messaging/spec.md`: its purpose paragraph says the outbox relay publishes "from `cmd/api`" and that `cmd/worker` only consumes. That is prose rather than a requirement, so no delta carries it and it survives the archive unchanged unless corrected here.
- [x] 8.7 `openspec/specs/videojob-lifecycle/spec.md`: the `CompleteJob`/`FailJob` requirement describes the persistence step; confirm whether it claims eventlessness and correct it if so.
- [x] 8.8 `npx --yes @fission-ai/openspec validate emit-videojob-terminal-events --strict --no-interactive` passes, then `/opsx:archive`.
