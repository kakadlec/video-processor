## Purpose

Defines how the Notification bounded context persists its own state: the connection string it requires at startup, the shape and uniqueness of the stored preference, how its schema is created, and where its connection pool sits in the API's shutdown sequence.

## ADDED Requirements

### Requirement: Notification Owns Its Own PostgreSQL Configuration

The Notification context SHALL read its connection string from `NOTIFICATION_POSTGRES_DSN` and SHALL NOT read, share, or fall back to `IDENTITY_POSTGRES_DSN` or `VIDEO_POSTGRES_DSN`. The variable SHALL be required at `cmd/api` startup: when it is absent, startup SHALL fail with a clear error naming the variable rather than starting a process whose preference routes cannot serve a request.

A separate variable and a separate pool are what make the context's persistence its own. Which physical server the value points at is a deployment decision — pointing all three at one server is permitted and is what local development does — but the code SHALL NOT be the thing that assumes it.

#### Scenario: Startup fails when the DSN is absent

- **GIVEN** `NOTIFICATION_POSTGRES_DSN` is not set
- **WHEN** `cmd/api` starts
- **THEN** startup fails with an error naming `NOTIFICATION_POSTGRES_DSN`, and no HTTP listener is opened

#### Scenario: Startup fails when the database is unreachable

- **GIVEN** `NOTIFICATION_POSTGRES_DSN` is set to an unreachable database
- **WHEN** `cmd/api` starts
- **THEN** startup fails rather than serving preference routes that would error on every request

#### Scenario: The context does not borrow another context's connection string

- **GIVEN** `IDENTITY_POSTGRES_DSN` and `VIDEO_POSTGRES_DSN` are both set and `NOTIFICATION_POSTGRES_DSN` is not
- **WHEN** `cmd/api` starts
- **THEN** startup fails; neither existing variable is used as a substitute

### Requirement: The Schema Is Created Idempotently at Startup

The Notification context SHALL create its own storage at `cmd/api` startup, and doing so SHALL be safe to repeat on every start and across concurrent replicas. It SHALL NOT create, alter, or drop any table owned by another context.

#### Scenario: Starting twice against the same database succeeds

- **GIVEN** a database where the Notification schema has already been created
- **WHEN** `cmd/api` starts again against it
- **THEN** startup succeeds and existing preferences are preserved

#### Scenario: Two replicas starting together both migrate successfully

- **GIVEN** a database where the Notification schema does not yet exist
- **WHEN** two replicas start simultaneously and both attempt to create it
- **THEN** both succeed and the storage exists once — a first-time create SHALL be serialized rather than left to race, because a replica losing that race would fail to start

#### Scenario: Migration failure fails startup

- **GIVEN** a database where the schema cannot be created
- **WHEN** `cmd/api` starts
- **THEN** startup fails with an error rather than continuing with storage in an unknown state

### Requirement: Storage Enforces One Preference Per User, Event Type, and Channel

The stored form SHALL make the triple of user, event type, and channel unique, and the uniqueness SHALL be enforced by the database rather than by a read-then-write in application code. A write SHALL resolve in a single atomic statement that reads no row beforehand, so two concurrent writes of the same triple SHALL leave exactly one row and SHALL NOT fail with a constraint violation surfaced to either caller.

Enforcing the invariant in the schema is what keeps a second API replica from creating a duplicate that the consumer would later resolve to two conflicting destinations for one event.

#### Scenario: Concurrent writes of one triple converge on one preference

- **GIVEN** two concurrent requests writing the same user, event type, and channel
- **WHEN** both complete
- **THEN** exactly one preference exists for that triple, both requests report success, and the stored value is one of the two submitted

#### Scenario: Distinct triples coexist

- **GIVEN** one user
- **WHEN** they write preferences for two different accepted event types on the same channel
- **THEN** both are stored and both are returned by a read

### Requirement: The Notification Pool Participates in Ordered Shutdown

`cmd/api`'s shutdown SHALL close the Notification connection pool alongside the pools it already closes, and SHALL do so only after the HTTP server has stopped and any goroutine holding a database connection has been joined. A close failure SHALL be logged and SHALL NOT prevent the remaining shutdown steps from running.

#### Scenario: The pool closes after the server stops

- **GIVEN** `cmd/api` is running and receives a termination signal
- **WHEN** it shuts down
- **THEN** the HTTP server stops accepting requests and in-flight work is resolved before the Notification pool is closed

#### Scenario: A close failure does not abort shutdown

- **GIVEN** shutdown is underway and closing the Notification pool returns an error
- **WHEN** the error occurs
- **THEN** it is logged and the remaining shutdown steps still run
