## ADDED Requirements

### Requirement: The AMQP Adapter Holds Only Connection And Lifecycle Plumbing

The AMQP connection adapter SHALL live at `internal/platform/rabbitmq`, alongside `internal/platform/redis` and `internal/platform/ratelimit`, and SHALL contain no exchange name, routing key, or queue name belonging to a specific bounded context's use case.

`ddd-architecture`'s "Shared infrastructure with no owning context lives under internal/platform" scenario permits this package "only connection/lifecycle plumbing — never domain or application logic for a specific context's use case", and that boundary decides the split precisely. Opening, health-checking, and closing a connection, and declaring an arbitrary exchange/queue/dead-letter topology, are plumbing: Phase 7's Notification context will declare its own topology through the same functions. The concrete names `video.jobs` and `video_job.queued` are Video Processing's and live in that context (see the `videojob-messaging` capability) — which is why this change needs no delta against that canonical scenario rather than quietly straining it.

This placement is the opposite of MinIO's, deliberately: `internal/video/infrastructure/storage` sits inside the Video Processing context because every one of its consumers belongs to that context, and the broker's do not.

#### Scenario: The package imports no bounded context

- **GIVEN** any Go file under `internal/platform/rabbitmq/`
- **WHEN** its imports are inspected
- **THEN** every `video-processor/internal/...` import it declares is itself under `video-processor/internal/platform/`

This is an allow-list rather than a prohibition naming `identity` and `video`, because `ddd-architecture` already names a Notification context for Phase 7: a rule listing today's two contexts would silently permit importing the third the day it appears.

#### Scenario: The package names no context's entities

- **GIVEN** any non-test Go file under `internal/platform/rabbitmq/`
- **WHEN** its string literals are inspected
- **THEN** none is an exchange, queue, or routing-key name specific to a bounded context

Test files are excluded because the test that enforces this rule necessarily contains the literals it forbids.

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
- **THEN** it returns a nil connection and a non-nil error, and the error contains neither the URI's username nor its password

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

### Requirement: Topology Declaration Is Idempotent And Takes A Descriptor

`internal/platform/rabbitmq` SHALL expose a `Topology` descriptor and a `DeclareTopology(conn, topo)` function that declares the exchange, the work queue, the dead-letter exchange, and the dead-letter queue the descriptor names, together with the bindings between them, and that succeeds when called repeatedly with the same descriptor against a broker where that topology already exists.

The descriptor carries names and bound values only, and this package SHALL NOT define a default value for it. A caller supplies the topology it owns. That is what keeps this package context-free while one implementation serves the Video Processing context today and the Notification context later.

Declaration order SHALL be: dead-letter exchange, dead-letter queue, and their binding first; then the work exchange, the work queue, and its binding. RabbitMQ does not validate at declare time that a queue's `x-dead-letter-exchange` names an existing exchange, and silently drops dead-lettered messages when it does not — so declaring the sink first makes a partial failure leave a topology that is visibly incomplete rather than complete-looking and lossy.

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

### Requirement: Declared Queues Are Bounded, And Overflow Refuses Rather Than Discards

`DeclareTopology` SHALL declare the work queue with a maximum length and an overflow policy of `reject-publish`, and SHALL declare the dead-letter queue with a maximum length, a message TTL, an overflow policy of `drop-head`, and no dead-letter exchange of its own.

Two bounds, not one. Bounding only the work queue would relocate growth rather than cap it, since what a dead-lettering queue sheds lands in the dead-letter queue; the dead-letter queue forwards nowhere and drops its own head, so the chain terminates.

`reject-publish` rather than the default `drop-head` on the work queue: a full queue must refuse the incoming publish rather than silently evicting the oldest queued item. A publisher told its message was refused can leave its own durable record of that work unpublished and retry, which turns a full queue into back-pressure — nothing is lost, and the system resumes the moment the queue drains. A dropped head is work that vanishes with no record anywhere that it existed.

`reject-publish` rather than `reject-publish-dlx`: a publisher that retries a refused message would deposit one dead-lettered copy per attempt, filling the dead-letter queue with duplicates of work that was never lost. The negative acknowledgement the publisher already receives is the authoritative record, and it is the one that can name the row the message came from.

The work queue SHALL NOT carry a message TTL. That omission is deliberate and load-bearing: expiring a live work message moves it to the dead-letter queue without touching the database row describing the same work, and this system's `VideoJob` state machine has no edge out of `queued` except to `processing` — so an expired message would leave a job reporting `queued` forever, an inconsistency nothing in this phase repairs. An honest TTL here would require both a reconciler and a new state-machine edge; a maximum length with `reject-publish` bounds the queue without creating the inconsistency at all.

#### Scenario: A full work queue refuses the publish rather than evicting

- **GIVEN** a work queue declared with a maximum length, filled to that length, and a publishing channel in confirm mode
- **WHEN** a further message is published to it
- **THEN** the broker returns a negative acknowledgement, and the messages already queued are unchanged

A publish outside confirm mode returns nothing to the publisher: AMQP's `basic.publish` is asynchronous and unacknowledged by default, so an overflow rejection is reported as a `basic.nack` only on a channel that has requested confirms. A publisher that needs to know whether the broker took responsibility for a message SHALL enable confirms and await the acknowledgement rather than treating a nil return as acceptance.

#### Scenario: The dead-letter queue forwards nowhere

- **GIVEN** a topology declared by `DeclareTopology`
- **WHEN** the dead-letter queue's arguments are inspected
- **THEN** it carries a message TTL and a maximum length, and no dead-letter exchange of its own

#### Scenario: The work queue carries no message TTL

- **GIVEN** a topology declared by `DeclareTopology`
- **WHEN** the work queue's arguments are inspected
- **THEN** they include a maximum length and an overflow policy, and no `x-message-ttl`

### Requirement: The Adapter Is Tested Against A Real RabbitMQ Instance

`internal/platform/rabbitmq`'s tests SHALL exercise `Open`, `Ping`, `Close`, and `DeclareTopology` against a running broker rather than a fake, reached through a `RABBITMQ_TEST_URL` environment variable. When that variable is unset, **this package's** tests SHALL skip with a clear message rather than fail, matching `internal/platform/redis` and `internal/video/infrastructure/storage` exactly.

A fake proves nothing about the two behaviors this package exists to get right: that a handshake against a real broker succeeds or fails as reported, and that a redeclaration with the arguments it declares is accepted while a conflicting one is not. Both are broker-enforced, and a test double would assert only that the package calls the functions the test double was written to expect.

The skip is scoped to this package, and it is not the posture `cmd/api`'s `TestMain` takes for `ffmpeg` and MinIO — that one exits non-zero, because those back behavior the suite would otherwise report green while covering none of. Nothing in the running application opens an AMQP connection after this change, so there is no such coverage to lose here; when a later change makes the broker load-bearing for a composition root, that change owns tightening its own entrypoint's `TestMain`.

The broker SHALL be reached through a dedicated account rather than the built-in `guest`. RabbitMQ confines `guest` to loopback as the broker itself sees it, and every connection in this project's local and CI environments arrives over a Docker network from another address — so a `guest` URI fails with `ACCESS_REFUSED` in both, presenting as every test in the package failing at `Open` and reading like an absent broker.

Tests SHALL exercise the exported `DeclareTopology` itself, passing descriptors whose names are scoped to the individual test, and SHALL delete the exchanges and queues they declared when they finish, including on failure.

#### Scenario: Tests skip with a clear message when no broker is configured

- **GIVEN** `RABBITMQ_TEST_URL` is unset
- **WHEN** this package's tests run
- **THEN** they skip with a message naming the variable, and `go test ./...` still passes on a machine with no broker available

#### Scenario: A test leaves no topology behind

- **GIVEN** a test that calls `DeclareTopology` with a test-scoped descriptor
- **WHEN** it finishes, whether it passed or failed
- **THEN** none of the entities it declared remains on the broker

#### Scenario: This change wires no composition root

- **GIVEN** this change's diff
- **WHEN** `cmd/api` and `cmd/worker` are inspected
- **THEN** neither opens an AMQP connection, and the running application starts and serves every existing endpoint with no broker reachable
