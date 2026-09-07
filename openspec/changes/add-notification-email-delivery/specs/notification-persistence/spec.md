## MODIFIED Requirements

### Requirement: Exactly One Named Read Path Loads the Signing Secret

The context SHALL expose exactly one repository operation that loads the stored secret, and it SHALL be named and documented as the delivery path's operation. Every other read SHALL continue to project only whether a secret is present and SHALL NOT select the column.

This is a narrowing of the existing rule, not a relaxation of it. HMAC signing requires the original bytes, so the value has to be loadable somewhere; what makes it safe is that the somewhere is singular, named, and provably not on any path that builds an HTTP response. The read used by the preference routes SHALL remain unable to load it, so the response types those routes build still cannot carry a value that was never fetched.

That the secret-loading operation has no caller in the Notification context's HTTP service SHALL be enforced by a test rather than by convention. That service is the only HTTP process that links this package at all after the HTTP tier was split by bounded context, so the test's subject is narrower than it was, and for the other HTTP services the property holds by construction rather than by inspection.

#### Scenario: The preference read still cannot load a secret

- **WHEN** the operation backing the preference routes runs
- **THEN** it selects no secret column and its result type has no field able to hold one

#### Scenario: The delivery read loads the secret it needs

- **GIVEN** an enabled webhook preference with a stored secret
- **WHEN** the delivery path loads it
- **THEN** it receives the full preference, secret included, and can compute a signature with it

#### Scenario: The delivery read restores a preference that has no secret

- **GIVEN** an enabled preference on a channel that does not sign, stored with no secret
- **WHEN** the delivery path loads it
- **THEN** it receives the full preference and no error, rather than failing because the stored secret is empty

#### Scenario: The Notification HTTP service does not call the secret-loading operation

- **WHEN** the sources of the Notification context's HTTP service are inspected
- **THEN** none of them calls the secret-loading operation

## ADDED Requirements

### Requirement: The Stored Form Requires a Signing Secret Only on a Channel That Signs

The stored form SHALL enforce, in the database rather than in application code, that a preference on a channel that signs carries a non-empty secret, and SHALL admit a preference on a channel that does not. The constraint SHALL be conditional on the channel rather than unconditional on the column.

Enforcing it in the schema is what keeps the invariant true of the table rather than of the one package that writes it, so it survives a second writer or a manual statement during an incident — the reason the unconditional form was chosen when one channel existed. Making it conditional rather than dropping it preserves exactly that for the channel that still needs it.

A write SHALL continue to resolve in a single atomic statement that reads no row beforehand, for every case: a submitted secret, an omitted secret on a channel that signs, and an omitted secret on a channel that does not. The third case SHALL be a create, and the second SHALL remain the refusal that reports that a secret is required.

The stored value for a preference created with no secret SHALL be the empty string rather than a null, and the statement that writes it SHALL name the column rather than omit it, because the column is non-nullable with no default and an omitted column would write a null the constraint does not govern.

The change to the constraint SHALL be applied by the context's existing startup migration, SHALL be safe to re-execute on every start and across concurrent replicas, and SHALL require no backfill: every row stored before it is on the channel that signs and already satisfies it.

#### Scenario: A preference with no secret is stored on a non-signing channel

- **WHEN** a preference on a channel that does not sign is created with no secret
- **THEN** the row is stored with an empty secret rather than a null, and the database accepts it

#### Scenario: A preference with no secret is refused on a signing channel

- **WHEN** a statement attempts to store a preference on a channel that signs with an empty secret
- **THEN** the database refuses it, independently of which application code issued it

#### Scenario: The migration is idempotent and needs no backfill

- **GIVEN** a database holding preferences written before the constraint became conditional
- **WHEN** the migration runs, twice and from two processes at once
- **THEN** it succeeds every time, every existing row is left unchanged, and none is rewritten
