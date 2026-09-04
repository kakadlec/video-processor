# videojob-terminal-events Specification

## Purpose
Define the integration events a `VideoJob`'s terminal outcome produces: the two event types and their pinned payloads, the transaction that records one, the applied-write condition under which exactly one is recorded however many actors race to finish a job, the terminal-event topology those events are published to, and the process the relay carrying them runs in. Delivery of a recorded event, and everything about how a relay claims and publishes rows, is `videojob-outbox-relay`'s; the fenced write that records one is `videojob-persistence`'s.

## Requirements

### Requirement: A Terminal Outcome Is Recorded as an Integration Event in the Same Transaction as the Outcome Itself

When a `VideoJob` reaches `completed` or `failed`, the Video Processing context SHALL record a `video_job_outbox` row describing that outcome, and SHALL commit that row in the **same database transaction** as the `video_jobs` write that produced the outcome. A reader SHALL never be able to observe a terminal job without the event describing it, or the event without the job.

This SHALL NOT be implemented as a second write issued after the state write. A terminal job with no event notifies nobody and is indistinguishable, from outside, from a job that never finished; an event with no job would announce an outcome the system does not hold. The outbox exists precisely so that neither is representable.

The event SHALL be written by the same repository operation that performs the fenced terminal `UPDATE` — see `videojob-persistence` — rather than by a separate application-layer step. The application layer SHALL NOT gain a use case, a port, or a parameter for event emission: `CompleteJob` and `FailJob` keep the signatures and the `applied` semantics they already have.

#### Scenario: A completed job and its event commit together

- **GIVEN** a `processing` `VideoJob` whose actor holds the row's current fence epoch
- **WHEN** that actor commits the `completed` outcome
- **THEN** the job reads as `completed` and exactly one unpublished `video_job.completed.v1` outbox row names it

#### Scenario: A failed job and its event commit together

- **GIVEN** a `processing` `VideoJob` whose actor holds the row's current fence epoch
- **WHEN** that actor commits the `failed` outcome
- **THEN** the job reads as `failed` and exactly one unpublished `video_job.failed.v1` outbox row names it

#### Scenario: A failed transaction leaves neither the outcome nor the event

- **GIVEN** a terminal write whose transaction cannot commit
- **WHEN** the operation returns an error
- **THEN** the job's stored status is unchanged and no outbox row describing that outcome exists

### Requirement: Exactly One Event Is Recorded Per Job Outcome, by the Actor Whose Write Applied

An event SHALL be recorded **only** when the fenced conditional statement affected a row. The two outcomes in which it affects none SHALL each record nothing:

- A write refused by the fence (`ErrJobFenced`) — another actor won the outcome, and that actor's own write already recorded the event.
- A write that finds the stored row already carrying exactly this caller's outcome (`applied == false`) — this caller's earlier commit already recorded it.

This is the whole correctness argument for the capability, and it rests on `videojob-lease-recovery` rather than on caution. A worker presumed dead can return mid-run while a sweeper has already abandoned its job, and both may hold the same epoch; the terminal statement's `status = 'processing'` conjunct is what makes exactly one of them apply. Binding event emission to that same statement's row count — rather than to a second predicate evaluated separately — means the actor who wins the outcome and the actor who announces it cannot diverge. A job that reached a terminal state SHALL NOT go unannounced, and two actors racing to finish it SHALL NOT produce two records of that outcome.

**This guarantee is scoped to the durable record, not to delivery, and the distinction SHALL NOT be blurred.** `videojob-outbox-relay` is deliberately at-least-once: a publish the broker acknowledged, followed by a lost transaction, leaves the row unstamped and a later poll republishes it. One outbox row can therefore become more than one message, and a consumer's own crash or nack produces the same duplicate independently of the relay. What this requirement removes is the duplicate the *lease-recovery design itself* would otherwise create — a second, differently-worded outcome for one job, which no consumer could deduplicate because the two records describe genuinely different outcomes. Collapsing the two duplicates into one problem would leave that one unsolved and promise an exactly-once delivery no part of this system provides.

**Deduplicating deliveries is therefore the consumer's obligation, and it SHALL be carried by whatever consumes this queue** — the later Phase 7 change owns that, keyed on the `job_id` and event type each message carries. Nothing in this capability may be read as relieving it of that.

The completion retry that `videojob-worker` performs is covered by the second bullet: a retry after a lost response finds its own outcome recorded, records no second event, and still reports success.

#### Scenario: A fenced terminal write records no event

- **GIVEN** a `processing` `VideoJob` that has since been requeued, advancing its epoch
- **WHEN** the superseded actor commits a `completed` outcome at its held epoch
- **THEN** the write is fenced, the stored row is unchanged, and no outbox row is written as a result of that call

#### Scenario: Two actors at the same epoch produce one event between them

- **GIVEN** a `processing` `VideoJob` observed at the same epoch by a still-running leaseless worker and by a sweeper that has reached the abandonment bound
- **WHEN** both commit a terminal outcome, one `completed` and one `failed`
- **THEN** exactly one write applies and exactly one terminal outbox row exists for that job

#### Scenario: One record may still be delivered more than once

- **GIVEN** exactly one terminal outbox row for a job
- **WHEN** the relay publishes it, the broker acknowledges, and the relay's transaction is lost before it commits
- **THEN** a later poll publishes that same row again, and the duplicate is the consumer's to discard rather than a violation of this requirement

#### Scenario: A retried completion records no second event

- **GIVEN** a `completed` `VideoJob` whose terminal event was already recorded by this actor
- **WHEN** the same actor retries the identical completion at the same epoch
- **THEN** the call reports success with `applied == false` and the job still has exactly one terminal outbox row

### Requirement: The Terminal Event Types and Payloads Are Pinned and Carry a Generation

The context SHALL emit exactly two terminal event types, `video_job.completed.v1` and `video_job.failed.v1`. Their payloads SHALL carry at minimum the fields `ddd-architecture` pins for `VideoJobCompleted` and `VideoJobFailed`:

- **completed**: `type`, `job_id`, `user_id`, `frame_count`, `storage_key`, `occurred_at`
- **failed**: `type`, `job_id`, `user_id`, `error_reason`, `occurred_at`

`occurred_at` SHALL be generated once per outcome and SHALL be the same value in the payload and in the row's `occurred_at` column.

The generation suffix SHALL be part of the **event type string**, not only of an exchange or queue name. Every relay in this system claims from one shared `video_job_outbox` filtered on `event_type` and nothing else, so the event type is the only predicate that actually separates one generation from another; a versioned exchange alone isolates nothing, because the crossing would happen before anything is published. This is the same reasoning `videojob-messaging` records for the dispatch generation, applied at the point where a future payload change would otherwise have no escape hatch.

`Repository.Update` receiving a job whose status is neither `completed` nor `failed` SHALL return an error without writing, rather than committing a state change with no corresponding event type.

#### Scenario: The completion payload carries the pinned fields

- **WHEN** a completion event is recorded
- **THEN** its payload names the event type, the job id, the owning user id, the frame count, the result storage key, and the time the outcome occurred

#### Scenario: The failure payload carries the pinned fields

- **WHEN** a failure event is recorded
- **THEN** its payload names the event type, the job id, the owning user id, the error reason, and the time the outcome occurred

#### Scenario: The event type equals the routing key it is published under

- **WHEN** a terminal outbox row is published
- **THEN** the AMQP routing key it is published under is byte-identical to the row's `event_type`

#### Scenario: A non-terminal status is refused rather than recorded

- **GIVEN** a `VideoJob` whose in-memory status is not `completed` or `failed`
- **WHEN** the terminal write is attempted with it
- **THEN** it returns an error and neither a `video_jobs` write nor an outbox row is committed

### Requirement: The Context Owns a Terminal-Event Topology With a Queue Declared Ahead of Its Consumer

`internal/video/infrastructure/messaging` SHALL define a second topology for the terminal events: its own exchange, one durable work queue bound to **both** terminal routing keys, and the existing dead-letter sink. The names SHALL live in the context rather than in `internal/platform/rabbitmq`, which `ddd-architecture` confines to connection and lifecycle plumbing.

`TerminalEventsTopology` SHALL return exactly:

| Entity | Name | Type | Arguments |
|---|---|---|---|
| Terminal exchange | `video.jobs.terminal.v1` | `direct`, durable | — |
| Routing keys | `video_job.completed.v1`, `video_job.failed.v1` | — | — |
| Terminal queue | `video.jobs.terminal.events.v1` | durable | `x-max-length` 10 000, `x-overflow` `reject-publish`, `x-dead-letter-exchange` `video.jobs.dlx` |
| Dead-letter exchange | `video.jobs.dlx` | `fanout`, durable | — |
| Dead-letter queue | `video.jobs.dead` | durable | `x-message-ttl` 24 h, `x-max-length` 10 000, `x-overflow` `drop-head`, **no** `x-dead-letter-exchange` |

These values SHALL be pinned here for the same reason `videojob-messaging` pins the dispatch topology's: unpinned names cannot drift detectably, and a generation that is only a convention is not an isolation mechanism.

The queue name does not name a single job state, because it carries two; it names the class of event instead. The generation suffix on the exchange and the queue SHALL match the one on the event types, and the three SHALL be bumped together for the reason the dispatch generation records — the `event_type` is what a relay claims on, the exchange is what a broker routes on, and versioning either alone leaves the other's crossing open.

The dead-letter exchange and queue SHALL be the dispatch topology's existing ones and SHALL carry no generation suffix. The sink is fanout, so one place to look at holds every generation's and every stream's dead letters; a second sink would fragment that with nothing gained.

The work queue SHALL be declared even though no consumer exists yet, and this SHALL NOT be treated as premature. The relay publishes **mandatory** and stamps `published_at` only for messages the broker both acknowledged and routed (`videojob-outbox-relay`); publishing into an exchange with no binding would return every message unroutable, leaving its row unpublished and re-attempted on every poll indefinitely. A declared queue holds the events durably until Phase 7's consumer reads them.

The queue SHALL be bounded by a maximum length with `reject-publish`, like the job queue: a full queue SHALL nack the publish, leaving the row unpublished for a later poll, rather than dropping the event or dead-lettering one copy per attempt. It SHALL carry no message TTL, for the same reason the job queue carries none — an expired message would be discarded without touching the `video_jobs` row it describes, and the outcome would then never be announced.

The topology SHALL be declared on every dial, by every process that connects to it.

#### Scenario: Terminal events survive having no consumer

- **GIVEN** a broker with the terminal topology declared and nothing consuming its queue
- **WHEN** terminal events are published
- **THEN** their rows are stamped published and the messages remain on the queue

#### Scenario: Both terminal routing keys reach the same queue

- **WHEN** a completion event and a failure event are published
- **THEN** both are routed to the terminal work queue

#### Scenario: The topology is redeclared after a reconnect

- **GIVEN** a broker whose terminal exchange and queue were deleted while a publisher was disconnected
- **WHEN** the publisher reconnects
- **THEN** the topology is declared again and publishing resumes without a message being returned unroutable

### Requirement: The Terminal Relay Runs in the Process That Writes the Rows

The relay carrying terminal events SHALL run in `cmd/worker`, which is the process that writes them — through its own terminal writes and through its recovery sweeper's abandonment write. It SHALL own its broker connection, dial with bounded backoff, and redeclare its topology after every dial, exactly as `cmd/api`'s dispatch relay does; broker reachability SHALL NOT become a startup gate for the worker.

Placing it in `cmd/api` instead SHALL be treated as rejected rather than merely unchosen: it would work, because the outbox table is shared and `FOR UPDATE SKIP LOCKED` makes concurrent relays safe, but it would make the announcement of a job's outcome depend on an API replica being up — a dependency the outcome itself does not have.

The two relays SHALL claim disjoint event-type sets, so running both adds no contention on the same rows and no duplicate dispatch.

The worker SHALL cancel and **join** this relay before closing its database pool, for the same reason it already joins its sweeper: the relay holds a transaction across its broker round trip, and closing the pool underneath it would abort a claim instead of resolving it.

#### Scenario: A worker running alone publishes its own terminal events

- **GIVEN** a running worker and no API replica
- **WHEN** it commits a terminal outcome
- **THEN** the corresponding event is published to the terminal topology and its row is stamped published

#### Scenario: The terminal relay does not claim dispatch rows

- **GIVEN** unpublished job-dispatch rows in the outbox
- **WHEN** the worker's terminal relay runs
- **THEN** none of them is claimed or published, and none of their `published_at` values changes as a result of that relay

#### Scenario: The relay is joined before the pool closes

- **GIVEN** a worker whose terminal relay is mid-claim when the shutdown signal arrives
- **WHEN** the worker shuts down
- **THEN** that claim either commits or rolls back on its own terms, and no database handle is closed underneath it
