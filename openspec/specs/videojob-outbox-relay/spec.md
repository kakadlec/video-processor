# videojob-outbox-relay Specification

## Purpose

Define the transactional-outbox relay that carries `video_job.queued` events from PostgreSQL to the AMQP broker: how it claims unpublished rows without two replicas dispatching the same one, why the claim, the publish, and the `published_at` stamp commit together, what counts as proof that a message actually reached a queue, and how the relay's connection is owned and stopped inside `cmd/api`.

The relay is the first thing in this system to publish to `videojob-messaging`'s job-dispatch topology, and it is deliberately in no request path: `POST /upload` records the dispatch in the same transaction that queues the job (`videojob-persistence`) and returns without touching the broker. The row is the durable record; the relay is what turns it into a message. Nothing consumes those messages yet — `migrate-upload-to-async-processing` is what introduces a consumer, and this specification records the one obligation that hands it.

## Requirements

### Requirement: The Relay Carries Unpublished Outbox Rows to the Broker

A relay SHALL run inside `cmd/api`, periodically claiming unpublished `video_job_outbox` rows of the `video_job.queued` event type, publishing each to the job-dispatch topology, and marking the published ones. It SHALL be composed of an `OutboxRepository` in `internal/video/infrastructure/postgres` and a publisher in `internal/video/infrastructure/messaging`, so that no single infrastructure package depends on both a database driver and an AMQP client.

The relay is not in any request path. `POST /upload` neither publishes nor waits on the broker; a message reaches the queue some time after the transaction that recorded it committed.

#### Scenario: An unpublished row is published and marked

- **GIVEN** a `video_job_outbox` row with `event_type` `video_job.queued` and a `NULL` `published_at`
- **WHEN** the relay runs
- **THEN** a message carrying that row's payload is on the job queue, and the row's `published_at` is set

#### Scenario: A published row is not published again

- **GIVEN** an outbox row whose `published_at` is already set
- **WHEN** the relay runs
- **THEN** it is not claimed and no further message is produced from it

### Requirement: The Claim Is Scoped to One Event Type

The relay's claim SHALL filter on `event_type = 'video_job.queued'`, and a supporting index SHALL exist for that predicate.

This is not an optimization. `video_job_outbox` has accumulated an unpublished `video_job.created` row for every job created since Phase 3, and those rows are internal events that must never reach the job queue. An unfiltered claim would re-read that backlog on every poll and, with a bounded batch, could starve the rows the relay exists to deliver — publishing nothing while appearing to work.

The event-type string SHALL be a single constant shared between the insert and the claim, so the writer and the reader cannot drift apart into a relay that silently matches nothing.

#### Scenario: Creation events are never dispatched

- **GIVEN** unpublished `video_job.created` rows in the outbox
- **WHEN** the relay runs
- **THEN** none of them is published, and none of their `published_at` values changes

#### Scenario: A backlog of other event types does not starve dispatch

- **GIVEN** more unpublished `video_job.created` rows than the relay's batch size, and one unpublished `video_job.queued` row older or newer than them
- **WHEN** the relay runs
- **THEN** the `video_job.queued` row is published

### Requirement: Concurrent Relays Do Not Publish the Same Row Twice

The claim SHALL use `SELECT … FOR UPDATE SKIP LOCKED`, so that a second `cmd/api` replica polling at the same moment steps over rows already claimed rather than blocking on them or re-reading them.

This system is being prepared to run multiple `cmd/api` replicas, each running its own relay. The guard is PostgreSQL-side deliberately: it protects the outbox row, not the job, which is a different contention from the Redis lease a later change adds around worker job pickup.

#### Scenario: Two concurrent relays split the work

- **GIVEN** several unpublished `video_job.queued` rows and two relays claiming at the same time
- **WHEN** both complete
- **THEN** each row was published exactly once, and neither relay blocked waiting for the other

### Requirement: Claim, Publish, and Mark Commit Together

The claim, the publishes, and the `published_at` update SHALL happen inside one database transaction, which commits only after the broker has acknowledged the messages being marked.

This holds row locks across a broker round trip, and that cost is accepted for a specific reason: committing the claim first and publishing afterwards would, on a crash between the two, leave a row marked published that was never published — a job dispatch lost permanently and silently, which is the one outcome an outbox exists to prevent. The batch size and the acknowledgement wait SHALL both be bounded so the lock interval is short and capped.

The failure this ordering does allow is the recoverable one: a crash after the broker acknowledged but before the commit leaves the row unstamped and a later poll republishes it. That is at-least-once delivery, which any consumer must tolerate regardless, since a redelivery after a nack or a consumer crash produces the same duplicate.

#### Scenario: A crash before commit republishes rather than loses

- **GIVEN** the broker has acknowledged a message but the relay's transaction has not committed
- **WHEN** the process is lost and a later poll runs
- **THEN** the row is still unpublished and is published again, rather than being marked published without having been delivered

### Requirement: Only Acknowledged, Routed Messages Are Marked Published

The publishing channel SHALL run in publisher-confirm mode, and the relay SHALL publish **mandatory**, correlate any `basic.return` with its confirmation, and mark `published_at` only for messages that were both acknowledged **and** not returned.

A confirmation alone is not proof of delivery, and the difference is the whole point of an outbox. A publisher confirm says the *exchange* accepted the publish; a non-mandatory publish to an exchange with no queue bound for the routing key is acknowledged and then discarded. Stamping on the confirmation alone would therefore mark a row published for a message that reached no queue — a dispatch lost permanently, which is precisely the failure mode this capability exists to make impossible. Publishing mandatory turns that silent discard into a `basic.return` the relay can see, and a returned message leaves its row unstamped for the next poll.

A negative acknowledgement SHALL NOT be treated as a failure of the relay. The job queue's `reject-publish` overflow policy nacks a publish when the queue is at its maximum length, which is designed back-pressure: the row stays unpublished, the next poll retries it, and nothing is lost. A relay that treated a nack as fatal would turn back-pressure into a crash loop, and one that marked rows regardless would turn it into silent loss.

The consequence is worth stating rather than discovering: while nothing consumes the job queue, a full queue stalls this relay and unpublished rows accumulate indefinitely. Uploads are unaffected, because they still complete in-request, and the backlog resumes draining when the queue is retired.

#### Scenario: A nacked publish leaves the row for the next poll

- **GIVEN** the job queue is at its maximum length
- **WHEN** the relay attempts to publish a claimed row
- **THEN** the broker nacks it, the row's `published_at` stays `NULL`, the relay does not error out, and a later poll attempts it again

#### Scenario: An unroutable message leaves its row unstamped

- **GIVEN** a topology whose job queue is not bound for the routing key being published
- **WHEN** the relay publishes a claimed row mandatory and the broker acknowledges but returns it
- **THEN** the row's `published_at` stays `NULL`, so the dispatch is retried rather than recorded as delivered

#### Scenario: Publishing without confirms is not sufficient

- **GIVEN** a relay implementation that publishes without enabling confirm mode
- **WHEN** the broker refuses a message
- **THEN** that implementation cannot distinguish acceptance from refusal, and marking `published_at` on the basis of a nil return would lose the dispatch — so confirm mode is required, not optional

### Requirement: Pre-Existing Unpublished Rows Are Bounded by the Cutover, Not by This Change

The change that switches the job-dispatch topology to a new generation SHALL define an explicit cutoff for pre-existing unpublished `video_job.queued` rows before it begins publishing into that generation, and SHALL modify this requirement in the same change. `video_job_outbox.occurred_at` is what makes such a cutoff expressible.

Nothing consumes the job queue yet. Every message this relay publishes names a job that the same `POST /upload` request has already driven to `completed` or `failed`, with its source object deleted, so a **published** message is inert by construction: the cutover consumes a different generation of the topology, and nothing can ever read the messages sitting in this one.

**Unpublished** rows are a different matter, and a separate queue generation does not isolate them. A row that was never successfully published — nacked against a full queue, or written while the broker was unreachable — is still claimable, so a relay later repointed at the cutover's generation would deliver it there: a live dispatch for a job that finished long ago and whose source object no longer exists.

This is a current-state requirement with a deliberate expiry, recorded in a canonical spec rather than in a proposal that archives: the obligation cannot be discharged by the change that created it, because that change is not the one that switches generations.

#### Scenario: A stale unpublished row is not delivered as a live dispatch

- **GIVEN** an unpublished `video_job.queued` row written before the cutover, naming a job already `completed` with its source object deleted
- **WHEN** a relay begins publishing into the cutover's generation of the topology
- **THEN** that row is excluded by an explicit cutoff rather than dispatched to a worker that would fail to fetch its source

### Requirement: Published Messages Are Persistent and Carry No Expiry

The relay SHALL publish with delivery mode 2 and SHALL leave the per-message `expiration` property unset, satisfying `videojob-messaging`'s publisher obligation.

Both halves matter and the second is invisible when omitted: RabbitMQ honours a message's own `expiration` independently of the queue's arguments, so setting one would expire a job message into the dead-letter queue even though the job queue deliberately carries no `x-message-ttl`.

#### Scenario: A published job message survives a broker restart

- **GIVEN** the relay has published a message and the broker has acknowledged it
- **WHEN** the broker is restarted
- **THEN** the message is still queued

#### Scenario: A published job message carries no expiration

- **GIVEN** a message the relay published
- **WHEN** its properties are inspected
- **THEN** its `expiration` property is unset

### Requirement: The Relay Declares the Topology on Every Connection

After each successful dial — the first and every re-dial — the relay SHALL call `rabbitmq.DeclareTopology` with `JobDispatchTopology()` before opening its publishing channel.

Nothing else declares it. `internal/video/infrastructure/messaging` defines the descriptor and `internal/platform/rabbitmq` can declare one, but no code path had ever called the two together — so against a fresh broker the exchange does not exist, and a publish to a missing exchange closes the channel rather than returning a routable error. Declaring on connect is also what makes the declaration idempotent guarantee useful: a reconnect after the broker was recreated finds the topology gone and restores it, instead of publishing into nothing.

#### Scenario: A fresh broker gets its topology before the first publish

- **GIVEN** a broker with none of the job-dispatch topology declared and an unpublished `video_job.queued` row
- **WHEN** the relay connects and runs
- **THEN** the exchange, job queue, and dead-letter sink exist, and the row is published and marked

#### Scenario: A reconnect redeclares

- **GIVEN** a relay that has lost its connection to a broker whose topology has since been removed
- **WHEN** it re-dials
- **THEN** it declares the topology again before publishing, rather than publishing into a missing exchange

### Requirement: The Broker Connection Is the Relay's, Not a Startup Gate

`RABBITMQ_URL` SHALL be required configuration at `cmd/api` startup: an unset value SHALL stop startup, like every other required variable. Broker **reachability** SHALL NOT be a startup gate — the relay SHALL own its connection, dial it in its own goroutine, and retry with backoff on failure or on a connection it loses while running.

This is deliberately neither posture already in the codebase. MinIO is fail-closed because a result that cannot be stored cannot be delivered, and that failure is in the request path; the relay is in no request path, so a broker outage delays dispatch and costs nothing else. Redis's fail-open is also the wrong analogy: those features degrade a request by skipping an optimization, and here there is no request to degrade. The relay needs reconnection logic regardless, because an AMQP connection can drop at any time — once that loop exists, a fatal first dial buys nothing that requiring the variable does not already buy, while coupling API availability to broker availability.

#### Scenario: Startup fails with no RABBITMQ_URL

- **GIVEN** `RABBITMQ_URL` is unset
- **WHEN** `cmd/api` starts
- **THEN** it exits with a clear configuration error naming the variable

#### Scenario: The API serves with the broker unreachable

- **GIVEN** `RABBITMQ_URL` is set to an address with no broker listening
- **WHEN** `cmd/api` starts
- **THEN** it starts and serves every route, the relay retries in the background, and no request fails because of the broker

### Requirement: The Relay Stops With the Process

The relay SHALL stop on shutdown, finishing or rolling back its in-flight transaction rather than being torn down mid-claim.

#### Scenario: Shutdown does not strand a claim

- **WHEN** `cmd/api` shuts down while the relay holds a claim
- **THEN** the transaction is resolved rather than abandoned, and no row is left marked published without having been delivered
