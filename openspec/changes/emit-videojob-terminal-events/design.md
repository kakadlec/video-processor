## Context

Every producer of a terminal `VideoJob` outcome already funnels through one statement. `CompleteJob` and `FailJob` are `Repository.Update`'s only two callers, and the sweeper's abandonment write goes through `FailJob` as well — so the worker's success, the worker's extraction failure, and the sweeper's abandonment are three paths into a single conditional `UPDATE ... WHERE id = $1 AND lease_epoch = $2 AND status = 'processing'`.

That statement is also where the system's fence lives. `applied == true` means this actor won the terminal outcome; `applied == false` with no error means the row already carried exactly this outcome (a retry finding its own earlier commit); `ErrJobFenced` means another actor won. Phase 7's correctness hinges entirely on the event being tied to the first of those three and to nothing else — the whole point of the lease-recovery design is that a superseded worker and its successor can both finish the same job, and a user must not be notified twice for it.

The existing relay is single-purpose in three places at once: `Relay` holds one `eventType`, `OutboxRepository.Claim` filters on one `event_type`, and `Publisher` is constructed with one fixed routing key. The routing key equals the `event_type` string by an invariant that `TestRoutingKeyMatchesTheOutboxEventType` pins across two packages that do not import each other.

## Goals / Non-Goals

**Goals:**
- A durable, transactionally-consistent record of `VideoJobCompleted` and `VideoJobFailed`, written exactly once per job outcome.
- Those records reach the broker and wait durably on a queue for a consumer that does not exist yet.
- No change to the worker's acknowledgement decision table, to `applied` semantics, or to any HTTP response.

**Non-Goals:**
- Any consumer, delivery channel, retry policy, or `NotificationPreference` — those are later Phase 7 changes.
- `UserRegistered` and anything in the Identity context.
- Publishing the accumulated `video_job.created` backlog, which stays deliberately unpublished.

## Decisions

### 1. `Update` writes the event, rather than a new sibling method

`internal/video/domain/repository.go` argues against making `Update` outbox-aware: it "would turn event publication into a side effect of a general-purpose write and would decide, in advance and by accident, what the completion and failure events look like." Both halves of that objection expire here. The shape is now being decided deliberately, and `Update` is not general-purpose in fact — its only two callers are the two terminal use cases, and its statement hardcodes `status = 'processing'` as the precondition, so it can persist nothing but a terminal transition.

The alternative, a sibling `UpdateTerminal` next to `Update` mirroring how `Enqueue` sits next to `Update`, was rejected because it would leave `Update` with zero callers. `Enqueue` earned its separate existence by having a distinct precondition and a distinct caller; a terminal sibling would have neither.

What survives from that comment is its actual concern, restated as a requirement: the event is a defined part of the terminal write's contract, not an incidental effect. `Update`'s port documentation is rewritten to say so.

### 2. The event is written only when the conditional statement applied

`Update` becomes transactional: begin, run the conditional `UPDATE`, and only if it affected a row insert the outbox event and commit. Zero rows affected rolls back and hands off to the unchanged `classifyRefusedUpdate`, which reads the stored row on the pool afterwards.

This binds the event to the fence with no second predicate to keep in sync — the same statement that decides who won is the one whose row count gates the insert. Both non-applying outcomes therefore emit nothing:

| Outcome | Row written | Event written |
|---|---|---|
| `applied == true` | yes | yes |
| `applied == false` (own outcome already recorded) | no | no |
| `ErrJobFenced` (another actor won) | no | no |

The second row is what makes a retried `CompleteJob` — the worker retries it 4× on a context detached from shutdown — safe to repeat. The third is what stops the sweeper's abandonment `failed` and a superseded worker's `completed` from both notifying.

A job reaching `Update` in a status that is neither `completed` nor `failed` is a defect rather than an event with no payload shape; it returns an error before opening the transaction.

### 3. Two event types, versioned from the start

`video_job.completed.v1` and `video_job.failed.v1`, with payloads exactly as `ddd-architecture` pins them:

```
completed: { type, job_id, user_id, frame_count, storage_key, occurred_at }
failed:    { type, job_id, user_id, error_reason,             occurred_at }
```

The generation suffix goes on the event type rather than on the exchange alone, for the reason `topology.go` already documents at length: every relay claims from one shared `video_job_outbox` filtered on `event_type` and nothing else, so the predicate is the only thing that actually separates generations. Starting at `.v1` costs nothing now and means a future payload change has an escape hatch that does not require renaming an exchange to isolate anything.

`occurred_at` is generated once, before the transaction opens, and used for both the row and the payload — the same discipline `Enqueue` follows.

### 4. The claim takes a set of event types; the routing key comes from the row

`Claim(ctx, eventTypes []string, limit)` replaces the single-type parameter, and `OutboxMessage` gains an `EventType` field that `Publisher` uses as the per-message routing key instead of the fixed one it is constructed with. The queued relay passes a one-element set and behaves identically.

Alternatives considered:
- **One relay instance per event type** (three total). Rejected: three AMQP connections and three poll loops for what is one logical stream, and the two terminal types must land on one queue in order anyway.
- **A single wildcard claim with no filter.** Rejected for the reason the filter exists: the permanent, never-shrinking `video_job.created` backlog would be re-read on every poll and, with a bounded batch, starve the rows meant to go out.

Because the routing key now travels with the row, `TestRoutingKeyMatchesTheOutboxEventType` generalizes into an assertion over the whole set of published types rather than one literal pair.

The claim predicate becomes `event_type = ANY($1)`. The partial index `video_job_outbox_unpublished_idx (event_type, occurred_at) WHERE published_at IS NULL` still answers it, as a per-value index scan rather than a single range scan; ordering by `occurred_at` across a set means "oldest first across the set", which is what the relay wants. Parameter binding for a `text[]` through `database/sql` + the pgx stdlib driver is to be confirmed against the real PostgreSQL the suite already runs on; `= ANY(string_to_array($1, ','))` is the fallback if the array binding does not survive that layer.

### 5. One topology for both terminal events, one queue

A new exchange, one durable queue bound to both routing keys, and the existing dead-letter sink (which deliberately carries no generation suffix, so every generation's dead letters land in one place).

The queue is declared now, two changes before anything consumes it. That is the point: the relay publishes **mandatory** and stamps `published_at` only for messages the broker both acknowledged and routed, so publishing into an exchange with no binding would return every message unroutable and leave the rows to be re-attempted every two seconds, forever. A declared, unconsumed queue holds them durably instead, bounded by the same `x-max-length` + `reject-publish` back-pressure the job queue uses — a full queue nacks, the row waits, nothing is lost.

The alternative — write rows in this change and defer topology and relay to the consumer's change — was rejected because it would concentrate the relay generalization, the topology, the consumer, HMAC signing, and the retry policy into one oversized change, and because it makes this change unverifiable end to end.

### 6. The terminal relay runs in `cmd/worker`

The rows are written by the worker (its own terminal writes and its sweeper's abandonment write), so the relay draining them is placed there. Running it in `cmd/api` would work — the table is shared and `FOR UPDATE SKIP LOCKED` makes concurrent relays safe — but it would make notification of a job's outcome depend on an API replica being up, which is a dependency the outcome itself does not have.

`cmd/worker`'s shutdown ordering is load-bearing and already documented: the relay holds a database transaction while it runs, so it is cancelled and **joined** before the PostgreSQL pool closes, exactly as `cmd/api` does. The relay owns its own connection and dials with backoff, so an unreachable broker remains a non-gate at worker startup, matching what `RABBITMQ_URL` already means there.

Both relays declare their own topology on every dial, so neither process's startup order depends on the other's.

## Risks / Trade-offs

- **A queue nothing drains grows until the consumer ships.** → Bounded by `x-max-length` with `reject-publish`: the broker nacks, the relay leaves `published_at` NULL, and the row is retried later. Nothing is lost and the database is the durable record either way. Two changes is a short window, and the queue depth is directly observable in the broker.
- **Making `Update` transactional adds a round trip and a transaction to the hottest terminal path.** → It is one write per job, at the end of an `ffmpeg` run measured in seconds. The alternative — a separate insert outside the transaction — is exactly the dual-write inconsistency the outbox pattern exists to prevent.
- **A caller that reads `applied == false` as failure would now also silently lose an event.** → No such caller exists (`CompleteJob`/`FailJob` return it as `Applied` in `TransitionResult`, and the worker treats it as success). The coupling is stated as a requirement so a future change cannot break it silently.
- **Generalizing `Claim` and `Publisher` touches the live dispatch path.** → The queued relay's arguments become a one-element set and a routing key read from a column that already holds exactly that literal; the existing relay and publisher tests cover the behavior unchanged, and the routing-key pinning test is what proves the column and the constant still agree.
- **A second AMQP connection per worker replica.** → One connection, opened lazily by the relay's own dial loop, on a broker already sized for the API's relays.

## Migration Plan

No schema migration and no data migration. Both event types are new, so there is no pre-existing backlog to retire and the `schema.sql` generation-cutoff statement — deliberately pinned to the previous dispatch generation's literal — is not touched.

Deploy order is unconstrained: an old worker writes no terminal events (harmless — the queue is empty), and a new worker publishing into a topology no one consumes is the intended steady state until the consumer ships. Rollback is a redeploy of the previous worker image; rows already written stay in the outbox, unpublished and inert, and are picked up whenever a new-generation relay runs again.

## Open Questions

- Whether `[]string` binds as `text[]` through the pgx stdlib driver, or whether the `string_to_array` fallback in decision 4 is needed. Resolved during implementation against the real PostgreSQL the suite runs on.
