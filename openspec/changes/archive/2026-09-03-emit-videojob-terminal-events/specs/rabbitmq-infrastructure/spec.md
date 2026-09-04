## MODIFIED Requirements

### Requirement: Topology Declaration Is Idempotent And Takes A Descriptor

`internal/platform/rabbitmq` SHALL expose a `Topology` descriptor and a `DeclareTopology(conn, topo)` function that declares the exchange, the work queue, the dead-letter exchange, and the dead-letter queue the descriptor names, together with the bindings between them, and that succeeds when called repeatedly with the same descriptor against a broker where that topology already exists.

**The descriptor SHALL carry a set of work-queue routing keys rather than exactly one, and `DeclareTopology` SHALL bind the work queue under every key in that set.** A topology needing one binding SHALL express that as a one-element set and SHALL be declared identically to before. The reason the set exists is that a single queue may legitimately be the destination of more than one event type — `videojob-terminal-events` binds one queue to both `video_job.completed.v1` and `video_job.failed.v1`, because the two outcomes are one stream to whatever consumes them and must be delivered in the order they occurred.

**An empty set SHALL be rejected with an error, and nothing SHALL be declared.** The work exchange is direct, so a queue bound under no key receives nothing while the topology still reports success; every mandatory publish into that exchange would then be returned unroutable and its outbox row re-attempted indefinitely. Refusing the descriptor makes that a startup error rather than a silent, self-perpetuating publish loop — the same reason the dead-letter sink is declared first.

The descriptor carries names and bound values only, and this package SHALL NOT define a default value for it. A caller supplies the topology it owns. That is what keeps this package context-free while one implementation serves the Video Processing context today and the Notification context later. **A set of caller-supplied routing keys carries no bounded-context name, so widening the field does not widen this package's knowledge of any context.**

Declaration order SHALL be: dead-letter exchange, dead-letter queue, and their binding first; then the work exchange, the work queue, and its bindings. RabbitMQ does not validate at declare time that a queue's `x-dead-letter-exchange` names an existing exchange, and silently drops dead-lettered messages when it does not — so declaring the sink first makes a partial failure leave a topology that is visibly incomplete rather than complete-looking and lossy.

`DeclareTopology` SHALL open its own channel and close it before returning, so a failed declaration cannot leave a caller's long-lived publishing or consuming channel closed. AMQP provides no way to reopen a closed channel.

Every exchange and queue SHALL be declared `durable: true`, `autoDelete: false`, `exclusive: false`, and `noWait: false`. Those four flags are independent, and "durable" constrains none of the other three: an exclusive queue is visible only to the connection that declared it and vanishes when that connection closes, an auto-delete queue vanishes when its last consumer goes away, and `noWait: true` returns before the broker has answered, so a rejected declaration would surface later as a channel-level exception rather than as this function's error. Any of the three defeats what durability is here for — a queue that outlives the process that declared it, holding work for a consumer that has not started yet.

#### Scenario: Declaring twice succeeds

- **GIVEN** a live connection to a broker with none of a given descriptor's topology declared
- **WHEN** `DeclareTopology` is called twice with that same descriptor
- **THEN** both calls return no error, and the exchange, work queue, dead-letter exchange, and dead-letter queue all exist

#### Scenario: A conflicting redeclaration is rejected by the broker

- **GIVEN** a broker where a descriptor's work queue already exists as `DeclareTopology` declared it
- **WHEN** `DeclareTopology` is called with a descriptor carrying the same names but a different bound value
- **THEN** the broker rejects it with a precondition failure, confirming the arguments this function declares are the ones it pins

#### Scenario: Declaration fails against a closed connection

- **GIVEN** a connection that has been closed
- **WHEN** `DeclareTopology` is called with it
- **THEN** it returns a non-nil error and declares nothing

#### Scenario: A work queue is bound under every routing key in the descriptor

- **GIVEN** a descriptor naming two work-queue routing keys
- **WHEN** `DeclareTopology` is called with it
- **THEN** a message published to the work exchange under either key is routed to the work queue

#### Scenario: A descriptor with no routing key is refused

- **GIVEN** a descriptor whose set of work-queue routing keys is empty
- **WHEN** `DeclareTopology` is called with it
- **THEN** it returns a non-nil error and declares no exchange or queue
