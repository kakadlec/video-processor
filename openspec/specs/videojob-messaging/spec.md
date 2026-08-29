# videojob-messaging Specification

## Purpose

Define `internal/video/infrastructure/messaging`, the Video Processing context's job-dispatch topology on the shared AMQP broker: the pinned exchange, routing key, queue, and dead-letter sink; the generation scheme a later cutover extends; and the obligation on any publisher to publish persistently.

The names live in the context rather than in `internal/platform/rabbitmq` because `ddd-architecture` confines that package to connection/lifecycle plumbing, never a specific context's use case. The generic descriptor and the function that declares it are defined by `rabbitmq-infrastructure`; only the values are here. Nothing publishes to or consumes from this topology yet — that is `add-videojob-source-key-and-outbox-relay`'s and `migrate-upload-to-async-processing`'s.

## Requirements

### Requirement: The Video Processing Context Owns Its Job-Dispatch Topology

`internal/video/infrastructure/messaging` SHALL define the Video Processing context's job-dispatch topology and expose it as a `JobDispatchTopology` function returning the `internal/platform/rabbitmq.Topology` descriptor that context declares.

The definition lives in the context rather than in `internal/platform/rabbitmq` because `ddd-architecture` confines that package to "connection/lifecycle plumbing — never domain or application logic for a specific context's use case", and an exchange named `video.jobs` carrying a routing key named after a `VideoJob` event is precisely a specific context's use case. The generic descriptor and the function that declares it are plumbing and stay in `internal/platform`; the names are Video Processing's and live here. Phase 7's Notification context will define its own topology in its own `infrastructure` package the same way, rather than extending this one.

#### Scenario: The names live in the video context

- **GIVEN** the strings `video.jobs`, `video_job.queued`, and the job queue's name
- **WHEN** the repository's non-test Go source is searched for them
- **THEN** they appear under `internal/video/`, and not under `internal/platform/`

The scope is non-test Go source deliberately. Documentation and OpenSpec artifacts name the topology throughout — that is what they are for — and the guard in `internal/platform/rabbitmq` skips `_test.go` files, since the test that enforces this rule necessarily contains the very literals it forbids.

### Requirement: The Job-Dispatch Topology Is Pinned

`JobDispatchTopology` SHALL return exactly:

| Entity | Name | Type | Arguments |
|---|---|---|---|
| Job exchange | `video.jobs.v1` | `direct`, durable | — |
| Routing key | `video_job.queued` | — | — |
| Job queue | `video.jobs.queued.v1` | durable | `x-max-length` 10 000, `x-overflow` `reject-publish`, `x-dead-letter-exchange` `video.jobs.dlx` |
| Dead-letter exchange | `video.jobs.dlx` | `fanout`, durable | — |
| Dead-letter queue | `video.jobs.dead` | durable | `x-message-ttl` 24 h, `x-max-length` 10 000, `x-overflow` `drop-head`, **no** `x-dead-letter-exchange` |

The routing key SHALL equal the persisted outbox `event_type` string for the same event, so the database and the broker name it identically.

The **exchange**, not the routing key, SHALL carry the generation suffix, and the queue name SHALL follow it. A later change cuts over from in-request processing to a worker and must not let a not-yet-redeployed replica's messages reach the new consumer. Versioning only the queue does not achieve that: a direct exchange delivers each publish to *every* queue bound with the matching routing key, so a second queue bound to the same exchange with the same key receives the old replicas' messages too. Versioning the exchange gives the old and new publishers genuinely separate paths while leaving the routing key equal to the event type, which versioning the key would have broken.

The dead-letter exchange and dead-letter queue SHALL NOT carry a generation suffix. Both generations dead-letter into the same fanout sink deliberately: a dead-lettered message is for inspection, and one place to look is better than one per generation.

#### Scenario: JobDispatchTopology returns the pinned values

- **WHEN** `JobDispatchTopology` is called
- **THEN** its exchange, routing key, job queue, dead-letter exchange, and dead-letter queue names are exactly the values tabulated above, and its bound values are 10 000, 24 h, and 10 000

#### Scenario: A later generation changes the exchange, not the routing key

- **GIVEN** a future change introducing a second generation of this topology
- **WHEN** it derives its descriptor from this one
- **THEN** it changes the exchange and queue names and leaves the routing key equal to the outbox `event_type`, so publishers of different generations do not share a delivery path

### Requirement: Job Messages Are Published Persistently And Without Expiry

Any publisher of a job message to this topology SHALL mark it persistent (AMQP delivery mode 2) and SHALL leave the per-message `expiration` property unset.

The two obligations are separate and both are load-bearing. RabbitMQ honours a message's own `expiration` independently of the queue's arguments, so a publisher that set one would expire the message into the dead-letter queue even though the job queue deliberately carries no `x-message-ttl` — reproducing exactly the failure that omission exists to prevent: a message dead-lettered with no update to its `video_jobs` row, against a state machine with no transition out of `queued` except to `processing`, leaving the job reporting `queued` to its owner forever.

A queue declared durable survives a broker restart; the messages in it do not unless each was published persistently, and a transient message in a durable queue is discarded on restart with no error to anyone. Durable queue, persistent message, and persisted broker storage are three conditions, and the guarantee needs all three — which matters here because the relay that publishes these messages stamps its outbox row as published once the broker acknowledges, after which the message is the only remaining record that the job is waiting.

No publisher exists yet, so the scenarios below are not currently verified by any test; they describe broker behavior reachable only once something publishes. The change that introduces the outbox relay owns demonstrating them end to end — publish, confirm, restart the broker, observe the message still queued — and this requirement is what obliges it to. The obligation is recorded against the topology rather than against the publisher deliberately: a relay written without it would look correct against every test that can be written before it exists.

#### Scenario: A transient message does not survive a broker restart

- **GIVEN** a durable job queue holding a message published with the default (transient) delivery mode
- **WHEN** the broker is restarted
- **THEN** the message is gone, demonstrating that queue durability alone does not carry it

#### Scenario: A persistent message survives a broker restart

- **GIVEN** the same durable job queue holding a message published with delivery mode 2
- **WHEN** the broker is restarted
- **THEN** the message is still queued

#### Scenario: A published job message carries no expiration

- **GIVEN** a job message published to this topology
- **WHEN** its properties are inspected
- **THEN** its `expiration` property is unset, so the queue's absent `x-message-ttl` is the only expiry policy in effect and there is none
