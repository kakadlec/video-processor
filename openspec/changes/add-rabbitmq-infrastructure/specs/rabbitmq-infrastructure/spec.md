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

### Requirement: Topology Declaration Is Idempotent

`internal/platform/rabbitmq.DeclareTopology` SHALL declare the exchange, the job queue, the dead-letter exchange, and the dead-letter queue, together with the bindings between them, and SHALL succeed when called repeatedly against a broker where that topology already exists.

Every name and argument the topology is declared with SHALL be exported from this package as a constant, so that the relay and the worker added by later Phase 6 changes bind to the same identifiers rather than to string literals repeated at each call site. AMQP rejects a redeclaration whose arguments differ from the existing queue's, which makes a drifted literal a startup failure at a distance rather than a silent mismatch — the constants exist so that failure cannot be reached by copying.

#### Scenario: Declaring twice succeeds

- **GIVEN** a live connection to a broker with none of this topology declared
- **WHEN** `DeclareTopology` is called twice in succession
- **THEN** both calls return no error, and the exchange, job queue, dead-letter exchange, and dead-letter queue all exist

#### Scenario: A conflicting redeclaration is rejected by the broker

- **GIVEN** a broker where the job queue already exists as declared by `DeclareTopology`
- **WHEN** the same queue name is declared again with different arguments
- **THEN** the broker rejects it with a precondition failure, confirming the declared arguments are the ones this package pins

#### Scenario: Declaration fails against a closed connection

- **GIVEN** a connection that has been closed
- **WHEN** `DeclareTopology` is called with it
- **THEN** it returns a non-nil error and declares nothing

### Requirement: Both Queues Are Bounded By The Topology

The job queue SHALL be declared with a message TTL and a maximum length, and SHALL dead-letter into the dead-letter exchange rather than discarding silently. The dead-letter queue SHALL carry a message TTL and a maximum length of its own, and SHALL discard on expiry and overflow rather than forwarding anywhere.

A bound on the job queue alone would relocate unbounded growth instead of capping it: a TTL that dead-letters into an unbounded destination lets the destination grow without limit, and during the window opened by the next Phase 6 change the destination is where every message ends up, because nothing consumes the job queue until the cutover. Both bounds together are what make the broker's storage footprint finite regardless of how long a consumer is absent.

The job queue's overflow policy SHALL reject the incoming publish rather than dropping the oldest queued message. A publisher that is told its message was refused can leave the corresponding outbox row unpublished and retry; a silently dropped head is a job lost with no record anywhere that it existed.

#### Scenario: The declared job queue enforces a length bound by rejecting publishes

- **GIVEN** a job queue declared with a maximum length, filled to that length
- **WHEN** a further message is published to it
- **THEN** the publish is refused rather than silently evicting the oldest message, and the refused message is dead-lettered

#### Scenario: The dead-letter queue forwards nowhere

- **GIVEN** the topology as declared by `DeclareTopology`
- **WHEN** the dead-letter queue's arguments are inspected
- **THEN** it carries a message TTL and a maximum length, and no dead-letter exchange of its own

### Requirement: The Adapter Is Tested Against A Real RabbitMQ Instance

`internal/platform/rabbitmq`'s tests SHALL exercise `Open`, `Ping`, `Close`, and `DeclareTopology` against a running broker rather than a fake, reached through a `RABBITMQ_TEST_URL` environment variable. When that variable is unset, **this package's** tests SHALL skip with a clear message rather than fail, matching `internal/platform/redis` and `internal/video/infrastructure/storage` exactly.

A fake proves nothing about the two behaviors this package exists to get right: that a handshake against a real broker succeeds or fails as reported, and that a redeclaration with the arguments this package pins is accepted while a conflicting one is not. Both are broker-enforced, and a test double would assert only that the package calls the functions the test double was written to expect.

The skip is scoped to this package, and it is not the posture `cmd/api`'s `TestMain` takes for `ffmpeg` and MinIO — that one exits non-zero, because those back behavior the suite would otherwise report green while covering none of. Nothing in the running application opens an AMQP connection after this change, so there is no such coverage to lose here; when a later Phase 6 change makes the broker load-bearing for a composition root, that change owns tightening its own entrypoint's `TestMain`.

Each test SHALL declare its topology under names scoped to that test rather than the exported production constants, so a run leaves no queue behind that a later run with different arguments would collide with, and SHALL delete what it declared when it finishes, including on failure.

#### Scenario: Tests skip with a clear message when no broker is configured

- **GIVEN** `RABBITMQ_TEST_URL` is unset
- **WHEN** this package's tests run
- **THEN** they skip with a message naming the variable, and `go test ./...` still passes on a machine with no broker available

#### Scenario: A test leaves no topology behind

- **GIVEN** a test that declares an exchange and a queue under test-scoped names
- **WHEN** it finishes, whether it passed or failed
- **THEN** neither the exchange nor the queue remains declared on the broker

#### Scenario: This change wires no composition root

- **GIVEN** this change's diff
- **WHEN** `cmd/api` and `cmd/worker` are inspected
- **THEN** neither opens an AMQP connection, and the running application starts and serves every existing endpoint with no broker reachable
