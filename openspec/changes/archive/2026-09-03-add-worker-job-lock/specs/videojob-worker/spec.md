## MODIFIED Requirements

### Requirement: A Message the Worker Cannot Act On Is Dead-Lettered, Never Requeued and Never Acked Away

When the worker cannot act on a message, it SHALL reject the message **without requeue**, so the topology's dead-letter route takes it. This SHALL apply to a message whose payload cannot be parsed, one naming a job that does not exist, one whose job the worker could not claim, and one whose job was **taken away from the worker while it ran** — a terminal write refused by the fence (see `videojob-lease-recovery`).

It SHALL NOT requeue such a message: none of these conditions is transient, so requeueing produces an unbounded redelivery loop against a message that will never succeed. A fenced job is the sharpest case — it has already been re-dispatched by the sweeper, so a requeue here would add a second live delivery for a job that now has a rightful holder.

It SHALL NOT acknowledge such a message either. An acknowledged message is gone from the broker, which would leave nothing to enumerate afterwards — the dead-letter queue is the only place these anomalies remain visible, and `videojob-messaging` keeps it unversioned so there is one place to look.

A rejected message SHALL NOT cause a transition. In particular a lost claim SHALL NOT be turned into a `FailJob` call: the job belongs to whichever consumer won. A fenced write SHALL NOT be retried unfenced, re-read, or converted into a failure for the same reason.

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

### Requirement: The Worker Deletes the Source Object, and Only If It Won the Claim

**This requirement's name is now shorthand for a wider rule and SHALL be read through this paragraph.** Winning the claim is the *consumer's* way of earning the right to delete a source object, and it is no longer the only one: the sweeper never claims anything, yet `videojob-lease-recovery` requires it to delete the source of a job it abandons. The governing condition is therefore **having applied this job's terminal write** — the consumer applies it by claiming, extracting, and committing; the sweeper applies it by winning the conditional abandonment write at the epoch its scan observed. Every "did not win the claim" clause below is an instance of that condition, not an independent one, and an actor that did not apply the terminal write deletes nothing whichever role it is in.

The worker SHALL delete the source object named by the message once the job it claimed has **committed** a terminal state — `completed` or `failed` — on success and on failure alike. The deletion SHALL be attempted on every path that returns normally after such a commit. It is deliberately *not* a deferred cleanup: a panic unwinding between the commit and the delete leaves the object behind, which is the same leak this requirement already accepts below and strictly safer than the alternative a defer would invite.

**It SHALL NOT delete the source object on any path where the job did not reach a committed terminal state**, and this is the condition that matters most, because getting it wrong is unrecoverable rather than merely untidy. A panic mid-extraction, a `CompleteJob` write that would not commit, a shutdown deadline that expired — each leaves the job in `processing`, and the source bytes are the only thing from which that job can ever be finished. Deleting them turns a job the sweeper's fenced takeover could have recovered into one that can only be failed. An unconditional deferred delete registered at claim time therefore SHALL NOT be used; the delete SHALL be guarded on the committed outcome.

**It SHALL NOT delete the source object when its terminal write was refused by the fence**, and this case is now the sharpest instance of the rule rather than a hypothetical. A fenced write means another worker holds the job *right now* and is reading those exact bytes; deleting them would destroy a running extraction's input. The commit the delete is guarded on SHALL be this worker's own committed write, never merely the observation that the job has reached a terminal state.

The bytes left behind in that case leak, and that is the deliberate trade: a leaked object is reclaimable by the storage lifecycle rule, while a deleted one is gone.

A consumer SHALL NOT delete the source object when it did not win the claim either. Another consumer is processing that job from those exact bytes, and deleting them would destroy a running extraction's input — the failure mode generation isolation exists to prevent, reintroduced from inside.

**The sweeper's abandonment is the other way to earn the deletion, and it is gated the same way.** It SHALL delete the source object only when its `failed` write was *applied by that call* — not when the row merely already carries the outcome it intended, which is what a second sweeper at the same bound observes (see `videojob-lease-recovery`). The one actor that may delete on an already-present outcome is a caller retrying a write it made itself and whose response it lost, because that work is its own.

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

#### Scenario: A fenced worker leaves the source object for the job's new holder

- **GIVEN** a worker whose job was requeued and re-claimed mid-extraction, so its terminal write is refused by the fence
- **WHEN** it gives up on the message
- **THEN** the source object is still present, and the new holder's extraction reads it successfully

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
- **THEN** no object exists under that job's source key, and its idempotency key no longer maps that content to it

#### Scenario: A second sweeper at the same bound deletes nothing

- **GIVEN** two sweepers reaching the bound for one job, the first having applied the `failed` write
- **WHEN** the second finds the row already carrying that outcome
- **THEN** it deletes no source object and clears no idempotency key, because it applied nothing

#### Scenario: A failed deletion does not redeliver the dispatch

- **GIVEN** a job processed to a terminal state and object storage rejecting the delete
- **WHEN** the worker finishes the message
- **THEN** the failure is logged with the storage key, the message is acknowledged, and the job is not processed a second time

### Requirement: The Worker Clears a Failed Job's Idempotency Key

When a job reaches `failed` under this process — whether committed by the consumer handling its dispatch or by the sweeper abandoning it after repeated recovery (see `videojob-lease-recovery`) — the worker SHALL delete that job's idempotency key immediately, so an identical-content resubmission is treated as a fresh attempt rather than being deduplicated for the remainder of the fixed window.

The worker SHALL reconstruct the key from the job's owner and its persisted content hash, and SHALL delete it through an operation that removes the key only when it still refers to this job — the finalized value names the job, so matching on the job identifier proves ownership exactly as the reservation token did, and a key already reclaimed by a newer request names neither.

It SHALL NOT clear the key for a job whose `failed` write it did not itself apply. A worker whose terminal write was refused by the fence has not failed anything, and a sweeper that found the row already carrying the outcome it intended did not fail it either; clearing in either case would release a mapping that belongs to a job another actor owns. The single exception is the one the source-object rule already carries: a caller retrying its own write whose response was lost.

The reservation **token** SHALL NOT be persisted anywhere to enable this. It is a possession capability whose whole purpose is to be held only by the request that minted it; storing it in a table that outlives the key's window, and that every job read touches, would buy nothing the job identifier does not already prove.

A failure to clear the key SHALL be logged and SHALL NOT fail the job or prevent acknowledgement. The key expires on its own window regardless; the guarantee this requirement adds is promptness, not eventual removal.

#### Scenario: Retry after an asynchronous failure is not blocked

- **GIVEN** a submitted video whose processing failed in the worker
- **WHEN** the same user resubmits identical content immediately afterwards
- **THEN** the submission is treated as fresh and creates a new `VideoJob`, rather than returning the failed one

#### Scenario: Retry after repeated abandonment is not blocked either

- **GIVEN** a submitted video whose job the sweeper failed after exhausting its recovery attempts
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

### Requirement: The Worker Stops With Its Process and Finishes the Message in Hand

On `SIGINT` or `SIGTERM` the worker SHALL stop accepting new deliveries, SHALL finish the job it is currently processing and acknowledge it, SHALL stop its sweeper and its lease renewal, and SHALL then close the broker connection and its database, Redis, and storage handles in an order that keeps the in-flight work valid.

The sweeper SHALL be cancelled and **joined** before those handles close, exactly as `cmd/api` joins its outbox relay: it holds a database transaction while it runs, and closing the pool underneath a requeue would abort it rather than resolve it. Lease renewal SHALL stop when the in-flight job does, and the lease SHALL be released rather than left to expire, so a restarted deployment does not wait out a lease whose holder is gone.

It SHALL NOT abandon an in-flight extraction by exiting immediately. The redelivery that would follow cannot re-claim the job — the claim predicate refuses a `processing` row — so an abrupt exit converts an orderly restart into a job that only the sweeper can recover, after the lease it stopped renewing has expired.

Shutdown SHALL be bounded: if the in-flight job does not finish within the deadline, the worker SHALL exit anyway rather than block indefinitely, logging the job identifier so it is enumerable. Such a job is no longer stranded permanently — its lease lapses and a sweeper returns it to the queue — and the deadline's cost is a duplicated extraction rather than a lost job.

#### Scenario: An in-flight job is finished before exit

- **GIVEN** a worker mid-extraction
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

#### Scenario: An abandoned job's lease is released rather than left to expire

- **GIVEN** a worker that finished its in-flight job during shutdown
- **WHEN** it exits
- **THEN** the lease store reports that job as not held immediately, without waiting for the expiry

#### Scenario: Shutdown does not block forever

- **GIVEN** a worker whose in-flight extraction exceeds the shutdown deadline
- **WHEN** the deadline passes
- **THEN** the worker logs the in-flight job identifier and exits, and that job is recovered by a later sweep once its lease lapses
