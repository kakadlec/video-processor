## ADDED Requirements

### Requirement: Delivery Runs in Its Own Entrypoint, Requiring Only Its Own Configuration

The Notification context's event consumer SHALL be a third entrypoint, `cmd/notifier`, built from the same source tree and shipped in the same image as `cmd/api` and `cmd/worker`, and started as that image with a different command. It SHALL listen on no port.

It SHALL require exactly the configuration it uses — the Notification context's own PostgreSQL DSN and the broker URL — and SHALL fail fast with an error naming a missing variable rather than starting in a degraded mode. It SHALL NOT require identity configuration, object-storage configuration, Redis, or `ffmpeg`: it authenticates no caller, stores no artifact, holds no lease, and runs no extraction. Requiring any of them would misrepresent what the process does.

A separate process rather than a goroutine inside an existing one is required for the reason `videojob-terminal-events` gives for placing the terminal relay in the worker, applied to the other direction: an outbound request to a third party must not share a lifecycle with serving HTTP requests, nor with the worker's single-extraction-at-a-time shape. The three processes scale on different axes.

Broker reachability SHALL NOT be a startup gate. The consumer SHALL dial with bounded backoff and SHALL redial when the connection or channel is lost.

#### Scenario: The notifier starts with only its own configuration

- **GIVEN** the Notification DSN and the broker URL are set and no other application variable is
- **WHEN** `cmd/notifier` starts
- **THEN** it starts and begins consuming, opening no port

#### Scenario: A missing required variable fails startup

- **GIVEN** the Notification DSN is absent
- **WHEN** `cmd/notifier` starts
- **THEN** startup fails with an error naming the variable, and nothing is consumed

#### Scenario: An unreachable broker does not prevent startup

- **GIVEN** the broker is down
- **WHEN** `cmd/notifier` starts
- **THEN** it starts, retries the dial with backoff, and begins consuming once the broker returns

#### Scenario: Each of the three processes starts without the others

- **GIVEN** the built image
- **WHEN** any one of the API, the worker, and the notifier is started alone with its own configuration
- **THEN** it starts and operates without the other two running

### Requirement: The Consumer Declares the Terminal Topology From the Context's Own Copy of the Names

The consumer SHALL declare the terminal-event topology on every dial, exactly as its publisher does, so neither process's startup depends on the other's order and a recreated broker cannot leave a consume opening on a missing queue.

The names it declares, and the message structures it decodes, SHALL be declared by the Notification context itself. The context SHALL NOT import any package of the Video Processing context to obtain them. `ddd-architecture` forbids that import as a property of the build, not merely of the moment an event is handled, which is the same reason the context already declares its own `UserID` and its own copies of the two event-type strings.

That duplication SHALL be pinned by a test in a composition root that legitimately imports both contexts, asserting the copied topology names and the copied payload field names equal the ones the Video Processing context publishes. An unpinned copy cannot drift detectably: a renamed exchange would leave the consumer bound to a queue nothing publishes to, and a renamed payload field would decode as its zero value.

The dependency rule SHALL be enforced across **every** package of the Notification context, including its infrastructure packages, rather than across its domain and application packages alone. The infrastructure package that holds the copy is precisely where the temptation to import the original lives.

#### Scenario: The consumer declares its topology on every dial

- **GIVEN** a broker whose terminal exchange and queue were deleted while the consumer was disconnected
- **WHEN** the consumer reconnects
- **THEN** it declares them again and resumes consuming

#### Scenario: The copied names equal the published ones

- **WHEN** the Notification context's terminal topology names are compared with the Video Processing context's
- **THEN** the exchange, the queue, and both routing keys are byte-identical

#### Scenario: The copied payload fields equal the published ones

- **WHEN** a payload the Video Processing context writes is decoded by the Notification context's message type
- **THEN** every field it carries is populated, and none decodes as a zero value because of a name mismatch

#### Scenario: No Notification package imports Video Processing

- **WHEN** every package under the Notification context is inspected for its imports
- **THEN** none of them imports any package of the Video Processing or Identity contexts, infrastructure packages included

### Requirement: The Consumer's Disposition Turns on Whether an Attempt Has Been Made

Each delivery SHALL be resolved to exactly one disposition. The table below is the mapping, and it SHALL be read through the rule that produces it rather than as a list to be shortened: what a situation is entitled to depends first on whether **this handler has attempted anything yet**, and second on whether the condition that stopped it clears on its own. A situation that is not literally named here SHALL be placed by that rule, not by the nearest-looking row.

| Situation | Disposition |
|---|---|
| Delivered; budget exhausted and recorded; nothing to deliver — no matching preference, disabled, before the enrolment boundary, or a claim refused because the delivery is **already resolved**; or an outcome that could not be recorded after the attempts were made | Acknowledge |
| A body that cannot be decoded, or an event type this consumer does not recognize | Dead-letter, without requeue |
| A failure of the consumer's own dependency **before any attempt is made** — the Notification database unreachable while reading preferences or while claiming — or a claim refused because another claim is **held and not yet reclaimable** | Requeue, after a pause |

The third disposition is a deliberate difference from `videojob-worker`, which has only the first two, and the difference SHALL NOT be read as an inconsistency. There, a requeued job meets a row that has already moved past `queued` and can only lose the claim again, so redelivery loops rather than recovers. Here this handler has attempted nothing, and the condition that blocked it — a database that is down, or a claim another consumer still holds — is one that resolves itself, so dead-lettering would discard a user's notification because of a blip. The pause before the next delivery is what prevents that disposition from becoming a hot loop against a database that is still down.

**The requeue disposition SHALL be reachable only before an attempt has been made by this handler**, and this boundary is load-bearing rather than incidental. Nothing this handler has done can be lost by a redelivery, so redelivery genuinely retries. Once *this* handler holds the claim, a redelivery meeting that row would be refused and requeued indefinitely rather than resolving anything — so requeueing after a failed recording would discard the outcome permanently, and could send the webhook a second time first. Every pre-attempt failure SHALL take this disposition, including a failure of the read that resolves preferences, not only a failure of the claim itself: at that point no request has been made and no claim is held, so nothing distinguishes the two.

A claim refused as *held by another* is a pre-attempt outcome and SHALL requeue for the same reason, which is not a contradiction of the rule above but an application of it: this handler has attempted nothing. Acknowledging it instead would be the silent failure this table exists to prevent — a consumer that crashes after claiming is normally redelivered within seconds, long before the reclaim bound, so an acknowledgement there would strand a `pending` row nothing else will ever meet and drop the notification without a trace. The loop terminates: the holder either resolves the delivery, after which the refusal is reported as *already resolved* and acknowledged, or it stops without resolving, after which the reclaim bound expires and the claim is granted.

The cost of that choice SHALL be stated rather than discovered: with a prefetch of one, a message requeued this way returns to the head of the queue and is taken again by the same consumer, so an abandoned claim blocks every other delivery on that queue until the reclaim bound expires. That is head-of-line blocking bounded by the reclaim bound, which is why the bound is sized as a small multiple of one claimant's maximum budget rather than as a comfortable round number. It is accepted over the alternatives: acknowledging loses a notification silently, and renewing a claim with a heartbeat — the mechanism `videojob-worker` uses — is substantially more machinery than a static bound plus a startup check, for a stall measured in minutes.

An outcome that cannot be recorded SHALL therefore be retried a bounded number of times against a context detached from shutdown, exactly as `videojob-worker` retries a terminal write it has already earned. If it still cannot be recorded, the message SHALL be acknowledged and the failure SHALL be logged with the delivery identifier and the outcome that was lost. This leaves a row in `pending` that nothing will ever resolve, and that is an accepted accounting loss rather than a recoverable state: acknowledging is chosen because the webhook was actually sent, requeueing would lose the outcome as described above, and dead-lettering would report a failure for a delivery that succeeded. A `pending` row older than the reclaim bound with no consumer in flight is the operational signal for this case, and `docs/operations.md` SHALL name it as such.

A message that cannot be decoded SHALL NOT be requeued: redelivering it produces the same failure forever.

#### Scenario: An undecodable body is dead-lettered

- **GIVEN** a delivery whose body is not valid for either terminal message type
- **WHEN** the consumer handles it
- **THEN** it is dead-lettered without requeue, and no delivery is attempted

#### Scenario: An unrecognized event type is dead-lettered

- **GIVEN** a delivery whose event type is neither of the two the context recognizes
- **WHEN** the consumer handles it
- **THEN** it is dead-lettered without requeue

#### Scenario: A database outage before any attempt requeues rather than discards

- **GIVEN** the Notification database is unreachable when the consumer tries to claim
- **WHEN** the consumer handles a terminal event
- **THEN** the message is requeued, no delivery is attempted, and the consumer pauses before taking the next message

#### Scenario: A failure reading preferences requeues too

- **GIVEN** the Notification database is unreachable when the consumer reads the owner's deliverable preferences, before any claim is attempted
- **WHEN** the consumer handles a terminal event
- **THEN** the message is requeued exactly as a claim-time failure is, and no delivery is attempted

#### Scenario: A claim held by another consumer is requeued, not acknowledged

- **GIVEN** a delivery whose claim is held by another consumer and is not yet past the reclaim bound
- **WHEN** this consumer handles a redelivery of the same event
- **THEN** the message is requeued after a pause, no request is made, and the message is not acknowledged

#### Scenario: A claim refused because the delivery is resolved is acknowledged

- **GIVEN** a delivery already resolved by an earlier handling of the same event
- **WHEN** this consumer handles a redelivery of it
- **THEN** the claim is refused as already resolved, no request is made, and the message is acknowledged

#### Scenario: An outcome that cannot be recorded is acknowledged, not requeued

- **GIVEN** a delivery whose attempts have completed and whose resolution fails on every bounded retry
- **WHEN** the consumer finishes handling it
- **THEN** the message is acknowledged, the loss is logged with the delivery identifier and the outcome, and the message is neither requeued nor dead-lettered

#### Scenario: A resolution refused by the fence is acknowledged without retry

- **GIVEN** a delivery whose claim was superseded by a reclaim while its attempts were running
- **WHEN** it resolves and the write is refused because its claim token is no longer current
- **THEN** the refusal is logged, no retry is made, and the message is acknowledged — the successor owns the outcome

#### Scenario: Nothing to deliver is an acknowledgement, not a failure

- **GIVEN** a terminal event whose owner has no matching enabled preference
- **WHEN** the consumer handles it
- **THEN** the message is acknowledged and neither dead-lettered nor requeued

### Requirement: Shutdown Joins the In-Flight Delivery Before Closing What It Borrows

On a termination signal the consumer SHALL stop taking new deliveries, SHALL wait — under a bounded drain — for the delivery in hand to reach a disposition, and SHALL close the database pool that delivery borrows only after that wait has succeeded. The handler SHALL run on a context that shutdown does not cancel, so a signal does not abort an outbound request mid-flight or prevent an outcome from being recorded.

This is the ordering `cmd/api` and `cmd/worker` already hold, for the same reason: closing a handle underneath an operation that borrows it turns a resolvable state into an aborted one. A delivery interrupted after its claim but before its outcome is recorded is exactly the case the reclaim period exists to repair, and shutdown SHALL NOT be a routine way of producing it.

The two rules meet when the drain expires, and the resolution SHALL be stated rather than left to the implementation: at that point the handler is still running on its detached context, so it can never be joined, and "close only after the join" and "exit at the bound" cannot both be honoured. **The bound SHALL win.** The process SHALL exit without an orderly close of the pool, and this SHALL be a named exception to the ordering above rather than a violation of it. Nothing is lost by it that the reclaim bound does not already cover: an unresolved claim left by an aborted process is precisely the state a later consumer reclaims, and a pool closed by process exit releases the same connections the server would reclaim on the closed socket. Skipping the close is chosen over extending the wait because an unbounded wait turns one hung destination into a process that never terminates.

#### Scenario: A delivery in flight at shutdown is finished

- **GIVEN** the consumer is mid-delivery when a termination signal arrives
- **WHEN** it shuts down
- **THEN** that delivery reaches a disposition and its outcome is recorded before the database pool is closed

#### Scenario: No new deliveries are taken after the signal

- **GIVEN** a termination signal has been received
- **WHEN** further messages are available on the queue
- **THEN** none is taken, and they remain for another consumer or a later start

#### Scenario: The drain is bounded

- **GIVEN** a delivery that does not finish within the drain bound
- **WHEN** the bound elapses
- **THEN** the process exits rather than hanging, the orderly pool close is skipped rather than waited on, and the unresolved claim is reclaimable by a later consumer
