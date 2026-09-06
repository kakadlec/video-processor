## MODIFIED Requirements

### Requirement: The Recognized Event Types Equal the Emitted Terminal Event Types

The event-type values this capability accepts SHALL be exactly the values the Video Processing context publishes for a completed and a failed job. The Notification context SHALL NOT import any Video Processing package to obtain them — it declares its own constants — so the equality SHALL be asserted by an automated test in `internal/contracts`, the test-only package that exists to see both contexts.

That package replaces the composition root the assertion used to live in. Since the HTTP tier was split by bounded context, no process imports both contexts, so there is no root left to host it; the drift it guards against is unchanged, and `ddd-architecture` grants that package the cross-context import on the condition that it declares nothing outside its test files.

Without that assertion the two independently-declared literals can drift with nothing failing, and a consumer would then resolve every delivered event against an event type no stored preference names — a silent total delivery failure rather than a build error.

#### Scenario: The constants are pinned across the two contexts

- **GIVEN** the Notification context declares its accepted event types and the Video Processing context declares the event types it writes to its outbox
- **WHEN** the test suite runs
- **THEN** a test in `internal/contracts` asserts each Notification event-type constant is equal to the corresponding Video Processing constant, and fails if either is renamed or re-versioned alone

### Requirement: The Signing Secret Is Never Disclosed

The signing secret SHALL be treated as a credential. No response body SHALL contain it, on any route, for any caller — including the owner who set it. It SHALL NOT appear in any log line or error message. A read that feeds a response SHALL instead report only whether a secret is present.

The secret cannot be stored as a one-way hash the way a password is, because signing a delivery requires the original bytes. Non-disclosure is therefore the whole of its protection, and it SHALL hold on every path rather than on the read route alone.

Exactly one path SHALL load the value: the delivery path, through the single named repository operation `notification-persistence` requires, whose only consumer computes a signature with it. This is a narrowing of the rule rather than an exception carved out of it — the value has to be loadable somewhere for a signature to exist at all, and what keeps non-disclosure true is that the somewhere is singular, named, and provably absent from the Notification context's HTTP service. The routes' own read SHALL remain unable to load it.

The claim is now stronger than it was when one process served every context: the other HTTP services do not link the Notification context's repository at all, so for them the property is enforced by the build rather than by a test. The test SHALL be retained and re-targeted at the one HTTP service that could reach the operation.

#### Scenario: Reading a preference reports only that a secret exists

- **GIVEN** a stored webhook preference carrying a secret
- **WHEN** its owner reads their preferences
- **THEN** the response describes the preference — event type, channel, enabled flag, destination — and reports the presence of a secret as a boolean, and the secret's value appears nowhere in the response

#### Scenario: Writing a preference does not echo the secret back

- **GIVEN** an authenticated user
- **WHEN** they submit a preference carrying a secret and the write succeeds
- **THEN** the response has the same shape a read produces, and the submitted secret is not echoed

#### Scenario: The delivery path is the only one that loads it

- **WHEN** the sources of the Notification context's HTTP service are inspected
- **THEN** none of them calls the operation that loads the secret, and the operation the routes do call selects no secret column

#### Scenario: No other HTTP service can reach the operation at all

- **WHEN** the build graphs of the Identity and Video Processing HTTP services are inspected
- **THEN** neither links the Notification context's repository package, so neither can call the secret-loading operation

#### Scenario: A delivery does not log the secret it signed with

- **GIVEN** a delivery signed with a stored secret
- **WHEN** the consumer's log output is examined
- **THEN** it names the preference's triple and the delivery identifier, and contains no secret value
