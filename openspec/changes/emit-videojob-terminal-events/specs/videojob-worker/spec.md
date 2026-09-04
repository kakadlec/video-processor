## MODIFIED Requirements

### Requirement: The Worker Is Its Own Composition Root With Its Own Configuration Surface

`cmd/worker` SHALL be a separate binary with its own composition root, not a mode of `cmd/api`. It SHALL require the configuration for the services it actually uses — the Video Processing database, object storage, Redis, and the broker — and SHALL NOT require identity configuration, which it has no use for.

**It SHALL run the terminal-event relay and SHALL NOT run the job-dispatch relay.** The previous form of this requirement barred the worker from running any relay, on the ground that a second relay would double the claim polling against the outbox table for no additional dispatch. That reasoning held while one event stream existed; it does not survive `videojob-terminal-events`, which adds a second, disjoint stream. The two relays claim non-overlapping event-type sets, so neither polls for the other's rows and no dispatch is duplicated. The worker takes the terminal stream because it is the process that writes those rows — through its own terminal writes and its sweeper's abandonment write — so an outcome's announcement does not come to depend on an API replica being up. The dispatch stream stays in `cmd/api`, which is where `POST /upload` writes its rows.

Broker reachability SHALL be treated as the worker's own concern rather than a fatal startup gate, matching the relay: the worker SHALL dial, SHALL redial with bounded backoff when the connection or the consuming channel is lost, and SHALL redeclare the topology after every successful dial. **This SHALL hold for both of the worker's broker connections — the consumer's and the relay's — each of which owns its own dial loop and declares its own topology.** Within the worker, the consumer declares the job topology and the terminal relay declares the terminal-event topology; the dispatch relay that also declares the job topology runs in `cmd/api`, not here (`videojob-outbox-relay`). Both processes declaring is the point: neither process's startup may depend on the other having run first, so against a fresh or recreated broker a worker started alone still has a queue to consume from and an exchange to publish into.

Unlike the API, the worker SHALL exit non-zero if it cannot reach object storage or the database at startup — it has no request path to degrade, and a worker that consumes messages it cannot possibly process would drain the queue into the dead-letter queue.

#### Scenario: The worker starts without identity configuration

- **WHEN** the worker is started with the database, object storage, Redis, and broker configuration but no identity variables
- **THEN** it starts and begins consuming

#### Scenario: The worker starts before the broker is reachable

- **GIVEN** a broker that is not yet accepting connections
- **WHEN** the worker starts
- **THEN** it does not exit, it retries with backoff, and it begins consuming and relaying once the broker is available

#### Scenario: The worker declares both topologies on every dial

- **GIVEN** a broker whose exchanges and queues have been deleted while the worker was disconnected
- **WHEN** the worker reconnects
- **THEN** the job topology and the terminal-event topology are declared again, and consumption and publishing resume without a message being published into a missing exchange

#### Scenario: The worker does not claim job-dispatch rows

- **GIVEN** unpublished job-dispatch outbox rows
- **WHEN** the worker is running
- **THEN** its relay does not claim or publish them, and their `published_at` values are changed only by the API's dispatch relay

#### Scenario: Unreachable storage stops the worker rather than draining the queue

- **WHEN** the worker is started with object-storage configuration it cannot reach
- **THEN** it exits with an error naming the failure, rather than consuming and dead-lettering messages it could never have processed

### Requirement: The Worker Stops With Its Process and Finishes the Message in Hand

On `SIGINT` or `SIGTERM` the worker SHALL stop accepting new deliveries, SHALL let the current handler finish and apply its normal Ack/Reject decision when it completes before the deadline, SHALL stop its sweeper, **its terminal-event relay,** and its lease renewal, and SHALL then close the broker connections and its database, Redis, and storage handles in an order that keeps the in-flight work valid.

The sweeper **and the relay** SHALL be cancelled and **joined** before those handles close, exactly as `cmd/api` joins its outbox relay: each holds a database transaction while it runs — the sweeper across a requeue, the relay across a claim and its broker round trip — and closing the pool underneath either would abort it rather than resolve it. Lease renewal SHALL stop when the in-flight job does. Lease release SHALL be attempted only when this worker applied a terminal outcome or its bounded completion retry found its own identical outcome already present. A release error SHALL be logged and left to TTL expiry without changing the normal Ack/Reject disposition. After a fenced write, an already-present failure, or a non-terminal error, this worker has no cleanup right and SHALL leave the lease to expire (or leave a newer epoch's lease untouched).

It SHALL NOT abandon an in-flight extraction by exiting immediately. The redelivery that would follow cannot re-claim the job — the claim predicate refuses a `processing` row — so an abrupt exit converts an orderly restart into a job that only the sweeper can recover, after the lease it stopped renewing has expired.

**A terminal event whose row is committed but not yet published SHALL NOT delay shutdown.** The row is the durable record; an unpublished one is picked up by this or another worker's relay on a later poll, exactly as an unpublished dispatch row is. The relay SHALL be stopped and joined at whatever point its current cycle reaches, not drained to empty.

Shutdown SHALL be bounded: if the in-flight job does not finish within the deadline, the worker SHALL exit anyway rather than block indefinitely, logging the job identifier so it is enumerable. Such a job is no longer stranded permanently: after its lease lapses, the sweeper requeues it or terminally abandons it at the configured bound. The accepted deadline cost is duplicated work or bounded terminal failure rather than indefinite `processing`.

#### Scenario: An in-flight job is finished before exit

- **GIVEN** a worker mid-extraction whose handler finishes successfully before the shutdown deadline
- **WHEN** it receives `SIGTERM`
- **THEN** it completes that job, acknowledges the message, and exits — and the job is `completed`, not `processing`

#### Scenario: No new work is claimed after the signal

- **GIVEN** a worker that has received `SIGTERM` and is finishing its current job, with further messages queued
- **WHEN** it exits
- **THEN** no additional job was claimed, and the queued messages are still available to another worker

#### Scenario: The sweeper is joined before the database closes

- **GIVEN** a worker whose sweeper is mid-requeue when the shutdown signal arrives
- **WHEN** the worker shuts down
- **THEN** the requeue either commits or rolls back on its own terms, and no database handle is closed underneath it

#### Scenario: The terminal relay is joined before the database closes

- **GIVEN** a worker whose terminal-event relay is holding a claim when the shutdown signal arrives
- **WHEN** the worker shuts down
- **THEN** that claim either commits or rolls back on its own terms, and no database handle is closed underneath it

#### Scenario: An unpublished terminal event does not block exit

- **GIVEN** a worker that committed a terminal outcome whose event has not yet been published
- **WHEN** it receives `SIGTERM`
- **THEN** it exits without waiting for that publish, and the row remains unpublished and claimable by a later relay cycle

#### Scenario: A job finished during shutdown has its lease released

- **GIVEN** a worker that applied a cleanup-eligible terminal outcome during shutdown and whose lease release succeeds
- **WHEN** it exits
- **THEN** the lease store reports that job as not held immediately, without waiting for the expiry

#### Scenario: Shutdown does not block forever

- **GIVEN** a worker whose in-flight extraction exceeds the shutdown deadline
- **WHEN** the deadline passes
- **THEN** the worker logs the in-flight job identifier and exits, and that job is recovered by a later sweep once its lease lapses
