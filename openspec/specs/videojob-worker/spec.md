# videojob-worker Specification

## Purpose

Define `cmd/worker`, the process that turns a dispatched `video_job.queued` message into a finished `VideoJob`: what it consumes, how it claims a job so a duplicate delivery cannot double-process it, when it acknowledges, dead-letters, or leaves a message outstanding, which side effects it owns (the source object, the failed job's idempotency key) and under exactly which conditions, its own composition root and configuration surface, and how it shuts down.

It is the consumer that `videojob-messaging`'s topology and `videojob-outbox-relay`'s publishing were built for. The extraction sequence it runs is `videojob-execution`'s `ProcessVideoJob`; the transitions it drives are `videojob-lifecycle`'s; the conditional claim underneath them is `videojob-persistence`'s. This capability owns only what the worker process itself decides — it makes no access-control decision (`video-processing-access`) and serves no HTTP.
## Requirements
### Requirement: cmd/worker Consumes the Job Queue and Runs Each Dispatch to a Terminal State

A `cmd/worker` entrypoint SHALL consume the job-dispatch queue defined by `videojob-messaging` and, for each message, run `ProcessVideoJob` against the `job_id` and `source_key` the message carries, driving the job to `completed` or `failed`.

It SHALL call `CompleteJob` on success **only**, and only after `ProcessVideoJob` has reported that the result is stored in the bucket. Because storing the result is part of `ProcessVideoJob`'s own sequence, a result reporting success is itself the durability guarantee the worker waits for; the worker SHALL NOT record any additional ownership artifact before completing the job. If `ProcessVideoJob` reports failure, the worker SHALL NOT call `CompleteJob`, so a job's persisted status never claims `completed` for a result that was not stored.

It SHALL NOT call `FailJob`: `ProcessVideoJob` already fails the job itself for a fetch, extraction, or storage failure, so a worker that also called it would ask the domain for a rejected `failed → failed` transition. An implementer SHALL NOT add a failure call "for symmetry".

**The terminal write can fail after the result is already durable, and the worker SHALL have a policy for it rather than discovering one.** `CompleteJob` returns its repository error and leaves the stored job in `processing`, so a transient database failure at that moment produces a job with a usable result that no listing shows. The worker SHALL retry the terminal write a bounded number of times with backoff, on a context detached from any cancellation that may have caused the failure, because the overwhelmingly likely cause is transient and one more attempt costs nothing next to a re-extraction.

If the write still cannot commit, the worker SHALL reject the message without requeue, SHALL leave the job as it stands, and SHALL log the job identifier and the result `StorageKey` — never a credential — so the orphan is enumerable. It SHALL NOT delete the source object in that case (see the source-ownership requirement below), and SHALL NOT acknowledge the message: the job did not reach a terminal state, and both the bytes and the dead-lettered message are what a later recovery has to work from.

This orphan class is not introduced here — the synchronous pipeline produced the same shape when the post-processing write failed — but it becomes the worker's to bound, and reconciling it remains out of scope.

The worker SHALL take the source location from the message rather than reconstructing it. `ProcessVideoJob` accepts a source `StorageKey` precisely so a process that shares no filesystem with the HTTP handler can run it, and the key embeds a generated upload identifier that is not derivable from any other field.

The worker SHALL NOT serve HTTP, SHALL NOT be reachable from outside the deployment, and SHALL NOT perform an access-control decision (see `video-processing-access`).

#### Scenario: A dispatched job is processed to completion

- **GIVEN** a `VideoJob` in `queued` status whose source object holds a video `ffmpeg` can decode, and a message for it on the job queue
- **WHEN** the worker consumes that message
- **THEN** the job reaches `completed` with a `StorageKey` and `FrameCount`, the result zip is present in the bucket under that key, and the message is acknowledged

#### Scenario: An undecodable source fails the job exactly once

- **GIVEN** a `VideoJob` in `queued` status whose source object `ffmpeg` cannot decode
- **WHEN** the worker consumes its message
- **THEN** the job reaches `failed` with a persisted reason, and the worker itself performed no transition call beyond the claim — the failure was recorded by `ProcessVideoJob`

#### Scenario: A result that was not stored leaves the job failed, not completed

- **GIVEN** a dispatched job whose frames extract successfully but whose zip cannot be stored
- **WHEN** the worker finishes the message
- **THEN** the job's persisted status is `failed`, not `completed`, and `GetJobStatus` never reports a `StorageKey` for an object that is not in the bucket

#### Scenario: The worker uses the key from the message

- **GIVEN** a message whose `source_key` names a stored object
- **WHEN** the worker processes it
- **THEN** it fetches exactly that key, and does not derive a key from the job's original filename or identifier

### Requirement: Prefetch Is One, and Acknowledgement Follows the Terminal Write

The worker SHALL set a consumer prefetch of exactly one unacknowledged message and SHALL acknowledge a message only after the transition that makes its job terminal has been committed.

Prefetch above one SHALL NOT be configured. The unit of work is a full extraction — seconds to minutes of `ffmpeg`, not microseconds — so buffering buys no throughput. What it costs is availability of the buffered work: a prefetched message is held by this consumer and is not offered to any other, so it waits behind work of unbounded duration while an idle worker elsewhere has nothing to take. The messages themselves are not lost — a prefetched delivery has not been handled, its job is still `queued`, and the broker requeues it when this consumer's connection closes — so the reason for the bound is fairness and latency, not durability.

Acknowledging before the terminal write SHALL NOT be done: a crash between the acknowledgement and the commit destroys the only remaining record that the job needs processing.

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

When the worker cannot act on a message, it SHALL reject the message **without requeue**, so the topology's dead-letter route takes it. This SHALL apply to a message whose payload cannot be parsed, one naming a job that does not exist, one whose job the worker could not claim, and one whose job was **taken away from the worker while it ran** — a terminal write refused by the fence (see `videojob-lease-recovery`).

It SHALL NOT requeue such a message: none of these conditions is transient, so requeueing produces an unbounded redelivery loop against a message that will never succeed. A fenced result means either a newer epoch was re-dispatched and may have a live successor, or another actor committed a different terminal outcome at the same epoch. Requeueing is wrong in both cases: it would add a competing delivery in the first and can never claim the already-terminal row in the second.

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

`cmd/worker` SHALL be a separate binary with its own composition root, not a mode of `cmd/api`. It SHALL require the configuration for the services it actually uses — the Video Processing database, object storage, Redis, and the broker — and SHALL NOT require identity configuration, which it has no use for.

It SHALL NOT run the outbox relay. The relay belongs to `cmd/api`, and running it in both would double the claim polling against the outbox table for no additional dispatch.

Broker reachability SHALL be treated as the worker's own concern rather than a fatal startup gate, matching the relay: the worker SHALL dial, SHALL redial with bounded backoff when the connection or the consuming channel is lost, and SHALL redeclare the topology after every successful dial. The relay declares the same topology on its own dials (`videojob-outbox-relay`), and both declaring is the point: neither process's startup may depend on the other having run first, so against a fresh or recreated broker a worker started alone still has a queue to consume from.

Unlike the API, the worker SHALL exit non-zero if it cannot reach object storage or the database at startup — it has no request path to degrade, and a worker that consumes messages it cannot possibly process would drain the queue into the dead-letter queue.

#### Scenario: The worker starts without identity configuration

- **WHEN** the worker is started with the database, object storage, Redis, and broker configuration but no identity variables
- **THEN** it starts and begins consuming

#### Scenario: The worker starts before the broker is reachable

- **GIVEN** a broker that is not yet accepting connections
- **WHEN** the worker starts
- **THEN** it does not exit, it retries with backoff, and it begins consuming once the broker is available

#### Scenario: The worker declares the topology on every dial

- **GIVEN** a broker whose exchanges and queues have been deleted while the worker was disconnected
- **WHEN** the worker reconnects
- **THEN** the topology is declared again and consumption resumes without a message being published into a missing exchange

#### Scenario: Unreachable storage stops the worker rather than draining the queue

- **WHEN** the worker is started with object-storage configuration it cannot reach
- **THEN** it exits with an error naming the failure, rather than consuming and dead-lettering messages it could never have processed

### Requirement: The Worker Stops With Its Process and Finishes the Message in Hand

On `SIGINT` or `SIGTERM` the worker SHALL stop accepting new deliveries, SHALL let the current handler finish and apply its normal Ack/Reject decision when it completes before the deadline, SHALL stop its sweeper and its lease renewal, and SHALL then close the broker connection and its database, Redis, and storage handles in an order that keeps the in-flight work valid.

The sweeper SHALL be cancelled and **joined** before those handles close, exactly as `cmd/api` joins its outbox relay: it holds a database transaction while it runs, and closing the pool underneath a requeue would abort it rather than resolve it. Lease renewal SHALL stop when the in-flight job does. Lease release SHALL be attempted only when this worker applied a terminal outcome or its bounded completion retry found its own identical outcome already present. A release error SHALL be logged and left to TTL expiry without changing the normal Ack/Reject disposition. After a fenced write, an already-present failure, or a non-terminal error, this worker has no cleanup right and SHALL leave the lease to expire (or leave a newer epoch's lease untouched).

It SHALL NOT abandon an in-flight extraction by exiting immediately. The redelivery that would follow cannot re-claim the job — the claim predicate refuses a `processing` row — so an abrupt exit converts an orderly restart into a job that only the sweeper can recover, after the lease it stopped renewing has expired.

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

#### Scenario: A job finished during shutdown has its lease released

- **GIVEN** a worker that applied a cleanup-eligible terminal outcome during shutdown and whose lease release succeeds
- **WHEN** it exits
- **THEN** the lease store reports that job as not held immediately, without waiting for the expiry

#### Scenario: Shutdown does not block forever

- **GIVEN** a worker whose in-flight extraction exceeds the shutdown deadline
- **WHEN** the deadline passes
- **THEN** the worker logs the in-flight job identifier and exits, and that job is recovered by a later sweep once its lease lapses
