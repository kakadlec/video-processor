# videojob-worker Specification

## Purpose

Define `cmd/worker`, the process that turns a dispatched `video_job.queued` message into a finished `VideoJob`: what it consumes, how it claims a job so a duplicate delivery cannot double-process it, when it acknowledges, dead-letters, or leaves a message outstanding, which side effects it owns (the source object, the failed job's idempotency key) and under exactly which conditions, its own composition root and configuration surface, and how it shuts down.

It is the consumer that `videojob-messaging`'s topology and `videojob-outbox-relay`'s publishing were built for. The extraction sequence it runs is `videojob-execution`'s `ProcessVideoJob`; the transitions it drives are `videojob-lifecycle`'s; the conditional claim underneath them is `videojob-persistence`'s. This capability owns only what the worker process itself decides — it makes no access-control decision (`video-processing-access`) and serves no HTTP beyond the metrics-only listener `service-metrics` defines.
## Requirements
### Requirement: cmd/worker Consumes the Job Queue and Runs Each Dispatch to a Terminal State

A `cmd/worker` entrypoint SHALL consume the job-dispatch queue defined by `videojob-messaging` and, for each message, run `ProcessVideoJob` against the `job_id` and `source_key` the message carries, driving the job to `completed` or `failed` — or, for a transient object-storage failure within its bound, back to `queued` with a fresh dispatch of its own (see the acknowledgement requirement below).

It SHALL call `CompleteJob` on success **only**, and only after `ProcessVideoJob` has reported that the result is stored in the bucket. Because storing the result is part of `ProcessVideoJob`'s own sequence, a result reporting success is itself the durability guarantee the worker waits for; the worker SHALL NOT record any additional ownership artifact before completing the job. If `ProcessVideoJob` reports failure, the worker SHALL NOT call `CompleteJob`, so a job's persisted status never claims `completed` for a result that was not stored.

It SHALL NOT call `FailJob` or `RetryVideoJob`: `ProcessVideoJob` already fails the job itself for an extraction failure, a missing source object, or a storage failure whose retry bound is spent, and returns it to `queued` itself for a transient storage failure within that bound, so a worker that also called either would ask the domain for a transition it refuses. An implementer SHALL NOT add a failure call "for symmetry".

**The terminal write can fail after the result is already durable, and the worker SHALL have a policy for it rather than discovering one.** `CompleteJob` returns its repository error and leaves the stored job in `processing`, so a transient database failure at that moment produces a job with a usable result that no listing shows. The worker SHALL retry the terminal write a bounded number of times with backoff, on a context detached from any cancellation that may have caused the failure, because the overwhelmingly likely cause is transient and one more attempt costs nothing next to a re-extraction.

If the write still cannot commit, the worker SHALL reject the message without requeue, SHALL leave the job as it stands, and SHALL log the job identifier and the result `StorageKey` — never a credential — so the orphan is enumerable. It SHALL NOT delete the source object in that case (see the source-ownership requirement below), and SHALL NOT acknowledge the message: the job did not reach a terminal state, and both the bytes and the dead-lettered message are what a later recovery has to work from.

This orphan class is not introduced here — the synchronous pipeline produced the same shape when the post-processing write failed — but it becomes the worker's to bound, and reconciling it remains out of scope.

The worker SHALL take the source location from the message rather than reconstructing it. `ProcessVideoJob` accepts a source `StorageKey` precisely so a process that shares no filesystem with the HTTP handler can run it, and the key embeds a generated upload identifier that is not derivable from any other field.

The worker SHALL serve no HTTP route other than the metrics-only listener `service-metrics` defines, SHALL NOT be reachable from outside the deployment, and SHALL NOT perform an access-control decision (see `video-processing-access`).

#### Scenario: A dispatched job is processed to completion

- **GIVEN** a `VideoJob` in `queued` status whose source object holds a video `ffmpeg` can decode, and a message for it on the job queue
- **WHEN** the worker consumes that message
- **THEN** the job reaches `completed` with a `StorageKey` and `FrameCount`, the result zip is present in the bucket under that key, and the message is acknowledged

#### Scenario: An undecodable source fails the job exactly once

- **GIVEN** a `VideoJob` in `queued` status whose source object `ffmpeg` cannot decode
- **WHEN** the worker consumes its message
- **THEN** the job reaches `failed` with a persisted reason, and the worker itself performed no transition call beyond the claim — the failure was recorded by `ProcessVideoJob`

#### Scenario: A result that was not stored leaves the job failed, not completed

- **GIVEN** a dispatched job whose frames extract successfully but whose zip cannot be stored, on an attempt at which its retry bound is spent
- **WHEN** the worker finishes the message
- **THEN** the job's persisted status is `failed`, not `completed`, and `GetJobStatus` never reports a `StorageKey` for an object that is not in the bucket

#### Scenario: The worker uses the key from the message

- **GIVEN** a message whose `source_key` names a stored object
- **WHEN** the worker processes it
- **THEN** it fetches exactly that key, and does not derive a key from the job's original filename or identifier

### Requirement: Prefetch Is One, and Acknowledgement Follows a Committed Transition

The worker SHALL set a consumer prefetch of exactly one unacknowledged message and SHALL acknowledge a message only after a transition that settles its dispatch has been committed: the one that makes its job terminal, or — for a transient object-storage retry — the `processing → queued` transition that commits a fresh dispatch in the same transaction.

Prefetch above one SHALL NOT be configured. The unit of work is a full extraction — seconds to minutes of `ffmpeg`, not microseconds — so buffering buys no throughput. What it costs is availability of the buffered work: a prefetched message is held by this consumer and is not offered to any other, so it waits behind work of unbounded duration while an idle worker elsewhere has nothing to take. The messages themselves are not lost — a prefetched delivery has not been handled, its job is still `queued`, and the broker requeues it when this consumer's connection closes — so the reason for the bound is fairness and latency, not durability.

Acknowledging before that commit SHALL NOT be done: a crash between the acknowledgement and the commit destroys the only remaining record that the job needs processing. The retry transition satisfies this for the same reason a terminal write does — once it commits, the outbox row it wrote is that record.

#### Scenario: Only one message is outstanding at a time

- **GIVEN** several messages waiting on the job queue and one running worker
- **WHEN** the worker is mid-extraction on the first
- **THEN** the broker reports exactly one unacknowledged delivery for that consumer, and the remaining messages are still queued

#### Scenario: A worker killed mid-extraction leaves its message redeliverable

- **GIVEN** a worker that has claimed a job and is running the extraction
- **WHEN** the process is killed without acknowledging
- **THEN** the broker redelivers that message rather than dropping it

#### Scenario: Two workers do not both process one job

- **GIVEN** two running workers and one message that the broker delivers twice
- **WHEN** both attempt the job
- **THEN** exactly one claims it and runs the extraction, the other is refused by the claim, and exactly one result object exists for that job

### Requirement: A Message the Worker Cannot Act On Is Dead-Lettered, Never Requeued and Never Acked Away

When the worker cannot act on a message, it SHALL reject the message **without requeue**, so the topology's dead-letter route takes it. This SHALL apply to a message whose payload cannot be parsed, one naming no source object, one naming a job that does not exist, one whose job the worker could not claim, and one whose job was **taken away from the worker while it ran** — a terminal write refused by the fence (see `videojob-lease-recovery`).

**"Cannot act on" means the message names work this worker will never be able to perform, not work it could not perform on this attempt.** That distinction is now load-bearing rather than implicit: exactly one condition — a claim whose outcome the persistence layer could not report — is licensed to be retried instead, and the requirement below states it. Every condition enumerated here, and **every condition not enumerated in either requirement**, SHALL be dead-lettered. The default SHALL remain rejection, so a failure mode neither requirement anticipated behaves as it does today rather than entering a retry loop nobody reasoned about.

It SHALL NOT requeue a message that falls under this requirement: none of these conditions is transient, so requeueing produces an unbounded redelivery loop against a message that will never succeed. A fenced result means either a newer epoch was re-dispatched and may have a live successor, or another actor committed a different terminal outcome at the same epoch. Requeueing is wrong in both cases: it would add a competing delivery in the first and can never claim the already-terminal row in the second.

This SHALL also cover every failure that occurs **after** the claim has been won, including a terminal write whose own transaction could not reach the database. Such a message SHALL be dead-lettered rather than requeued even though its failure is transient, because by then the row is `processing` and the claim predicate admits `queued` alone: a redelivery could only lose the claim. Recovery for that `processing` row belongs to `videojob-lease-recovery`'s sweeper, which reaches it because it is `processing`, and the disposition SHALL NOT be changed on the argument that the underlying failure was temporary. The one post-claim failure this does not cover is a transient object-storage failure that `ProcessVideoJob` has already returned to `queued`: that message is neither requeued nor dead-lettered but acknowledged, under its own requirement below, because the work it named has already been dispatched again by a committed write. That row is `queued`, which the sweeper does not scan and does not need to: the dispatch the retry committed carries it.

It SHALL NOT acknowledge such a message either. An acknowledged message is gone from the broker, which would leave nothing to enumerate afterwards — the dead-letter queue is the only place these anomalies remain visible, and `videojob-messaging` keeps it unversioned so there is one place to look.

Rejecting a message SHALL NOT cause any additional job transition as part of that rejection. In particular a lost claim SHALL NOT be turned into a `FailJob` call: the job belongs to whichever consumer won. A fenced write SHALL NOT be retried unfenced, re-read, or converted into a failure for the same reason.

A fenced outcome SHALL be logged distinctly from a lost claim, naming the job, the epoch the worker held, and — when the extraction had succeeded — the result key it stored. That key is not necessarily an orphan: it is the job's own result key, so the object under it may be the successor's or this run's, whichever was written last. `videojob-lease-recovery` states why either is acceptable; the log line exists so an operator can tell that a second run produced a result at all, not so the object can be recovered separately.

#### Scenario: An unparseable message is dead-lettered

- **GIVEN** a message on the job queue whose body is not a valid queued-job payload
- **WHEN** the worker consumes it
- **THEN** the message appears in the dead-letter queue, it is not redelivered to the worker, and no `VideoJob` was modified

#### Scenario: A message naming an unknown job is dead-lettered

- **GIVEN** a message naming a `VideoJob` identifier no row matches
- **WHEN** the worker consumes it
- **THEN** the message appears in the dead-letter queue and the worker continues consuming subsequent messages

#### Scenario: A stale dispatch for a finished job is dead-lettered without side effects

- **GIVEN** a message naming a job already in `completed` status
- **WHEN** the worker consumes it
- **THEN** the claim is refused, the message appears in the dead-letter queue, and the job's status, `StorageKey`, `FrameCount`, and `ErrorReason` are unchanged

#### Scenario: A job taken over mid-extraction is dead-lettered, not retried

- **GIVEN** a worker whose job was requeued and re-claimed while its extraction ran, and whose terminal write is therefore refused by the fence
- **WHEN** it finishes with the message
- **THEN** the message appears in the dead-letter queue, no unfenced write is attempted, the job carries only the current holder's state, and the worker logs the job, its held epoch, and any result key it stored

#### Scenario: A row that was read but could not be interpreted is dead-lettered

- **GIVEN** a reachable database holding a `video_jobs` row the aggregate refuses to reconstruct, and a dispatch naming it
- **WHEN** the worker consumes that dispatch
- **THEN** the message appears in the dead-letter queue and is not redelivered, because the failure is a property of the stored row and not of the database's availability

#### Scenario: A terminal write that could not reach the database is dead-lettered, not requeued

- **GIVEN** a worker that has won the claim and whose terminal write cannot reach the database
- **WHEN** it finishes with the message
- **THEN** the message appears in the dead-letter queue rather than being requeued, the row is left `processing`, and the sweeper subsequently recovers it without operator action

### Requirement: Source Deletion Follows a Terminal Write Applied by This Actor

The consumer SHALL attempt to delete the source object named by the message when its terminal write was **applied**. A completion retry that finds its identical outcome already present SHALL also attempt deletion because that bounded retry is completing cleanup for its own possibly lost-response write; a failure path that merely finds an identical outcome already present SHALL acknowledge but SHALL NOT clean up. The sweeper earns the same right only by applying its conditional abandonment write. Deletion SHALL be attempted on every normal return that satisfies one of those conditions. It is deliberately *not* deferred: a panic between commit and delete leaves the object behind, which is safer than deleting before ownership is established.

**It SHALL NOT delete the source object on any path where the job did not reach a committed terminal state**, and this is the condition that matters most, because getting it wrong is unrecoverable rather than merely untidy. A panic mid-extraction, a `CompleteJob` write that would not commit, a shutdown deadline that expired — each leaves the job in `processing`, and the source bytes are the only thing from which that job can ever be finished. Deleting them turns a job the sweeper's fenced takeover could have recovered into one that can only be failed. An unconditional deferred delete registered at claim time therefore SHALL NOT be used; the delete SHALL be guarded on the committed outcome.

**It SHALL NOT delete the source object when its terminal write was refused by the fence.** The fence can mean either that a newer epoch owns the job or that another actor committed a different terminal outcome at the same epoch; in both cases this actor applied nothing and has no cleanup right. When a successor is still processing, deletion would also destroy its input. Any residue is safer than premature deletion and remains reclaimable by the storage lifecycle rule.

A consumer SHALL NOT delete the source object when it did not win the claim either. Another consumer is processing that job from those exact bytes, and deleting them would destroy a running extraction's input — the failure mode generation isolation exists to prevent, reintroduced from inside.

**The sweeper's abandonment is the other way to earn the deletion, and it is gated the same way.** It SHALL attempt to delete the source object only when its `failed` write was *applied by that call* — not when the row merely already carries the outcome it intended, which is what a second sweeper at the same bound observes (see `videojob-lease-recovery`). The one actor that may delete on an already-present outcome is a caller retrying a write it made itself and whose response it lost, because that work is its own.

The deletion SHALL be best effort, as it was when the HTTP handler owned it: one attempt, no retry, a failure logged with the `StorageKey` and not escalated. A failed deletion SHALL NOT prevent the message from being acknowledged, because the job is already terminal and the dispatch must not be redelivered.

**A job that is never dispatched leaks its source object permanently**, and this capability SHALL NOT claim otherwise. Once ownership moves here, no component deletes the source of a job whose message was never published, never delivered, or dead-lettered before the claim. The object-storage lifecycle rule on the source key prefix is the only remaining guarantee, and `docs/operations.md` SHALL describe it as such rather than as a backstop.

#### Scenario: Successful cleanup removes a completed job's source

- **GIVEN** a dispatched job the worker processes to `completed` and whose source deletion succeeds
- **WHEN** the message has been acknowledged
- **THEN** no object exists under that job's source key

#### Scenario: Successful cleanup removes a failed job's source

- **GIVEN** a dispatched job whose extraction fails, whose failure write this run applies, and whose source deletion succeeds
- **WHEN** the message has been acknowledged
- **THEN** the job is `failed` and no object exists under its source key

#### Scenario: A job left in processing keeps its source object

- **GIVEN** a claimed job whose extraction succeeded but whose `CompleteJob` write cannot commit after the bounded retries
- **WHEN** the worker gives up on the message
- **THEN** the job is still `processing`, the source object is still present, the message is dead-lettered rather than acknowledged, and the job identifier and result storage key are logged

#### Scenario: A fenced worker does not delete the source object

- **GIVEN** a worker whose job was requeued and re-claimed mid-extraction, so its terminal write is refused by the fence
- **WHEN** it gives up on the message
- **THEN** this worker makes no source-deletion call; the object remains available while a successor still needs it, or may already have been removed by that successor's own terminal cleanup

#### Scenario: A panic mid-extraction does not delete the source object

- **GIVEN** a claimed job whose processing panics before any terminal transition commits
- **WHEN** the worker unwinds
- **THEN** the source object is still present, because no terminal state was committed

#### Scenario: A lost claim leaves the source object alone

- **GIVEN** a message naming a job another consumer has already claimed
- **WHEN** the worker is refused the claim and dead-letters the message
- **THEN** the source object still exists, so the consumer that won the claim can still read it

#### Scenario: The sweeper deletes the source of a job it abandoned

- **GIVEN** a job the sweeper fails after the requeue bound, whose terminal write that call applied
- **WHEN** the sweep finishes with it
- **THEN** the sweeper makes one best-effort attempt to delete the source object and clear the idempotency key; either resource may remain if its cleanup call fails

#### Scenario: A second sweeper at the same bound deletes nothing

- **GIVEN** two sweepers reaching the bound for one job, the first having applied the `failed` write
- **WHEN** the second finds the row already carrying that outcome
- **THEN** it deletes no source object and clears no idempotency key, because it applied nothing

#### Scenario: A failed deletion does not redeliver the dispatch

- **GIVEN** a job processed to a terminal state and object storage rejecting the delete
- **WHEN** the worker finishes the message
- **THEN** the failure is logged with the storage key, the message is acknowledged, and the job is not processed a second time

### Requirement: The Worker Clears a Failed Job's Idempotency Key

When a job reaches `failed` under this process — whether committed by the consumer handling its dispatch or by the sweeper abandoning it after repeated recovery (see `videojob-lease-recovery`) — the worker SHALL immediately attempt to delete that job's idempotency key, so an identical-content resubmission is treated as a fresh attempt rather than being deduplicated for the remainder of the fixed window.

The worker SHALL reconstruct the key from the job's owner and its persisted content hash, and SHALL attempt deletion through an operation that removes the key only when it still refers to this job — the finalized value names the job, so matching on the job identifier proves ownership exactly as the reservation token did, and a key already reclaimed by a newer request names neither.

It SHALL NOT clear the key for a job whose `failed` write it did not itself apply. A worker whose terminal write was refused by the fence has not failed anything, and a sweeper that found the row already carrying the outcome it intended did not fail it either; clearing in either case would release a mapping that belongs to a job another actor owns. The completion retry exception in the source-object rule does not apply here: completed jobs deliberately retain their idempotency mapping, while an already-present failure is acknowledged without cleanup.

The reservation **token** SHALL NOT be persisted anywhere to enable this. It is a possession capability whose whole purpose is to be held only by the request that minted it; storing it in a table that outlives the key's window, and that every job read touches, would buy nothing the job identifier does not already prove.

A failure to clear the key SHALL be logged and SHALL NOT fail the job or prevent acknowledgement. The key expires on its own window regardless; the obligation this requirement adds is one prompt attempt, not guaranteed immediate removal.

#### Scenario: Retry after an asynchronous failure is not blocked

- **GIVEN** a submitted video whose processing failed in the worker and whose conditional idempotency clear succeeded
- **WHEN** the same user resubmits identical content immediately afterwards
- **THEN** the submission is treated as fresh and creates a new `VideoJob`, rather than returning the failed one

#### Scenario: Retry after repeated abandonment is not blocked either

- **GIVEN** a submitted video whose job the sweeper failed after exhausting its recovery attempts and whose conditional idempotency clear succeeded
- **WHEN** the same user resubmits identical content immediately afterwards
- **THEN** the submission is treated as fresh and creates a new `VideoJob`

#### Scenario: A successful job's key is left pointing at it

- **GIVEN** a submitted video the worker processes to `completed`
- **WHEN** the same user resubmits identical content within the window
- **THEN** the existing completed job is returned, because the worker cleared nothing

#### Scenario: A fenced worker clears nothing

- **GIVEN** a worker whose failure write was refused by the fence because another holder owns the job
- **WHEN** it finishes with the message
- **THEN** the job's idempotency key is unchanged

#### Scenario: The clear cannot remove a key a newer request owns

- **GIVEN** a failed job whose idempotency key has already been reclaimed by a newer submission of the same content
- **WHEN** the worker attempts its clear for the failed job
- **THEN** the key is left intact and the newer submission's reservation is unaffected

### Requirement: The Worker Is Its Own Composition Root With Its Own Configuration Surface

`cmd/worker` SHALL be a separate binary with its own composition root, not a mode of `cmd/video-api`. It SHALL require the configuration for the services it actually uses — the Video Processing database, object storage, Redis, and the broker — and SHALL NOT require identity configuration, which it has no use for.

**It SHALL run the terminal-event relay and SHALL NOT run the job-dispatch relay.** The previous form of this requirement barred the worker from running any relay, on the ground that a second relay would double the claim polling against the outbox table for no additional dispatch. That reasoning held while one event stream existed; it does not survive `videojob-terminal-events`, which adds a second, disjoint stream. The two relays claim non-overlapping event-type sets, so neither polls for the other's rows and no dispatch is duplicated. The worker takes the terminal stream because it is the process that writes those rows — through its own terminal writes and its sweeper's abandonment write — so an outcome's announcement does not come to depend on an API replica being up. The dispatch stream stays in `cmd/video-api`, which is where `POST /upload` writes its rows.

Broker reachability SHALL be treated as the worker's own concern rather than a fatal startup gate, matching the relay: the worker SHALL dial, SHALL redial with bounded backoff when the connection or the consuming channel is lost, and SHALL redeclare the topology after every successful dial. **This SHALL hold for both of the worker's broker connections — the consumer's and the relay's — each of which owns its own dial loop and declares its own topology.** Within the worker, the consumer declares the job topology and the terminal relay declares the terminal-event topology; the dispatch relay that also declares the job topology runs in `cmd/video-api`, not here (`videojob-outbox-relay`). Both processes declaring is the point: neither process's startup may depend on the other having run first, so against a fresh or recreated broker a worker started alone still has a queue to consume from and an exchange to publish into.

Unlike the API, the worker SHALL exit non-zero if it cannot reach object storage or the database at startup — it has no request path to degrade, and a worker that consumes messages it cannot possibly process would drain the queue into the dead-letter queue.

#### Scenario: The worker starts without identity configuration

- **WHEN** the worker is started with the database, object storage, Redis, and broker configuration but no identity variables
- **THEN** it starts and begins consuming

#### Scenario: The worker starts before the broker is reachable

- **GIVEN** a broker that is not yet accepting connections
- **WHEN** the worker starts
- **THEN** it does not exit, it retries with backoff, and it begins consuming and relaying once the broker is available

#### Scenario: Each of the worker's connections declares its own topology on every dial

- **GIVEN** a broker whose exchanges and queues have been deleted while the worker was disconnected
- **WHEN** the consumer's connection redials
- **THEN** it declares the job topology and resumes consuming, and it declares nothing of the terminal-event topology
- **AND WHEN** the terminal relay's connection redials
- **THEN** it declares the terminal-event topology and resumes publishing without a message being published into a missing exchange

The two loops are independent, so a redeclaration follows each connection's own dial rather than the worker's. Whichever of the two reconnects first restores its own half; the topology the other owns is restored when that one redials.

#### Scenario: The worker does not claim job-dispatch rows

- **GIVEN** unpublished job-dispatch outbox rows
- **WHEN** the worker is running
- **THEN** its relay does not claim or publish them, and their `published_at` values are changed only by the API's dispatch relay

#### Scenario: Unreachable storage stops the worker rather than draining the queue

- **WHEN** the worker is started with object-storage configuration it cannot reach
- **THEN** it exits with an error naming the failure, rather than consuming and dead-lettering messages it could never have processed

### Requirement: The Worker Stops With Its Process and Finishes the Message in Hand

On `SIGINT` or `SIGTERM` the worker SHALL stop accepting new deliveries, SHALL let the current handler finish and apply its normal Ack/Reject decision when it completes before the deadline, SHALL stop its sweeper, **its terminal-event relay,** and its lease renewal, and SHALL then close the broker connections and its database, Redis, and storage handles in an order that keeps the in-flight work valid.

The sweeper **and the relay** SHALL be cancelled and **joined** before those handles close, exactly as `cmd/video-api` joins its outbox relay: each holds a database transaction while it runs — the sweeper across a requeue, the relay across a claim and its broker round trip — and closing the pool underneath either would abort it rather than resolve it. Lease renewal SHALL stop when the in-flight job does. Lease release SHALL be attempted only when this worker applied a terminal outcome or its bounded completion retry found its own identical outcome already present. A release error SHALL be logged and left to TTL expiry without changing the normal Ack/Reject disposition. After a fenced write, an already-present failure, or a non-terminal error, this worker has no cleanup right and SHALL leave the lease to expire (or leave a newer epoch's lease untouched).

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

### Requirement: A Dispatch Whose Claim Outcome the Worker Could Not Learn Is Requeued, Paced

When the worker's claim step **could not learn whether the claim was won**, because the persistence layer could not answer, the worker SHALL requeue the message rather than dead-lettering it, and SHALL pause before taking its next delivery.

The condition SHALL be identified positively and narrowly: a persistence call in the claim step — the authoritative load that precedes the claim, the claim itself, or the existence probe that follows a zero-row claim — could not be answered, so this worker did not learn its claim's outcome, and therefore **this worker has run no extraction, acquired no lease, read no source object, written no event and attempted no terminal write**. It SHALL be distinguished from a claim that was decided against this worker — a lost claim, which dead-letters — and from every failure in which the database *did* answer: a row that could not be interpreted, and a statement the server refused, both of which are permanent and dead-letter. The worker SHALL NOT identify the condition by inspecting a database driver's error values; the distinction SHALL be carried as a sentinel raised by the layer that made the call, per `videojob-persistence` and `videojob-execution`.

**What licenses the requeue is not the state of the stored row, which this worker cannot know, but that the worker produced no side effect and a redelivery decides the row through mechanisms that already exist.** The condition can be raised at three points: by the authoritative load that precedes any claim, where no claim has been attempted and the row is in whatever state it already had; by the conditional claim statement itself, whose result is read over the connection that carried it, so that a failure reading it cannot say whether it committed; or by a statement the claim step issues only after the claim has provably affected no row, where it certainly did not. The requirement is written against what holds on all three — this worker did not learn its claim's outcome, and has acquired no lease, read no source object, run no extraction, written no event and attempted no terminal write — and asserts nothing about the stored row. The redelivery re-runs the same authoritative load and the same conditional claim, and they decide the row as they would for any dispatch:

- **The row is `queued`.** The redelivery claims it. It is not a retry of work but the same dispatch arriving later, decided by the same conditional claim.
- **The row does not exist, is still `pending`, or is already `processing` or terminal.** The redelivery is dead-lettered under the requirement above — as an unknown job, as a transition the aggregate refuses, or as a lost claim. A `processing` row belongs to a live holder or, if abandoned, to `videojob-lease-recovery`'s sweeper.
- **The claim committed and its result was lost.** This is the one branch that necessarily becomes the sweeper's input. The row is `processing` and carries **no lease**, because `videojob-execution` acquires a lease only once a claim is reported won and here none was. The redelivery loses the claim and is dead-lettered under the requirement above, and the row is then exactly what `videojob-lease-recovery`'s sweeper exists to recover: `processing`, unleased, requeued at a fresh epoch after two observations.

This is the whole of the justification. **It SHALL NOT be restated as a guarantee that the system is unchanged** — no call in the claim step can provide that, and the disposition does not need it. It SHALL NOT be extended to any condition that can arise once this worker may have produced a side effect, and in particular not to a failure arising after a claim was reported won, where a redelivery could only lose the claim.

The requeue SHALL NOT be bounded by a delivery count. The message is the only record that a `queued` job still needs dispatching, and `videojob-lease-recovery`'s sweeper does not reach a `queued` row; discarding the message after N attempts reproduces the defect this requirement exists to remove. A bound SHALL NOT be obtained by changing the work queue's type, by republishing a counter-carrying copy — which would make the worker a second producer on a stream `videojob-outbox-relay` owns — or by counting in worker-local memory, which counts to one per replica.

**The requeue SHALL be paced, and the pause SHALL be taken after the negative acknowledgement and before the next delivery is accepted.** A requeued message returns toward the front of the queue and is offered again at once, so an unpaced requeue spins at the speed of a failed connection attempt. The pause SHALL be supplied by the composition root rather than read from inside the consumer, so that a requeue's observable behaviour can be exercised at a value a test can wait for.

**The pause SHALL be a fixed constant rather than configuration**, on the same reasoning that makes the lease TTL, the sweep interval and the requeue bound constants: it is a correctness-adjacent margin whose right value depends on the number of running replicas, not a deployment preference. Exposing it as an environment variable would invite a deployment to lower it without the arithmetic below, or to raise it past the point where a dispatch resumes promptly once the dependency returns. Being a constructor parameter is not the same as being configuration — the parameter exists so a test can supply a short value, and the composition root SHALL pass the constant.

The pause SHALL be taken on the consumer's own cancellable context, not on the detached context the handler runs under. Shutdown therefore skips it, and it cannot extend the drain the worker waits on for work in flight — by the time it runs, the handler has returned and nothing is in flight.

**The pause bounds each consumer's rate, not the message's, and the difference SHALL be stated wherever the value is chosen.** A pausing consumer is not idle from the broker's point of view: the negative acknowledgement restores its prefetch credit, so the broker may push the requeued message — its own or another replica's — straight into that consumer's buffer, where it waits until the pause ends. Across N replicas the message is therefore attempted up to N times per pause, and those attempts arrive as a burst once per pause rather than evenly spaced. The *average* deployment-wide redelivery interval is still approximately the pause divided by N, and a dispatch resumes within one pause of the dependency answering again. A reader choosing or reviewing that value needs the deployment-wide number and its burst shape, not the per-consumer one.

**The accepted cost is head-of-line blocking, unbounded in duration.** At a prefetch of one, a requeued message occupies each replica in turn for as long as the condition lasts. That is accepted here — where the analogous consumer in `notification-event-consumer` accepts it only under a bound — for a reason specific to this condition: it is entered only when the dependency **every** message on this queue requires is unavailable, so there is no work any replica could be doing instead and the queue's head and tail are blocked equally. That premise SHALL be preserved by keeping the condition narrow; a broader rule admitting a per-message permanent failure would let one message block every replica against healthy work.

**A second accepted cost: an ambiguous commit spends one of the sweeper's bounded requeues.** Each pass through the committed branch above leaves a row `videojob-lease-recovery` requeues at a fresh epoch, consuming one of the requeues it permits before it commits `failed`. A server available enough to commit and unavailable enough to lose its result can therefore exhaust that bound on a single job. The outcome is then a job its owner can see and re-upload — the committed failure clears the idempotency key — rather than a job stranded in `queued` with nothing able to reach it, which is the outcome this requirement exists to remove. The trade SHALL NOT be answered by widening the sweeper's bound, which would weaken recovery for every other job to soften an outcome that is already visible and already recoverable by the user.

#### Scenario: A dispatch whose claim the repository could not answer is requeued

- **GIVEN** a job in `queued` status and a worker whose authoritative load reads it but whose claim statement cannot reach the database, so that statement never reaches the server
- **WHEN** the worker consumes that job's dispatch
- **THEN** the message is requeued rather than dead-lettered, the job is still `queued`, no source object was read, no lease was acquired, and nothing appears in the dead-letter queue

#### Scenario: The dispatch survives the outage and is processed when it ends

- **GIVEN** a dispatch requeued because the database was unreachable
- **WHEN** the database becomes reachable again
- **THEN** the next redelivery claims the job and drives it to a terminal state, with no operator action and no replay of the dead-letter queue

#### Scenario: The worker does not spin while the database is down

- **GIVEN** a worker that has requeued a dispatch because the database is unreachable
- **WHEN** it returns to consuming
- **THEN** it waits the configured pause before accepting another delivery, and the number of redeliveries observed over an interval is bounded by that pause and the number of running replicas

#### Scenario: Shutdown is not delayed by the pause

- **GIVEN** a worker that has just requeued a dispatch and is in its pause
- **WHEN** shutdown is signalled
- **THEN** the pause is abandoned and the consumer stops, rather than running the pause to completion

#### Scenario: A claim decided against the worker is still dead-lettered

- **GIVEN** a reachable database and a dispatch naming a job that is no longer `queued`
- **WHEN** the worker consumes it
- **THEN** the message is dead-lettered rather than requeued, because the claim was decided and this worker lost it

#### Scenario: An ambiguous claim commit is requeued and then recovered by the sweeper

- **GIVEN** a claim that committed in the database but whose result the worker could not read
- **WHEN** the worker applies this requirement's disposition
- **THEN** it requeues the dispatch on the same sentinel as every other branch, having acquired no lease and read no source object — the disposition does not distinguish the branches, and is not required to

#### Scenario: The redelivery of an ambiguous claim commit is dead-lettered, and the row is the sweeper's

- **GIVEN** a dispatch requeued after a claim that had in fact committed
- **WHEN** the redelivery arrives and finds the row `processing`
- **THEN** it is refused by the claim and dead-lettered under the requirement above, and the row — `processing` with no lease — is recovered by the sweeper on its ordinary two-observation path, at the cost of one of its bounded requeues

### Requirement: A Dispatch Superseded by a Transient Object-Storage Retry Is Acknowledged

When `ProcessVideoJob` returns the requeued-for-retry sentinel, the worker SHALL acknowledge the message.

This is an acknowledgement rule on its own footing — it selects the consumer's existing `Ack` for a distinct sentinel and adds no disposition value, and it is the non-terminal committed transition the prefetch requirement above names — and not an exception to either the dead-letter or the unknown-claim requirement, and it SHALL NOT be read as weakening them. The message is not one the worker cannot act on, so dead-lettering it would put a healthy job's history in the place reserved for anomalies; and it is not a message whose outcome the worker failed to learn, so requeueing it would put a second dispatch for the same job on the queue beside the one the retry write already committed. **This delivery is spent because a committed write superseded it**: the row is `queued` at an advanced epoch and a fresh dispatch row exists in the outbox, so the relay, not this message, carries the job forward. The rule that rejection is the default for any failure nobody enumerated SHALL be unaffected — the acknowledgement is keyed on the sentinel alone, and a retry write that failed for any other reason reaches the default.

On acknowledging, the worker SHALL release the lease it held only when this run's retry write was the one applied, gated exactly as the failure path gates its cleanup. It SHALL NOT delete the source object and SHALL NOT clear the idempotency key: the next attempt reads the one, and the other, where a reservation was made and has not expired, keeps answering a resubmission of identical content with this job rather than creating a second.

A retry write refused by the fence SHALL be handled as any other fenced outcome under the dead-letter requirement above.

#### Scenario: A transient storage failure acknowledges the dispatch and keeps the job's inputs

- **GIVEN** a dispatched job whose source object cannot be read because the object store is failing, at an epoch below the retry bound
- **WHEN** the worker finishes the message
- **THEN** the message is acknowledged rather than requeued or dead-lettered, the job is `queued` at the next epoch with a new dispatch row, the source object still exists, the idempotency key was not cleared, and this run's lease is released

#### Scenario: The superseded dispatch is not the one that carries the job forward

- **GIVEN** a job returned to `queued` by a transient storage retry whose message was acknowledged
- **WHEN** the dispatch relay publishes the new dispatch row and the object store has recovered
- **THEN** a worker claims the job at its new epoch and runs it to a terminal state, and if at-least-once delivery produces a duplicate of either dispatch, exactly one delivery wins the conditional claim and every other is dead-lettered as a lost claim without modifying the job

#### Scenario: A missing source object is not retried

- **GIVEN** a dispatched job whose source key names no stored object
- **WHEN** the worker finishes the message
- **THEN** the job is `failed`, the message is acknowledged after the ordinary failure cleanup, and no dispatch row is written for another attempt
