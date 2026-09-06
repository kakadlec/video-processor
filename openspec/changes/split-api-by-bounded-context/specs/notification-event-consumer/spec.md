## MODIFIED Requirements

### Requirement: The Consumer Declares the Terminal Topology From the Context's Own Copy of the Names

The consumer SHALL declare the terminal-event topology on every dial, exactly as its publisher does, so neither process's startup depends on the other's order and a recreated broker cannot leave a consume opening on a missing queue.

The names it declares, and the message structures it decodes, SHALL be declared by the Notification context itself. The context SHALL NOT import any package of the Video Processing context to obtain them. `ddd-architecture` forbids that import as a property of the build, not merely of the moment an event is handled, which is the same reason the context already declares its own `UserID` and its own copies of the two event-type strings.

That duplication SHALL be pinned by a test in `internal/contracts`, the test-only package `ddd-architecture` permits to import both contexts, asserting the copied topology names and the copied payload field names equal the ones the Video Processing context publishes. The pin previously lived in a composition root that imported both; splitting the HTTP tier by bounded context left no such root, and the pin is relocated rather than weakened — its permission to cross contexts is now conditional on the package declaring nothing outside its test files. An unpinned copy cannot drift detectably: a renamed exchange would leave the consumer bound to a queue nothing publishes to, and a renamed payload field would decode as its zero value.

The dependency rule SHALL be enforced across **every** package of the Notification context, including its infrastructure packages, rather than across its domain and application packages alone. The infrastructure package that holds the copy is precisely where the temptation to import the original lives.

#### Scenario: The consumer declares its topology on every dial

- **GIVEN** a broker whose terminal exchange and queue were deleted while the consumer was disconnected
- **WHEN** the consumer reconnects
- **THEN** it declares them again and resumes consuming

#### Scenario: The copied names equal the published ones

- **WHEN** the Notification context's terminal topology names are compared with the Video Processing context's
- **THEN** the exchange, the queue, and both routing keys are byte-identical

#### Scenario: The copied payload fields equal the published ones

- **WHEN** a payload the Video Processing context writes is decoded by the Notification context's message type
- **THEN** every field it carries is populated, and none decodes as a zero value because of a name mismatch

#### Scenario: The pinning package holds no production code

- **WHEN** `internal/contracts` is inspected
- **THEN** it declares nothing outside its `_test.go` files apart from a package comment, and no other package imports it

#### Scenario: No Notification package imports Video Processing

- **WHEN** every package under the Notification context is inspected for its imports
- **THEN** none of them imports any package of the Video Processing or Identity contexts, infrastructure packages included
