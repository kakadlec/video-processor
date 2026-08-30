## MODIFIED Requirements

### Requirement: The Job-Dispatch Topology Is Pinned

`JobDispatchTopology` SHALL return exactly:

| Entity | Name | Type | Arguments |
|---|---|---|---|
| Job exchange | `video.jobs.v2` | `direct`, durable | — |
| Routing key | `video_job.queued` | — | — |
| Job queue | `video.jobs.queued.v2` | durable | `x-max-length` 10 000, `x-overflow` `reject-publish`, `x-dead-letter-exchange` `video.jobs.dlx` |
| Dead-letter exchange | `video.jobs.dlx` | `fanout`, durable | — |
| Dead-letter queue | `video.jobs.dead` | durable | `x-message-ttl` 24 h, `x-max-length` 10 000, `x-overflow` `drop-head`, **no** `x-dead-letter-exchange` |

The routing key SHALL equal the persisted outbox `event_type` string for the same event, so the database and the broker name it identically.

The **exchange**, not the routing key, SHALL carry the generation suffix, and the queue name SHALL follow it. Versioning only the queue does not isolate anything: a direct exchange delivers each publish to *every* queue bound with the matching routing key, so a second queue bound to the same exchange with the same key receives the other generation's messages too. Versioning the exchange gives the two generations genuinely separate delivery paths while leaving the routing key equal to the event type, which versioning the key would have broken.

**What a generation bump protects against is the rolling-deploy window, not stale messages.** This correction matters because the wrong reason invites the wrong conclusion. Stale messages are already harmless: `videojob-lifecycle`'s claim is a conditional transition out of `queued`, so a message naming a job that is no longer `queued` is refused and dead-lettered with no side effect. What that conditional transition does **not** protect is a job that is *legitimately* `queued` while two different processing models are live. During a deploy in which a replica still processing in-request coexists with a worker, both can act on the same job: the atomic claim decides which one processes it, but the loser's own cleanup then deletes the source object out from under the winner's running extraction. Separate exchanges make that overlap impossible, because a message published by one generation is never delivered to the other's consumer.

A generation SHALL therefore be introduced by any change that alters *which processes act on a dispatched job*, independently of whether the message format changes. A change that alters only the queue's arguments SHALL also introduce a generation, because AMQP rejects a redeclaration with different arguments.

**Retiring a superseded generation SHALL be an explicit deletion, performed by an operator after every replica is on the new build, and SHALL NOT be performed by application code.** The job queue carries no message TTL, so a superseded generation does not drain on its own; and a deletion executed at startup would race a not-yet-redeployed replica still publishing into it. The system SHALL be correct whether or not the deletion has happened — an unretired generation is a bounded, idle queue.

The dead-letter exchange and dead-letter queue SHALL NOT carry a generation suffix. Every generation dead-letters into the same fanout sink deliberately: a dead-lettered message is for inspection, and one place to look is better than one per generation.

#### Scenario: JobDispatchTopology returns the pinned values

- **WHEN** `JobDispatchTopology` is called
- **THEN** its exchange, routing key, job queue, dead-letter exchange, and dead-letter queue names are exactly the values tabulated above, and its bound values are 10 000, 24 h, and 10 000

#### Scenario: The generation lives on the exchange, and the routing key still equals the event type

- **WHEN** the descriptor's exchange and queue names are compared against the previous generation's
- **THEN** both carry the new suffix, while the routing key is unchanged and still equals the outbox `event_type` string

#### Scenario: A message published to the superseded generation cannot reach the new consumer

- **GIVEN** both generations' exchanges and queues declared on one broker, and a consumer bound only to the current generation's queue
- **WHEN** a message is published to the superseded generation's exchange with the shared routing key
- **THEN** the consumer receives nothing, and the message remains on the superseded generation's queue

#### Scenario: Retirement is not automated

- **WHEN** the application starts against a broker that still holds a superseded generation's exchange and queue
- **THEN** it declares only the current generation and deletes nothing, and it starts and operates normally
