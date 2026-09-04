## ADDED Requirements

### Requirement: Storage Holds One Delivery Record Per Preference and Job, Claimed Atomically

The Notification context SHALL store one delivery record per `(user_id, event_type, channel, job_id)`, and the uniqueness of that identity SHALL be enforced by the database rather than by a read-then-write in application code. The record SHALL carry a stable delivery identifier, a claim token, the delivery's status, the number of attempts made, and the times it was claimed and resolved. It SHALL NOT carry the signing secret, the request body, or any part of the destination beyond what identifies the preference.

Claiming SHALL be a **single atomic statement that both inserts and refuses**: it SHALL create the record when none exists, SHALL refuse when one exists that is resolved or freshly claimed, and SHALL grant a claim over one that was claimed but left unresolved beyond a bounded period. It SHALL preserve the existing delivery identifier when it grants such a reclaim, so a receiver deduplicating on that identifier still sees one logical delivery.

The two identifiers are separate because they answer different questions, and collapsing them would be a correctness bug rather than a simplification. The **delivery identifier** is what the receiver sees and deduplicates on, so it SHALL survive a reclaim. The **claim token** identifies *which* grant is current, and SHALL be reissued on every grant, reclaims included. Expiry of the reclaim bound proves only that a claim is old — it does not prove the process holding it stopped, so a slow first claimant can still be running when a second is granted. Resolution SHALL therefore be fenced on the claim token: a write whose token is no longer the current one SHALL be refused and SHALL change nothing, so the superseded claimant cannot overwrite the outcome its successor recorded. This is the same fence `videojob-worker` puts on a terminal write with `lease_epoch`, for the same reason.

A single statement rather than a check followed by an insert is what makes the guarantee hold across processes. Two consumers evaluating "has this been delivered?" separately both read *no*, and both deliver; that is precisely the duplicate the record exists to prevent.

The table SHALL be created by the same migration path the context already owns, and SHALL NOT alter any existing table.

#### Scenario: A second claim on a resolved delivery is refused

- **GIVEN** a delivery record whose status is resolved
- **WHEN** a claim for the same identity is attempted
- **THEN** it is refused and the stored record is unchanged

#### Scenario: Concurrent claims grant exactly one

- **GIVEN** two processes claiming the same delivery identity at the same time
- **WHEN** both statements run
- **THEN** exactly one is granted and one record exists

#### Scenario: An unresolved claim is reclaimable after the bound

- **GIVEN** a delivery record claimed but never resolved, older than the reclaim bound
- **WHEN** a claim for the same identity is attempted
- **THEN** it is granted, the attempt count restarts, the delivery identifier is unchanged, and a new claim token is issued

#### Scenario: A superseded claimant cannot resolve

- **GIVEN** a delivery record reclaimed after the bound, so a new claim token is current
- **WHEN** the previous claimant resolves using the token it holds
- **THEN** the write is refused, no row is changed, and the record still reflects the current claimant

#### Scenario: The current claimant resolves

- **GIVEN** a delivery record whose claim token is the one the caller holds
- **WHEN** it resolves the delivery
- **THEN** the write is applied and the record moves out of `pending`

#### Scenario: An unresolved claim inside the bound is refused

- **GIVEN** a delivery record claimed moments ago and not yet resolved
- **WHEN** a claim for the same identity is attempted
- **THEN** it is refused, so two consumers do not deliver concurrently

#### Scenario: The record holds no secret

- **WHEN** a stored delivery record is read in full
- **THEN** it contains no signing secret and no delivered request body

### Requirement: Exactly One Named Read Path Loads the Signing Secret

The context SHALL expose exactly one repository operation that loads the stored secret, and it SHALL be named and documented as the delivery path's operation. Every other read SHALL continue to project only whether a secret is present and SHALL NOT select the column.

This is a narrowing of the existing rule, not a relaxation of it. HMAC signing requires the original bytes, so the value has to be loadable somewhere; what makes it safe is that the somewhere is singular, named, and provably not on any path that builds an HTTP response. The read used by the preference routes SHALL remain unable to load it, so the response types those routes build still cannot carry a value that was never fetched.

That the secret-loading operation has no caller in the HTTP composition root SHALL be enforced by a test rather than by convention.

#### Scenario: The preference read still cannot load a secret

- **WHEN** the operation backing the preference routes runs
- **THEN** it selects no secret column and its result type has no field able to hold one

#### Scenario: The delivery read loads the secret it needs

- **GIVEN** an enabled webhook preference with a stored secret
- **WHEN** the delivery path loads it
- **THEN** it receives the full preference, secret included, and can compute a signature with it

#### Scenario: The HTTP composition root does not call the secret-loading operation

- **WHEN** the HTTP composition root's sources are inspected
- **THEN** none of them calls the secret-loading operation

## MODIFIED Requirements

### Requirement: Notification Owns Its Own PostgreSQL Configuration

The Notification context SHALL read its connection string from `NOTIFICATION_POSTGRES_DSN` and SHALL NOT read, share, or fall back to `IDENTITY_POSTGRES_DSN` or `VIDEO_POSTGRES_DSN`. The variable SHALL be required at the startup of **every process that uses the context** — `cmd/api` and `cmd/notifier` alike: when it is absent, startup SHALL fail with a clear error naming the variable rather than starting a process that cannot serve a preference request or resolve an event.

The requirement is generalized rather than rewritten. It was scoped to `cmd/api` because that was the only process holding the pool; the notifier holds one for the same reason and SHALL fail the same way. `cmd/worker` reads none of them and is unaffected.

A separate variable and a separate pool are what make the context's persistence its own. Which physical server the value points at is a deployment decision — pointing all three at one server is permitted and is what local development does — but the code SHALL NOT be the thing that assumes it.

#### Scenario: Startup fails when the DSN is absent

- **GIVEN** `NOTIFICATION_POSTGRES_DSN` is not set
- **WHEN** `cmd/api` starts
- **THEN** startup fails with an error naming `NOTIFICATION_POSTGRES_DSN`, and no HTTP listener is opened

#### Scenario: The notifier fails the same way

- **GIVEN** `NOTIFICATION_POSTGRES_DSN` is not set
- **WHEN** `cmd/notifier` starts
- **THEN** startup fails with an error naming `NOTIFICATION_POSTGRES_DSN`, and no consumer is opened

#### Scenario: Startup fails when the database is unreachable

- **GIVEN** `NOTIFICATION_POSTGRES_DSN` is set to an unreachable database
- **WHEN** a process using the context starts
- **THEN** startup fails rather than serving preference routes, or consuming events, that would error every time

#### Scenario: The context does not borrow another context's connection string

- **GIVEN** `IDENTITY_POSTGRES_DSN` and `VIDEO_POSTGRES_DSN` are both set and `NOTIFICATION_POSTGRES_DSN` is not
- **WHEN** a process using the context starts
- **THEN** startup fails; neither existing variable is used as a substitute

### Requirement: The Schema Is Created Idempotently at Startup

The Notification context SHALL create its own storage at startup **in every process that uses it** — `cmd/api` and `cmd/notifier` alike — and doing so SHALL be safe to repeat on every start and across concurrent replicas and processes. It SHALL NOT create, alter, or drop any table owned by another context.

Both processes migrate for the same reason both sides of the terminal topology declare it: neither process's startup may depend on the other's having run first. A notifier started against a database no API has yet touched SHALL create what it needs rather than fail, and the serialization that already protects two replicas racing to a first-time create SHALL cover this case unchanged.

#### Scenario: Starting twice against the same database succeeds

- **GIVEN** a database where the Notification schema has already been created
- **WHEN** a process using the context starts again against it
- **THEN** startup succeeds and existing preferences and delivery records are preserved

#### Scenario: Two replicas starting together both migrate successfully

- **GIVEN** a database where the Notification schema does not yet exist
- **WHEN** two replicas start simultaneously and both attempt to create it
- **THEN** both succeed and the storage exists once — a first-time create SHALL be serialized rather than left to race, because a replica losing that race would fail to start

#### Scenario: The notifier migrates without the API having started

- **GIVEN** a database where the Notification schema does not yet exist and no `cmd/api` process has run against it
- **WHEN** `cmd/notifier` starts
- **THEN** it creates the schema and begins consuming

#### Scenario: Migration failure fails startup

- **GIVEN** a database where the schema cannot be created
- **WHEN** a process using the context starts
- **THEN** startup fails with an error rather than continuing with storage in an unknown state
