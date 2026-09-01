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

When the worker cannot act on a message, it SHALL reject the message **without requeue**, so the topology's dead-letter route takes it. This SHALL apply to a message whose payload cannot be parsed, one naming a job that does not exist, and one whose job the worker could not claim.

It SHALL NOT requeue such a message: none of these conditions is transient, so requeueing produces an unbounded redelivery loop against a message that will never succeed.

It SHALL NOT acknowledge such a message either. An acknowledged message is gone from the broker, which would leave nothing to enumerate afterwards — the dead-letter queue is the only place these anomalies remain visible, and `videojob-messaging` keeps it unversioned so there is one place to look.

A rejected message SHALL NOT cause a transition. In particular a lost claim SHALL NOT be turned into a `FailJob` call: the job belongs to whichever consumer won.

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

### Requirement: The Worker Deletes the Source Object, and Only If It Won the Claim

The worker SHALL delete the source object named by the message once the job it claimed has **committed** a terminal state — `completed` or `failed` — on success and on failure alike. The deletion SHALL be attempted on every path that returns normally after such a commit. It is deliberately *not* a deferred cleanup: a panic unwinding between the commit and the delete leaves the object behind, which is the same leak this requirement already accepts below and strictly safer than the alternative a defer would invite.

**It SHALL NOT delete the source object on any path where the job did not reach a committed terminal state**, and this is the condition that matters most, because getting it wrong is unrecoverable rather than merely untidy. A panic mid-extraction, a `CompleteJob` write that would not commit, a shutdown deadline that expired — each leaves the job in `processing`, and the source bytes are the only thing from which that job can ever be finished. Deleting them turns a job a later fenced takeover could have recovered into one that can only be failed. An unconditional deferred delete registered at claim time therefore SHALL NOT be used; the delete SHALL be guarded on the committed outcome.

The bytes left behind in that case leak, and that is the deliberate trade: a leaked object is reclaimable by the storage lifecycle rule, while a deleted one is gone.

It SHALL NOT delete the source object when it did not win the claim either. Another consumer is processing that job from those exact bytes, and deleting them would destroy a running extraction's input — the failure mode generation isolation exists to prevent, reintroduced from inside.

The deletion SHALL be best effort, as it was when the HTTP handler owned it: one attempt, no retry, a failure logged with the `StorageKey` and not escalated. A failed deletion SHALL NOT prevent the message from being acknowledged, because the job is already terminal and the dispatch must not be redelivered.

**A job that is never dispatched leaks its source object permanently**, and this capability SHALL NOT claim otherwise. Once ownership moves here, no component deletes the source of a job whose message was never published, never delivered, or dead-lettered before the claim. The object-storage lifecycle rule on the source key prefix is the only remaining guarantee, and `docs/operations.md` SHALL describe it as such rather than as a backstop.

#### Scenario: A completed job's source object is gone

- **GIVEN** a dispatched job the worker processes to `completed`
- **WHEN** the message has been acknowledged
- **THEN** no object exists under that job's source key

#### Scenario: A failed job's source object is gone

- **GIVEN** a dispatched job whose extraction fails
- **WHEN** the message has been acknowledged
- **THEN** the job is `failed` and no object exists under its source key

#### Scenario: A job left in processing keeps its source object

- **GIVEN** a claimed job whose extraction succeeded but whose `CompleteJob` write cannot commit after the bounded retries
- **WHEN** the worker gives up on the message
- **THEN** the job is still `processing`, the source object is still present, the message is dead-lettered rather than acknowledged, and the job identifier and result storage key are logged

#### Scenario: A panic mid-extraction does not delete the source object

- **GIVEN** a claimed job whose processing panics before any terminal transition commits
- **WHEN** the worker unwinds
- **THEN** the source object is still present, because no terminal state was committed

#### Scenario: A lost claim leaves the source object alone

- **GIVEN** a message naming a job another consumer has already claimed
- **WHEN** the worker is refused the claim and dead-letters the message
- **THEN** the source object still exists, so the consumer that won the claim can still read it

#### Scenario: A failed deletion does not redeliver the dispatch

- **GIVEN** a job processed to a terminal state and object storage rejecting the delete
- **WHEN** the worker finishes the message
- **THEN** the failure is logged with the storage key, the message is acknowledged, and the job is not processed a second time

### Requirement: The Worker Clears a Failed Job's Idempotency Key

When a job the worker claimed reaches `failed`, the worker SHALL delete that job's idempotency key immediately, so an identical-content resubmission is treated as a fresh attempt rather than being deduplicated for the remainder of the fixed window.

The worker SHALL reconstruct the key from the job's owner and its persisted content hash, and SHALL delete it through an operation that removes the key only when it still refers to this job — the finalized value names the job, so matching on the job identifier proves ownership exactly as the reservation token did, and a key already reclaimed by a newer request names neither.

The reservation **token** SHALL NOT be persisted anywhere to enable this. It is a possession capability whose whole purpose is to be held only by the request that minted it; storing it in a table that outlives the key's window, and that every job read touches, would buy nothing the job identifier does not already prove.

A failure to clear the key SHALL be logged and SHALL NOT fail the job or prevent acknowledgement. The key expires on its own window regardless; the guarantee this requirement adds is promptness, not eventual removal.

#### Scenario: Retry after an asynchronous failure is not blocked

- **GIVEN** a submitted video whose processing failed in the worker
- **WHEN** the same user resubmits identical content immediately afterwards
- **THEN** the submission is treated as fresh and creates a new `VideoJob`, rather than returning the failed one

#### Scenario: A successful job's key is left pointing at it

- **GIVEN** a submitted video the worker processes to `completed`
- **WHEN** the same user resubmits identical content within the window
- **THEN** the existing completed job is returned, because the worker cleared nothing

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

On `SIGINT` or `SIGTERM` the worker SHALL stop accepting new deliveries, SHALL finish the job it is currently processing and acknowledge it, and SHALL then close the broker connection and its database, Redis, and storage handles in an order that keeps the in-flight work valid.

It SHALL NOT abandon an in-flight extraction by exiting immediately. The redelivery that would follow cannot re-claim the job — the claim predicate refuses a `processing` row — so an abrupt exit converts an orderly restart into a stranded job.

Shutdown SHALL be bounded: if the in-flight job does not finish within the deadline, the worker SHALL exit anyway rather than block indefinitely, accepting the stranded job as the lesser outcome and logging the job identifier so it is enumerable.

#### Scenario: An in-flight job is finished before exit

- **GIVEN** a worker mid-extraction
- **WHEN** it receives `SIGTERM`
- **THEN** it completes that job, acknowledges the message, and exits — and the job is `completed`, not `processing`

#### Scenario: No new work is claimed after the signal

- **GIVEN** a worker that has received `SIGTERM` and is finishing its current job, with further messages queued
- **WHEN** it exits
- **THEN** no additional job was claimed, and the queued messages are still available to another worker

#### Scenario: Shutdown does not block forever

- **GIVEN** a worker whose in-flight extraction exceeds the shutdown deadline
- **WHEN** the deadline passes
- **THEN** the worker logs the in-flight job identifier and exits
