# videojob-outbox-relay Specification

## Purpose

Define the transactional-outbox relay that carries the current job-dispatch generation's events from PostgreSQL to the AMQP broker: how it claims unpublished rows without two replicas dispatching the same one, why the claim, the publish, and the `published_at` stamp commit together, what counts as proof that a message actually reached a queue, how the claim's `event_type` filter is what keeps one dispatch generation from crossing into another, and how the relay's connection is owned and stopped inside `cmd/api`.

The relay is the only thing in this system that publishes to `videojob-messaging`'s job-dispatch topology, and it is deliberately in no request path: `POST /upload` records the dispatch in the same transaction that queues the job (`videojob-persistence`) and returns without touching the broker. The row is the durable record; the relay is what turns it into a message. `cmd/worker` is what consumes those messages — see `videojob-worker` — so a message this relay publishes is now a live trigger rather than an inert side effect.

## Requirements

### Requirement: The Relay Carries Unpublished Outbox Rows to the Broker

A relay SHALL run inside `cmd/api`, periodically claiming unpublished `video_job_outbox` rows of the current job-dispatch generation's event type, publishing each to the job-dispatch topology, and marking the published ones. It SHALL be composed of an `OutboxRepository` in `internal/video/infrastructure/postgres` and a publisher in `internal/video/infrastructure/messaging`, so that no single infrastructure package depends on both a database driver and an AMQP client.

The relay is not in any request path. `POST /upload` neither publishes nor waits on the broker; a message reaches the queue some time after the transaction that recorded it committed.

#### Scenario: An unpublished row is published and marked

- **GIVEN** a `video_job_outbox` row carrying the current generation's dispatch `event_type` and a `NULL` `published_at`
- **WHEN** the relay runs
- **THEN** a message carrying that row's payload is on the job queue, and the row's `published_at` is set

#### Scenario: A published row is not published again

- **GIVEN** an outbox row whose `published_at` is already set
- **WHEN** the relay runs
- **THEN** it is not claimed and no further message is produced from it

### Requirement: The Claim Is Scoped to One Event Type

The relay's claim SHALL filter on the `event_type` of the job-dispatch generation it publishes into, and a supporting index SHALL exist for that predicate.

This is not an optimization, and it now serves two purposes rather than one.

It keeps internal events off the job queue. `video_job_outbox` has accumulated an unpublished `video_job.created` row for every job created since Phase 3, and those rows are internal events that must never reach the job queue. An unfiltered claim would re-read that backlog on every poll and, with a bounded batch, could starve the rows the relay exists to deliver — publishing nothing while appearing to work.

**It is also what isolates dispatch generations, and that is load-bearing during a rolling deploy.** Every replica's relay reads the same `video_job_outbox` table, so a filter shared across generations would let a new replica's relay claim an old replica's row and publish it into the new generation, and an old replica's relay claim a new replica's row and publish it into the old one — where nothing consumes it and the job waits in `queued` forever. Isolating at the exchange cannot help, because the crossing happens before anything is published, and an already-deployed relay cannot be given a new predicate. The current generation's `event_type` SHALL therefore differ from every previous generation's, and a relay SHALL NOT be given a predicate that matches more than its own.

The event-type string SHALL be a single constant shared between the insert, the claim, and the routing key, so the writer, the reader, and the broker cannot drift apart into a relay that silently matches nothing.

#### Scenario: Creation events are never dispatched

- **GIVEN** unpublished `video_job.created` rows in the outbox
- **WHEN** the relay runs
- **THEN** none of them is published, and none of their `published_at` values changes

#### Scenario: A backlog of other event types does not starve dispatch

- **GIVEN** more unpublished `video_job.created` rows than the relay's batch size, and one unpublished dispatch row older or newer than them
- **WHEN** the relay runs
- **THEN** the dispatch row is published

#### Scenario: A relay never claims another generation's dispatch row

- **GIVEN** unpublished dispatch rows written under a previous generation's `event_type`
- **WHEN** the current generation's relay runs
- **THEN** none of them is claimed or published, and none of their `published_at` values changes as a result of that relay

### Requirement: Concurrent Relays Do Not Publish the Same Row Twice

The claim SHALL use `SELECT … FOR UPDATE SKIP LOCKED`, so that a second `cmd/api` replica polling at the same moment steps over rows already claimed rather than blocking on them or re-reading them.

This system is being prepared to run multiple `cmd/api` replicas, each running its own relay. The guard is PostgreSQL-side deliberately: it protects the outbox row, not the job, which is a different contention from the Redis lease a later change adds around worker job pickup.

#### Scenario: Two concurrent relays split the work

- **GIVEN** several unpublished dispatch rows and two relays claiming at the same time
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

### Requirement: Pre-Existing Unpublished Rows Are Bounded by an Explicit Cutoff

Unpublished outbox rows that pre-date the switch to the current job-dispatch generation SHALL be excluded from dispatch by an explicit cutoff applied before the relay first publishes into that generation, and SHALL NOT be delivered to a worker.

The cutoff SHALL be structural: because a generation bump changes the `event_type` string (see `videojob-messaging`) and the claim filters on it, the current generation's relay cannot match a row written by the previous one. That is what makes the exclusion airtight rather than best-effort — it holds for rows a not-yet-redeployed replica writes *after* the switch as well as before it, which no timestamp could.

It SHALL additionally be recorded: a schema migration SHALL stamp `published_at` on the previous generation's rows that are still unpublished, so exclusion is a fact about each row rather than an inference a future reader has to reconstruct from two string literals. The migration SHALL select those rows by the previous generation's `event_type` together with a null `published_at`, and SHALL NOT bound them by a timestamp — a timestamp would leave exactly the rows a not-yet-redeployed replica writes after the migration ran, which are as undeliverable as the rest. The migration is the record; the event-type scoping is the mechanism, and this requirement asks for both.

The rows in question are not merely stale, they are undeliverable. Each was written by a `POST /upload` request that went on to process the job in-request, drive it to `completed` or `failed`, and delete its source object; dispatching one would hand a worker a job whose input no longer exists.

The conditional claim in `videojob-lifecycle` would refuse most of them anyway, and that SHALL NOT be relied upon as the boundary. It is an accidental consequence of a predicate written for a different purpose, it does not cover a row whose job is still `queued` (a request that crashed between the enqueue and the processing), and it would fill the dead-letter queue with a batch of expected garbage, degrading the one place operators look for anomalies.

A later change that switches the job-dispatch generation again SHALL apply the same treatment to rows unpublished at that point, and SHALL modify this requirement rather than leave it describing only the first cutover.

#### Scenario: A stale unpublished row is not delivered as a live dispatch

- **GIVEN** an unpublished outbox row written before the cutover, naming a job already `completed` with its source object deleted
- **WHEN** the migration has run and the relay polls for unpublished rows
- **THEN** the row is not claimed — its `event_type` does not match the current generation's, and it carries a `published_at` stamp besides — and no message naming that job is published to the current generation

#### Scenario: A previous generation's row written after the migration is still excluded

- **GIVEN** a not-yet-redeployed replica that enqueues a job after the migration has already run, writing an unstamped row under the previous generation's `event_type`
- **WHEN** the current generation's relay polls
- **THEN** it does not claim that row, demonstrating that the exclusion is not a timestamp boundary a late writer could fall outside of

#### Scenario: The cutoff does not stamp rows written after it

- **GIVEN** the migration has run
- **WHEN** a new `POST /upload` request enqueues a job, writing a fresh outbox row under the current generation's `event_type`
- **THEN** that row is unpublished, the relay claims and publishes it, and the worker receives it

#### Scenario: The dead-letter queue is not used as the cutoff

- **WHEN** the deployment completes and the worker has been consuming for a full poll cycle
- **THEN** no dead-lettered message corresponds to a pre-cutover outbox row, because none was ever published

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

`cmd/worker`'s consumer declares the same topology on every dial for the same reason (see `videojob-worker`), and the two SHALL NOT be treated as redundant: declaration is idempotent, and each process has to be able to start against a fresh or recreated broker without the other having run first. Against a broker where nothing has declared it, the exchange does not exist, and a publish to a missing exchange closes the channel rather than returning a routable error. Declaring on connect is also what makes the declaration idempotent guarantee useful: a reconnect after the broker was recreated finds the topology gone and restores it, instead of publishing into nothing.

#### Scenario: A fresh broker gets its topology before the first publish

- **GIVEN** a broker with none of the job-dispatch topology declared and an unpublished dispatch row
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
