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

#### Scenario: The Notification HTTP service does not call the secret-loading operation

- **WHEN** the sources of the Notification context's HTTP service are inspected
- **THEN** none of them calls the secret-loading operation
