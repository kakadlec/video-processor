## ADDED Requirements

### Requirement: Every Process Emits Machine-Readable Records

Every process this repository builds — `cmd/identity-api`, `cmd/video-api`, `cmd/notification-api`, `cmd/worker`, and `cmd/notifier` — SHALL emit its diagnostic output as structured records through `log/slog`, and SHALL NOT write diagnostic output through the standard library's package-level `log` functions, through `fmt.Print*`, or through any other unstructured path.

A record SHALL carry, at minimum, a timestamp, a severity level, a message, and the emitting service's identity. Information that identifies what a record is *about* — a job identifier, a lease epoch, a storage key, a delivery identifier, an attempt number, an event type, a channel — SHALL be carried as a named field and SHALL NOT be interpolated into the message.

The message SHALL be a fixed string for a given call site: it SHALL NOT be assembled by formatting a value into it, so that records from one call site remain groupable after their fields change.

#### Scenario: A record carrying an identifier

- **WHEN** any process logs an event concerning a specific job, delivery, or stored object
- **THEN** that identifier appears as a named field of the record, and the record's message is the same fixed string for every occurrence of that event

#### Scenario: No unstructured output path remains

- **WHEN** a source-level test walks the syntax of every non-test `.go` file under `cmd/` and `internal/`
- **THEN** it finds no call to a package-level `log` output or exit function (`log.Print`, `log.Printf`, `log.Println`, `log.Fatal`, `log.Fatalf`, `log.Fatalln`, `log.Panic*`) and no call to `fmt.Print`, `fmt.Printf`, `fmt.Println`, or an `fmt.Fprint*` whose destination is standard output or standard error, and it fails naming the file and line if it does

Value-producing calls (`fmt.Errorf`, `fmt.Sprintf`) are unaffected: they build a value, they do not emit output.

#### Scenario: A startup precondition that cannot be met

- **WHEN** a process cannot satisfy a startup precondition that is documented as fatal
- **THEN** it emits a record at error severity naming the precondition and then exits non-zero as an explicit, separate step, rather than through a helper that logs and exits in one call

### Requirement: Every Record Names the Process That Emitted It

Every record SHALL carry a field identifying the service that emitted it, bound once at that process's startup rather than supplied by each call site. A shared `internal/` package logging on behalf of a process SHALL inherit that identity without being told what process it is running in.

This is what makes one aggregated stream readable: `docker-compose.yml` runs three replicas of `cmd/worker` and five services in total, all writing to the same collected output, and without it a record cannot be attributed to its source.

#### Scenario: A shared package logs

- **WHEN** a package under `internal/` emits a record while running inside `cmd/worker`
- **THEN** the record names `cmd/worker` as the emitting service, and the same package running inside `cmd/video-api` names that service instead

#### Scenario: Concurrent replicas

- **WHEN** more than one replica of the same process runs against one collected stream
- **THEN** each record can be attributed to the service that produced it

### Requirement: One Record Format In Every Environment

The record format SHALL be JSON, in every environment, and SHALL NOT be selectable by configuration. No alternative human-readable or console format SHALL be offered.

The reason is not presentation. The non-disclosure guarantee below is a property of how a specific encoder reaches a value, and the two candidate encoders reach values by different paths: a text renderer consults `fmt`, where `internal/notification/domain.Secret`'s defences live, while a JSON encoder consults `encoding/json`, where that type's `MarshalJSON` deliberately returns an error. A configurable format would mean the guarantee holds under whichever encoder was verified and is untested under the other, in the environment where a developer is least likely to be watching for it.

#### Scenario: The format cannot be switched

- **WHEN** a process starts under any configuration this repository documents
- **THEN** it emits JSON records, and no configuration value changes that

### Requirement: The Logger Is Configured Once Per Composition Root

Each composition root SHALL construct its logger — format, severity threshold, destination, and service identity — as part of its own startup, and SHALL install it as the process-wide default. No package under `internal/` SHALL construct a logger, read logging configuration, or decide a severity threshold.

Where a package already accepts an injected logger so that its own tests can read what it writes, it SHALL continue to, and SHALL treat a nil logger as the process-wide default. Injection SHALL NOT be introduced anywhere it is not already present: threading a logger through constructors that do not need one would be the mechanism by which a package acquires an opinion about its process's logging, which the first paragraph forbids.

#### Scenario: A shared package needs no configuration

- **WHEN** a package under `internal/` logs
- **THEN** it does so through the process-wide default and reads no environment variable to do it

#### Scenario: A test reads what a use case writes

- **WHEN** a use case that accepts an injected logger is constructed with one in a test
- **THEN** its records go to that logger, and constructing it with nil sends them to the process-wide default instead

### Requirement: No Domain Value Is Passed To a Log Call

A log call site SHALL build every field from a scalar it has extracted itself — a string, an integer, a boolean, a duration, or a time. It SHALL NOT pass a value of arbitrary type to the logger, and SHALL NOT rely on a type rendering itself for the log.

Concretely: every argument a log call passes after its message SHALL be a typed attribute constructor. The loosely-typed alternating key-and-value form that `slog`'s top-level functions also accept SHALL NOT be used, because its value position takes any type and is therefore the same hole as an arbitrary-value attribute. Requiring typed attributes is a constraint on the arguments, not on which logging function is called: the ordinary `Info`/`Warn`/`Error` calls accept typed attributes directly, so no call site is obliged to use the attribute-only variant.

This SHALL be enforced at the source level, by a test that walks the syntax of every non-test file under `cmd/` and `internal/` and fails on any field constructed from an arbitrary value or passed in the alternating form. A behavioural test cannot hold this claim: it can only observe the call sites that exist when it is written.

The rule is deliberately stronger than redacting known-sensitive types. `internal/notification/domain.Secret` protects itself through `fmt`, and the aggregates that hold one as a plain field — `PreferenceIntent` and `NotificationPreference` — are protected only because `fmt` cannot call a method on a struct field. A JSON encoder has no such limitation, and `Secret`'s deliberate marshalling error would discard the entire record rather than redact one field of it. Foreclosing the arbitrary-value path removes the question instead of answering it per type.

This rule governs how a *value* reaches a record. It does not govern an error's own text, which remains subject to the existing prohibitions in `notification-webhook-delivery` and `notification-email-delivery`: a recorded reason and every log line on the delivery path are built from a classified error of this system's own, never from a transport error, whose `*url.Error` rendering would carry a destination's query string.

#### Scenario: An aggregate is passed to a log call

- **WHEN** a non-test source file constructs a log field from a value that is not one of the permitted scalar kinds
- **THEN** the source-level test fails and names the file and the call site

#### Scenario: A preference is logged

- **WHEN** any process logs an event concerning a notification preference
- **THEN** the record carries the `(user, event type, channel)` triple as three separate scalar fields, and carries no representation of the preference aggregate and no representation of its secret

### Requirement: HTTP Request and Panic Records Are Records Like Any Other

Every HTTP service SHALL emit its per-request access record and its recovered-panic record through the same logger, in the same format, as every other record it emits. Neither SHALL be produced by the HTTP framework's own logging middleware.

An access record SHALL carry the request method, the matched route, the request path, the response status, the request's duration, the response size, and — where the request carries an authenticated subject — that subject.

An access record SHALL NOT carry the request's query string, any request or response header, or any part of either body. A path segment in this system is at worst a storage key, which is already documented as loggable; a query string is unbounded caller-supplied text, and this system documents one case of a credential legitimately appearing in one.

A recovered panic SHALL be recorded at error severity with the panic value and the stack as fields, and SHALL still produce the response the service produced before.

#### Scenario: A request is served

- **WHEN** any HTTP service answers a request
- **THEN** it emits one access record in the same format as its other records, naming the method, matched route, path, status, duration, size, and the authenticated subject when there is one

#### Scenario: A request carries a query string

- **WHEN** a request arrives with a query string
- **THEN** no part of it appears in the access record

#### Scenario: A handler panics

- **WHEN** a handler panics and the recovery middleware runs
- **THEN** an error-severity record carries the panic value and the stack, and the client receives the same response as before

### Requirement: Severity Threshold Is Per-Process and Optional

Each process SHALL accept an optional configuration value setting its own minimum severity, defaulting to informational when it is absent. A value that cannot be parsed SHALL be refused at startup rather than silently replaced by the default.

The threshold is deliberately per-process and carries no cross-service consistency obligation, unlike the rate-limit configuration, which `rate-limiting` requires to hold one value everywhere because it governs one shared per-user budget. Raising one process's verbosity while leaving the others alone is the ordinary use of this value, not a misconfiguration.

#### Scenario: The value is absent

- **WHEN** a process starts with no severity configured
- **THEN** it emits informational records and above

#### Scenario: The value is unparseable

- **WHEN** a process starts with a severity value it cannot parse
- **THEN** it refuses to start and names the offending value, rather than starting at the default

#### Scenario: Two processes are configured differently

- **WHEN** one process is configured for debug severity and another is left at the default
- **THEN** both are correct, and no check requires them to agree
