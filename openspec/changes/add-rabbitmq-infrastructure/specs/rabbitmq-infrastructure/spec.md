## ADDED Requirements

### Requirement: The RabbitMQ Adapter Lives In internal/platform

The AMQP connection adapter SHALL live at `internal/platform/rabbitmq`, alongside `internal/platform/redis` and `internal/platform/ratelimit`, and SHALL NOT be placed under any bounded context's own `infrastructure` package.

This is the opposite placement from MinIO, deliberately. `internal/video/infrastructure/storage` sits inside the Video Processing context because every one of its consumers belongs to that context. The broker's consumers do not: the Video Processing context publishes job messages and the Notification context (Phase 7) subscribes to integration events on the same connection, which is exactly the cross-cutting infrastructure `ddd-architecture`'s "Monorepo Package Topology" requirement reserves `internal/platform/` for. This is infrastructure sharing only and SHALL NOT be read as permitting shared `domain` or `application` code between contexts.

#### Scenario: The package imports no bounded context

- **GIVEN** any Go file under `internal/platform/rabbitmq/`
- **WHEN** its imports are inspected
- **THEN** it imports no package under `internal/identity/` or `internal/video/`

### Requirement: AMQP Connection Is Configured From The Environment

`internal/platform/rabbitmq.LoadConfigFromEnv` SHALL read the broker's connection URL from the `RABBITMQ_URL` environment variable and SHALL fail with a clear, distinguishable error when it is unset or empty, rather than defaulting to a hardcoded address.

The variable holds a full AMQP URI rather than a `host:port` pair, unlike `REDIS_ADDR`. The URI is the protocol's own addressing form: it carries the scheme that selects TLS (`amqp` vs `amqps`), the credentials, and the virtual host, none of which a bare address can express. Splitting those into separate variables would reassemble a URI at startup from parts the operator already had in one.

#### Scenario: Missing RABBITMQ_URL returns a clear error

- **GIVEN** the `RABBITMQ_URL` environment variable is unset or empty
- **WHEN** `LoadConfigFromEnv` is called
- **THEN** it returns `ErrURLRequired` and no `Config`

#### Scenario: RABBITMQ_URL present is loaded into Config

- **GIVEN** the `RABBITMQ_URL` environment variable is set to an AMQP URI
- **WHEN** `LoadConfigFromEnv` is called
- **THEN** it returns a `Config` whose `URL` field equals that value, and no error

### Requirement: Open Establishes The Connection And Reports Unreachability

`internal/platform/rabbitmq.Open` SHALL dial the broker and complete the AMQP handshake, returning either a live connection or an error. It SHALL NOT return a connection that has not been established.

This diverges from `internal/platform/redis.Open` and `internal/video/infrastructure/storage.Open`, both of which construct a client without touching the network and let unreachability surface on first use. The divergence is forced by the protocol rather than chosen: AMQP has no lazy client — a connection is only usable after a handshake — so there is no object to hand back that would be meaningful before connecting. An implementer SHALL NOT wrap this in a deferred-connect shim to make it match the other two adapters.

#### Scenario: Open succeeds against a reachable broker

- **GIVEN** a `Config` whose `URL` addresses a running broker with valid credentials
- **WHEN** `Open` is called with it
- **THEN** it returns a non-nil connection and no error

#### Scenario: Open fails against an unreachable broker

- **GIVEN** a `Config` whose `URL` addresses no running broker
- **WHEN** `Open` is called with it
- **THEN** it returns a nil connection and a non-nil error, and the error names neither the URI's credentials nor its password component

### Requirement: Health Check Confirms Connectivity With A Real Round Trip

`internal/platform/rabbitmq` SHALL expose a `Ping` health check that performs a real round trip to the broker and reports failure distinguishably from success. It SHALL NOT report health from locally held state alone.

The underlying client exposes a cheap `IsClosed()` predicate, and that is not sufficient: it reports whether this process has observed a close, which is stale for exactly the failure a health check exists to catch — a broker that has become unreachable without the connection having been torn down yet. `Ping` therefore opens a channel and closes it, which is a synchronous protocol exchange the broker must answer.

#### Scenario: Ping succeeds against a live connection

- **GIVEN** a connection returned by `Open` against a running broker
- **WHEN** `Ping` is called
- **THEN** it returns no error

#### Scenario: Ping fails against a closed connection

- **GIVEN** a connection that has been closed
- **WHEN** `Ping` is called
- **THEN** it returns a non-nil error

### Requirement: Close Releases The Connection

`internal/platform/rabbitmq` SHALL expose a `Close` function that releases the connection and its underlying socket.

#### Scenario: Close succeeds on an open connection

- **GIVEN** a connection returned by `Open`
- **WHEN** `Close` is called on it
- **THEN** it returns no error, and a subsequent `Ping` on that connection fails rather than appearing healthy

### Requirement: The Declared Topology Is Pinned By This Package

`internal/platform/rabbitmq` SHALL export a `Topology` descriptor and a `DefaultTopology` function returning the job-dispatch topology this change introduces, so that every party to **that** topology reads one definition rather than string literals repeated at each call site. AMQP rejects a redeclaration whose arguments differ from the existing queue's, so a drifted literal is a startup failure at a distance; the descriptor exists so that failure cannot be reached by copying.

What that covers, precisely, because two later changes deliberately do not use `DefaultTopology` unchanged. The **exchange and routing key** are shared: every job-dispatch publisher and consumer uses them. The **job queue name is not permanently shared** — the cutover consumes a successor queue, which it obtains as a `Topology` value derived from this one with a new job-queue name, not as a fresh set of literals, and it must not consume `.v1`, whose contents by then are the residue the relay accumulated with nothing draining it. Phase 7's **Notification events do not belong to this topology at all**: they get their own exchange with their own fanout semantics, and putting them on the job exchange would route integration events to a queue that expects job dispatches. The descriptor is reused for the job-dispatch topology and its successor; it is not a registry of every exchange this system will ever declare.

`DefaultTopology` SHALL return exactly:

| Entity | Name | Type | Arguments |
|---|---|---|---|
| Job exchange | `video.jobs` | `direct`, durable | — |
| Routing key | `video_job.queued` | — | — |
| Job queue | `video.jobs.queued.v1` | durable | `x-message-ttl` 1 h, `x-max-length` 10 000, `x-overflow` `reject-publish-dlx`, `x-dead-letter-exchange` `video.jobs.dlx` |
| Dead-letter exchange | `video.jobs.dlx` | `fanout`, durable | — |
| Dead-letter queue | `video.jobs.dead` | durable | `x-message-ttl` 24 h, `x-max-length` 10 000, `x-overflow` `drop-head`, **no** `x-dead-letter-exchange` |

The routing key SHALL equal the persisted outbox `event_type` string for the same event, so the database and the broker name it identically. The job queue's name SHALL carry a version suffix: the change that cuts over to a worker consumes from a separately named queue rather than purging this one, and a suffix makes that successor a convention rather than an ad-hoc rename.

The two overflow policies and the dead-letter queue's absence of a dead-letter exchange SHALL NOT be fields of `Topology`. They are the invariants that make the topology bounded, and a caller able to vary them could declare an unbounded chain through the same API.

#### Scenario: DefaultTopology returns the pinned names

- **WHEN** `DefaultTopology` is called
- **THEN** its exchange, routing key, job queue, dead-letter exchange, and dead-letter queue names are exactly the values tabulated above, and its four TTL/length values are 1 h, 10 000, 24 h, and 10 000

### Requirement: Topology Declaration Is Idempotent And Takes A Descriptor

`internal/platform/rabbitmq.DeclareTopology` SHALL take a connection and a `Topology`, declare the exchange, the job queue, the dead-letter exchange, and the dead-letter queue with the bindings between them, and succeed when called repeatedly with the same descriptor against a broker where that topology already exists.

The descriptor is a parameter rather than a package-level constant read internally, and that is what makes this function testable: a `DeclareTopology(conn)` fixed to the production names could only be exercised by declaring those exact names on a shared broker, which would leave test-sized arguments behind under production names for a later run to collide with. Passing a descriptor lets the tests drive the same exported code path under names scoped to each test.

Declaration order SHALL be: dead-letter exchange, dead-letter queue, and their binding first; then the job exchange, the job queue, and its binding. RabbitMQ does not validate at declare time that a queue's `x-dead-letter-exchange` names an existing exchange, and silently drops dead-lettered messages when it does not — so declaring the sink first makes a partial failure leave a topology that is visibly incomplete rather than complete-looking and lossy.

`DeclareTopology` SHALL open its own channel and close it before returning, so a failed declaration cannot leave a caller's long-lived publishing or consuming channel closed. AMQP provides no way to reopen a closed channel.

#### Scenario: Declaring twice succeeds

- **GIVEN** a live connection to a broker with none of a given descriptor's topology declared
- **WHEN** `DeclareTopology` is called twice with that same descriptor
- **THEN** both calls return no error, and the exchange, job queue, dead-letter exchange, and dead-letter queue all exist

#### Scenario: A conflicting redeclaration is rejected by the broker

- **GIVEN** a broker where a descriptor's job queue already exists as `DeclareTopology` declared it
- **WHEN** `DeclareTopology` is called with a descriptor carrying the same names but a different message TTL
- **THEN** the broker rejects it with a precondition failure, confirming the arguments this package declares are the ones it pins

#### Scenario: Declaration fails against a closed connection

- **GIVEN** a connection that has been closed
- **WHEN** `DeclareTopology` is called with it
- **THEN** it returns a non-nil error and declares nothing

### Requirement: Both Queues Are Bounded By The Topology

The job queue SHALL be declared with a message TTL and a maximum length, and SHALL dead-letter into the dead-letter exchange rather than discarding silently. The dead-letter queue SHALL carry a message TTL and a maximum length of its own, and SHALL discard on expiry and overflow rather than forwarding anywhere.

Both queues and both exchanges SHALL be declared `durable: true`, `autoDelete: false`, `exclusive: false`, and `noWait: false`. Those four flags are independent of one another, and "durable" alone constrains none of the other three: an exclusive queue is visible only to the connection that declared it and vanishes when that connection closes, an auto-delete queue vanishes when its last consumer goes away, and `noWait: true` returns before the broker has answered, so a rejected declaration would surface later as a channel-level exception rather than as this function's error. Any of the three would defeat the guarantee the durable flag is here for — a queue that outlives the process that declared it, holding jobs for a consumer that has not started yet.

A bound on the job queue alone would relocate unbounded growth instead of capping it: a TTL that dead-letters into an unbounded destination lets the destination grow without limit, and during the window opened by the next Phase 6 change the destination is where every message ends up, because nothing consumes the job queue until the cutover. Both bounds together are what make the broker's storage footprint finite regardless of how long a consumer is absent.

The job queue's overflow policy SHALL reject the incoming publish rather than dropping the oldest queued message. A publisher that is told its message was refused can leave the corresponding outbox row unpublished and retry; a silently dropped head is a job lost with no record anywhere that it existed.

#### Scenario: The declared job queue enforces a length bound by rejecting publishes

- **GIVEN** a job queue declared with a maximum length, filled to that length, and a publishing channel in confirm mode
- **WHEN** a further message is published to it
- **THEN** the broker returns a negative acknowledgement rather than silently evicting the oldest message, and the refused message is dead-lettered

A publish outside confirm mode returns nothing to the publisher: AMQP's `basic.publish` is asynchronous and unacknowledged by default, so an overflow rejection is reported as a `basic.nack` only on a channel that has requested confirms. A publisher that needs to know whether the broker took responsibility for a message — which is exactly what lets the relay decide whether to stamp `published_at` — SHALL enable confirms and await the acknowledgement rather than treating a nil return as acceptance.

#### Scenario: The dead-letter queue forwards nowhere

- **GIVEN** the topology as declared by `DeclareTopology`
- **WHEN** the dead-letter queue's arguments are inspected
- **THEN** it carries a message TTL and a maximum length, and no dead-letter exchange of its own

### Requirement: Messages Published To This Topology Are Persistent

Any publisher of a job message to this topology SHALL mark it persistent (AMQP delivery mode 2). A queue declared durable survives a broker restart; the messages in it do not unless each was published persistently, and a transient message in a durable queue is discarded on restart with no error to anyone.

This is stated here, in the capability that owns the topology, rather than left to each publisher, because the durability argument the topology rests on is otherwise incomplete in a way that is invisible until it costs a job: the relay receives a broker acknowledgement, stamps `published_at`, and the row is done — so the message is the only remaining record that the job is waiting. Durable queue, persistent message, and persisted broker storage are three conditions, and the guarantee needs all three.

This change adds no publisher, so it does not verify the two scenarios below — they describe broker behavior that becomes reachable only once something publishes. `add-videojob-source-key-and-outbox-relay` owns demonstrating them end to end (publish, confirm, restart the broker, observe the message still queued), and this requirement is what obliges it to. Recording the obligation here rather than there is deliberate: it is a property of the topology's durability story, and a relay written without it would look correct against every test this change can run.

#### Scenario: A transient message does not survive a broker restart

- **GIVEN** a durable queue declared by `DeclareTopology` holding a message published with the default (transient) delivery mode
- **WHEN** the broker is restarted
- **THEN** the message is gone, demonstrating that queue durability alone does not carry it

#### Scenario: A persistent message survives a broker restart

- **GIVEN** the same durable queue holding a message published with delivery mode 2
- **WHEN** the broker is restarted
- **THEN** the message is still queued

### Requirement: The Adapter Is Tested Against A Real RabbitMQ Instance

`internal/platform/rabbitmq`'s tests SHALL exercise `Open`, `Ping`, `Close`, and `DeclareTopology` against a running broker rather than a fake, reached through a `RABBITMQ_TEST_URL` environment variable. When that variable is unset, **this package's** tests SHALL skip with a clear message rather than fail, matching `internal/platform/redis` and `internal/video/infrastructure/storage` exactly.

A fake proves nothing about the two behaviors this package exists to get right: that a handshake against a real broker succeeds or fails as reported, and that a redeclaration with the arguments this package pins is accepted while a conflicting one is not. Both are broker-enforced, and a test double would assert only that the package calls the functions the test double was written to expect.

The skip is scoped to this package, and it is not the posture `cmd/api`'s `TestMain` takes for `ffmpeg` and MinIO — that one exits non-zero, because those back behavior the suite would otherwise report green while covering none of. Nothing in the running application opens an AMQP connection after this change, so there is no such coverage to lose here; when a later Phase 6 change makes the broker load-bearing for a composition root, that change owns tightening its own entrypoint's `TestMain`.

Tests SHALL exercise the exported `DeclareTopology` itself, passing a descriptor whose names are scoped to the individual test rather than `DefaultTopology`'s. `DefaultTopology`'s values are asserted directly instead, which is what keeps the two obligations — exercise the real code path, leave no production-named queue behind — from contradicting each other. Every test SHALL delete the exchanges and queues it declared when it finishes, including on failure.

#### Scenario: Tests skip with a clear message when no broker is configured

- **GIVEN** `RABBITMQ_TEST_URL` is unset
- **WHEN** this package's tests run
- **THEN** they skip with a message naming the variable, and `go test ./...` still passes on a machine with no broker available

#### Scenario: A test leaves no topology behind

- **GIVEN** a test that calls `DeclareTopology` with a test-scoped descriptor
- **WHEN** it finishes, whether it passed or failed
- **THEN** none of the entities it declared remains on the broker, and no entity named by `DefaultTopology` was ever declared by it

#### Scenario: This change wires no composition root

- **GIVEN** this change's diff
- **WHEN** `cmd/api` and `cmd/worker` are inspected
- **THEN** neither opens an AMQP connection, and the running application starts and serves every existing endpoint with no broker reachable
