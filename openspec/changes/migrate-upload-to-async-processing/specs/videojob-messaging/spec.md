## MODIFIED Requirements

### Requirement: The Job-Dispatch Topology Is Pinned

`JobDispatchTopology` SHALL return exactly:

| Entity | Name | Type | Arguments |
|---|---|---|---|
| Job exchange | `video.jobs.v2` | `direct`, durable | — |
| Routing key | `video_job.queued.v2` | — | — |
| Job queue | `video.jobs.queued.v2` | durable | `x-max-length` 10 000, `x-overflow` `reject-publish`, `x-dead-letter-exchange` `video.jobs.dlx` |
| Dead-letter exchange | `video.jobs.dlx` | `fanout`, durable | — |
| Dead-letter queue | `video.jobs.dead` | durable | `x-message-ttl` 24 h, `x-max-length` 10 000, `x-overflow` `drop-head`, **no** `x-dead-letter-exchange` |

The routing key SHALL equal the persisted outbox `event_type` string for the same event, so the database and the broker name it identically.

**A generation SHALL therefore be carried by that shared string as well as by the exchange, and the two SHALL be bumped together.** Versioning the exchange alone does not isolate generations, and the reason is upstream of the broker: the outbox relay claims rows by `event_type` (see `videojob-outbox-relay`), every replica's relay reads the same `video_job_outbox` table, and a relay that is already deployed cannot be taught a new predicate. With one event-type string across both generations, a new replica's relay would claim an old replica's row and publish it into the new generation — recreating exactly the overlap the exchange bump was meant to prevent — while an old replica's relay would claim a new replica's row and publish it into the old generation, where nothing consumes it and the job waits forever. Bumping the event type is what makes each relay claim only its own generation's rows, and it works precisely because the old relay's filter is a literal it will never match.

The consequence is that versioning here is not free the way the 6.1 design implied: the string is deliberately one vocabulary for the database and the broker, so a generation bump renames the persisted event as well as the routing key. That cost is accepted rather than engineered around, because the alternatives are a deployment sequence that has to be performed correctly by hand and a class of permanently stranded jobs.

The **queue name alone** SHALL NOT carry the generation, because versioning only the queue isolates nothing: a direct exchange delivers each publish to *every* queue bound with the matching routing key, so a second queue bound to the same exchange with the same key receives the other generation's messages too. The exchange and the routing key are what separate the delivery paths; the queue name follows them for legibility.

The exchange bump is kept alongside the routing-key bump rather than dropped as redundant. The two close different holes — the event type stops a relay from *claiming* the wrong generation's row, and the exchange stops a broker from *delivering* to the wrong generation's queue — and a future change that reuses a routing key across exchanges would find only the second one standing.

**What a generation bump protects against is the rolling-deploy window, not stale messages.** This correction matters because the wrong reason invites the wrong conclusion. Stale messages are already harmless: `videojob-lifecycle`'s claim is a conditional transition out of `queued`, so a message naming a job that is no longer `queued` is refused and dead-lettered with no side effect. What that conditional transition does **not** protect is a job that is *legitimately* `queued` while two different processing models are live. During a deploy in which a replica still processing in-request coexists with a worker, both can act on the same job: the atomic claim decides which one processes it, but the loser's own cleanup then deletes the source object out from under the winner's running extraction. Separate generations make that overlap impossible: the old replica's row is never claimed by the new relay, and a message published by one generation is never delivered to the other's consumer.

A generation SHALL be introduced by any change that alters *which processes act on a dispatched job*, independently of whether the message format changes. A change that alters only the queue's arguments SHALL also introduce a generation, because AMQP rejects a redeclaration with different arguments.

**Retiring a superseded generation SHALL be an explicit deletion, performed by an operator after every replica is on the new build, and SHALL NOT be performed by application code.** The job queue carries no message TTL, so a superseded generation does not drain on its own; and a deletion executed at startup would race a not-yet-redeployed replica still publishing into it. The system SHALL be correct whether or not the deletion has happened — an unretired generation is a bounded, idle queue.

The dead-letter exchange and dead-letter queue SHALL NOT carry a generation suffix. Every generation dead-letters into the same fanout sink deliberately: a dead-lettered message is for inspection, and one place to look is better than one per generation.

#### Scenario: JobDispatchTopology returns the pinned values

- **WHEN** `JobDispatchTopology` is called
- **THEN** its exchange, routing key, job queue, dead-letter exchange, and dead-letter queue names are exactly the values tabulated above, and its bound values are 10 000, 24 h, and 10 000

#### Scenario: The generation is carried by the exchange, the queue, and the routing key alike

- **WHEN** the descriptor's exchange and queue names are compared against the previous generation's
- **THEN** the exchange, the queue, and the routing key all carry the new suffix, and the routing key still equals the outbox `event_type` string

#### Scenario: A relay of one generation does not claim another generation's outbox rows

- **GIVEN** unpublished outbox rows written by both generations of the enqueue path, distinguished only by their `event_type`
- **WHEN** each generation's relay polls
- **THEN** each claims only the rows whose `event_type` matches its own generation, so no row crosses generations

#### Scenario: A message published to the superseded generation cannot reach the new consumer

- **GIVEN** both generations' exchanges and queues declared on one broker, and a consumer bound only to the current generation's queue
- **WHEN** a message is published to the superseded generation's exchange with that generation's routing key
- **THEN** the consumer receives nothing, and the message remains on the superseded generation's queue

#### Scenario: Retirement is not automated

- **WHEN** the application starts against a broker that still holds a superseded generation's exchange and queue
- **THEN** it declares only the current generation and deletes nothing, and it starts and operates normally
