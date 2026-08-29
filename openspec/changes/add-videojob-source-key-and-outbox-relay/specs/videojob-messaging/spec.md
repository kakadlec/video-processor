## MODIFIED Requirements

### Requirement: Job Messages Are Published Persistently And Without Expiry

Any publisher of a job message to this topology SHALL mark it persistent (AMQP delivery mode 2) and SHALL leave the per-message `expiration` property unset.

The two obligations are separate and both are load-bearing. RabbitMQ honours a message's own `expiration` independently of the queue's arguments, so a publisher that set one would expire the message into the dead-letter queue even though the job queue deliberately carries no `x-message-ttl` — reproducing exactly the failure that omission exists to prevent: a message dead-lettered with no update to its `video_jobs` row, against a state machine with no transition out of `queued` except to `processing`, leaving the job reporting `queued` to its owner forever.

The outbox relay is the publisher that satisfies this requirement, and it is the only one. A queue durable, a message persistent, and the broker's storage persisted are three conditions and the guarantee needs all three; the relay's own capability (`videojob-outbox-relay`) is where the first two are verified against a real broker, including a restart.

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
