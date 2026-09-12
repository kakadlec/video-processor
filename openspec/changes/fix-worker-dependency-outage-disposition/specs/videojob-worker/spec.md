## MODIFIED Requirements

### Requirement: A Message the Worker Cannot Act On Is Dead-Lettered, Never Requeued and Never Acked Away

When the worker cannot act on a message, it SHALL reject the message **without requeue**, so the topology's dead-letter route takes it. This SHALL apply to a message whose payload cannot be parsed, one naming no source object, one naming a job that does not exist, one whose job the worker could not claim, and one whose job was **taken away from the worker while it ran** — a terminal write refused by the fence (see `videojob-lease-recovery`).

**"Cannot act on" means the message names work this worker will never be able to perform, not work it could not perform on this attempt.** That distinction is now load-bearing rather than implicit: exactly one condition — a claim the persistence layer could not decide — is licensed to be retried instead, and the requirement below states it. Every condition enumerated here, and **every condition not enumerated in either requirement**, SHALL be dead-lettered. The default SHALL remain rejection, so a failure mode neither requirement anticipated behaves as it does today rather than entering a retry loop nobody reasoned about.

It SHALL NOT requeue a message that falls under this requirement: none of these conditions is transient, so requeueing produces an unbounded redelivery loop against a message that will never succeed. A fenced result means either a newer epoch was re-dispatched and may have a live successor, or another actor committed a different terminal outcome at the same epoch. Requeueing is wrong in both cases: it would add a competing delivery in the first and can never claim the already-terminal row in the second.

This SHALL also cover every failure that occurs **after** the claim has been won, including a terminal write whose own transaction could not reach the database. Such a message SHALL be dead-lettered rather than requeued even though its failure is transient, because by then the row is `processing` and the claim predicate admits `queued` alone: a redelivery could only lose the claim. Recovery for that row belongs to `videojob-lease-recovery`'s sweeper, which reaches it because it is `processing`, and the disposition SHALL NOT be changed on the argument that the underlying failure was temporary.

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

## ADDED Requirements

### Requirement: A Dispatch Whose Claim the Worker Could Not Decide Is Requeued, Paced

When the worker's claim step **could not reach the database**, the worker SHALL requeue the message rather than dead-lettering it, and SHALL pause before taking its next delivery.

The condition SHALL be identified positively and narrowly: the claim was attempted, the persistence layer could not answer, and therefore **no actor owns this job and this worker has changed nothing**. It SHALL be distinguished from a claim that was decided against this worker — which is a lost claim and dead-letters — and from a row that was read and could not be interpreted, which is permanent and dead-letters. The worker SHALL NOT identify it by inspecting a database driver's error values; the distinction SHALL be carried as a sentinel raised by the layer that made the call, per `videojob-persistence` and `videojob-execution`.

**What licenses the requeue is a statement about state, not about the error.** On this path the row is still `queued`, no lease exists, no source object has been read, no extraction has run and no event has been written — the system is in the state it was in before the delivery arrived. A redelivery is therefore not a retry of work but the same dispatch arriving later, decided by the same conditional claim. This is the whole of the justification, and it SHALL NOT be extended to any condition for which those statements are not all true.

The requeue SHALL NOT be bounded by a delivery count. The message is the only record that a `queued` job still needs dispatching, and `videojob-lease-recovery`'s sweeper does not reach a `queued` row; discarding the message after N attempts reproduces the defect this requirement exists to remove. A bound SHALL NOT be obtained by changing the work queue's type, by republishing a counter-carrying copy — which would make the worker a second producer on a stream `videojob-outbox-relay` owns — or by counting in worker-local memory, which counts to one per replica.

**The requeue SHALL be paced, and the pause SHALL be taken after the negative acknowledgement and before the next delivery is accepted.** A requeued message returns toward the front of the queue and is offered again at once, so an unpaced requeue spins at the speed of a failed connection attempt. The pause SHALL be supplied by the composition root rather than read from inside the consumer, so that a requeue's observable behaviour can be exercised at a value a test can wait for.

**The pause SHALL be a fixed constant rather than configuration**, on the same reasoning that makes the lease TTL, the sweep interval and the requeue bound constants: it is a correctness-adjacent margin whose right value depends on the number of running replicas, not a deployment preference. Exposing it as an environment variable would invite a deployment to lower it without the arithmetic below, or to raise it past the point where a dispatch resumes promptly once the dependency returns. Being a constructor parameter is not the same as being configuration — the parameter exists so a test can supply a short value, and the composition root SHALL pass the constant.

The pause SHALL be taken on the consumer's own cancellable context, not on the detached context the handler runs under. Shutdown therefore skips it, and it cannot extend the drain the worker waits on for work in flight — by the time it runs, the handler has returned and nothing is in flight.

**The pause bounds each consumer's rate, not the message's, and the difference SHALL be stated wherever the value is chosen.** A consumer sitting in its pause holds no delivery, so every other replica may take the requeued message immediately; across N replicas the effective redelivery interval is approximately the pause divided by N. A reader choosing or reviewing that value needs the deployment-wide number, not the per-consumer one.

**The accepted cost is head-of-line blocking, unbounded in duration.** At a prefetch of one, a requeued message occupies each replica in turn for as long as the condition lasts. That is accepted here — where the analogous consumer in `notification-event-consumer` accepts it only under a bound — for a reason specific to this condition: it is entered only when the dependency **every** message on this queue requires is unavailable, so there is no work any replica could be doing instead and the queue's head and tail are blocked equally. That premise SHALL be preserved by keeping the condition narrow; a broader rule admitting a per-message permanent failure would let one message block every replica against healthy work.

#### Scenario: A dispatch whose claim could not reach the database is requeued

- **GIVEN** a job in `queued` status and a worker whose database is unreachable
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

#### Scenario: An ambiguous claim commit is recovered by the sweeper

- **GIVEN** a claim that committed in the database but whose result the worker could not read, so the worker requeued the dispatch
- **WHEN** the redelivery arrives and finds the row `processing`
- **THEN** it is refused by the claim and dead-lettered, and the row — `processing` with no lease — is recovered by the sweeper on its ordinary two-observation path
